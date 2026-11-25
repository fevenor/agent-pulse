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
	p := Package{Manifest: Manifest{ID: "dsh", Version: "0.1.0", Format: 1, Protocol: 1, Platform: "any", Kind: "dsh-plugin", Entry: "index.js", Command: "dsh", AgentMin: "0.2.3", AgentMax: "0.2.3", Name: "DeepSeek Harness", Control: "control"}, SHA256: strings.Repeat("a", 64)}
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
	next, err := updateDSHProfilePatch(c, p, []byte(dshSamplePatch), true)
	if err != nil {
		t.Fatal(err)
	}
	out := string(next)
	for _, keep := range []string{"# Your patch layer for this dsh profile", "apiKeyEnv: AXONHUB_APIKEY", "!!js \"!ctx.get('profileContext')\"", "id: other-plugin", "./plugins/other.js", "2026-09-28"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("非本软件内容丢失：%s\n%s", keep, out)
		}
	}
	loader := dshEntryURL(c.Adapters.Directory, p)
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
	again, err := updateDSHProfilePatch(c, p, next, true)
	if err != nil || string(again) != out {
		t.Fatal("重复启用不应改变配置", err)
	}
	// 来源变化时只替换本行 config。
	c.SourceID = "host-renamed"
	changed, err := updateDSHProfilePatch(c, p, next, true)
	if err != nil || strings.Contains(string(changed), "host-dsh") || !strings.Contains(string(changed), "source_id: host-renamed") {
		t.Fatal("来源变化后未更新受管理行", err)
	}
	if strings.Count(string(changed), "id: agent-pulse") != 1 {
		t.Fatal("受管理行重复")
	}
}

func TestDSHProfilePatchDisableRemovesOwnedRowOnly(t *testing.T) {
	c, p, _ := dshFixture(t, dshSamplePatch)
	enabled, err := updateDSHProfilePatch(c, p, []byte(dshSamplePatch), true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := updateDSHProfilePatch(c, p, enabled, false)
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
	untouched, err := updateDSHProfilePatch(c, p, []byte(dshSamplePatch), false)
	if err != nil || string(untouched) != dshSamplePatch {
		t.Fatal("无本软件行时禁用不应改写文件")
	}
}

func TestDSHProfilePatchCreatesFileAndRejectsConflicts(t *testing.T) {
	c, p, path := dshFixture(t, "")
	created, err := updateDSHProfilePatch(c, p, nil, true)
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
			if _, err := updateDSHProfilePatch(c, p, []byte(tc.patch), true); err == nil {
				t.Fatal("冲突配置必须拒绝")
			}
			if readPatch(t, path) != tc.patch {
				t.Fatal("拒绝时改写了原配置")
			}
		})
	}
	// 已手动安装 bundle 的 profile 不再插入受管理行。
	write(t, filepath.Join(filepath.Dir(path), "package.json"), `{"name":"dsh-profile-web","dependencies":{"dsh-agent-pulse-adapter":"file:/x"},"dsh":{"profile":{"bundles":["dsh-agent-pulse-adapter"]}}}`)
	if _, err = updateDSHProfilePatch(c, p, nil, true); err == nil || !strings.Contains(err.Error(), "手动安装") {
		t.Fatal("手动安装 bundle 时必须拒绝受管理安装", err)
	}
}

// 现有受管理行升级到不可变入口；其他安装来源不接管。
func TestDSHProfilePatchUpdatesOwnedEntries(t *testing.T) {
	c, p, _ := dshFixture(t, "")
	current, err := updateDSHProfilePatch(c, p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	target := dshEntryURL(c.Adapters.Directory, p)
	older := p
	older.Version, older.SHA256 = "0.0.9", strings.Repeat("b", 64)
	fixed := dshFileURL(filepath.Join(c.Adapters.Directory, "loaders", "dsh", "index.js"))
	for _, previous := range []string{fixed, fixed + "?v=" + older.SHA256, dshEntryURL(c.Adapters.Directory, older)} {
		stale := []byte(strings.Replace(string(current), target, previous, 1))
		updated, err := updateDSHProfilePatch(c, p, stale, true)
		if err != nil || !bytes.Equal(updated, current) {
			t.Fatalf("自有行升级失败：%v\n%s", err, updated)
		}
		removed, err := updateDSHProfilePatch(c, p, stale, false)
		if err != nil || bytes.Contains(removed, []byte("agent-pulse")) {
			t.Fatal("停用自有行失败", err)
		}
	}
	foreign := []byte(strings.Replace(string(current), target, "file:///opt/other/agent-pulse/index.js", 1))
	if _, err := updateDSHProfilePatch(c, p, foreign, true); err == nil {
		t.Fatal("其他来源同名行应保留并报告冲突")
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

// 完整走随包控制入口和向导，宿主直接加载不可变包；包括未提交安装与恢复窗口。
func TestSetupDSHInstallsPackageEntryAndProfileRow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("DSH 受管理接入当前只验收 Linux 宿主")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("真实插件导入检查需要 Node")
	}
	resolvedNode, err := exec.Command(node, "-p", "process.execPath").Output()
	if err != nil {
		t.Fatal(err)
	}
	node = strings.TrimSpace(string(resolvedNode))
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
	adapterRoot := filepath.Join("..", "..", "..", "adapters", "dsh")
	var manifest Manifest
	manifestRaw, err := os.ReadFile(filepath.Join(adapterRoot, "manifest.json"))
	if err != nil || json.Unmarshal(manifestRaw, &manifest) != nil {
		t.Fatal("无法读取 DSH 清单", err)
	}
	manifest.Platform = runtime.GOOS + "/" + runtime.GOARCH
	for _, name := range []string{"index.js", "adapter.js", "delivery.js", "package.json"} {
		data, err := os.ReadFile(filepath.Join(adapterRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(source, name), string(data))
	}
	own, _ := os.Executable()
	program, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(source, "control"), string(program))
	t.Setenv("AGENT_PULSE_BUILTIN_CONTROL_FIXTURE", "1")
	pkg, err := BuildPackage(source, repo, manifest)
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
	c := config.Bridge{SourceID: "setup-dsh", HubURL: hub.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(home, "adapters"), Managed: map[string]string{}}}
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
		loader := dshEntryURL(c.Adapters.Directory, pkg)
		for _, want := range []string{"# Your patch layer", "!!js", "other-plugin", "id: agent-pulse", "name: " + loader, "source_id: setup-dsh"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("profile 缺少 %s：\n%s", want, raw)
			}
		}
		if strings.Count(raw, "id: agent-pulse") != 1 {
			t.Fatalf("重复执行后受管理行重复：\n%s", raw)
		}
		if _, err := os.Stat(filepath.Join(c.Adapters.Directory, "loaders", "dsh")); !os.IsNotExist(err) {
			t.Fatal("Bridge 不应生成 DSH loader")
		}
	}
	c, err = config.LoadBridge(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// 不管 journal 指向空安装还是旧版本，直接导入的包都只能是 URL 指定的不可变版本。
	installedState, err := loadState(c.Adapters.Directory)
	if err != nil {
		t.Fatal(err)
	}
	old := pkg
	old.Version, old.SHA256 = "0.0.9", strings.Repeat("f", 64)
	for _, before := range []State{
		{Installed: map[string]Installed{}, Rejected: map[string]string{}},
		{Installed: map[string]Installed{"dsh": {Package: old}}, Rejected: map[string]string{}},
	} {
		if err := AtomicJSON(filepath.Join(c.Adapters.Directory, "pending.json"), installJournal{State: before}); err != nil {
			t.Fatal(err)
		}
		if err := AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), before); err != nil {
			t.Fatal(err)
		}
		script := `import {readFileSync,rmSync} from "node:fs";
const [url, receipt, pending] = process.argv.slice(1);
const m = await import(url);
if (!Array.isArray(m.inject) || typeof m.apply !== "function") throw Error("插件入口无效");
const config = {source_id: "setup-dsh", bridge_address: "unix:/tmp/pulse-isolated.sock", runtime_receipt: receipt};
m.apply({on(){},effect(){}}, config);
rmSync(pending);
const again = await import(url); again.apply({on(){},effect(){}}, config);
console.log(JSON.parse(readFileSync(receipt, "utf8")).version);`
		cmd := exec.Command(node, "--input-type=module", "-e", script, "--", dshEntryURL(c.Adapters.Directory, pkg), filepath.Join(c.Adapters.Directory, "runtime", "dsh.json"), filepath.Join(c.Adapters.Directory, "pending.json"))
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != pkg.Version {
			t.Fatalf("未提交/恢复窗口加载错误版本：%v\n%s", err, out)
		}
	}
	if err := AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), installedState); err != nil {
		t.Fatal(err)
	}
	if err := Disable(c, "dsh", configPath); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readPatch(t, patchPath), "agent-pulse") {
		t.Fatal("随包控制入口停用后仍有自有条目")
	}
}

func TestDSHEntryOwnershipComparesFileURLPaths(t *testing.T) {
	// Windows file URL 的盘符前有 /，不能转成本机路径后与 C:\... 直接比较。
	for _, directory := range []string{"/home/test user/适配", "C:/Users/test user/adapters"} {
		p := Package{Manifest: Manifest{ID: "dsh", Version: "0.1.3", Entry: "index.js"}, SHA256: strings.Repeat("a", 64)}
		entry := dshEntryURL(directory, p)
		if !ownsDSHEntry(directory, p.ID, entry) || !ownsDSHEntry(directory, p.ID, entry+"?v=old") {
			t.Fatalf("自有 file URL 未识别：%s", entry)
		}
		other := dshFileURL(filepath.Join(directory, "packages", "dsh-other", "index.js"))
		if ownsDSHEntry(directory, p.ID, other) {
			t.Fatalf("接管了其他适配器：%s", other)
		}
	}
}
