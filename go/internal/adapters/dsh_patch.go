package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"agent-pulse/internal/config"
	"gopkg.in/yaml.v3"
)

// 本文件只由适配包的 ExecuteControl 调用，Bridge 不解释 DSH profile。

// dshRowID 是 DSH profile 中属于本安装器的插件行 id，与手动安装 bundle 时的行 id 相同，二者不能并存。
const dshRowID = "agent-pulse"
const dshBundleName = "dsh-agent-pulse-adapter"

// dshEntryURL 直接引用已校验的不可变包；安装状态和恢复日志不参与宿主加载。
func dshEntryURL(directory string, p Package) string {
	return dshFileURL(filepath.Join(packageDir(directory, p), filepath.FromSlash(p.Entry)))
}

// file URL 的路径始终以 / 开头，Windows 盘符也是 /C:/...；所有权比较保持在 URL 层。
func dshFileURL(filename string) string {
	filename = filepath.ToSlash(filename)
	if !strings.HasPrefix(filename, "/") {
		filename = "/" + filename
	}
	return (&url.URL{Scheme: "file", Path: filename}).String()
}

// 识别本安装器所属的当前/历史包入口，以及已有安装使用的固定 loader，避免重复登记。
func ownsDSHEntry(directory, id, name string) bool {
	u, err := url.Parse(name)
	if err != nil || u.Scheme != "file" || u.Host != "" {
		return false
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = path.Clean(u.Path), "", "", ""
	fixed := dshFileURL(filepath.Join(directory, "loaders", id, "index.js"))
	packages := dshFileURL(filepath.Join(directory, "packages", id)) + "/"
	return u.String() == fixed || strings.HasPrefix(u.String(), packages)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func dshConfigNode(c config.Bridge) *yaml.Node {
	str := func(value string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value} }
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		str("source_id"), str(c.SourceID),
		str("bridge_address"), str(c.Listen),
		str("runtime_receipt"), str(filepath.Join(c.Adapters.Directory, "runtime", "dsh.json")),
	}}
}

func dshRowNode(loader string, c config.Bridge) *yaml.Node {
	str := func(value string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value} }
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		str("id"), str(dshRowID),
		str("name"), str(loader),
		str("config"), dshConfigNode(c),
	}}
}

func dshPatchNode(loader string, c config.Bridge) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: "AgentPulse 受管理行：由 Bridge 维护，手动修改会在下次同步时被覆盖。", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "insert"},
		{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{dshRowNode(loader, c)}},
	}}
}

func dshOwnedRow(row *yaml.Node) bool {
	return mappingValue(row, "id") != nil && mappingValue(row, "id").Value == dshRowID
}

// updateDSHProfilePatch 维护 DSH 用户 patch（cordis.patch.yml）中属于本安装器的单条 insert 行。
// 保留其他条目、注释与 !!js 标签，重新序列化不保证字节一致；已存在其他来源的同名行或已手动安装 bundle 时拒绝覆盖。
func updateDSHProfilePatch(c config.Bridge, p Package, raw []byte, enable bool) ([]byte, error) {
	path := c.Adapters.Managed[p.ID]
	if err := rejectManualDSHInstall(path, enable); err != nil {
		return nil, err
	}
	doc := yaml.Node{}
	if len(bytes.TrimSpace(raw)) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.SequenceNode, Tag: "!!seq"}}
	} else if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil, errors.New("DSH 配置必须为 YAML 数组；不覆盖无效配置")
	}
	root := doc.Content[0]
	loader := dshEntryURL(c.Adapters.Directory, p)
	kept := []*yaml.Node{}
	found := false
	for _, item := range root.Content {
		if item.Kind != yaml.MappingNode {
			kept = append(kept, item)
			continue
		}
		insert := mappingValue(item, "insert")
		if insert == nil || insert.Kind != yaml.SequenceNode {
			if dshOwnedRow(item) {
				return nil, errors.New("profile 中已有 id 为 agent-pulse 的覆盖行；请先手动移除再由 Bridge 管理")
			}
			kept = append(kept, item)
			continue
		}
		rows := []*yaml.Node{}
		for _, row := range insert.Content {
			if !dshOwnedRow(row) {
				rows = append(rows, row)
				continue
			}
			name := mappingValue(row, "name")
			if name == nil || !ownsDSHEntry(c.Adapters.Directory, p.ID, name.Value) {
				return nil, errors.New("profile 中已有其他来源的 agent-pulse 行；请先手动移除再由 Bridge 管理")
			}
			if enable && !found {
				rows = append(rows, dshRowNode(loader, c))
				found = true
			}
		}
		// 只含 insert 且已空的补丁条目整体移除，避免留下空列表。
		if len(rows) == 0 && len(item.Content) == 2 {
			continue
		}
		insert.Content = rows
		kept = append(kept, item)
	}
	if enable && !found {
		kept = append(kept, dshPatchNode(loader, c))
	}
	root.Content = kept
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), encoder.Close()
}

// rejectManualDSHInstall 拒绝与手动安装的 bundle 并存：profile 的 package.json 登记了该 bundle 即视为手动安装。
func rejectManualDSHInstall(patchPath string, enable bool) error {
	if !enable {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(patchPath), "package.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var profile struct {
		Dependencies map[string]string `json:"dependencies"`
		DSH          struct {
			Profile struct {
				Bundles []string `json:"bundles"`
			} `json:"profile"`
		} `json:"dsh"`
	}
	if json.Unmarshal(raw, &profile) != nil {
		return errors.New("DSH profile package.json 无效；未覆盖原配置")
	}
	if _, ok := profile.Dependencies[dshBundleName]; ok {
		return errors.New("该 profile 已手动安装 dsh-agent-pulse-adapter bundle；不能与受管理安装同时使用")
	}
	for _, bundle := range profile.DSH.Profile.Bundles {
		if bundle == dshBundleName {
			return errors.New("该 profile 已手动安装 dsh-agent-pulse-adapter bundle；不能与受管理安装同时使用")
		}
	}
	return nil
}

// dshProfilePatchPath 仅由随包控制程序提供默认路径；Bridge 不维护 DSH 产品目录。
func dshProfilePatchPath(home string) string {
	root := os.Getenv("DSH_HOME")
	if root == "" {
		root = filepath.Join(home, ".dsh")
	}
	return filepath.Join(root, "profiles", "web", "cordis.patch.yml")
}
