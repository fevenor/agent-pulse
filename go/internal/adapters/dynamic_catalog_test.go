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
	"sync/atomic"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func TestUnknownAgentCatalogSetupUpgradeAndOfflineRepair(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("目标为Linux/Windows")
	}
	root := t.TempDir()
	t.Setenv("AGENT_PULSE_CONTROL_FIXTURE", "1")
	t.Setenv("AGENT_PULSE_CONTROL_ROOT", root)
	repo := filepath.Join(root, "repo")
	priv := filepath.Join(root, "private")
	pub := filepath.Join(root, "public")
	if err := GenerateKey(priv, pub); err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(pub)
	own, _ := os.Executable()
	program, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	build := func(id, v, hook string) Package {
		entry := "adapter"
		if runtime.GOOS == "windows" {
			entry += ".exe"
		}
		source := filepath.Join(root, "build", id, v)
		write(t, filepath.Join(source, entry), string(program))
		write(t, filepath.Join(source, "version.txt"), v)
		write(t, filepath.Join(source, "identity.txt"), id)
		p, err := BuildPackage(source, repo, Manifest{ID: id, Name: "未来来源 " + id, Version: v, Format: 1, Protocol: 1, Platform: runtime.GOOS + "/" + runtime.GOARCH, Kind: "executable", Entry: entry, Control: entry, Command: "not-installed-new-host", AgentMin: "1.2.0", AgentMax: "1.2.9", Hooks: map[string]string{hook: ""}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := build("future-agent", "1.0.0", "Finish")
	skip := build("skipped-agent", "1.0.0", "Finish")
	var active atomic.Value
	var checks, downloads, notModified atomic.Int32
	publish := func(seq uint64, packages ...Package) {
		t.Helper()
		if err := SignIndex(repo, priv, Index{Sequence: seq, Packages: packages}); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(repo, "index.json"))
		active.Store(raw)
	}
	publish(1, p1, skip)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/client-config":
			json.NewEncoder(w).Encode(map[string]string{"public_key": strings.TrimSpace(string(key))})
		case r.URL.Path == "/api/v1/adapters/index":
			checks.Add(1)
			raw := active.Load().([]byte)
			digest := sha256.Sum256(raw)
			etag := `"` + hex.EncodeToString(digest[:]) + `"`
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				notModified.Add(1)
				w.WriteHeader(304)
				return
			}
			w.Write(raw)
		case strings.HasPrefix(r.URL.Path, "/api/v1/adapters/packages/"):
			downloads.Add(1)
			http.ServeFile(w, r, filepath.Join(repo, "packages", strings.TrimPrefix(r.URL.Path, "/api/v1/adapters/packages/")+".zip"))
		case r.URL.Path == "/api/v1/adapters/status":
			w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(root, "bridge.yaml")
	c := config.Bridge{SourceID: "dynamic-fixture", HubURL: server.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(root, "cache"), PublicKey: string(key), Managed: map[string]string{"skipped-agent": filepath.Join(root, "skipped-agent.cfg")}}}
	write(t, c.Adapters.Managed["skipped-agent"], "other=保留用户字段\n")
	if err := writeSetupConfig(path, c, map[string]bool{"skipped-agent": true}, true); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "future-agent.cfg"), "theme=中文 ✅\n")
	var output bytes.Buffer
	if err := SetupInteractive(context.Background(), path, strings.NewReader("e\n\ns\n"), &output); err != nil {
		t.Fatal(err, output.String())
	}
	if !strings.Contains(output.String(), "未来来源 future-agent") {
		t.Fatal("未知Agent没有进入setup目录", output.String())
	}
	c, err = config.LoadBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Adapters.PublicKey = string(key)
	cfg := c.Adapters.Managed["future-agent"]
	raw, _ := os.ReadFile(cfg)
	if !strings.Contains(string(raw), "pulse_Finish=") || !strings.Contains(string(raw), "theme=中文 ✅") {
		t.Fatal("独立控制配置未安装或丢失用户内容", string(raw))
	}
	skipped, _ := os.ReadFile(c.Adapters.Managed["skipped-agent"])
	if string(skipped) != "other=保留用户字段\n" {
		t.Fatal("setup保持不变的来源被修改", string(skipped))
	}
	baseline := checks.Load()
	beforeDownloads := downloads.Load()
	if err := SetupInteractive(context.Background(), path, strings.NewReader("s\ns\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if checks.Load() != baseline || downloads.Load() != beforeDownloads {
		t.Fatal("普通setup未复用最小间隔和包缓存")
	}
	p2 := build("future-agent", "1.1.0", "Done")
	publish(2, p2, skip)
	// 节流期间不检查；强制同步立即取索引，但相同包不重下。
	if err := RefreshCatalog(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	if checks.Load() != baseline {
		t.Fatal("最小间隔未生效")
	}
	if err := RefreshCatalog(context.Background(), c, true); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCached(context.Background(), c, path); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(cfg)
	if strings.Contains(string(raw), "pulse_Finish=") || !strings.Contains(string(raw), "pulse_Done=") || !strings.Contains(string(raw), ";1.1.0") {
		t.Fatal("包升级未随即更新hook", string(raw))
	}
	state, _ := loadState(c.Adapters.Directory)
	if state.Installed["future-agent"].Package.Version != "1.1.0" {
		t.Fatal(state)
	}
	body, err := Dispatch(context.Background(), c, "future-agent", "Done", []byte("{}"))
	if err != nil || !bytes.Contains(body, []byte("中文完成")) {
		t.Fatal("未执行独立包转换", string(body), err)
	}
	var converted protocol.AdapterResult
	if json.Unmarshal(body, &converted) != nil || converted.Event == nil || converted.Event.Agent != "future-agent" || converted.Event.SourceID != c.SourceID || converted.Event.Validate() != nil {
		t.Fatal("未知Agent未转换为有效通用事件", string(body))
	}
	// 相同清单304也修复被用户删去的自有hook，且不重新下载包。
	before304 := downloads.Load()
	write(t, cfg, "theme=304缓存\n")
	if err := RefreshCatalog(context.Background(), c, true); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCached(context.Background(), c, path); err != nil {
		t.Fatal(err)
	}
	repaired304, _ := os.ReadFile(cfg)
	if notModified.Load() == 0 || downloads.Load() != before304 || !bytes.Contains(repaired304, []byte("pulse_Done=")) {
		t.Fatal("304未修复本地hook或重复下载", string(repaired304))
	}
	// 宿主自身不执行；仅固定本机接口的fixture，不当作真实宿主采集验收。
	// 外部拥有hook时仍可升级本机包，但hook字节与修改时间均不能变化。
	p3 := build("future-agent", "1.2.0", "Done")
	publish(3, p3, skip)
	beforeExternal, _ := os.ReadFile(cfg)
	externalInfo, _ := os.Stat(cfg)
	t.Setenv("AGENT_PULSE_CONTROL_DEFERRED", "1")
	if err := Sync(context.Background(), c, path); err != nil {
		t.Fatal(err)
	}
	externalState, _ := loadState(c.Adapters.Directory)
	afterExternal, _ := os.ReadFile(cfg)
	afterExternalInfo, _ := os.Stat(cfg)
	if externalState.Installed["future-agent"].Package.Version != "1.2.0" || !bytes.Equal(beforeExternal, afterExternal) || !externalInfo.ModTime().Equal(afterExternalInfo.ModTime()) {
		t.Fatal("外部管理hook时包未更新或文件被改写")
	}
	t.Setenv("AGENT_PULSE_CONTROL_DEFERRED", "")
	p2 = p3
	server.Close()
	write(t, cfg, "theme=离线副本\n")
	if err := SetupInteractive(context.Background(), path, strings.NewReader("e\n\ns\n"), &output, true); err != nil {
		t.Fatal("离线setup未使用可信副本", err, output.String())
	}
	raw, _ = os.ReadFile(cfg)
	if !bytes.Contains(raw, []byte("pulse_Done=")) || !bytes.Contains(raw, []byte("theme=离线副本")) {
		t.Fatal("离线修复失败", string(raw))
	}
	info, _ := os.Stat(cfg)
	selected := c
	selected.Adapters.Managed = map[string]string{"future-agent": cfg}
	if err := ApplyCached(context.Background(), selected, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(cfg)
	if !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("不变hook重复写入")
	}
	// 缺少入口可从已核验本地ZIP恢复，不联网。
	if err := os.Remove(filepath.Join(packageDir(c.Adapters.Directory, p2), p2.Entry)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCached(context.Background(), selected, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Dispatch(context.Background(), c, "future-agent", "Done", []byte("{}")); err != nil {
		t.Fatal("离线入口修复失败", err)
	}
	if err := Disable(c, "future-agent", path); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(cfg)
	if bytes.Contains(raw, []byte("pulse_")) || !bytes.Contains(raw, []byte("theme=离线副本")) {
		t.Fatal("停用丢失用户字段", string(raw))
	}
}

func TestRunChecksOnceAndNeverOnReconnect(t *testing.T) {
	repo, _, key, p := repositoryFixture(t)
	var checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/adapters/index" {
			checks.Add(1)
			http.ServeFile(w, r, filepath.Join(repo, "index.json"))
			return
		}
		w.Write([]byte("{}"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "bridge.yaml")
	c := config.Bridge{SourceID: "run-fixture", HubURL: server.URL, Adapters: config.AdapterUpdates{Directory: t.TempDir(), PublicKey: key}}
	if err := writeSetupConfig(path, c, nil, true); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Adapters.PublicKey = key
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reconnect := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() { Run(ctx, c, path, reconnect); close(done) }()
	deadline := time.After(2 * time.Second)
	for checks.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("启动未检查")
		case <-time.After(10 * time.Millisecond):
		}
	}
	reconnect <- struct{}{}
	reconnect <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if checks.Load() != 1 {
		t.Fatalf("重连触发额外检查：%d", checks.Load())
	}
	// 新一次启动不受setup间隔限制。
	if err := Sync(context.Background(), c, path); err != nil {
		t.Fatal(err)
	}
	if checks.Load() != 2 {
		t.Fatal("第二次启动没有检查")
	}
	_ = p
}

func TestDispatchWaitsForShortCommitNotSyncDownloads(t *testing.T) {
	dir := t.TempDir()
	syncUnlock, err := lockSync(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchState(context.Background(), dir); err != nil {
		t.Fatal("下载同步锁阻止旧hook选版", err)
	}
	syncUnlock()
	commit, err := lockInstall(dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := dispatchState(context.Background(), dir); done <- err }()
	select {
	case err := <-done:
		t.Fatal("提交中提前选版", err)
	case <-time.After(30 * time.Millisecond):
	}
	commit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
