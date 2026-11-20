package relay_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
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

// 浏览器用子协议携带令牌，服务端必须回传已选择的业务子协议。
func TestBrowserSyncThroughRelayNegotiatesProtocol(t *testing.T) {
	root := t.TempDir()
	r, err := relay.Open(config.Relay{Database: filepath.Join(root, "relay.db"), Token: "browser-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	server := httptest.NewServer(r.Handler())
	defer server.Close()
	cfg := config.Hub{Database: filepath.Join(root, "hub.db"), RelayURL: server.URL, RelayToken: "browser-test"}
	h, err := hub.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.RunRelay(ctx, cfg) }()
	defer func() { cancel(); <-done }()
	until(t, func() bool {
		status, _ := transport.Request(ctx, transport.Client(time.Second), server.URL, "browser-test", "GET", "/api/v1/snapshot", nil, nil)
		return status == 200
	})
	address := transport.WebSocketURL(server.URL, "/api/v1/sync")
	dialer := websocket.Dialer{HandshakeTimeout: time.Second, Subprotocols: []string{"agent-pulse", "pulse-token." + base64.RawURLEncoding.EncodeToString([]byte("wrong"))}}
	ws, response, err := dialer.Dial(address, nil)
	if ws != nil {
		ws.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal("子协议不能绕过鉴权", err)
	}
	dialer.Subprotocols[1] = "pulse-token." + base64.RawURLEncoding.EncodeToString([]byte("browser-test"))
	ws, _, err = dialer.Dial(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if ws.Subprotocol() != "agent-pulse" {
		t.Fatal("标准浏览器要求服务端选择已请求的子协议", ws.Subprotocol())
	}
	socket := &transport.JSONSocket{Conn: ws}
	socket.SetReadLimit(transport.MaxWireJSON)
	if err = socket.WriteJSON(protocol.Message{Type: "sync_hello", RequestID: "browser-probe", Payload: protocol.SyncHello{Device: protocol.Device{ID: "browser-test", Role: "web"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		socket.SetReadDeadline(time.Now().Add(3 * time.Second))
		var message struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err = socket.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if i == 0 && message.Type != "sync" {
			t.Fatal("缺少同步基准", message.Type)
		}
		if i == 1 {
			var checkpoint protocol.SyncCheckpoint
			if message.Type != "checkpoint" || json.Unmarshal(message.Payload, &checkpoint) != nil || checkpoint.Probe != "browser-probe" {
				t.Fatal("缺少权威探测回显", message.Type)
			}
		}
	}
}
