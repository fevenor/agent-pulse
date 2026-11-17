package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func codexHookPath(home string) string {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" && filepath.IsAbs(configured) {
		return filepath.Join(configured, "hooks.json")
	}
	return filepath.Join(home, ".codex", "hooks.json")
}

// 只认领本软件固定dispatch命令，不删除其他工具或自定义脚本。
var nativeCodexHook = regexp.MustCompile(`^"[^"\r\n]*(?:bridge\.exe|agent-pulse-bridge\.exe)" dispatch --config "[^"\r\n]+" --adapter "codex" --hook "([A-Za-z]+)"$`)
var linuxCodexHook = regexp.MustCompile(`^'[^'\r\n]*/agent-pulse-bridge' dispatch --config '[^'\r\n]+' --adapter 'codex' --hook '[A-Za-z]+'$`)

func windowsDefersCodex(path, platform string) bool {
	if platform != "windows" || path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	events, _ := root["hooks"].(map[string]any)
	for _, groups := range events {
		list, _ := groups.([]any)
		for _, g := range list {
			group, _ := g.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, item := range handlers {
				h, _ := item.(map[string]any)
				command, _ := h["command"].(string)
				if linuxCodexHook.MatchString(command) {
					return true
				}
			}
		}
	}
	return false
}

func codexSharedWindowsCommands(path string, events map[string]any, prefix string) map[string]string {
	result := map[string]string{}
	if runtime.GOOS != "linux" || !strings.HasPrefix(filepath.ToSlash(path), "/mnt/") {
		return result
	}
	for event, groups := range events {
		list, _ := groups.([]any)
		for _, g := range list {
			group, _ := g.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, item := range handlers {
				h, _ := item.(map[string]any)
				command, _ := h["command"].(string)
				if match := nativeCodexHook.FindStringSubmatch(command); match != nil && match[1] == event {
					result[event] = command
				}
				if linuxCodexHook.MatchString(command) || ownsHookCommand(command, prefix) {
					if windows, ok := h["command_windows"].(string); ok && windows != "" {
						result[event] = windows
					}
				}
			}
		}
	}
	return result
}

// Windows宿主首次读取由WSL建立的共享钩子时，通过宿主已有wsl.exe进入指定发行版。
// 不启动额外Bridge，不改变共享身份，也不依赖Windows进程拥有Linux环境变量。
func codexWSLCommand(exe, cfg, event string) (string, error) {
	values := []string{"wsl.exe", "--distribution", os.Getenv("WSL_DISTRO_NAME"), "--exec", "env"}
	for _, key := range []string{"CODEX_HOME", "CODEX_SQLITE_HOME"} {
		if value := os.Getenv(key); value != "" {
			values = append(values, key+"="+value)
		}
	}
	values = append(values, exe, "dispatch", "--config", cfg, "--adapter", "codex", "--hook", event)
	for i, value := range values {
		if strings.ContainsAny(value, "\"%!\r\n\x00") {
			return "", fmt.Errorf("共享Codex路径含Windows命令不可安全表达的字符")
		}
		values[i] = "\"" + value + "\""
	}
	return strings.Join(values, " "), nil
}
