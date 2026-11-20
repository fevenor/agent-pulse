package bridge

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/hub"
	"agent-pulse/internal/relay"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func awaitStream(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("长连接验收条件未达到")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func TestBridgeStreamRelayPersistsWhileHubOffline(t *testing.T) {
	root := t.TempDir()
	r, err := relay.Open(config.Relay{Database: filepath.Join(root, "relay.db"), Token: "token", QueueEvents: 100, QueueBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	server := httptest.NewServer(r.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); r.Run(ctx) }()
	defer func() { cancel(); <-relayDone }()
	b := New(config.Bridge{SourceID: "test-linux", HubURL: "http://127.0.0.1:1", RelayURL: server.URL, RelayToken: "token", QueueEvents: 10, QueueBytes: 65536})
	if code, _ := b.enqueue(event("offline-stream")); code != 202 {
		t.Fatal(code)
	}
	bridgeDone := make(chan struct{})
	go func() { defer close(bridgeDone); b.Run(ctx) }()
	defer func() { cancel(); <-bridgeDone }()
	awaitStream(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.queue) == 0 })
	var health struct {
		Pending int  `json:"pending"`
		Hub     bool `json:"hub_connected"`
	}
	_, err = transport.Request(ctx, transport.Client(time.Second), server.URL, "token", "GET", "/healthz", nil, &health)
	if err != nil || health.Pending != 1 || health.Hub {
		t.Fatal("Hub离线时必须由Relay持久确认", health, err)
	}
	hc := config.Hub{Database: filepath.Join(root, "hub.db"), RelayURL: server.URL, RelayToken: "token"}
	h, err := hub.Open(hc)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); h.RunRelay(ctx, hc) }()
	defer func() { cancel(); <-hubDone }()
	awaitStream(t, func() bool { s, e := h.History(ctx, 0, 50); return e == nil && len(s) == 1 })
	if code, _ := b.enqueue(event("online-stream")); code != 202 {
		t.Fatal(code)
	}
	awaitStream(t, func() bool { s, e := h.History(ctx, 0, 50); return e == nil && len(s) == 2 })
}
func TestBatchAcknowledgementLostDoesNotDuplicateAndBadEventIsIsolated(t *testing.T) {
	h, err := hub.Open(config.Hub{Database: filepath.Join(t.TempDir(), "hub.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	server := httptest.NewServer(h.Handler(""))
	defer server.Close()
	dial := func() *transport.JSONSocket {
		ws, _, e := websocket.DefaultDialer.Dial(transport.WebSocketURL(server.URL, "/api/v1/connect"), nil)
		if e != nil {
			t.Fatal(e)
		}
		s := &transport.JSONSocket{Conn: ws}
		_ = s.WriteJSON(protocol.Message{Type: "hello", Payload: protocol.Device{ID: "test-linux", Role: "bridge", CollectorID: "test"}})
		_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
		var hello protocol.Message
		if s.ReadJSON(&hello) != nil {
			t.Fatal("未握手")
		}
		return s
	}
	a := event("same")
	a.CollectorID = "test"
	s := dial()
	if s.WriteJSON(protocol.Message{Type: "events", RequestID: "lost", Payload: protocol.EventBatch{Events: []protocol.Event{a}}}) != nil {
		t.Fatal("发送失败")
	}
	awaitStream(t, func() bool { v, e := h.History(context.Background(), 0, 50); return e == nil && len(v) == 1 })
	_ = s.Close()
	again := dial()
	defer again.Close()
	bad := event("bad")
	bad.SourceID = "another-source"
	_ = again.WriteJSON(protocol.Message{Type: "events", RequestID: "retry", Payload: protocol.EventBatch{Events: []protocol.Event{a, bad}}})
	var receipt struct {
		Type    string                `json:"type"`
		Payload protocol.BatchReceipt `json:"payload"`
	}
	for receipt.Type != "receipts" {
		_ = again.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err = again.ReadJSON(&receipt); err != nil {
			t.Fatal(err)
		}
	}
	if len(receipt.Payload.Results) != 2 || !receipt.Payload.Results[0].Duplicate || receipt.Payload.Results[1].Status != 400 {
		t.Fatal("确认与逐事件拒绝不符", receipt)
	}
	history, err := h.History(context.Background(), 0, 50)
	if err != nil || len(history) != 1 {
		t.Fatal("确认丢失重试重复入库")
	}
}
