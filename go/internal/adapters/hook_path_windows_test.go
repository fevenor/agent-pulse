//go:build windows

package adapters

import (
	"agent-pulse/internal/config"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsHookOwnershipRecognizesEquivalentExtendedPaths(t *testing.T) {
	for _, tc := range []struct{ plain, extended string }{
		{`D:\程序目录\data\bridge.yaml`, `\\?\D:\程序目录\data\bridge.yaml`},
		{`\\server\share\bridge.yaml`, `\\?\UNC\server\share\bridge.yaml`},
	} {
		exe := `C:\Apps\bridge.exe`
		prefix := hookCommandPrefix(exe, tc.plain, "codex")
		legacy := quoteCMD(exe) + " dispatch --config " + quoteCMD(tc.extended) + " --adapter " + quoteCMD("codex") + " --hook " + quoteCMD("Stop")
		if !ownsHookCommand(legacy, prefix) || hookCommandPrefix(`\\?\`+exe, tc.extended, "codex") != prefix {
			t.Fatal("同一Windows路径未归一识别")
		}
		if ownsHookCommand(legacy, hookCommandPrefix(exe, tc.plain+".other", "codex")) || ownsHookCommand(legacy, hookCommandPrefix(exe, tc.plain, "other")) || ownsHookCommand("echo "+legacy, prefix) {
			t.Fatal("误认领其他配置、Agent或任意命令")
		}
	}
	for _, path := range []string{`\\?\GLOBALROOT\Device\file`, `\\.\pipe\test`} {
		if normalizeHookPath(path) != path {
			t.Fatal("不能把设备路径当成普通文件路径")
		}
	}
}

func TestWindowsReenableAndDisableDeduplicateEquivalentConfigurationPaths(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "bridge.yaml")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plain := hookCommandPrefix(exe, cfg, "codex")
	extended := quoteCMD(exe) + " dispatch --config " + quoteCMD(`\\?\`+cfg) + " --adapter " + quoteCMD("codex") + " --hook " + quoteCMD("Stop")
	other := hookCommand(hookCommandPrefix(exe, cfg+".other", "codex"), "Stop")
	original, _ := json.Marshal(map[string]any{"theme": "keep", "hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCommand(plain, "Stop")}, map[string]any{"type": "command", "command": extended}, map[string]any{"type": "command", "command": "echo keep"}, map[string]any{"type": "command", "command": other}}}}}})
	c := config.Bridge{SourceID: "isolated-path-alias", Listen: ""}
	p := Package{Manifest: Manifest{ID: "codex", Kind: "executable", ConfigStyle: "hooks", Hooks: map[string]string{"Stop": ""}}}
	after, err := hookConfig(c, cfg, p, original)
	if err != nil {
		t.Fatal(err)
	}
	count := func(raw []byte) int {
		t.Helper()
		var value struct {
			Theme string `json:"theme"`
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if value.Theme != "keep" || !bytes.Contains(raw, []byte("echo keep")) {
			t.Fatal("其他设置或用户钩子被删除")
		}
		owned, keptOther := 0, false
		for _, group := range value.Hooks["Stop"] {
			for _, hook := range group.Hooks {
				if ownsHookCommand(hook.Command, plain) {
					owned++
					if strings.Contains(hook.Command, `\\?\`) {
						t.Fatal("新入口不应再写入加长路径别名")
					}
				}
				if hook.Command == other {
					keptOther = true
				}
			}
		}
		if !keptOther {
			t.Fatal("其他Bridge配置的钩子被删除")
		}
		return owned
	}
	if count(after) != 1 {
		t.Fatal("同一配置的两套别名未去重")
	}
	again, err := hookConfig(c, cfg, p, after)
	if err != nil || !bytes.Equal(after, again) {
		t.Fatal("重复启用不幂等")
	}
	disabled, err := updateHookConfig(c, cfg, p, original, false)
	if err != nil || count(disabled) != 0 {
		t.Fatal("停用未清除两套等价入口")
	}
}
