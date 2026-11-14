package adapters

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-pulse/internal/config"

	"gopkg.in/yaml.v3"
)

func TestSetupCreatesPersistentHostIdentityAndPromptsHub(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "bridge.yaml")
	input := "\n" + strings.Repeat("s\n", 5)
	var output bytes.Buffer
	if err := SetupInteractive(context.Background(), path, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Hub 地址") {
		t.Fatal("新建配置时未提示 Hub 地址")
	}
	readIdentity := func() (string, string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			SourceID string `yaml:"source_id"`
			HostName string `yaml:"host_name"`
		}
		if err := yaml.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		return config.SourceID, config.HostName
	}
	firstID, hostName := readIdentity()
	if !strings.HasPrefix(firstID, "bridge-") || len(firstID) != len("bridge-")+48 {
		t.Fatalf("主机指纹格式无效：%q", firstID)
	}
	if hostName == "" {
		t.Fatal("主机名默认值为空")
	}

	if err := SetupInteractive(context.Background(), path, strings.NewReader(strings.Repeat("s\n", 5)), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	secondID, _ := readIdentity()
	if secondID != firstID {
		t.Fatalf("重复 Setup 改变了主机指纹：%q -> %q", firstID, secondID)
	}
}

func TestSetupListsDetectedAndPreviouslyManagedAgentsOnly(t *testing.T) {
	items := []setupAgent{
		{ID: "installed", Detected: true},
		{ID: "missing"},
		{ID: "previously-managed"},
	}
	selected := selectSetupAgents(items, map[string]string{"previously-managed": "config.json"})
	if len(selected) != 2 || selected[0].ID != "installed" || selected[1].ID != "previously-managed" {
		t.Fatalf("交互列表未按安装和管理状态筛选：%+v", selected)
	}
	if got := selectSetupAgents(items, nil); len(got) != 1 || got[0].ID != "installed" {
		t.Fatalf("未安装 Agent 不应显示：%+v", got)
	}
}

func TestSetupExamplePromptsHubAndPreservesEnvironmentReferences(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir())
	for _, tc := range []struct {
		name, source, input, wantSource, wantHub string
		wantPrompt                               bool
	}{
		{name: "未初始化示例", input: "http://hub.example.invalid:18700\n", wantHub: "http://hub.example.invalid:18700", wantPrompt: true},
		{name: "已有环境变量指纹", source: "${env:TEST_BRIDGE_SOURCE}", wantSource: "${env:TEST_BRIDGE_SOURCE}", wantHub: "${env:TEST_BRIDGE_HUB}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_BRIDGE_SOURCE", "stable-source-value")
			t.Setenv("TEST_BRIDGE_HUB", "http://hub.example.invalid:18700")
			t.Setenv("TEST_BRIDGE_HOST", "用户主机显示名")
			path := filepath.Join(t.TempDir(), "bridge.yaml")
			raw := "source_id: '" + tc.source + "'\nhost_name: '${env:TEST_BRIDGE_HOST}'\nhub_url: '${env:TEST_BRIDGE_HUB}'\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := SetupInteractive(context.Background(), path, strings.NewReader(tc.input+strings.Repeat("s\n", 5)), &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "Hub 地址（") != tc.wantPrompt {
				t.Fatalf("Hub 提示不符合预期：%s", output.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document yaml.Node
			if err := yaml.Unmarshal(after, &document); err != nil {
				t.Fatal(err)
			}
			root := document.Content[0]
			if tc.wantSource != "" && scalarYAML(root, "source_id") != tc.wantSource {
				t.Fatal("既有指纹环境变量被展开写回")
			}
			if scalarYAML(root, "hub_url") != tc.wantHub || scalarYAML(root, "host_name") != "${env:TEST_BRIDGE_HOST}" {
				t.Fatal("未修改字段的环境变量引用被覆盖")
			}
			if _, err := config.LoadBridge(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetupRejectsInvalidHubBeforeWriting(t *testing.T) {
	for _, address := range []string{"ftp://example.invalid", "http://example.invalid/api", "http://example.invalid?token=secret", "not a URL"} {
		for _, existing := range []bool{false, true} {
			t.Run(address+fmt.Sprint(existing), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "bridge.yaml")
				original := []byte("hub_url: 'http://127.0.0.1:18700'\n")
				if existing {
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				err := SetupInteractive(context.Background(), path, strings.NewReader(address+"\n"), &bytes.Buffer{})
				if err == nil {
					t.Fatal("应在写入前拒绝无效 Hub 地址")
				}
				after, readErr := os.ReadFile(path)
				if existing && (readErr != nil || !bytes.Equal(after, original)) {
					t.Fatal("无效输入改变了原配置")
				}
				if !existing && !os.IsNotExist(readErr) {
					t.Fatal("无效输入不应创建配置文件")
				}
			})
		}
	}
}

// 不调用真实宿主，直接验证 Windows 与 Linux 的默认来源边界。
func TestSetupAgentCatalogMatchesPlatformScope(t *testing.T) {
	for _, tc := range []struct {
		platform string
		want     []string
	}{
		{"windows", []string{"zcode"}},
		{"linux", []string{"codex", "antigravity", "qoderclicn", "opencode"}},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			home := t.TempDir()
			items := setupAgentCatalog(home, tc.platform)
			if len(items) != len(tc.want) {
				t.Fatalf("默认来源数量错误：%+v", items)
			}
			for i, id := range tc.want {
				if items[i].ID != id {
					t.Fatalf("默认来源顺序或范围错误：%+v", items)
				}
				relative, err := filepath.Rel(home, items[i].ConfigPath)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					t.Fatalf("默认配置超出测试用户目录：%q", items[i].ConfigPath)
				}
			}
		})
	}
}

// 收敛默认来源不删除用户既有的其他 managed 登记或宿主配置。
func TestSetupScopeChangePreservesOtherManagedEntries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bridge.yaml")
	expected := map[string]string{
		"codex":       filepath.Join(root, "old-codex.json"),
		"antigravity": filepath.Join(root, "existing-agy.json"),
		"qoderclicn":  filepath.Join(root, "existing-qoder.json"),
		"opencode":    filepath.Join(root, "existing-opencode.json"),
	}
	raw, err := yaml.Marshal(map[string]any{"source_id": "scope-test", "host_name": "测试主机", "hub_url": "http://hub.example.invalid:18700", "adapters": map[string]any{"directory": filepath.Join(root, "adapters"), "managed": expected}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreateSetupConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	expected["codex"] = filepath.Join(root, "new-codex.json")
	c.Adapters.Managed["codex"] = expected["codex"]
	if err = writeSetupConfig(path, c, map[string]bool{"codex": true}, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := config.ParseBridge(data)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range expected {
		if result.Adapters.Managed[id] != want {
			t.Fatalf("其他来源登记被清除或改写：来源=%s，收到=%q", id, result.Adapters.Managed[id])
		}
	}
}

func TestSetupAutomaticallyReusesOpenCodeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		files    []string
		wantFile string
	}{
		{"尚无配置", nil, "opencode.json"},
		{"已有JSON", []string{"opencode.json"}, "opencode.json"},
		{"已有JSONC", []string{"opencode.jsonc"}, "opencode.jsonc"},
		{"两者均在", []string{"opencode.json", "opencode.jsonc"}, "opencode.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			directory := filepath.Join(home, ".config", "opencode")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(`{"provider":"保留原设置"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := filepath.Join(directory, tc.wantFile)
			if got := defaultOpenCodeConfigPath(home); got != want {
				t.Fatalf("未沿用已有配置：%s，预期 %s", got, want)
			}
			for _, agent := range setupAgentCatalog(home, "linux") {
				if agent.ID == "opencode" && agent.ConfigPath != want {
					t.Fatal("扫描目录未使用自动识别路径")
				}
			}
		})
	}
}

func TestSetupSummaryListsNamesVersionsPathsAndOnlyRemindsEnabledCodex(t *testing.T) {
	agents := []setupAgent{
		{ID: "codex", Name: "Codex CLI", Version: "0.167.2"},
		{ID: "opencode", Name: "OpenCode", Version: "2.0.20"},
	}
	paths := map[string]string{"codex": "用户目录/codex.json", "opencode": "用户目录/opencode.jsonc"}
	results := map[string]string{"codex": "接入配置已完成", "opencode": "接入配置已完成"}
	for _, enabled := range []bool{false, true} {
		var output bytes.Buffer
		printSetupSummary(&output, agents, paths, results, "用户目录/bridge.yaml", enabled)
		text := output.String()
		for _, expected := range []string{"Codex CLI", "0.167.2", paths["codex"], "OpenCode", "2.0.20", paths["opencode"]} {
			if !strings.Contains(text, expected) {
				t.Fatalf("配置结果缺少名称、版本或路径 %q：%s", expected, text)
			}
		}
		if strings.Contains(text, "请在 /hooks") != enabled {
			t.Fatal("Codex 额外提醒不符合本次启用状态")
		}
		if strings.Contains(text, "请重启") || strings.Contains(text, "OpenCode 请") {
			t.Fatal("不应要求所有 Agent 做额外授权或手填 OpenCode 路径")
		}
		if strings.Index(text, "本次配置操作已结束") > strings.Index(text, "以后需要") {
			t.Fatal("应在配置结束后提示 setup 的维护用途")
		}
	}
}

func TestSetupAnnouncesConfigurationBeforePromptsAndFinishesWithMaintenanceHint(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir())
	path := filepath.Join(t.TempDir(), "bridge.yaml")
	var output bytes.Buffer
	if err := SetupInteractive(context.Background(), path, strings.NewReader("\n"), &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Index(text, "开始配置 Agent 接入") >= strings.Index(text, "Hub 地址（") {
		t.Fatal("配置开始说明必须在交互输入前")
	}
	if strings.Index(text, "已保存") < 0 || strings.Index(text, "安装 Agent 后") < 0 || strings.Index(text, "已保存") > strings.Index(text, "安装 Agent 后") {
		t.Fatal("未检测到 Agent 时也应先保存，再说明以后如何配置")
	}
}

func TestSetupIncompleteConfigurationReturnsErrorWithoutSuccessOrTrustHint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("此回归只用 Linux 隔离命令检查配置提示，不模拟 Windows 宿主")
	}
	home := t.TempDir()
	tools := filepath.Join(home, "tools")
	if err := os.MkdirAll(tools, 0700); err != nil {
		t.Fatal(err)
	}
	// 只提供版本回执，不启动真实宿主或调用模型。
	fake := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'codex-cli 0.167.2\\n'; else exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(tools, "codex"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", tools)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer hub.Close()
	path := filepath.Join(home, "bridge.yaml")
	raw := "source_id: setup-test\nhub_url: " + hub.URL + "\nadapters:\n  directory: " + filepath.Join(home, "adapters") + "\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := SetupInteractive(context.Background(), path, strings.NewReader("e\n\n"), &output)
	if err == nil {
		t.Fatal("Hub 没有发布公钥时不应返回成功，让安装器误报完成")
	}
	text := output.String()
	if !strings.Contains(text, "接入尚未完成") || !strings.Contains(text, "Codex CLI") || !strings.Contains(text, "0.167.2") {
		t.Fatalf("应明确列出未完成的 Agent 与版本：%s", text)
	}
	if strings.Contains(text, "请在 /hooks") || strings.Contains(text, "接入配置已完成") {
		t.Fatal("未安装成功不应提示已经完成或要求审核不存在的钩子")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Fatal("失败前不应创建 Agent 钩子")
	}
}

func TestSetupDoesNotRequireSpecialAntigravityLauncher(t *testing.T) {
	var out bytes.Buffer
	printSetupSummary(&out, []setupAgent{{ID: "antigravity", Name: "Antigravity CLI", Version: "1.5.9"}}, map[string]string{"antigravity": "/配置/hooks.json"}, map[string]string{"antigravity": "接入配置已完成"}, "/配置/bridge.yaml", false)
	if strings.Contains(out.String(), "run-adapter") || strings.Contains(out.String(), "包装") || strings.Contains(out.String(), "不会采集") {
		t.Fatal("配置后必须允许正常直接启动", out.String())
	}
}
