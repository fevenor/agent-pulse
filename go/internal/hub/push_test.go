package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestPushFailureRetainsSafeHTTPReasonForWebAndRestart(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("不能泄露的服务器错误正文"))
	}))
	defer s.Close()
	cfg := config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), Push: []config.Channel{{Name: "phone", Method: "GET", URL: s.URL, TimeoutSeconds: 1}}}
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := testEvent("failed-with-reason", "turn.completed", 1)
	if _, err = h.Accept(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	h.pushOne(context.Background())
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	w := httptest.NewRecorder()
	h.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/push-jobs", nil))
	var rows []map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if rows[0]["status"] != "failed" || rows[0]["http_status"] != float64(400) || rows[0]["failure_reason"] != "推送接口返回 HTTP 400" || rows[0]["business_status"] != nil {
		t.Fatal("失败原因必须可展示并持久化", rows)
	}
	if strings.Contains(w.Body.String(), "不能泄露") {
		t.Fatal("不应展示服务端原始响应")
	}
}
