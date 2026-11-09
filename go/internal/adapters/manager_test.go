package adapters

import (
	"agent-pulse/internal/config"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}
func TestSignedUpdatesTwoHostsRollbackAndRecovery(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux 安装器")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	priv := filepath.Join(root, "publisher.key")
	pubPath := filepath.Join(root, "publisher.pub")
	if err := GenerateKey(priv, pubPath); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(pubPath)
	commandDir := filepath.Join(root, "bin")
	write(t, filepath.Join(commandDir, "test-agent"), "#!/bin/sh\necho 'test-agent 1.2.3'\n")
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/adapters/index", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, filepath.Join(repo, "index.json")) })
	mux.HandleFunc("GET /api/v1/adapters/packages/{hash}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(repo, "packages", r.PathValue("hash")+".zip"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	build := func(v, body string) Package {
		dir := filepath.Join(root, "source", v)
		write(t, filepath.Join(dir, "adapter"), body)
		p, err := BuildPackage(dir, repo, Manifest{ID: "sample", Version: v, Format: 1, Protocol: 1, MinBridge: "0.2.0", Platform: "linux/amd64", Kind: "executable", Entry: "adapter", Command: "test-agent", AgentMin: "1.0.0", AgentMax: "1.9.9", ConfigStyle: "hooks", Hooks: map[string]string{"Stop": ""}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := build("1.0.0", "#!/bin/sh\nprintf '{}'\n")
	p2 := build("1.1.0", "#!/missing-interpreter\n")
	publish := func(seq uint64, p Package) {
		t.Helper()
		if err := SignIndex(repo, priv, Index{Sequence: seq, Packages: []Package{p}}); err != nil {
			t.Fatal(err)
		}
	}
	publish(1, p1)
	hosts := []config.Bridge{}
	for _, id := range []string{"host-a", "host-b"} {
		dir := filepath.Join(root, id)
		cfg := filepath.Join(dir, "agent.json")
		write(t, cfg, `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`)
		hosts = append(hosts, config.Bridge{SourceID: id, HubURL: srv.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(dir, "adapters"), PublicKey: string(pub), Managed: map[string]string{"sample": cfg}}})
	}
	for _, c := range hosts {
		if err := Sync(context.Background(), c, filepath.Join(root, "bridge.yaml")); err != nil {
			t.Fatal(err)
		}
		state, _ := loadState(c.Adapters.Directory)
		if state.Installed["sample"].Package.Version != "1.0.0" {
			t.Fatal(state)
		}
		raw, _ := os.ReadFile(c.Adapters.Managed["sample"])
		if !strings.Contains(string(raw), "echo keep") || !strings.Contains(string(raw), "dark") {
			t.Fatal("用户配置丢失")
		}
	}
	original, _ := os.ReadFile(filepath.Join(repo, "index.json"))
	publish(2, p2)
	if err := Sync(context.Background(), hosts[0], filepath.Join(root, "bridge.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Dispatch(context.Background(), hosts[0], "sample", "Stop", []byte(`{}`)); err == nil {
		t.Fatal("损坏入口应失败")
	}
	state, _ := loadState(hosts[0].Adapters.Directory)
	if state.Installed["sample"].Package.Version != "1.0.0" || state.Rejected[p2.SHA256] == "" {
		t.Fatal("未回滚或隔离", state)
	}
	if err := Sync(context.Background(), hosts[0], filepath.Join(root, "bridge.yaml")); err != nil {
		t.Fatal(err)
	}
	state, _ = loadState(hosts[0].Adapters.Directory)
	if state.Installed["sample"].Package.Version != "1.0.0" {
		t.Fatal("重复装入坏版本")
	}
	if err := AtomicFile(filepath.Join(repo, "index.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), hosts[0], "bridge.yaml"); err == nil {
		t.Fatal("接受了旧签名索引重放")
	}
	publish(3, p1)
	for _, c := range hosts {
		if err := Sync(context.Background(), c, filepath.Join(root, "bridge.yaml")); err != nil {
			t.Fatal(err)
		}
	}
	c := hosts[0]
	before, _ := loadState(c.Adapters.Directory)
	raw, _ := os.ReadFile(c.Adapters.Managed["sample"])
	next := []byte(`{"interrupted":true}`)
	if err := AtomicJSON(filepath.Join(c.Adapters.Directory, "pending.json"), installJournal{Path: c.Adapters.Managed["sample"], Before: raw, After: next, Existed: true, State: before}); err != nil {
		t.Fatal(err)
	}
	write(t, c.Adapters.Managed["sample"], string(next))
	if err := Sync(context.Background(), c, filepath.Join(root, "bridge.yaml")); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(c.Adapters.Managed["sample"])
	if !bytes.Equal(raw, restored) {
		t.Fatal("崩溃恢复没有还原配置")
	}
	// 新发布线只支持更新的宿主时，现有主机仍应选择索引中的兼容版本。
	incompatible := p2
	incompatible.AgentMin = "2.0.0"
	incompatible.AgentMax = "2.9.9"
	if err := SignIndex(repo, priv, Index{Sequence: 4, Packages: []Package{p1, incompatible}}); err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), c, "bridge.yaml"); err != nil {
		t.Fatal(err)
	}
	state, _ = loadState(c.Adapters.Directory)
	if state.Installed["sample"].Target != "1.0.0" || state.Installed["sample"].Error != "" {
		t.Fatal("未选择兼容包", state)
	}
	// 旧进程刚好在新版本切换后失败，不能因此回滚已选中的新版本。
	if err := rollback(c, "sample", p2.SHA256, "已被替换的失败包"); err != nil {
		t.Fatal(err)
	}
	state, _ = loadState(c.Adapters.Directory)
	if state.Installed["sample"].Error != "" {
		t.Fatal("旧包错误影响当前版本", state)
	}
	incompatible.MinBridge = "99.0.0"
	if err := SignIndex(repo, priv, Index{Sequence: 5, Packages: []Package{incompatible}}); err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), c, "bridge.yaml"); err != nil {
		t.Fatal(err)
	}
	state, _ = loadState(c.Adapters.Directory)
	if state.Installed["sample"].Package.Version != "1.0.0" || !strings.Contains(state.Installed["sample"].Error, "Bridge") {
		t.Fatal("不兼容的通用运行能力应保留旧包并说明原因", state)
	}
	rawIndex, _ := os.ReadFile(filepath.Join(repo, "index.json"))
	var signed SignedIndex
	_ = json.Unmarshal(rawIndex, &signed)
	signed.Signature = strings.Repeat("A", 88)
	if _, err := Verify(transportJSON(signed), string(pub)); err == nil {
		t.Fatal("接受了伪造签名")
	}
}

func TestExtractRejectsTraversalAndLinks(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "dir/../../escape", "C:/escape", "a\\b"} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		f, _ := z.Create(name)
		_, _ = f.Write([]byte("x"))
		_ = z.Close()
		if err := Extract(b.Bytes(), t.TempDir(), Manifest{Entry: name}); err == nil {
			t.Fatal("接受不安全路径", name)
		}
	}
}
