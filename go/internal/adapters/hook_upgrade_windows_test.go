package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-pulse/internal/config"
)

func TestWindowsHookUpgradeReplacesPriorExecutable(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "bridge.yaml")
	if err := os.WriteFile(cfg, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prior := filepath.Join(root, "internal", "0.5.0", filepath.Base(exe))
	otherConfig := filepath.Join(root, "other.yaml")
	old := windowsProcessCommand(prior, "dispatch", "--config", cfg, "--adapter", "antigravity", "--hook", "Stop")
	other := windowsProcessCommand(prior, "dispatch", "--config", otherConfig, "--adapter", "antigravity", "--hook", "Stop")
	raw, _ := json.Marshal(map[string]any{"agent-pulse-antigravity": map[string]any{"Stop": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": old}, map[string]any{"type": "command", "command": other}}},
	}}})
	p := Package{Manifest: Manifest{ID: "antigravity", ConfigStyle: "named-hooks", Hooks: map[string]string{"Stop": ""}}}
	current, err := hookConfig(config.Bridge{}, cfg, p, raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string][]struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(current, &got); err != nil {
		t.Fatal(err)
	}
	commands := []string{}
	for _, group := range got["agent-pulse-antigravity"]["Stop"] {
		for _, hook := range group.Hooks {
			commands = append(commands, hook.Command)
		}
	}
	want := hookCommand(hookCommandPrefix(exe, cfg, "antigravity"), "Stop")
	if len(commands) != 2 || commands[0] != other || commands[1] != want {
		t.Fatal("跨版本升级须替换旧入口并保留其他配置的钩子")
	}
	again, err := hookConfig(config.Bridge{}, cfg, p, current)
	if err != nil || string(again) != string(current) {
		t.Fatal("再次同步不应新增钩子", err)
	}
	prefix := hookCommandPrefix(exe, cfg, "antigravity")
	for _, command := range []string{old + " & echo extra", windowsProcessCommand(prior, "dispatch", "--config", cfg, "--adapter", "codex", "--hook", "Stop"), windowsProcessCommand(filepath.Join(root, "other.exe"), "dispatch", "--config", cfg, "--adapter", "antigravity", "--hook", "Stop")} {
		if ownsHookCommand(command, prefix) {
			t.Fatal("不得认领追加代码、其他Agent或其他程序")
		}
	}
}
