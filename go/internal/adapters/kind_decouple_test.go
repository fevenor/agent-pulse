package adapters

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-pulse/internal/config"
)

func signIndexForTest(t *testing.T, index Index) (raw []byte, publicKey string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(index)
	signed := SignedIndex{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))}
	raw, _ = json.Marshal(signed)
	return raw, base64.StdEncoding.EncodeToString(pub)
}

func goodManifest(kind string) Manifest {
	return Manifest{ID: "x-agent", Version: "1.0.0", Format: 1, Protocol: 1, Platform: "any", Kind: kind, Entry: "index.js", Command: "x-agent", AgentMin: "1.0.0", AgentMax: "1.0.0", Name: "X"}
}

// 解耦约束：签名索引的结构校验不判断运行方式。Hub 只是分发仓库，
// 含本机不认识的新 kind 的合法签名索引必须通过 Verify，由支持它的 Bridge 自行安装。
func TestVerifyAcceptsUnknownKindButRejectsMalformed(t *testing.T) {
	future := goodManifest("future-plugin-kind")
	p := Package{Manifest: future, SHA256: strings.Repeat("a", 64), Size: 10}
	raw, key := signIndexForTest(t, Index{Sequence: 1, Packages: []Package{p}})
	idx, err := Verify(raw, key)
	if err != nil {
		t.Fatalf("Verify 不得因未知 kind 拒绝签名索引：%v", err)
	}
	if len(idx.Packages) != 1 || idx.Packages[0].Kind != "future-plugin-kind" {
		t.Fatal("未知 kind 的包应原样返回给 Bridge 判定")
	}
	// 结构问题仍由 Verify 拒绝：越界入口、非法版本、重复键。
	for name, bad := range map[string]Manifest{
		"入口越界": func() Manifest { m := goodManifest("executable"); m.ConfigStyle = "hooks"; m.Entry = "../x"; return m }(),
		"版本非法": func() Manifest { m := goodManifest("executable"); m.ConfigStyle = "hooks"; m.Version = "one"; return m }(),
	} {
		raw, key := signIndexForTest(t, Index{Sequence: 1, Packages: []Package{{Manifest: bad, SHA256: strings.Repeat("b", 64), Size: 10}}})
		if _, err := Verify(raw, key); err == nil {
			t.Fatalf("%s：结构非法的清单应被 Verify 拒绝", name)
		}
	}
	dup := Package{Manifest: goodManifest("executable"), SHA256: strings.Repeat("c", 64), Size: 10}
	raw, key = signIndexForTest(t, Index{Sequence: 1, Packages: []Package{dup, dup}})
	if _, err := Verify(raw, key); err == nil {
		t.Fatal("重复包应被拒绝")
	}
}

func TestValidateRuntimeRejectsUnsupportedKindAndConfigStyle(t *testing.T) {
	controlled := goodManifest("future-plugin-kind")
	controlled.Control = "control"
	controlled.Command = ""
	controlled.ConfigStyle = "product-private"
	controlled.Hooks = map[string]string{"session/event": "native"}
	if err := controlled.ValidateRuntime(); err != nil {
		t.Fatalf("有控制入口的包不应受产品 kind/config_style/hooks 白名单限制：%v", err)
	}
	if err := goodManifest("future-plugin-kind").ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "运行方式") {
		t.Fatalf("未知 kind 应在 Bridge 侧被拒绝：%v", err)
	}
	noStyle := goodManifest("executable")
	if err := noStyle.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "配置格式") {
		t.Fatalf("无配置格式的 executable 应被拒绝：%v", err)
	}
	withStyle := goodManifest("executable")
	withStyle.ConfigStyle = "process-hooks"
	if err := withStyle.ValidateRuntime(); err != nil {
		t.Fatalf("合法 executable 被误拒：%v", err)
	}
}

// 本机 Bridge 对未知 kind 的候选：不下载、不执行，只返回兼容性错误。
func TestBridgeRejectsUnknownKindBeforeDownloadOrDetect(t *testing.T) {
	c := config.Bridge{SourceID: "s", Adapters: config.AdapterUpdates{Directory: t.TempDir()}}
	p := Package{Manifest: goodManifest("future-plugin-kind"), SHA256: strings.Repeat("d", 64), Size: 10}
	if err := preparePackage(context.Background(), c, p); err == nil || !strings.Contains(err.Error(), "运行方式") {
		t.Fatalf("preparePackage 应在下载前拒绝未知 kind：%v", err)
	}
	if _, err := detectPackage(context.Background(), c, p); err == nil || !strings.Contains(err.Error(), "运行方式") {
		t.Fatalf("detectPackage 应拒绝未知 kind：%v", err)
	}
	// 拒绝发生在任何网络访问之前：适配目录下不应出现下载或安装痕迹。
	entries, _ := os.ReadDir(c.Adapters.Directory)
	if len(entries) != 0 {
		t.Fatalf("拒绝前不应写入适配目录：%v", entries)
	}
}

// 缺少控制入口的未知形态不能由本机构建为可安装包。
func TestBuildPackageRejectsUnknownKind(t *testing.T) {
	source := t.TempDir()
	write(t, filepath.Join(source, "index.js"), "export {}\n")
	m := goodManifest("future-plugin-kind")
	_, err := BuildPackage(source, filepath.Join(t.TempDir(), "repo"), m)
	if err == nil || !strings.Contains(err.Error(), "运行方式") {
		t.Fatalf("BuildPackage 应拒绝未知 kind：%v", err)
	}
}

// Hub 必须能推广不理解其宿主细节的签名包；运行方式检查只影响本机安装。
func TestRepositoryAcceptsPackageOwnedRuntimeMetadata(t *testing.T) {
	source, repository := t.TempDir(), t.TempDir()
	write(t, filepath.Join(source, "index.js"), "export {}\n")
	write(t, filepath.Join(source, "control"), "测试控制入口，仅作为仓库字节\n")
	m := goodManifest("new-host-plugin")
	m.Control, m.Command, m.ConfigStyle = "control", "", "yaml-for-new-host"
	m.Hooks = map[string]string{"session/event": "private"}
	p, err := BuildPackage(source, repository, m)
	if err != nil {
		t.Fatal(err)
	}
	// 宿主兼容字段不由分发端解释；Bridge 仍会按当前版本政策拒绝无效本机范围。
	p.AgentMin = "host-specific-version"
	raw, key := signIndexForTest(t, Index{Sequence: 1, Packages: []Package{p}})
	data, err := os.ReadFile(filepath.Join(repository, "packages", p.SHA256+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := PromoteRepository(context.Background(), t.TempDir(), key, RepositorySource{Index: raw, Package: func(context.Context, Package) ([]byte, error) { return data, nil }}); err != nil {
		t.Fatal("Hub 不应解释宿主字段", err)
	}
	if err := p.ValidateRuntime(); err == nil {
		t.Fatal("本机仍需有效宿主版本范围")
	}
}

// 控制接口负责安装未知插件形态，Bridge 不增加产品分支也能安装、升级和停用。
func TestUnknownPluginKindInstallsThroughPackageControl(t *testing.T) {
	root, repository := t.TempDir(), t.TempDir()
	t.Setenv("AGENT_PULSE_CONTROL_FIXTURE", "1")
	t.Setenv("AGENT_PULSE_CONTROL_ROOT", root)
	own, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	entry := "control"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	write(t, filepath.Join(source, entry), string(program))
	write(t, filepath.Join(source, "plugin.txt"), "宿主自有的插件入口\n")
	m := goodManifest("future-private-plugin")
	m.ID, m.Control, m.Command, m.Entry = "new-source", entry, "", "plugin.txt"
	m.AgentMin, m.AgentMax, m.ConfigStyle = "1.2.0", "1.2.9", "private-text"
	c := config.Bridge{SourceID: "isolated", Adapters: config.AdapterUpdates{Directory: filepath.Join(root, "cache"), Managed: map[string]string{m.ID: filepath.Join(root, "agent.cfg")}}}
	write(t, c.Adapters.Managed[m.ID], "theme=保留\n")
	for i, v := range []string{"1.0.0", "1.0.1"} {
		m.Version = v
		m.Hooks = map[string]string{"session/event": v}
		write(t, filepath.Join(source, "version.txt"), v)
		p, err := BuildPackage(source, repository, m)
		if err != nil {
			t.Fatal(err)
		}
		raw, key := signIndexForTest(t, Index{Sequence: uint64(i + 1), Packages: []Package{p}})
		c.Adapters.PublicKey = key
		if err := AtomicFile(filepath.Join(c.Adapters.Directory, "index.cached.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(repository, "packages", p.SHA256+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		if err := AtomicFile(filepath.Join(c.Adapters.Directory, "downloads", p.SHA256+".zip"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := ApplyCached(context.Background(), c, "bridge.yaml"); err != nil {
			t.Fatal(err)
		}
		state, err := loadState(c.Adapters.Directory)
		if err != nil || state.Installed[m.ID].Package.Version != v || state.Installed[m.ID].Error != "" {
			t.Fatalf("未知形态安装失败：%v %+v", err, state.Installed[m.ID])
		}
		content, err := os.ReadFile(c.Adapters.Managed[m.ID])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "theme=保留") || !strings.Contains(string(content), ";"+v) {
			t.Fatal("产品配置未由控制入口生成", string(content))
		}
	}
	if err := Disable(c, m.ID, "bridge.yaml"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(c.Adapters.Managed[m.ID])
	if string(content) != "theme=保留\n" {
		t.Fatal("停用改写用户配置", string(content))
	}
}
