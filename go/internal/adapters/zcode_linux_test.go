package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-pulse/internal/config"
)

func installTestZCodeServer(t *testing.T, home, v string) string {
	t.Helper()
	dir := filepath.Join(home, ".zcode", "server", "releases", v+"-linux-x64-test")
	for _, name := range []string{"server-cli.js", "server-core.js", "zcode.cjs", "node"} {
		// 不执行这些夹具；只验证安装产物探测。
		write(t, filepath.Join(dir, "runtime", name), "探测夹具，不可运行")
	}
	if err := AtomicJSON(filepath.Join(dir, "manifest.json"), map[string]any{
		"product": "zcode-server", "appVersion": v, "target": "linux-x64",
	}); err != nil {
		t.Fatal(err)
	}
	if err := AtomicJSON(filepath.Join(home, ".zcode", "server", "current.json"), map[string]any{
		"version": v, "appVersion": v, "releaseDir": dir, "target": "linux-x64",
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestZCodeServerDetectsOnlyCurrentInstallationWithoutWSLOrPATH(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("当前ZCode适配包的实际交付平台为Linux amd64")
	}
	home := t.TempDir()
	installTestZCodeServer(t, home, "3.18.2")
	currentPath := filepath.Join(home, ".zcode", "server", "current.json")
	before, _ := os.ReadFile(currentPath)
	// 保留一个更高的历史目录也不能改变 current.json 选择；目录名不是实际宿主版本。
	installTestZCodeServer(t, home, "9.99.0")
	write(t, currentPath, string(before))
	t.Setenv("HOME", home)
	t.Setenv("WSL_DISTRO_NAME", "")
	t.Setenv("PATH", t.TempDir())
	v, err := Detect(context.Background(), Manifest{ID: "zcode", Command: "zcode"})
	if err != nil || v != "3.18.2" {
		t.Fatalf("应检测当前 Server 而不执行命令：版本=%q，错误=%v", v, err)
	}
	after, _ := os.ReadFile(currentPath)
	if string(after) != string(before) {
		t.Fatal("只读探测改变了安装记录")
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode", "cli", "config.json")); !os.IsNotExist(err) {
		t.Fatal("探测不应创建钩子配置")
	}
}

func TestZCodeServerRejectsIncompleteOrInconsistentCurrentInstallation(t *testing.T) {
	for _, change := range []string{"missing-current", "bad-json", "too-large", "outside-release", "relative-release",
		"bad-version", "current-version-mismatch", "current-target-mismatch", "manifest-version-mismatch",
		"manifest-product-mismatch", "manifest-target-mismatch", "missing-node", "node-not-executable", "missing-agent"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			dir := installTestZCodeServer(t, home, "3.18.2")
			currentPath := filepath.Join(home, ".zcode", "server", "current.json")
			manifestPath := filepath.Join(dir, "manifest.json")
			raw, _ := os.ReadFile(currentPath)
			var current map[string]any
			_ = json.Unmarshal(raw, &current)
			raw, _ = os.ReadFile(manifestPath)
			var manifest map[string]any
			_ = json.Unmarshal(raw, &manifest)
			switch change {
			case "missing-current":
				if err := os.Remove(currentPath); err != nil {
					t.Fatal(err)
				}
			case "bad-json":
				write(t, currentPath, "{")
			case "too-large":
				write(t, currentPath, strings.Repeat(" ", 64<<10+1))
			case "outside-release":
				current["releaseDir"] = t.TempDir()
			case "relative-release":
				current["releaseDir"] = ".zcode/server/releases/current"
			case "bad-version":
				current["version"] = "not-a-version"
			case "current-version-mismatch":
				current["appVersion"] = "3.18.3"
			case "current-target-mismatch":
				current["target"] = "linux-arm64"
			case "manifest-version-mismatch":
				manifest["appVersion"] = "3.18.3"
			case "manifest-product-mismatch":
				manifest["product"] = "other"
			case "manifest-target-mismatch":
				manifest["target"] = "windows-x64"
			case "missing-node", "missing-agent":
				name := "node"
				if change == "missing-agent" {
					name = "zcode.cjs"
				}
				if err := os.Remove(filepath.Join(dir, "runtime", name)); err != nil {
					t.Fatal(err)
				}
			case "node-not-executable":
				if err := os.Chmod(filepath.Join(dir, "runtime", "node"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if change != "missing-current" && change != "bad-json" && change != "too-large" {
				if err := AtomicJSON(currentPath, current); err != nil {
					t.Fatal(err)
				}
			}
			if err := AtomicJSON(manifestPath, manifest); err != nil {
				t.Fatal(err)
			}
			if v, err := zcodeServerVersion(home, "amd64"); err == nil || v != "" {
				t.Fatalf("不完整安装不应视为可用：版本=%q，错误=%v", v, err)
			}
		})
	}
}

func TestZCodeServerMissingInstallDoesNotFallbackToWindowsOrCreateFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	v, err := zcodeServerVersion(home, "amd64")
	if err == nil || v != "" {
		t.Fatalf("缺安装时不应接受版本：%q，%v", v, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatal("未安装时只读探测不应初始化目录")
	}
	if _, err := zcodeServerVersion(home, "386"); err == nil {
		t.Fatal("不支持的架构应明确拒绝")
	}
}

func TestZCodeLinuxSignedInstallPreservesConfigAndUsesNativeArgv(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("当前ZCode适配包的实际交付平台为Linux amd64")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	installTestZCodeServer(t, home, "3.18.2")
	repo := filepath.Join(home, "repository")
	private, public := filepath.Join(home, "key"), filepath.Join(home, "pub")
	if err := GenerateKey(private, public); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "source")
	own, _ := os.Executable()
	program, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(source, "adapter"), string(program))
	t.Setenv("AGENT_PULSE_BUILTIN_CONTROL_FIXTURE", "1")
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "adapters", "zcode", "manifest-linux.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	p, err := BuildPackage(source, repo, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignIndex(repo, private, Index{Sequence: 1, Packages: []Package{p}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/adapters/index", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(repo, "index.json"))
	})
	mux.HandleFunc("GET /api/v1/adapters/packages/{hash}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(repo, "packages", r.PathValue("hash")+".zip"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	key, _ := os.ReadFile(public)
	target := filepath.Join(home, ".zcode", "cli", "config.json")
	write(t, target, `{"theme":"keep","hooks":{"events":{"Stop":[{"hooks":[{"type":"process","command":"/user/keep","args":["私人钩子"]}]}]}}}`)
	c := config.Bridge{SourceID: "linux-zcode-test", HubURL: server.URL,
		Adapters: config.AdapterUpdates{Directory: filepath.Join(home, "adapters"), PublicKey: string(key), Managed: map[string]string{"zcode": target}}}
	configPath := filepath.Join(home, "有 空格", "bridge.yaml")
	for i := 0; i < 2; i++ {
		if err := Sync(context.Background(), c, configPath); err != nil {
			t.Fatal(err)
		}
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil || state.Installed["zcode"].Error != "" || state.Installed["zcode"].AgentVersion != "3.18.2" ||
		state.Installed["zcode"].Package.Platform != "linux/amd64" {
		t.Fatalf("Linux 签名安装失败：%+v，%v", state, err)
	}
	after, _ := os.ReadFile(target)
	var document map[string]any
	if err := json.Unmarshal(after, &document); err != nil {
		t.Fatal(err)
	}
	if document["theme"] != "keep" || !strings.Contains(string(after), "/user/keep") {
		t.Fatal("用户配置或钩子丢失")
	}
	hooks := document["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Fatal("缺省总开关未启用")
	}
	events := hooks["events"].(map[string]any)
	for event := range manifest.Hooks {
		groups := events[event].([]any)
		found := 0
		for _, group := range groups {
			for _, item := range group.(map[string]any)["hooks"].([]any) {
				h := item.(map[string]any)
				args, ok := h["args"].([]any)
				if ok && len(args) == 7 && args[2] == configPath {
					found++
					if h["type"] != "process" || args[0] != "dispatch" || args[4] != "zcode" || args[6] != event {
						t.Fatalf("原生参数错误：%+v", h)
					}
				}
			}
		}
		if found != 1 {
			t.Fatalf("%s 应恰有一个本 Bridge 钩子，实际 %d", event, found)
		}
	}
}
