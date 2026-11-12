package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-pulse/internal/config"
)

func TestDisableRemovesOwnedHooksAfterUpgradeAndKeepsUserChanges(t *testing.T) {
	for _, style := range []string{"command-hooks", "named-hooks", "process-hooks", "opencode-plugin"} {
		t.Run(style, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.json")
			configPath := filepath.Join(root, "bridge.yaml")
			c := config.Bridge{SourceID: "source", Listen: "127.0.0.1:18702", Adapters: config.AdapterUpdates{Directory: root, Managed: map[string]string{"sample": path}}}
			p := Package{Manifest: Manifest{ID: "sample", Kind: "executable", ConfigStyle: style, Hooks: map[string]string{"BeforeAction": ""}}}
			if style == "opencode-plugin" {
				p.Kind = style
			}
			first, err := hookConfig(c, configPath, p, []byte(`{"model":"original"}`))
			if err != nil {
				t.Fatal(err)
			}
			upgraded, err := hookConfig(c, configPath, p, first)
			if err != nil {
				t.Fatal(err)
			}
			state := State{Installed: map[string]Installed{"sample": {Package: p, ConfigBefore: first, ConfigAfter: upgraded}}, Rejected: map[string]string{}}
			if err := AtomicJSON(filepath.Join(root, "state.json"), state); err != nil {
				t.Fatal(err)
			}
			other := p
			other.ID = "other"
			current, err := hookConfig(c, configPath, other, upgraded)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal(current, &values); err != nil {
				t.Fatal(err)
			}
			values["model"] = "user-changed"
			current, err = json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, current, 0600); err != nil {
				t.Fatal(err)
			}
			if err := Disable(c, "sample", configPath); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(after, &values); err != nil {
				t.Fatal(err)
			}
			if values["model"] != "user-changed" {
				t.Fatal("禁用覆盖了用户后续修改")
			}
			if style == "opencode-plugin" {
				plugins := values["plugins"].([]any)
				if len(plugins) != 1 || plugins[0].(map[string]any)["package"] != filepath.Join(root, "loaders", "other") {
					t.Fatal("未仅移除当前 Bridge 的插件")
				}
			} else if style == "named-hooks" {
				if len(values["agent-pulse-sample"].(map[string]any)["BeforeAction"].([]any)) != 0 || len(values["agent-pulse-other"].(map[string]any)["BeforeAction"].([]any)) != 1 {
					t.Fatal("未仅移除当前 Bridge 的命名钩子")
				}
			} else {
				events := values["hooks"].(map[string]any)
				if style == "process-hooks" {
					events = events["events"].(map[string]any)
				}
				if len(events["BeforeAction"].([]any)) != 1 {
					t.Fatal("禁用后应只保留其他钩子")
				}
			}
			stored, err := loadState(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := stored.Installed["sample"]; ok {
				t.Fatal("禁用后仍保留安装状态")
			}
		})
	}
}

func TestDisablePreservesInvalidUserConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.json")
	c := config.Bridge{Adapters: config.AdapterUpdates{Directory: root, Managed: map[string]string{"sample": path}}}
	state := State{Installed: map[string]Installed{"sample": {Package: Package{Manifest: Manifest{ID: "sample"}}, ConfigAfter: []byte(`{}`)}}, Rejected: map[string]string{}}
	if err := AtomicJSON(filepath.Join(root, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid-user-config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Disable(c, "sample", filepath.Join(root, "bridge.yaml")); err == nil {
		t.Fatal("配置不可解析时必须拒绝写入")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "invalid-user-config" {
		t.Fatal("无效用户配置被覆盖")
	}
}
