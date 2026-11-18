package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

// 既有状态测试显式构造在线采集来源；不把离线的旧状态当实时事实。
func makeTestBridgeOnline(h *Hub, id, collector string) {
	if collector == "" {
		collector = "test"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.devices["bridge:"+id] = &connection{device: protocol.Device{ID: id, Role: "bridge", CollectorID: collector, LastSeen: time.Now().UTC()}}
}

func TestBridgeLossReconnectAndOldQueueNeverResurrectRunning(t *testing.T) {
	h := testHub(t)
	server := httptest.NewServer(h.Handler("test-token"))
	defer server.Close()
	ctx := context.Background()
	connect := func(collector string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/connect", http.Header{"Authorization": []string{"Bearer test-token"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.Close() })
		ws.WriteJSON(protocol.Message{Type: "hello", Payload: protocol.Device{ID: "linux", Role: "bridge", CollectorID: collector}})
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		var reply protocol.Message
		if err = ws.ReadJSON(&reply); err != nil || reply.Type != "hello" {
			t.Fatal(reply, err)
		}
		return ws
	}
	accept := func(id, kind, collector string, seq int64) {
		t.Helper()
		e := testEvent(id, kind, seq)
		e.CollectorID = collector
		if _, err := h.Accept(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	state := func() string {
		t.Helper()
		s, err := h.Snapshot(ctx)
		if err != nil || len(s.Sessions) != 1 {
			t.Fatal(s, err)
		}
		return s.Sessions[0].State
	}
	old := connect("old")
	accept("start", "session.started", "old", 1)
	if state() != "running" {
		t.Fatal("在线任务未运行")
	}
	old.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		gone := h.devices["bridge:linux"] == nil
		h.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state() != "unknown" {
		t.Fatal("Bridge停止后任务仍运行")
	}
	current := connect("new")
	if state() != "unknown" {
		t.Fatal("重连复活了旧任务")
	}
	accept("old-queue", "activity.observed", "old", 2)
	if state() != "unknown" {
		t.Fatal("旧连接队列复活了旧任务")
	}
	accept("real-progress", "activity.observed", "new", 3)
	if state() != "running" {
		t.Fatal("新真实事件未恢复运行")
	}
	accept("final", "run.failed", "new", 4)
	if state() != "failed" {
		t.Fatal("最终报错未释放任务")
	}
	accept("late-observer-loss", "observation.lost", "new", 5)
	if state() != "failed" {
		t.Fatal("来源故障覆盖已确认终态")
	}
	current.Close()
}

func TestEventBeforePresenceKeepsNewCollectorAndStartupDoesNotRestoreOldRunning(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	e := testEvent("early", "session.started", 1)
	e.CollectorID = "new"
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	err := h.registerPresenceLocked(ctx, protocol.Device{ID: "linux", Role: "bridge", CollectorID: "new"})
	h.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	makeTestBridgeOnline(h, "linux", "new")
	s, err := h.Snapshot(ctx)
	if err != nil || s.Sessions[0].State != "running" {
		t.Fatal("握手前真实事件被误失效", s, err)
	}
	cfg := h.config
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() })
	s, err = restarted.Snapshot(ctx)
	if err != nil || s.Sessions[0].State != "unknown" {
		t.Fatal(s, err)
	}
}

func TestDelayedFinalKeepsClosedSessionAndStillNotifies(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	makeTestBridgeOnline(h, "linux", "")
	for _, e := range []protocol.Event{testEvent("start", "session.started", 1), testEvent("close", "session.closed", 2)} {
		if _, err := h.Accept(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	final := testEvent("final", "run.failed", 3)
	final.SessionTitle = "最终会话名"
	if _, err := h.Accept(ctx, final); err != nil {
		t.Fatal(err)
	}
	s, err := h.Snapshot(ctx)
	if err != nil || s.Sessions[0].State != "closed" || s.Sessions[0].Title != "最终会话名" || len(s.Unread) != 1 || !s.Unread[0].Valid {
		t.Fatal("晚到终态重新打开会话或丢失提醒", s, err)
	}
}

func TestUnmarkedCollectorCannotMakeCachedRunningTrusted(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	e := testEvent("unmarked", "session.started", 1)
	e.CollectorID = ""
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.devices["bridge:linux"] = &connection{device: protocol.Device{ID: "linux", Role: "bridge", LastSeen: time.Now()}}
	h.mu.Unlock()
	s, err := h.Snapshot(ctx)
	if err != nil || s.Sessions[0].State != "unknown" {
		t.Fatal("无采集代次的旧状态仍允许唤醒", s, err)
	}
}
