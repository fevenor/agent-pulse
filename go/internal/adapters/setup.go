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
	fmt.Fprintln(out, "\n开始配置 Agent 接入。请选择要启用的 Agent；已有其他设置会保留。")
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
		fmt.Fprintln(out, "未检测到受支持的 Agent。Hub 地址与本机标识已保存。")
		fmt.Fprintln(out, "安装 Agent 后，可运行 agent-pulse-bridge setup 添加接入。")
		return nil
	}
	changed := map[string]string{}
	modified := map[string]bool{}
	hookPaths := map[string][]byte{}
	paths := map[string]string{}
	results := map[string]string{}
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
		paths[agent.ID] = current
		results[agent.ID] = state + "（未更改）"
		fmt.Fprintf(out, "\n%s\n版本：%s\n配置路径：%s\n当前接入状态：%s\n", agent.Name, versionText, current, state)
		fmt.Fprint(out, "请选择：[e] 启用接入  [d] 停用接入  [s] 保持不变：")
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
			fmt.Fprintf(out, "配置路径已自动识别（回车确认，或输入其他路径）：%s\n> ", path)
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
			paths[agent.ID] = path
			results[agent.ID] = "已选择启用，等待安装接入配置"
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
					results[agent.ID] = "停用失败：" + err.Error()
					fmt.Fprintf(out, "%s 停用失败：%v\n", agent.Name, err)
					continue
				}
				fmt.Fprintf(out, "已移除 %s 的 AgentPulse 钩子，保留其他配置。\n", agent.Name)
			}
			delete(c.Adapters.Managed, agent.ID)
			results[agent.ID] = "已停用接入"
			modified[agent.ID] = true
		default:
			fmt.Fprintln(out, "保持原有设置。")
		}
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return err
	}
	if err := writeSetupConfig(absolute, c, modified, newConfig); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n已更新 Bridge 配置：%s\n", absolute)

	if len(changed) > 0 {
		resolved, resolveErr := clientconfig.Resolve(ctx, c, absolute)
		if resolveErr != nil {
			for id := range changed {
				results[id] = "配置已保存，但无法连接 Hub，接入尚未完成"
			}
			fmt.Fprintf(out, "无法从 Hub 获取接入信息：%v\n", resolveErr)
		} else if resolved.Adapters.PublicKey == "" {
			for id := range changed {
				results[id] = "配置已保存，但 Hub 未提供发布公钥，接入尚未完成"
			}
			fmt.Fprintln(out, "Hub 尚未提供适配包发布公钥，请先联系 Hub 部署者补齐。")
		} else {
			// setup 只同步本次明确选择的宿主，避免顺带修改其他已授权 Agent。
			resolved.Adapters.Managed = changed
			if syncErr := Sync(ctx, resolved, absolute); syncErr != nil {
				for id := range changed {
					results[id] = "适配包安装失败：" + syncErr.Error()
				}
				fmt.Fprintf(out, "接入配置安装失败：%v\n", syncErr)
			} else {
				for _, status := range ReportState(resolved, nil).Items {
					if _, ok := changed[status.ID]; ok {
						if status.Installed != "" && status.Error == "" {
							if err := verifySetupHooks(resolved, absolute, status.ID); err != nil {
								results[status.ID] = "接入配置未完成：" + err.Error()
							} else {
								results[status.ID] = "接入配置已完成"
							}
						} else if status.Error != "" {
							results[status.ID] = "接入配置未完成：" + status.Error
						} else {
							results[status.ID] = "Hub 尚未提供适用的适配包，接入配置未完成"
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
			fmt.Fprintf(out, "已保存 Agent 配置文件：%s\n", path)
		}
	}
	printSetupSummary(out, agents, paths, results, absolute, changed["codex"] != "" && results["codex"] == "接入配置已完成")
	for id := range changed {
		if results[id] != "接入配置已完成" {
			return errors.New("部分 Agent 接入配置未完成，请按上面的结果排查后重新运行 setup")
		}
	}
	for _, result := range results {
		if strings.HasPrefix(result, "停用失败：") {
			return errors.New("部分 Agent 停用失败，原有设置已保留")
		}
	}
	return nil
}

// 配置结果按扫描顺序显示名称、宿主版本和实际路径，不用内部适配器标识代替名称。
func printSetupSummary(out io.Writer, agents []setupAgent, paths, results map[string]string, configPath string, codexEnabled bool) {
	fmt.Fprintln(out, "\nAgent 接入配置结果：")
	for _, agent := range agents {
		versionText := agent.Version
		if versionText == "" {
			versionText = "未检测到或版本无法识别"
		}
		fmt.Fprintf(out, "\n%s\n  版本：%s\n  配置路径：%s\n  结果：%s\n", agent.Name, versionText, paths[agent.ID], results[agent.ID])
	}
	fmt.Fprintf(out, "\n本次配置操作已结束。Bridge 配置文件：%s\n", configPath)
	if codexEnabled {
		fmt.Fprintln(out, "Codex CLI：请在 /hooks 中确认 AgentPulse 钩子已审核并信任；已信任的无需重复操作。")
	}
	fmt.Fprintln(out, "以后需要添加、停用 Agent 或调整配置路径，可运行 agent-pulse-bridge setup 修改。")
}

// OpenCode 优先沿用已有标准 JSON，其次沿用 JSONC；明确的 managed 路径仍优先。
func defaultOpenCodeConfigPath(home string) string {
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		jsonc := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		if _, err := os.Stat(jsonc); err == nil {
			return jsonc
		}
	}
	return path
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
		{ID: "opencode", Name: "OpenCode", Command: "opencode", ConfigPath: defaultOpenCodeConfigPath(home), Manifest: Manifest{ID: "opencode", Command: "opencode"}},
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
