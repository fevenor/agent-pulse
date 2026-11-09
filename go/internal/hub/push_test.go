package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func TestPushSuppressionAndRecovery(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	cfg := config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), Push: []config.Channel{{Name: "phone", Method: "GET", URL: server.URL, TimeoutSeconds: 1}}}
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"read", "active", "send", "uncertain"} {
		e := testEvent(id, "input.required", 0)
		e.SessionID = id
		if _, err = h.Accept(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.MarkRead(ctx, []string{"read"}); err != nil {
		t.Fatal(err)
	}
	h.pushOne(ctx)
	locked := false
	h.devices["test"] = &connection{device: protocol.Device{Role: "tray", Locked: &locked, LastSeen: time.Now()}}
	h.pushOne(ctx)
	delete(h.devices, "test")
	h.pushOne(ctx)
	if calls.Load() != 1 {
		t.Fatal("推送抑制错误", calls.Load())
	}
	if _, _, ok := h.claimPush(ctx); !ok {
		t.Fatal("未领取任务")
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var status string
	if err = h.db.QueryRow(`SELECT status FROM push_jobs WHERE event_id='uncertain'`).Scan(&status); err != nil || status != "unknown" {
		t.Fatal(status, err)
	}
	h.pushOne(ctx)
	if calls.Load() != 1 {
		t.Fatal("恢复时重复发送了结果不确定的请求")
	}
}
