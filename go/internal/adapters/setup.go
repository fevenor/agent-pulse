package adapters

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agent-pulse/internal/clientconfig"
	"agent-pulse/internal/config"
	"gopkg.in/yaml.v3"
)

type setupAgent struct {
	ID, Name, Command, ConfigPath string
	Manifest                      Manifest
	Version                       string
	Detected                      bool
}

// SetupInteractive 扫描当前用户已安装的支持 Agent，逐项询问启用、禁用或跳过。
// 它只修改明确选择的宿主配置文件；运行中的 Agent 是否立即重新加载由宿主决定。
func SetupInteractive(ctx context.Context, configPath string, in io.Reader, out io.Writer) error {
	if strings.TrimSpace(configPath) == "" {
		return errors.New("配置路径不能为空")
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	raw, readErr := os.ReadFile(absolute)
	newConfig := os.IsNotExist(readErr)
	if readErr != nil && !newConfig {
		return readErr
	}
	if !newConfig {
		var original yaml.Node
		if err := yaml.Unmarshal(raw, &original); err != nil || len(original.Content) != 1 || original.Content[0].Kind != yaml.MappingNode {
			return errors.New("配置 YAML 无效")
		}
		// 复制了示例但尚未生成指纹的配置同样属于首次设置。
		newConfig = strings.TrimSpace(scalarYAML(original.Content[0], "source_id")) == ""
	}
	c, err := loadOrCreateSetupConfig(absolute)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	if newConfig {
		defaultURL := c.HubURL
		fmt.Fprintf(out, "Hub 地址（回车使用 %s）：", defaultURL)
		input, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if value := strings.TrimSpace(input); value != "" {
			c.HubURL = value
		}
		if err := config.HTTPURL(c.HubURL); err != nil {
			return fmt.Errorf("Hub 地址无效：%w", err)
		}
		parsed, _ := url.Parse(c.HubURL)
		if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" {
			return errors.New("Hub 地址只填写服务根地址，不能包含路径或查询参数")
		}
	}
	if c.Adapters.Directory == "" {
		c.Adapters.Directory = filepath.Join(filepath.Dir(absolute), "adapters")
	}
	if !filepath.IsAbs(c.Adapters.Directory) {
		c.Adapters.Directory, _ = filepath.Abs(c.Adapters.Directory)
	}
	if c.Adapters.Managed == nil {
		c.Adapters.Managed = map[string]string{}
	}

	agents := selectSetupAgents(scanSetupAgents(ctx), c.Adapters.Managed)
	if len(agents) == 0 {
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return err
		}
		if err := writeSetupConfig(absolute, c, nil, newConfig); err != nil {
			return err
		}
		fmt.Fprintln(out, "未检测到已安装的支持 Agent；已保存 Hub 地址和主机指纹，可安装 Agent 后重新运行 setup。")
		return nil
	}
	changed := map[string]string{}
	modified := map[string]bool{}
	hookPaths := map[string][]byte{}
	for _, agent := range agents {
		current := c.Adapters.Managed[agent.ID]
		if current == "" {
			current = agent.ConfigPath
		}
		state := "未启用"
		if c.Adapters.Managed[agent.ID] != "" {
			state = "已启用"
		}
		versionText := agent.Version
		if !agent.Detected {
			versionText = "未检测到或版本无法识别"
		}
		fmt.Fprintf(out, "\n%s（%s，版本 %s）\n配置文件：%s\n当前状态：%s\n", agent.Name, agent.ID, versionText, current, state)
		fmt.Fprint(out, "请选择 [e]启用 [d]禁用 [s]跳过：")
		choice, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		choice = strings.ToLower(strings.TrimSpace(choice))
		switch choice {
		case "e", "enable", "启用":
			if !agent.Detected {
				fmt.Fprintf(out, "未能检测到 %s，无法自动启用；可先安装并确认其版本命令可用。\n", agent.Name)
				continue
			}
			path := current
			if path == "" {
				path = agent.ConfigPath
			}
			fmt.Fprintf(out, "配置路径（直接回车使用 %s）：", path)
			input, readErr := reader.ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			if value := strings.TrimSpace(input); value != "" {
				path = value
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			c.Adapters.Managed[agent.ID] = path
			changed[agent.ID] = path
			modified[agent.ID] = true
			if raw, readErr := os.ReadFile(path); readErr == nil {
				hookPaths[path] = raw
			} else if os.IsNotExist(readErr) {
				hookPaths[path] = nil
			} else {
				return readErr
			}
		case "d", "disable", "禁用":
			if c.Adapters.Managed[agent.ID] != "" {
				if raw, readErr := os.ReadFile(current); readErr == nil {
					hookPaths[current] = raw
				} else if os.IsNotExist(readErr) {
					hookPaths[current] = nil
				} else {
					return readErr
				}
				if err := Disable(c, agent.ID, absolute); err != nil {
					fmt.Fprintf(out, "禁用 %s 失败：%v\n", agent.Name, err)
					continue
				}
				fmt.Fprintf(out, "已移除 %s 的 AgentPulse 钩子，保留其他配置。\n", agent.Name)
			}
			delete(c.Adapters.Managed, agent.ID)
			modified[agent.ID] = true
		default:
			fmt.Fprintln(out, "已跳过。")
		}
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return err
	}
	if err := writeSetupConfig(absolute, c, modified, newConfig); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n已更新 Bridge 配置：%s\n", absolute)
	for id, path := range changed {
		fmt.Fprintf(out, "- 已授权 %s：%s\n", id, path)
	}
	if len(changed) > 0 {
		resolved, resolveErr := clientconfig.Resolve(ctx, c, absolute)
		if resolveErr != nil {
			fmt.Fprintf(out, "Bridge 钩子授权已更新；Hub 配置读取失败，适配包尚未同步：%v\n", resolveErr)
		} else if resolved.Adapters.PublicKey == "" {
			fmt.Fprintln(out, "Bridge 钩子授权已更新；Hub 尚未提供适配包发布公钥，无法验证并同步适配包。请在 Hub 配置 adapter_public_key 后重试。")
		} else {
			// setup 只同步本次明确选择的宿主，避免顺带修改其他已授权 Agent。
			resolved.Adapters.Managed = changed
			if syncErr := Sync(ctx, resolved, absolute); syncErr != nil {
				fmt.Fprintf(out, "Bridge 钩子授权已更新；适配包同步失败：%v\n", syncErr)
			} else {
				for _, status := range ReportState(resolved, nil).Items {
					if _, ok := changed[status.ID]; ok {
						if status.Installed != "" && status.Error == "" {
							fmt.Fprintf(out, "适配包已就绪：%s %s\n", status.ID, status.Installed)
						} else if status.Error != "" {
							fmt.Fprintf(out, "适配包未就绪：%s，%s\n", status.ID, status.Error)
						} else {
							fmt.Fprintf(out, "适配包未发布或未安装：%s\n", status.ID)
						}
					}
				}
			}
		}
	}
	for path, before := range hookPaths {
		after, readErr := os.ReadFile(path)
		changedOnDisk := readErr == nil && string(before) != string(after)
		if before == nil && os.IsNotExist(readErr) {
			changedOnDisk = false
		}
		if changedOnDisk {
			fmt.Fprintf(out, "已修改 Agent 配置文件：%s\n", path)
		}
	}
	if len(modified) > 0 {
		fmt.Fprintln(out, "请重启或重新加载对应 Agent；此操作只修改配置，不保证运行中的 Agent 立即生效。Codex 等宿主可能还需要用户确认项目或命令信任。")
	}
	return nil
}

// 按当前平台的确认范围列出来源，避免 Windows 扫描无计划的终端产品。
func setupAgentCatalog(home, platform string) []setupAgent {
	if platform == "windows" {
		return []setupAgent{{ID: "zcode", Name: "ZCode", Command: "ZCode.exe", ConfigPath: filepath.Join(home, ".zcode", "cli", "config.json"), Manifest: Manifest{ID: "zcode", Command: "ZCode.exe"}}}
	}
	return []setupAgent{
		{ID: "codex", Name: "Codex CLI", Command: "codex", ConfigPath: filepath.Join(home, ".codex", "hooks.json"), Manifest: Manifest{ID: "codex", Command: "codex"}},
		{ID: "antigravity", Name: "Antigravity CLI", Command: "agy", ConfigPath: filepath.Join(home, ".gemini", "config", "hooks.json"), Manifest: Manifest{ID: "antigravity", Command: "agy"}},
		{ID: "qoderclicn", Name: "Qoder 中国版 CLI", Command: "qoderclicn", ConfigPath: filepath.Join(home, ".qoder-cn", "settings.json"), Manifest: Manifest{ID: "qoderclicn", Command: "qoderclicn"}},
		{ID: "opencode", Name: "OpenCode", Command: "opencode", ConfigPath: filepath.Join(home, ".config", "opencode", "opencode.json"), Manifest: Manifest{ID: "opencode", Command: "opencode"}},
	}
}

func scanSetupAgents(ctx context.Context) []setupAgent {
	home, _ := os.UserHomeDir()
	items := setupAgentCatalog(home, runtime.GOOS)
	for i := range items {
		version, err := Detect(ctx, items[i].Manifest)
		if err == nil {
			items[i].Version = version
			items[i].Detected = true
		}
	}
	return items
}

func selectSetupAgents(items []setupAgent, managed map[string]string) []setupAgent {
	selected := make([]setupAgent, 0, len(items))
	for _, item := range items {
		if item.Detected || managed[item.ID] != "" {
			selected = append(selected, item)
		}
	}
	return selected
}

func loadOrCreateSetupConfig(path string) (config.Bridge, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return config.Bridge{}, err
	}
	var document yaml.Node
	if err == nil {
		if err = yaml.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return config.Bridge{}, errors.New("配置 YAML 无效")
		}
	} else {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := document.Content[0]
	if strings.TrimSpace(scalarYAML(root, "source_id")) == "" {
		id, err := setupSourceID()
		if err != nil {
			return config.Bridge{}, err
		}
		setYAMLScalar(root, "source_id", id)
	}
	if strings.TrimSpace(scalarYAML(root, "host_name")) == "" {
		name, _ := os.Hostname()
		setYAMLScalar(root, "host_name", strings.TrimSpace(name))
	}
	data, err := yaml.Marshal(&document)
	if err != nil {
		return config.Bridge{}, err
	}
	// 验证通过前不写入原文件，保留既有指纹和环境变量表达式。
	return config.ParseBridge(data)
}

func scalarYAML(root *yaml.Node, key string) string {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key && root.Content[i+1].Kind == yaml.ScalarNode {
			return root.Content[i+1].Value
		}
	}
	return ""
}

func setupSourceID() (string, error) {
	var id [24]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("无法生成 Bridge 主机指纹：%w", err)
	}
	return "bridge-" + hex.EncodeToString(id[:]), nil
}

func writeSetupConfig(path string, c config.Bridge, changed map[string]bool, updateHub bool) error {
	var document yaml.Node
	if raw, err := os.ReadFile(path); err == nil {
		if err = yaml.Unmarshal(raw, &document); err != nil {
			return fmt.Errorf("配置 YAML 无法解析：%w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if document.Kind == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("配置根节点必须为对象")
	}
	root := document.Content[0]
	setMissingYAMLScalar(root, "listen", c.Listen)
	setMissingYAMLScalar(root, "source_id", c.SourceID)
	setMissingYAMLScalar(root, "host_name", c.HostName)
	if updateHub {
		setYAMLScalar(root, "hub_url", c.HubURL)
	} else {
		setMissingYAMLScalar(root, "hub_url", c.HubURL)
	}
	adapters := mapYAML(root, "adapters")
	setMissingYAMLScalar(adapters, "directory", c.Adapters.Directory)
	managed := mapYAML(adapters, "managed")
	for id, path := range c.Adapters.Managed {
		if changed[id] {
			setYAMLScalar(managed, id, path)
		}
	}
	for id := range changed {
		if _, keep := c.Adapters.Managed[id]; !keep {
			deleteYAML(managed, id)
		}
	}
	var out strings.Builder
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return err
	}
	_ = encoder.Close()
	data := []byte(out.String())
	if _, err := config.ParseBridge(data); err != nil {
		return err
	}
	return AtomicFile(path, data, 0600)
}

func setMissingYAMLScalar(parent *yaml.Node, key, value string) {
	if strings.TrimSpace(scalarYAML(parent, key)) == "" {
		setYAMLScalar(parent, key, value)
	}
}

func mapYAML(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key && parent.Content[i+1].Kind == yaml.MappingNode {
			return parent.Content[i+1]
		}
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value
}
func setYAMLScalar(parent *yaml.Node, key, value string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}
			return
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle})
}
func deleteYAML(parent *yaml.Node, key string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return
		}
	}
}
