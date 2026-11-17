package adapters

import (
	"agent-pulse/internal/config"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexSharedHomeAndWindowsDeferral(t *testing.T) {
	home := t.TempDir()
	shared := t.TempDir()
	t.Setenv("CODEX_HOME", shared)
	if codexHookPath(home) != filepath.Join(shared, "hooks.json") {
		t.Fatal("没有遵循CODEX_HOME")
	}
	path := filepath.Join(shared, "hooks.json")
	raw := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/home/user/.local/bin/agent-pulse-bridge' dispatch --config '/home/user/bridge.yaml' --adapter 'codex' --hook 'Stop'","command_windows":"existing"}]}]}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if !windowsDefersCodex(path, "windows") || windowsDefersCodex(path, "linux") {
		t.Fatal("共享配置管理归属不符")
	}
	after, _ := os.ReadFile(path)
	if string(after) != raw {
		t.Fatal("检查不能修改私人配置")
	}
	if nativeCodexHook.MatchString(`echo "bridge.exe" dispatch --config "a" --adapter "codex" --hook "Stop"`) {
		t.Fatal("不能认领任意用户脚本")
	}
}

func TestSharedCodexPreservesWindowsBranchAndRejectsUnrelatedHooks(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Debian")
	t.Setenv("CODEX_HOME", "/mnt/c/Users/test/.codex")
	t.Setenv("CODEX_SQLITE_HOME", "/home/test/.codex/sqlite")
	raw := `{"Stop":[{"hooks":[{"command":"\"D:\\AgentPulse\\bridge.exe\" dispatch --config \"D:\\AgentPulse\\bridge.yaml\" --adapter \"codex\" --hook \"Stop\""},{"command":"echo user-hook"}]}]}`
	var events map[string]any
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		t.Fatal(err)
	}
	// 识别Windows钩子只根据生成器的精确语法，不依赖当前测试平台。
	handlers := events["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)
	if !nativeCodexHook.MatchString(handlers[0].(map[string]any)["command"].(string)) || nativeCodexHook.MatchString(handlers[1].(map[string]any)["command"].(string)) {
		t.Fatal("命令归属边界不符")
	}
	command, err := codexWSLCommand("/home/test/.local/bin/agent-pulse-bridge", "/home/test/bridge.yaml", "Stop")
	if err != nil || !strings.Contains(command, "CODEX_SQLITE_HOME=/home/test/.codex/sqlite") || !strings.Contains(command, `"--distribution" "Debian"`) {
		t.Fatal("原生Windows兜底没有固定环境", err)
	}
	if _, err = codexWSLCommand("/home/bad%/bridge", "/cfg", "Stop"); err == nil {
		t.Fatal("不安全命令路径不能写入")
	}
}

func TestWSLSharedInstallKeepsOneLogicalHookPerEvent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("WSL安装路径在Linux验证")
	}
	t.Setenv("WSL_DISTRO_NAME", "Debian")
	t.Setenv("CODEX_HOME", "/mnt/c/Users/test/.codex")
	windows := `"D:\AgentPulse\bridge.exe" dispatch --config "D:\AgentPulse\bridge.yaml" --adapter "codex" --hook "Stop"`
	raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": windows}, map[string]any{"type": "command", "command": "echo user"}}}}}})
	c := config.Bridge{Adapters: config.AdapterUpdates{Managed: map[string]string{"codex": "/mnt/c/Users/test/.codex/hooks.json"}}}
	p := Package{Manifest: Manifest{ID: "codex", Kind: "executable", ConfigStyle: "hooks", Hooks: map[string]string{"Stop": ""}}}
	after, err := hookConfig(c, "/home/test/bridge.yaml", p, raw)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks map[string][]struct{ Hooks []map[string]any }
	}
	if err = json.Unmarshal(after, &parsed); err != nil {
		t.Fatal(err)
	}
	owned := 0
	user := 0
	for _, group := range parsed.Hooks["Stop"] {
		for _, h := range group.Hooks {
			if h["command_windows"] == windows {
				owned++
			}
			if h["command"] == "echo user" {
				user++
			}
		}
	}
	if owned != 1 || user != 1 {
		t.Fatal("接管未合并为一条平台钩子或删除了用户钩子")
	}
	repeated, err := hookConfig(c, "/home/test/bridge.yaml", p, after)
	if err != nil || !bytes.Equal(repeated, after) {
		t.Fatal("再次启用共享配置不幂等", err)
	}
}
