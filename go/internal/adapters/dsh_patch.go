package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-pulse/internal/config"
	"gopkg.in/yaml.v3"
)

// dshRowID 是 DSH profile 中属于本安装器的插件行 id，与手动安装 bundle 时的行 id 相同，二者不能并存。
const dshRowID = "agent-pulse"
const dshBundleName = "dsh-agent-pulse-adapter"

// dshLoaderURL 是 profile patch 中插件行的 name：指向适配目录内固定 loader 的绝对 file: URL。
// Loader 以原生 ESM 导入它，不依赖 profile 的 node_modules 或 pnpm；Node 按 URL 缓存 ESM 模块，
// 查询串随当前包摘要变化，包升级后的 profile 重组才会真正加载新 loader 与新插件，无需重启宿主。
func dshLoaderURL(directory, id string, pkg *Package) string {
	path := filepath.ToSlash(filepath.Join(directory, "loaders", id, "index.js"))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := &url.URL{Scheme: "file", Path: path}
	if pkg != nil && pkg.SHA256 != "" {
		u.RawQuery = "v=" + pkg.SHA256
	}
	return u.String()
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

// loaderPath 取 loader file: URL 的路径部分，去掉版本查询串，用于识别本安装器自己的行。
func loaderPath(loader string) string {
	if i := strings.Index(loader, "?"); i >= 0 {
		return loader[:i]
	}
	return loader
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
	loader := dshLoaderURL(c.Adapters.Directory, p.ID, &p)
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
			// 同一 loader 路径的行都属于本安装器，不论查询串是旧版的空值还是历史版本的 ?v=摘要；
			// 只有路径不同才是"其他来源"。
			if name == nil || loaderPath(name.Value) != loaderPath(loader) {
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

// writeDSHLoader 写入固定 loader：宿主每次加载当前版本包都经它导入，并写下运行回执。
// 包升级只改 state.json，不触碰 profile；回执只说明已加载，不说明事件已采集。
func writeDSHLoader(c config.Bridge, p Package) error {
	dir := filepath.Join(c.Adapters.Directory, "loaders", p.ID)
	manifest, err := json.MarshalIndent(map[string]any{
		"name":        "@agent-pulse/dsh-loader-" + p.ID,
		"version":     p.Version,
		"private":     true,
		"type":        "module",
		"description": "AgentPulse 固定 loader：导入当前已安装的 DeepSeek Harness 适配包",
		"exports":     map[string]string{".": "./index.js", "./locale/*.json": "./locale/*.json", "./icon": "./icon.svg"},
	}, "", "  ")
	if err != nil {
		return err
	}
	source := packageDir(c.Adapters.Directory, p)
	for _, name := range []string{"locale/en.json", "locale/zh.json", "icon.svg"} {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		if err = AtomicFile(filepath.Join(dir, filepath.FromSlash(name)), data, 0600); err != nil {
			return err
		}
	}
	root, _ := json.Marshal(c.Adapters.Directory)
	id, _ := json.Marshal(p.ID)
	statePath, _ := json.Marshal(filepath.Join(c.Adapters.Directory, "state.json"))
	loader := fmt.Sprintf(`import { readFileSync, mkdirSync, writeFileSync, renameSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
const root = %s, id = %s;
let state = JSON.parse(readFileSync(%s, "utf8"));
try { state = JSON.parse(readFileSync(join(root, "pending.json"), "utf8")).state; } catch (error) { if (error.code !== "ENOENT") throw error; }
const p = state.installed[id].package;
// Node 按 URL 缓存 ESM 模块：带版本查询串，包升级后的 profile 重组才会真正加载新版本。
const plugin = await import(pathToFileURL(join(root, "packages", id, p.version, p.sha256, p.entry)).href + "?v=" + p.sha256);
function receipt() {
  try {
    const dir = join(root, "runtime"); mkdirSync(dir, {recursive: true});
    const target = join(dir, id + ".json"), tmp = target + "." + process.pid;
    writeFileSync(tmp, JSON.stringify({version: p.version, at: new Date().toISOString()}), {mode: 0o600});
    renameSync(tmp, target);
  } catch {}
}
export const inject = plugin.inject;
export function apply(ctx, config) { const dispose = plugin.apply(ctx, config); receipt(); return dispose; }
`, root, id, statePath)
	if err = AtomicFile(filepath.Join(dir, "index.js"), []byte(loader), 0600); err != nil {
		return err
	}
	return AtomicFile(filepath.Join(dir, "package.json"), append(manifest, '\n'), 0600)
}
