package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-pulse/internal/config"
	"gopkg.in/yaml.v3"
)

const dshSamplePatch = `# Your patch layer for this dsh profile, applied after every bundle layer:
# a top-level YAML array of loader patch entries.
- id: llm-pi-ai
  name: "@deepseek-ai/dsh-llm-pi-ai"
  config:
    providers:
      axonhub:
        apiKeyEnv: AXONHUB_APIKEY
- id: model-discovery
  name: "@local/dsh-model-discovery"
  config:
    enabled: !!js "!ctx.get('profileContext')"
    since: 2026-09-28
- insert:
    - id: other-plugin
      name: "./plugins/other.js"
      config: {}
`

func dshFixture(t *testing.T, patch string) (config.Bridge, Package, string) {
	t.Helper()
	home := t.TempDir()
	directory := filepath.Join(home, "adapters")
	patchPath := filepath.Join(home, "profiles", "web", "cordis.patch.yml")
	write(t, patchPath, patch)
	c := config.Bridge{SourceID: "host-dsh", Listen: "unix:/tmp/agent-pulse-1000/host.sock", Adapters: config.AdapterUpdates{Directory: directory, Managed: map[string]string{"dsh": patchPath}}}
	p := Package{Manifest: Manifest{ID: "dsh", Version: "0.1.0", Format: 1, Protocol: 1, Platform: "any", Kind: "dsh-plugin", Entry: "index.js", Command: "dsh", AgentMin: "0.2.3", AgentMax: "0.2.3", Name: "DeepSeek Harness"}}
	return c, p, patchPath
}

func readPatch(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDSHProfilePatchEnableKeepsOtherEntriesCommentsAndTags(t *testing.T) {
	c, p, _ := dshFixture(t, dshSamplePatch)
	next, err := updateHookConfig(c, "", p, []byte(dshSamplePatch), true)
	if err != nil {
		t.Fatal(err)
	}
	out := string(next)
	for _, keep := range []string{"# Your patch layer for this dsh profile", "apiKeyEnv: AXONHUB_APIKEY", "!!js \"!ctx.get('profileContext')\"", "id: other-plugin", "./plugins/other.js", "2026-09-28"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("非本软件内容丢失：%s\n%s", keep, out)
		}
	}
	loader := dshLoaderURL(c.Adapters.Directory, "dsh", &p)
	if !strings.Contains(out, "id: agent-pulse") || !strings.Contains(out, "name: "+loader) {
		t.Fatalf("未写入受管理行：\n%s", out)
	}
	if !strings.Contains(out, "source_id: host-dsh") || !strings.Contains(out, "bridge_address: unix:/tmp/agent-pulse-1000/host.sock") {
		t.Fatalf("未写入 Bridge 配置：\n%s", out)
	}
	// 结果仍是合法的 patch 数组，且再次启用幂等。
	var doc yaml.Node
	if err = yaml.Unmarshal(next, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode {
		t.Fatalf("输出不是 YAML 数组：%v\n%s", err, out)
	}
	again, err := updateHookConfig(c, "", p, next, true)
	if err != nil || string(again) != out {
		t.Fatal("重复启用不应改变配置", err)
	}
	// 来源变化时只替换本行 config。
	c.SourceID = "host-renamed"
	changed, err := updateHookConfig(c, "", p, next, true)
	if err != nil || strings.Contains(string(changed), "host-dsh") || !strings.Contains(string(changed), "source_id: host-renamed") {
		t.Fatal("来源变化后未更新受管理行", err)
	}
	if strings.Count(string(changed), "id: agent-pulse") != 1 {
		t.Fatal("受管理行重复")
	}
}

func TestDSHProfilePatchDisableRemovesOwnedRowOnly(t *testing.T) {
	c, p, _ := dshFixture(t, dshSamplePatch)
	enabled, err := updateHookConfig(c, "", p, []byte(dshSamplePatch), true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := updateHookConfig(c, "", p, enabled, false)
	if err != nil {
		t.Fatal(err)
	}
	out := string(disabled)
	if strings.Contains(out, "agent-pulse") || strings.Contains(out, "AgentPulse 受管理行") {
		t.Fatalf("禁用后仍保留受管理行：\n%s", out)
	}
	for _, keep := range []string{"# Your patch layer", "!!js", "other-plugin", "apiKeyEnv: AXONHUB_APIKEY"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("禁用改写了其他条目：%s\n%s", keep, out)
		}
	}
	// 不存在本软件行时禁用不改动文件。
	untouched, err := updateHookConfig(c, "", p, []byte(dshSamplePatch), false)
	if err != nil || string(untouched) != dshSamplePatch {
		t.Fatal("无本软件行时禁用不应改写文件")
	}
}

func TestDSHProfilePatchCreatesFileAndRejectsConflicts(t *testing.T) {
	c, p, path := dshFixture(t, "")
	created, err := updateHookConfig(c, "", p, nil, true)
	if err != nil || !strings.Contains(string(created), "id: agent-pulse") {
		t.Fatal("空配置应创建受管理行", err)
	}
	for _, tc := range []struct{ name, patch string }{
		{"不是数组", "id: x\nconfig: {}\n"},
		{"其他来源同名行", "- insert:\n    - id: agent-pulse\n      name: ./my-copy/index.js\n      config: {}\n"},
		{"手动覆盖行", "- id: agent-pulse\n  config:\n    source_id: manual\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p, path := dshFixture(t, tc.patch)
			if _, err := updateHookConfig(c, "", p, []byte(tc.patch), true); err == nil {
				t.Fatal("冲突配置必须拒绝")
			}
			if readPatch(t, path) != tc.patch {
				t.Fatal("拒绝时改写了原配置")
			}
		})
	}
	// 已手动安装 bundle 的 profile 不再插入受管理行。
	write(t, filepath.Join(filepath.Dir(path), "package.json"), `{"name":"dsh-profile-web","dependencies":{"dsh-agent-pulse-adapter":"file:/x"},"dsh":{"profile":{"bundles":["dsh-agent-pulse-adapter"]}}}`)
	if _, err = updateHookConfig(c, "", p, nil, true); err == nil || !strings.Contains(err.Error(), "手动安装") {
		t.Fatal("手动安装 bundle 时必须拒绝受管理安装", err)
	}
}

// 升级接管：旧版受管理行的 name 不带版本查询串，同一 loader 路径必须被识别为本安装器所有并替换为新 URL。
func TestDSHProfilePatchTakesOverLegacyManagedRow(t *testing.T) {
	c, p, _ := dshFixture(t, "")
	p.SHA256 = strings.Repeat("e", 64)
	loader := dshLoaderURL(c.Adapters.Directory, "dsh", &p)
	legacy := loaderPath(loader)
	if legacy == loader {
		t.Fatal("测试前提：loader URL 必须带版本查询串")
	}
	withLegacy, err := updateHookConfig(c, "", p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧版 Bridge 写出的行：把新 URL 换成无查询串的旧 URL。
	stale := strings.Replace(string(withLegacy), loader, legacy, 1)
	// 模拟升级：行里是历史版本的 ?v=<旧摘要>，同样必须被接管。
	older := Package{SHA256: strings.Repeat("f", 64)}
	historical := strings.Replace(string(withLegacy), loader, dshLoaderURL(c.Adapters.Directory, "dsh", &older), 1)
	upgradedHistorical, err := updateHookConfig(c, "", p, []byte(historical), true)
	if err != nil {
		t.Fatalf("历史版本的受管理行应被接管：%v", err)
	}
	if strings.Count(string(upgradedHistorical), "id: agent-pulse") != 1 || !strings.Contains(string(upgradedHistorical), "name: "+loader+"\n") {
		t.Fatalf("接管历史版本后应只剩一条当前 loader URL 的受管理行：\n%s", upgradedHistorical)
	}
	upgraded, err := updateHookConfig(c, "", p, []byte(stale), true)
	if err != nil {
		t.Fatalf("旧版受管理行应被接管而不是拒绝：%v", err)
	}
	if strings.Count(string(upgraded), "id: agent-pulse") != 1 || !strings.Contains(string(upgraded), "name: "+loader) || strings.Contains(string(upgraded), "name: "+legacy+"\n") {
		t.Fatalf("接管后应只剩一条新 URL 的受管理行：\n%s", upgraded)
	}
	// 禁用同样能移除旧版行。
	removed, err := updateHookConfig(c, "", p, []byte(stale), false)
	if err != nil || strings.Contains(string(removed), "agent-pulse") {
		t.Fatal("禁用应能移除旧版受管理行", err)
	}
	// 但路径不同的行仍是"其他来源"，必须拒绝。
	foreign := strings.Replace(string(stale), legacy, "file:///opt/other/agent-pulse/index.js", 1)
	if _, err = updateHookConfig(c, "", p, []byte(foreign), true); err == nil {
		t.Fatal("其他来源的 agent-pulse 行必须拒绝")
	}
}

func TestDSHManifestValidate(t *testing.T) {
	m := Manifest{ID: "dsh", Version: "0.1.0", Format: 1, Protocol: 1, Platform: "any", Kind: "dsh-plugin", Entry: "index.js", Command: "dsh", AgentMin: "0.2.3", AgentMax: "0.2.3"}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if warning, err := hostCompatibility("0.2.3-alpha.2", m); err != nil || warning == "" {
		t.Fatal("预发布后缀应按 x.y.z 版本线兼容并给出提示", err, warning)
	}
	if warning, err := hostCompatibility("0.3.0", m); err != nil || warning == "" {
		t.Fatal("次版本变化应允许使用但提示", err, warning)
	}
	if _, err := hostCompatibility("1.0.0", m); err == nil {
		t.Fatal("主版本变化应拒绝")
	}
}

// 完整走向导：探测 dsh 版本、下载签名包、写 loader 与 profile 行；重复执行幂等。
func TestSetupDSHInstallsLoaderAndProfileRow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("DSH 受管理接入当前只验收 Linux 宿主")
	}
	home := t.TempDir()
	tools := filepath.Join(home, "tools")
	write(t, filepath.Join(tools, "dsh"), "#!/bin/sh\nprintf '0.2.3-alpha.2\\n'\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", tools)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	repo := filepath.Join(home, "repository")
	private := filepath.Join(home, "publisher.key")
	public := filepath.Join(home, "publisher.pub")
	if err := GenerateKey(private, public); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(public)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "source")
	write(t, filepath.Join(source, "index.js"), "export const inject = []; export function apply() {}\n")
	write(t, filepath.Join(source, "adapter.js"), "export {};\n")
	write(t, filepath.Join(source, "locale", "en.json"), `{"meta":{"title":"AgentPulse"}}`+"\n")
	write(t, filepath.Join(source, "icon.svg"), "<svg xmlns=\"http://www.w3.org/2000/svg\"/>\n")
	pkg, err := BuildPackage(source, repo, Manifest{ID: "dsh", Version: "0.1.0", Format: 1, Protocol: 1, Platform: "any", Kind: "dsh-plugin", Entry: "index.js", Command: "dsh", AgentMin: "0.2.3", AgentMax: "0.2.3", Name: "DeepSeek Harness"})
	if err != nil {
		t.Fatal(err)
	}
	if err = SignIndex(repo, private, Index{Sequence: 1, Packages: []Package{pkg}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/client-config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"public_key": strings.TrimSpace(string(pub))})
	})
	mux.HandleFunc("GET /api/v1/adapters/index", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join(repo, "index.json")) })
	mux.HandleFunc("GET /api/v1/adapters/packages/{hash}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(repo, "packages", r.PathValue("hash")+".zip"))
	})
	hub := httptest.NewServer(mux)
	t.Cleanup(hub.Close)
	patchPath := dshProfilePatchPath(home)
	write(t, patchPath, dshSamplePatch)
	c := config.Bridge{SourceID: "setup-dsh", HubURL: hub.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(home, "adapters"), Managed: map[string]string{"dsh": patchPath}}}
	data, err := yaml.Marshal(map[string]any{"source_id": c.SourceID, "listen": c.Listen, "hub_url": c.HubURL, "adapters": map[string]any{"directory": c.Adapters.Directory, "managed": c.Adapters.Managed}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "bridge.yaml")
	write(t, configPath, string(data))
	for pass := 0; pass < 2; pass++ {
		var output bytes.Buffer
		if err := SetupInteractive(context.Background(), configPath, strings.NewReader("e\n\n"), &output); err != nil {
			t.Fatalf("DSH 向导应完成：%v\n%s", err, output.String())
		}
		if !strings.Contains(output.String(), "接入配置已完成") || strings.Contains(output.String(), "未完成") {
			t.Fatalf("向导没有明确报告成功：\n%s", output.String())
		}
		state, err := loadState(c.Adapters.Directory)
		if err != nil {
			t.Fatal(err)
		}
		installed := state.Installed["dsh"]
		if installed.Package.SHA256 != pkg.SHA256 || installed.Error != "" || installed.AgentVersion != "0.2.3-alpha.2" {
			t.Fatalf("安装状态不正确：%+v", installed)
		}
		raw := readPatch(t, patchPath)
		loader := dshLoaderURL(c.Adapters.Directory, "dsh", &pkg)
		for _, want := range []string{"# Your patch layer", "!!js", "other-plugin", "id: agent-pulse", "name: " + loader, "source_id: setup-dsh"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("profile 缺少 %s：\n%s", want, raw)
			}
		}
		if strings.Count(raw, "id: agent-pulse") != 1 {
			t.Fatalf("重复执行后受管理行重复：\n%s", raw)
		}
		loaderJS, err := os.ReadFile(filepath.Join(c.Adapters.Directory, "loaders", "dsh", "index.js"))
		if err != nil || !bytes.Contains(loaderJS, []byte(`"packages", id, p.version, p.sha256, p.entry`)) || !bytes.Contains(loaderJS, []byte(`"runtime"`)) {
			t.Fatal("固定 loader 未写入或缺少当前包/回执逻辑", err)
		}
		// Node 按 URL 缓存 ESM：loader 必须带版本查询串，否则包升级后 profile 重组仍运行旧版本。
		if !bytes.Contains(loaderJS, []byte(`?v=" + p.sha256`)) {
			t.Fatal("固定 loader 缺少版本查询串，升级后不会加载新版本")
		}
		for _, name := range []string{"package.json", "locale/en.json", "icon.svg"} {
			if _, err := os.Stat(filepath.Join(c.Adapters.Directory, "loaders", "dsh", filepath.FromSlash(name))); err != nil {
				t.Fatalf("loader 目录缺少 %s", name)
			}
		}
		// 用真实 Node 导入固定 loader：应经 state.json 定位当前包并导出 inject/apply，apply 后写下运行回执。
		if node, err := exec.LookPath("node"); err == nil {
			script := `const m = await import(process.argv[1]); if (!Array.isArray(m.inject) || typeof m.apply !== "function") process.exit(2); m.apply({ on() {}, effect() {} }, {});`
			cmd := exec.Command(node, "--input-type=module", "-e", script, "--", dshLoaderURL(c.Adapters.Directory, "dsh", &pkg))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("固定 loader 无法导入：%v\n%s", err, out)
			}
			var running Runtime
			if raw, err := os.ReadFile(filepath.Join(c.Adapters.Directory, "runtime", "dsh.json")); err != nil || json.Unmarshal(raw, &running) != nil || running.Version != pkg.Version {
				t.Fatalf("loader 未写运行回执：%v %+v", err, running)
			}
		}
	}
}
