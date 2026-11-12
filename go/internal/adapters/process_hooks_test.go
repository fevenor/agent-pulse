package adapters

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestProcessHooksPreserveUserSettingsAndUpgrade(t *testing.T) {
	root := map[string]any{}
	if err := json.Unmarshal([]byte(`{"theme":"dark","hooks":{"enabled":false,"timeoutMs":5000,"events":{"Stop":[{"matcher":"keep","hooks":[{"type":"process","command":"custom.exe","args":["x"]}]}]}}}`), &root); err != nil {
		t.Fatal(err)
	}
	p := Package{Manifest: Manifest{ID: "zcode", Hooks: map[string]string{"Stop": "", "SessionStart": ""}}}
	config := filepath.Join(t.TempDir(), "中文 空格 ' & $.json")
	exe := filepath.Join(t.TempDir(), "old", "bridge.exe")
	for _, path := range []string{exe, filepath.Join(t.TempDir(), "new", "bridge.exe")} {
		raw, err := processHookConfig(root, path, config, p)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &root); err != nil {
			t.Fatal(err)
		}
	}
	hooks := root["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	stop := events["Stop"].([]any)
	if root["theme"] != "dark" || hooks["enabled"] != false || hooks["timeoutMs"] != float64(5000) || len(stop) != 2 {
		t.Fatal("用户配置被覆盖或升级重复安装")
	}
	kept := stop[0].(map[string]any)
	if kept["matcher"] != "keep" || kept["hooks"].([]any)[0].(map[string]any)["command"] != "custom.exe" {
		t.Fatal("用户钩子被删除")
	}
	handler := stop[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if handler["onError"] != "allow" || handler["args"].([]any)[2] != config {
		t.Fatal("参数或失败策略被改变")
	}
	raw, err := processHookConfig(map[string]any{}, exe, config, p)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &root) != nil || root["hooks"].(map[string]any)["enabled"] != true {
		t.Fatal("新安装没有启用钩子")
	}
}

func TestProcessHooksRejectInvalidShape(t *testing.T) {
	for _, input := range []string{`{"hooks":[]}`, `{"hooks":{"events":[]}}`, `{"hooks":{"events":{"Stop":{}}}}`, `{"hooks":{"events":{"Stop":[{"hooks":{}}]}}}`} {
		var root map[string]any
		_ = json.Unmarshal([]byte(input), &root)
		if _, err := processHookConfig(root, "bridge.exe", "config.json", Package{}); err == nil {
			t.Fatal("接受了无效用户配置", input)
		}
	}
}
