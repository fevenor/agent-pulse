package bridge

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
	"agent-pulse/internal/hub"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func event(id string) protocol.Event {
	return protocol.Event{EventID: id, SourceID: "test-linux", SessionID: "main", Agent: "test", Scope: "main", Kind: "input.required", Title: "等待输入", Body: "请选择", OccurredAt: time.Now().UTC()}
}

func TestQueueBoundaries(t *testing.T) {
	b := New(config.Bridge{QueueEvents: 1, QueueBytes: 65536})
	e := event("a")
	if s, d := b.enqueue(e); s != 202 || d {
		t.Fatal(s, d)
	}
	if s, d := b.enqueue(e); s != 202 || !d {
		t.Fatal(s, d)
	}
	e.Body = "不同"
	if s, _ := b.enqueue(e); s != 409 {
		t.Fatal(s)
	}
	if s, _ := b.enqueue(event("b")); s != 503 {
		t.Fatal(s)
	}
	if b.rejected != 1 || b.bytes == 0 {
		t.Fatal("溢出未计数")
	}
	b = New(config.Bridge{QueueEvents: 100, QueueBytes: 1})
	if s, _ := b.enqueue(event("bytes")); s != 503 {
		t.Fatal("字节上限无效")
	}
}

func TestOfflineRetryLostAckAndTwoClients(t *testing.T) {
	h, err := hub.Open(config.Hub{Database: filepath.Join(t.TempDir(), "hub.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var online atomic.Bool
	var loseAck atomic.Bool
	loseAck.Store(true)
	handler := h.Handler("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			w.WriteHeader(503)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/v1/events" && loseAck.CompareAndSwap(true, false) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Errorf("提交失败 %d", rec.Code)
			}
			w.WriteHeader(503)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	b := New(config.Bridge{SourceID: "test-linux", HubURL: upstream.URL, QueueEvents: 10, QueueBytes: 65536})
	local := httptest.NewServer(b.Handler())
	defer local.Close()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); b.Run(ctx) }()
	defer func() { cancel(); <-finished }()
	e := event("offline")
	if err = Emit(ctx, strings.TrimPrefix(local.URL, "http://"), e); err != nil {
		t.Fatal(err)
	}
	s, err := h.Snapshot(ctx)
	if err != nil || len(s.Unread) != 0 {
		t.Fatal("离线时不应入库")
	}
	online.Store(true)
	deadline := time.Now().Add(8 * time.Second)
	for {
		b.mu.Lock()
		pending := len(b.queue)
		b.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("队列未排空")
		}
		time.Sleep(20 * time.Millisecond)
	}
	history, err := h.History(ctx, 0, 50)
	if err != nil || len(history) != 1 {
		t.Fatal("确认丢失重试产生重复事件", err, len(history))
	}
	clients := []*websocket.Conn{}
	for _, id := range []string{"one", "two"} {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http")+"/api/v1/connect", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		if err = ws.WriteJSON(protocol.Message{Type: "hello", Payload: protocol.Device{ID: id, Role: "client"}}); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var hello protocol.Message
		if err = ws.ReadJSON(&hello); err != nil || hello.Type != "hello" {
			t.Fatal(err, hello)
		}
		clients = append(clients, ws)
	}
	if _, err = transport.Request(ctx, transport.Client(time.Second), upstream.URL, "", "POST", "/api/v1/read", protocol.ReadRequest{EventIDs: []string{e.EventID}}, nil); err != nil {
		t.Fatal(err)
	}
	current, err := h.Snapshot(ctx)
	if err != nil || len(current.Unread) != 0 || len(current.Sessions) != 1 {
		t.Fatal("已读同步未生效")
	}
	for _, ws := range clients {
		for {
			var msg struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err = ws.ReadJSON(&msg); err != nil {
				t.Fatal(err)
			}
			var v protocol.Version
			_ = json.Unmarshal(msg.Payload, &v)
			if msg.Type == "changed" && v.Version >= current.Version.Version {
				break
			}
		}
	}
}

func TestLocalIngressRejectsBrowserAndLargePayload(t *testing.T) {
	b := New(config.Bridge{SourceID: "test-linux", QueueEvents: 10, QueueBytes: 65536})
	server := httptest.NewServer(b.Handler())
	defer server.Close()
	for _, tc := range []struct {
		origin, host, body string
		want               int
	}{
		{"https://evil.invalid", "", `{}`, 403},
		{"", "evil.invalid:18702", `{}`, 403},
		{"", "", `{"body":"` + strings.Repeat("x", 65536) + `"}`, 413},
		{"", "", `{"scope":"child"}`, 400},
	} {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/events", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", tc.origin)
		if tc.host != "" {
			req.Host = tc.host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatal(resp.StatusCode, tc.want)
		}
	}
}
