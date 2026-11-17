package adapters

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"agent-pulse/internal/config"
)

// 从旧版五秒配置升级，验证宿主上限、共享分支、用户配置和重复同步。
func TestHookTimeoutUpgradeRespectsCodexLifecycleLimits(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		shared   bool
	}{{"原生Codex", "codex", false}, {"其他Agent", "qoderclicn", false}, {"WSL共享Codex", "codex", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.shared && runtime.GOOS != "linux" {
				t.Skip("WSL共享安装在Linux验证")
			}
			configPath := filepath.Join(t.TempDir(), "bridge.yaml")
			managed := filepath.Join(t.TempDir(), "hooks.json")
			if tc.shared {
				managed = "/mnt/c/Users/test/.codex/hooks.json"
				t.Setenv("WSL_DISTRO_NAME", "Debian")
			}
			c := config.Bridge{Adapters: config.AdapterUpdates{Managed: map[string]string{tc.id: managed}}}
			p := Package{Manifest: Manifest{ID: tc.id, Kind: "executable", ConfigStyle: "hooks", Hooks: map[string]string{"SessionEnd": "", "Interrupt": "", "Stop": "", "PermissionRequest": ""}}}
			type group struct {
				Hooks []map[string]any `json:"hooks"`
			}
			type document struct {
				Hooks map[string][]group `json:"hooks"`
			}
			// 共享入口先提供既有Windows分支，避免调用真实wsl环境。
			var original document
			original.Hooks = map[string][]group{}
			for event := range p.Hooks {
				original.Hooks[event] = []group{{Hooks: []map[string]any{{"type": "command", "command": "echo 用户自有钩子", "timeout": 70}}}}
				if tc.shared {
					command := windowsProcessCommand(`D:\AgentPulse\bridge.exe`, "dispatch", "--config", `D:\AgentPulse\bridge.yaml`, "--adapter", "codex", "--hook", event)
					original.Hooks[event] = append(original.Hooks[event], group{Hooks: []map[string]any{{"type": "command", "command": command, "timeout": 5}}})
				}
			}
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			current, err := hookConfig(c, configPath, p, raw)
			if err != nil {
				t.Fatal(err)
			}
			var old document
			if err = json.Unmarshal(current, &old); err != nil {
				t.Fatal(err)
			}
			for _, groups := range old.Hooks {
				for _, g := range groups {
					for _, h := range g.Hooks {
						if h["command"] != "echo 用户自有钩子" {
							h["timeout"] = 5
						}
					}
				}
			}
			raw, err = json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if tc.shared {
				t.Setenv("WSL_DISTRO_NAME", "")
			} // systemd未继承WSL环境时也须应用同一限制。
			upgraded, err := hookConfig(c, configPath, p, raw)
			if err != nil {
				t.Fatal(err)
			}
			var result document
			if err = json.Unmarshal(upgraded, &result); err != nil {
				t.Fatal(err)
			}
			for event, groups := range result.Hooks {
				owned, user := 0, 0
				want := float64(5)
				if tc.id == "codex" && (event == "SessionEnd" || event == "Interrupt") {
					want = 3
				}
				for _, g := range groups {
					for _, h := range g.Hooks {
						if h["command"] == "echo 用户自有钩子" {
							user++
							if h["timeout"] != float64(70) {
								t.Fatal("改动了用户自有钩子")
							}
							continue
						}
						owned++
						if h["timeout"] != want {
							t.Errorf("%s超时=%v，期望%v", event, h["timeout"], want)
						}
						if tc.shared && h["command_windows"] == nil {
							t.Fatal("丢失共享Windows分支")
						}
					}
				}
				if owned != 1 || user != 1 {
					t.Fatalf("%s钩子数量错误：自有%d、用户%d", event, owned, user)
				}
			}
			again, err := hookConfig(c, configPath, p, upgraded)
			if err != nil || !bytes.Equal(again, upgraded) {
				t.Fatal("重复同步不幂等", err)
			}
		})
	}
}
