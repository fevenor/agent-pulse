package adapters

import (
	"agent-pulse/internal/config"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCatalogAttemptThrottleContextAnd304(t *testing.T) {
	repo, _, key, _ := repositoryFixture(t)
	raw, _ := os.ReadFile(filepath.Join(repo, "index.json"))
	var calls atomic.Int32
	var notModified atomic.Int32
	var fail atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			notModified.Add(1)
			w.WriteHeader(304)
			return
		}
		w.Write(raw)
	})
	first := httptest.NewServer(handler)
	defer first.Close()
	c := config.Bridge{HubURL: first.URL, Adapters: config.AdapterUpdates{Directory: t.TempDir(), PublicKey: key}}
	if err := RefreshCatalog(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	if err := RefreshCatalog(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("连续检查未节流")
	}
	if err := RefreshCatalog(context.Background(), c, true); err != nil {
		t.Fatal(err)
	}
	if notModified.Load() != 1 {
		t.Fatal("强制检查未复用可信304")
	}
	second := httptest.NewServer(handler)
	defer second.Close()
	c.HubURL = second.URL
	if err := RefreshCatalog(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("换Hub后误用了之前的时间节流")
	}
	c.Adapters.Directory = t.TempDir()
	fail.Store(true)
	before := calls.Load()
	if err := RefreshCatalog(context.Background(), c, false); err == nil {
		t.Fatal("更新失败未报告")
	}
	if err := RefreshCatalog(context.Background(), c, false); err == nil {
		t.Fatal("无可信缓存应报告暂不可用")
	}
	if calls.Load() != before+1 {
		t.Fatal("失败尝试未参与节流")
	}
	if err := RefreshCatalog(context.Background(), c, true); err == nil {
		t.Fatal("强制失败未报告")
	}
	if calls.Load() != before+2 {
		t.Fatal("强制参数没有绕过失败节流")
	}
}
