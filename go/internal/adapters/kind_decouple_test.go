package adapters

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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
		"命令含空格": func() Manifest { m := goodManifest("executable"); m.ConfigStyle = "hooks"; m.Command = "x y"; return m }(),
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
	if err := goodManifest("dsh-plugin").ValidateRuntime(); err != nil {
		t.Fatalf("dsh-plugin 应受支持：%v", err)
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

// 发布者侧仍只能打出当前工具链认识运行方式的包。
func TestBuildPackageRejectsUnknownKind(t *testing.T) {
	source := t.TempDir()
	write(t, filepath.Join(source, "index.js"), "export {}\n")
	m := goodManifest("future-plugin-kind")
	_, err := BuildPackage(source, filepath.Join(t.TempDir(), "repo"), m)
	if err == nil || !strings.Contains(err.Error(), "运行方式") {
		t.Fatalf("BuildPackage 应拒绝未知 kind：%v", err)
	}
}
