package adapters

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ZCode 以 argv 调用进程，不经过 shell；保留其他钩子及用户显式关闭的总开关。
func processHookConfig(root map[string]any, exe, configPath string, p Package) ([]byte, error) {
	return updateProcessHookConfig(root, exe, configPath, p, true)
}

func updateProcessHookConfig(root map[string]any, exe, configPath string, p Package, enable bool) ([]byte, error) {
	hooks := map[string]any{}
	if value, exists := root["hooks"]; exists {
		var ok bool
		hooks, ok = value.(map[string]any)
		if !ok {
			return nil, errors.New("hooks 必须为对象")
		}
	}
	events := map[string]any{}
	if value, exists := hooks["events"]; exists {
		var ok bool
		events, ok = value.(map[string]any)
		if !ok {
			return nil, errors.New("hooks.events 必须为对象")
		}
	}
	for event, value := range events {
		groups, ok := value.([]any)
		if !ok {
			return nil, errors.New("hook 事件必须为数组")
		}
		kept := []any{}
		for _, value := range groups {
			group, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("hook 条目必须为对象")
			}
			commands, ok := group["hooks"].([]any)
			if !ok {
				return nil, errors.New("hook 命令必须为数组")
			}
			rest := []any{}
			for _, command := range commands {
				handler, ok := command.(map[string]any)
				if !ok {
					return nil, errors.New("hook 命令条目无效")
				}
				if !ownsProcessHook(handler, exe, configPath, p.ID) {
					rest = append(rest, handler)
				}
			}
			if len(rest) > 0 {
				group["hooks"] = rest
				kept = append(kept, group)
			}
		}
		events[event] = kept
	}
	for event, matcher := range p.Hooks {
		if !enable {
			break
		}
		handler := map[string]any{"type": "process", "command": exe,
			"args":      []string{"dispatch", "--config", configPath, "--adapter", p.ID, "--hook", event},
			"timeoutMs": 1000, "onError": "allow"}
		group := map[string]any{"hooks": []any{handler}}
		if matcher != "" {
			group["matcher"] = matcher
		}
		groups, _ := events[event].([]any)
		events[event] = append(groups, group)
	}
	if _, exists := hooks["enabled"]; enable && !exists {
		hooks["enabled"] = true
	}
	hooks["events"] = events
	root["hooks"] = hooks
	raw, err := json.MarshalIndent(root, "", "  ")
	return append(raw, '\n'), err
}

func ownsProcessHook(handler map[string]any, exe, configPath, id string) bool {
	command, _ := handler["command"].(string)
	args, ok := handler["args"].([]any)
	return handler["type"] == "process" && strings.EqualFold(filepath.Base(command), filepath.Base(exe)) &&
		ok && len(args) == 7 && args[0] == "dispatch" && args[1] == "--config" && sameHookConfigFile(args[2], configPath) &&
		args[3] == "--adapter" && args[4] == id && args[5] == "--hook"
}

// 宿主参数可带 Windows 扩展路径、短路径或不同大小写；所有权按实际文件身份确认。
// 文件尚未创建时只接受清理后完全相同的绝对路径，不把其他 Bridge 的登记当成本软件所有。
func sameHookConfigFile(value any, expected string) bool {
	path, ok := value.(string)
	if !ok || !filepath.IsAbs(path) || !filepath.IsAbs(expected) {
		return false
	}
	if filepath.Clean(path) == filepath.Clean(expected) {
		return true
	}
	first, firstErr := os.Stat(path)
	second, secondErr := os.Stat(expected)
	return firstErr == nil && secondErr == nil && os.SameFile(first, second)
}
