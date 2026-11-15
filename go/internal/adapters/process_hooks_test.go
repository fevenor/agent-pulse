package adapters

import (
	"agent-pulse/internal/config"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSetupVerifiesActualHooksAndRespectsDisabledSwitch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zcode.json")
	bridgePath := filepath.Join(root, "bridge.yaml")
	p := Package{Manifest: Manifest{ID: "zcode", Kind: "executable", ConfigStyle: "process-hooks", Hooks: map[string]string{"Stop": ""}}}
	c := config.Bridge{Adapters: config.AdapterUpdates{Directory: root, Managed: map[string]string{"zcode": path}}}
	state := State{Installed: map[string]Installed{"zcode": {Package: p}}, Rejected: map[string]string{}}
	if err := AtomicJSON(filepath.Join(root, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw    string
		wantOK bool
	}{{`{}`, true}, {`{"hooks":{"enabled":false}}`, false}} {
		next, err := hookConfig(c, bridgePath, p, []byte(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, next, 0600); err != nil {
			t.Fatal(err)
		}
		err = verifySetupHooks(c, bridgePath, "zcode")
		if (err == nil) != tc.wantOK {
			t.Fatal(tc.raw, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if verifySetupHooks(c, bridgePath, "zcode") == nil {
		t.Fatal("只凭安装记录不能报告实际钩子存在")
	}
}

func TestProcessHookSameFileVariantsAreReplacedAndDisabledWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "中文 空格 bridge.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "同文件.json")
	if runtime.GOOS == "windows" {
		alias = `\\?\` + cfg
	} else if err := os.Link(cfg, alias); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "new", "bridge.exe")
	oldExe := filepath.Join(dir, "old", "bridge.exe")
	handler := func(path, command string) map[string]any {
		return map[string]any{"type": "process", "command": command, "args": []any{"dispatch", "--config", path, "--adapter", "zcode", "--hook", "Stop"}}
	}
	p := Package{Manifest: Manifest{ID: "zcode", ConfigStyle: "process-hooks", Hooks: map[string]string{"Stop": ""}}}
	root := map[string]any{"keep": "用户配置", "hooks": map[string]any{"enabled": true, "events": map[string]any{"Stop": []any{map[string]any{"hooks": []any{handler(cfg, oldExe), handler(alias, oldExe), handler(other, oldExe), handler(cfg, "user.exe")}}}}}}
	raw, err := processHookConfig(root, exe, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"dispatch"`) != 3 {
		t.Fatal("等价路径没有去掉重复自有钩子", string(raw))
	}
	if !strings.Contains(string(raw), "用户配置") || !strings.Contains(string(raw), "other.json") || !strings.Contains(string(raw), "user.exe") {
		t.Fatal("其他授权或用户钩子被改动")
	}
	if json.Unmarshal(raw, &root) != nil {
		t.Fatal("配置无效")
	}
	again, err := processHookConfig(root, exe, alias, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(again), `"dispatch"`) != 3 {
		t.Fatal("再次同步重复登记", string(again))
	}
	if json.Unmarshal(again, &root) != nil {
		t.Fatal("配置无效")
	}
	disabled, err := updateProcessHookConfig(root, exe, cfg, p, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(disabled), `"dispatch"`) != 2 {
		t.Fatal("停用没有移除等价路径自有条目", string(disabled))
	}
	if sameHookConfigFile(other, cfg) || sameHookConfigFile(12, cfg) || sameHookConfigFile("relative.json", cfg) {
		t.Fatal("不应认领其他配置或非路径")
	}
}
