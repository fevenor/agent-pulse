package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// 完整走版本探测、签名索引、安装和向导核验，不以单次JSONC合并代替已有接入的重启用。
func setupOpenCodeJSONCFixture(t *testing.T, raw, rejected string) (config.Bridge, string, Package) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("当前OpenCode向导只支持Linux宿主；不模拟Windows接入")
	}
	home := t.TempDir()
	tools := filepath.Join(home, "tools")
	write(t, filepath.Join(tools, "opencode"), "#!/bin/sh\nprintf 'opencode 2.2.0\\n'\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", tools)
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
	write(t, filepath.Join(source, "index.js"), "export default { setup() {} };\n")
	pkg, err := BuildPackage(source, repo, Manifest{ID: "opencode", Version: "0.3.5", Format: 1, Protocol: 1, Platform: "any", Kind: "opencode-plugin", Entry: "index.js", Command: "opencode", AgentMin: "2.0.20", AgentMax: "2.0.20"})
	if err != nil {
		t.Fatal(err)
	}
	if err = SignIndex(repo, private, Index{Sequence: 6, Packages: []Package{pkg}}); err != nil {
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
	configPath := filepath.Join(home, "bridge.yaml")
	hostPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	write(t, hostPath, raw)
	c := config.Bridge{SourceID: "setup-jsonc-regression", Listen: "127.0.0.1:18702", HubURL: hub.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(home, "adapters"), Managed: map[string]string{"opencode": hostPath}}}
	data, err := yaml.Marshal(map[string]any{"source_id": c.SourceID, "listen": c.Listen, "hub_url": c.HubURL, "adapters": map[string]any{"directory": c.Adapters.Directory, "managed": c.Adapters.Managed}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, configPath, string(data))
	if rejected != "" {
		signed, err := os.ReadFile(filepath.Join(repo, "index.json"))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(signed)
		old := pkg
		old.Version = "0.3.4"
		old.SHA256 = strings.Repeat("a", 64)
		state := State{Sequence: 6, IndexHash: hex.EncodeToString(digest[:]), Installed: map[string]Installed{"opencode": {Package: old, Target: pkg.Version, AgentVersion: "2.2.0", Error: rejected, ConfigBefore: []byte(raw), ConfigAfter: []byte(raw)}}, Rejected: map[string]string{pkg.SHA256: rejected}}
		if err = AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), state); err != nil {
			t.Fatal(err)
		}
	}
	return c, configPath, pkg
}

func TestSetupOpenCodeJSONCReenableWithCachedConfigurationFailure(t *testing.T) {
	const original = "{\n // 保留提供方说明\n \"provider\":{\"number\":900719925474099312345},\n \"permissions\":[{\"action\":\"shell\",\"resource\":\"git *\",\"effect\":\"ask\"}],\n}\n"
	for _, cached := range []bool{false, true} {
		name := "首次启用与重复启用"
		reason := ""
		if cached {
			name = "旧解析错误已缓存"
			reason = "宿主配置必须为 JSON 对象；不覆盖无效配置"
		}
		t.Run(name, func(t *testing.T) {
			c, path, pkg := setupOpenCodeJSONCFixture(t, original, reason)
			for pass := 0; pass < 2; pass++ {
				var output bytes.Buffer
				if err := SetupInteractive(context.Background(), path, strings.NewReader("e\n\n"), &output); err != nil {
					t.Fatalf("合法JSONC向导应完成：%v\n%s", err, output.String())
				}
				if !strings.Contains(output.String(), "接入配置已完成") || strings.Contains(output.String(), "未完成") {
					t.Fatal("向导没有明确报告成功")
				}
				state, err := loadState(c.Adapters.Directory)
				if err != nil {
					t.Fatal(err)
				}
				if state.Installed["opencode"].Package.SHA256 != pkg.SHA256 || state.Installed["opencode"].Error != "" || state.Rejected[pkg.SHA256] != "" {
					t.Fatal("重启用后仍保留旧配置错误")
				}
				raw, err := os.ReadFile(c.Adapters.Managed["opencode"])
				if err != nil {
					t.Fatal(err)
				}
				for _, keep := range []string{"// 保留提供方说明", "900719925474099312345", "permissions", "ask"} {
					if !bytes.Contains(raw, []byte(keep)) {
						t.Fatal("非本软件配置被改写")
					}
				}
			}
		})
	}
}

func TestSetupOpenCodeDoesNotReleaseBadPackageOrInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, raw, reason string }{
		{"坏入口隔离保持", "{ /* 保留 */ \"provider\":{} }", "适配包入口无法启动"},
		{"无效配置仍拒绝", "[]", "宿主配置必须为 JSON 对象；不覆盖无效配置"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, path, pkg := setupOpenCodeJSONCFixture(t, tc.raw, tc.reason)
			var output bytes.Buffer
			if err := SetupInteractive(context.Background(), path, strings.NewReader("e\n\n"), &output); err == nil {
				t.Fatal("仍有真实问题时不能返回成功")
			}
			raw, err := os.ReadFile(c.Adapters.Managed["opencode"])
			if err != nil || string(raw) != tc.raw {
				t.Fatal("拒绝时改写了原配置")
			}
			state, err := loadState(c.Adapters.Directory)
			if err != nil || state.Rejected[pkg.SHA256] != tc.reason {
				t.Fatal("不能解除坏包或无效配置的隔离记录")
			}
		})
	}
}

// 首次安装的宿主配置错误只记接入失败；用户修正后同一索引与包应可重试。
func TestSetupOpenCodeConfigurationFailureDoesNotRejectPackage(t *testing.T) {
	c, path, pkg := setupOpenCodeJSONCFixture(t, "[]", "")
	var output bytes.Buffer
	if err := SetupInteractive(context.Background(), path, strings.NewReader("e\n\n"), &output); err == nil {
		t.Fatal("无效配置不能报告成功")
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.Rejected[pkg.SHA256] != "" {
		t.Fatal("宿主配置错误被错误隔离为坏包")
	}
	write(t, c.Adapters.Managed["opencode"], "{ /* 已修正且保留 */ \"provider\":{\"keep\":true}, }")
	output.Reset()
	if err = SetupInteractive(context.Background(), path, strings.NewReader("e\n\n"), &output); err != nil {
		t.Fatal("同索引配置修正后不能恢复", err)
	}
	state, err = loadState(c.Adapters.Directory)
	if err != nil || state.Installed["opencode"].Package.SHA256 != pkg.SHA256 || state.Installed["opencode"].Error != "" {
		t.Fatal("修正后没有安装当前包")
	}
}
