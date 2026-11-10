package relay_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/config"
	"agent-pulse/internal/hub"
	"agent-pulse/internal/relay"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func until(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func TestSessionManagementThroughRelayRequiresHubCommit(t *testing.T) {
	root := t.TempDir()
	r, err := relay.Open(config.Relay{Database: filepath.Join(root, "relay.db"), Token: "test", QueueEvents: 100, QueueBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	cfg := config.Hub{Database: filepath.Join(root, "hub.db"), RelayURL: srv.URL, RelayToken: "test"}
	h, err := hub.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event := protocol.Event{EventID: "wait", SourceID: "source", Agent: "test", SessionID: "main", Kind: "approval.required", OccurredAt: time.Now().UTC(), Title: "审批", Scope: "main"}
	if _, err = h.Accept(ctx, event); err != nil {
		t.Fatal(err)
	}
	closed := "closed"
	change := protocol.SessionChange{SessionKey: protocol.SessionKey{SourceID: "source", Agent: "test", SessionID: "main"}, LastEventID: "wait", OverrideState: &closed}
	client := transport.Client(time.Second)
	if status, _ := transport.Request(ctx, client, srv.URL, "test", "POST", "/api/v1/sessions/manage", change, nil); status != 503 {
		t.Fatal("离线管理不可入中继队列", status)
	}
	done := make(chan struct{})
	go func() { defer close(done); h.RunRelay(ctx, cfg) }()
	until(t, func() bool {
		status, _ := transport.Request(ctx, client, srv.URL, "test", "GET", "/api/v1/snapshot", nil, nil)
		return status == 200
	})
	if status, err := transport.Request(ctx, client, srv.URL, "test", "POST", "/api/v1/sessions/manage", change, nil); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	if status, _ := transport.Request(ctx, client, srv.URL, "test", "POST", "/api/v1/sessions/manage", change, nil); status != 409 {
		t.Fatal("旧修正请求应被拒绝", status)
	}
	cancel()
	<-done
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = hub.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	snapshot, err := h.Snapshot(context.Background())
	if err != nil || snapshot.Sessions[0].OverrideState != "closed" || snapshot.Sessions[0].State != "waiting" || snapshot.Unread[0].Read {
		t.Fatal("重启后应保留人工覆盖、来源事实与未读", snapshot, err)
	}
}

func TestAdapterUpdateThroughRelay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("安装器仅验证 Linux")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	priv := filepath.Join(root, "key")
	pubFile := filepath.Join(root, "pub")
	if err := adapters.GenerateKey(priv, pubFile); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(pubFile)
	bin := filepath.Join(root, "bin")
	_ = os.MkdirAll(bin, 0700)
	if err := os.WriteFile(filepath.Join(bin, "test-agent"), []byte("#!/bin/sh\necho 1.0.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(root, "source")
	_ = os.MkdirAll(source, 0700)
	_ = os.WriteFile(filepath.Join(source, "adapter"), []byte("#!/bin/sh\nprintf '{}'\n"), 0700)
	m := adapters.Manifest{ID: "relay-test", Version: "1.0.0", Format: 1, Protocol: 1, MinBridge: "0.2.0", Platform: runtime.GOOS + "/" + runtime.GOARCH, Kind: "executable", Entry: "adapter", Command: "test-agent", AgentMin: "1.0.0", AgentMax: "1.0.0", ConfigStyle: "hooks", Hooks: map[string]string{"Stop": ""}}
	p, err := adapters.BuildPackage(source, repo, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = adapters.SignIndex(repo, priv, adapters.Index{Sequence: 1, Packages: []adapters.Package{p}}); err != nil {
		t.Fatal(err)
	}
	r, err := relay.Open(config.Relay{Database: filepath.Join(root, "relay.db"), Token: "test", QueueEvents: 100, QueueBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rs := httptest.NewServer(r.Handler())
	defer rs.Close()
	hc := config.Hub{Database: filepath.Join(root, "hub.db"), AdapterDirectory: repo, RelayURL: rs.URL, RelayToken: "test"}
	h, err := hub.Open(hc)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	hs := httptest.NewServer(h.Handler(""))
	defer hs.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); h.RunRelay(ctx, hc) }()
	defer func() { cancel(); <-done }()
	until(t, func() bool {
		status, err := transport.Request(ctx, transport.Client(time.Second), rs.URL, "test", "GET", "/api/v1/adapters/index", nil, nil)
		return err == nil && status == 200
	})
	offline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer offline.Close()
	for _, sourceID := range []string{"direct", "relay"} {
		c := config.Bridge{SourceID: sourceID, HubURL: hs.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(root, sourceID), PublicKey: string(pub), Managed: map[string]string{"relay-test": filepath.Join(root, sourceID, "agent.json")}}}
		if sourceID == "relay" {
			c.HubURL = offline.URL
			c.RelayURL = rs.URL
			c.RelayToken = "test"
		}
		if err = adapters.Sync(ctx, c, filepath.Join(root, sourceID, "bridge.yaml")); err != nil {
			t.Fatal(err)
		}
		report := adapters.ReportState(c, nil)
		if len(report.Items) != 1 || report.Items[0].Installed != "1.0.0" || report.Items[0].Running != "" {
			t.Fatal("下载与实际运行必须区分", report)
		}
	}
}
func TestRelayRestartReadAndDuplex(t *testing.T) {
	c := config.Relay{Database: filepath.Join(t.TempDir(), "relay.db"), Token: "test-secret", QueueEvents: 100, QueueBytes: 1 << 20}
	r, err := relay.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := transport.Client(time.Second)
	e := protocol.Event{EventID: "relay-offline", SourceID: "linux-a", Agent: "test", SessionID: "main", Scope: "main", Kind: "input.required", Title: "请确认", OccurredAt: time.Now().UTC()}
	for i := 0; i < 2; i++ {
		var receipt protocol.Receipt
		status, err := transport.Request(ctx, client, srv.URL, c.Token, "POST", "/api/v1/events", e, &receipt)
		if err != nil || status != 202 || receipt.Accepted != "durable" || receipt.Duplicate != (i == 1) {
			t.Fatal(status, receipt, err)
		}
	}
	if status, _ := transport.Request(ctx, client, srv.URL, "wrong", "GET", "/api/v1/snapshot", nil, nil); status != 401 {
		t.Fatal("Relay 未鉴权", status)
	}
	if status, _ := transport.Request(ctx, client, srv.URL, c.Token, "GET", "/api/v1/snapshot", nil, nil); status != 503 {
		t.Fatal("离线应明确不可达", status)
	}
	read := protocol.ReadRequest{EventIDs: []string{e.EventID}}
	if status, err := transport.Request(ctx, client, srv.URL, c.Token, "POST", "/api/v1/read", read, nil); err != nil || status != 202 {
		t.Fatal(status, err)
	}
	srv.Close()
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = relay.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	srv = httptest.NewServer(r.Handler())
	defer srv.Close()
	runDone := make(chan struct{})
	go func() { defer close(runDone); r.Run(ctx) }()
	defer func() { cancel(); <-runDone }()
	hc := config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), RelayURL: srv.URL, RelayToken: c.Token}
	h, err := hub.Open(hc)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	connectionCtx, stopHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); h.RunRelay(connectionCtx, hc) }()
	defer func() { stopHub(); <-hubDone }()
	until(t, func() bool {
		history, err := h.History(ctx, 0, 50)
		if err != nil || len(history) != 1 {
			return false
		}
		snapshot, err := h.Snapshot(ctx)
		return err == nil && len(snapshot.Unread) == 0
	})
	until(t, func() bool {
		var status struct {
			Pending   int  `json:"pending"`
			Connected bool `json:"hub_connected"`
		}
		_, err := transport.Request(ctx, client, srv.URL, c.Token, "GET", "/healthz", nil, &status)
		return err == nil && status.Connected && status.Pending == 0
	})
	var s protocol.Snapshot
	if _, err = transport.Request(ctx, client, srv.URL, c.Token, "GET", "/api/v1/snapshot", nil, &s); err != nil || len(s.Sessions) != 1 {
		t.Fatal(err, s)
	}
	ws, _, err := websocket.DefaultDialer.Dial(transport.WebSocketURL(srv.URL, "/api/v1/connect"), http.Header{"Authorization": []string{"Bearer " + c.Token}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.WriteJSON(protocol.Message{Type: "hello", Payload: protocol.Device{ID: "test-relay-tray", Role: "tray"}})
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var message protocol.Message
	if err = ws.ReadJSON(&message); err != nil || message.Type != "hello" {
		t.Fatal(err, message)
	}
	until(t, func() bool {
		s, err := h.Snapshot(ctx)
		return err == nil && len(s.Devices) == 1 && s.Devices[0].Path == "relay"
	})
	e.EventID = "live"
	e.Kind = "turn.completed"
	if _, err = transport.Request(ctx, client, srv.URL, c.Token, "POST", "/api/v1/events", e, nil); err != nil {
		t.Fatal(err)
	}
	if err = ws.ReadJSON(&message); err != nil || message.Type != "changed" {
		t.Fatal(err, message)
	}
	until(t, func() bool { s, err := h.Snapshot(ctx); return err == nil && len(s.Unread) == 1 })
	if status, err := transport.Request(ctx, client, srv.URL, c.Token, "POST", "/api/v1/read", protocol.ReadRequest{EventIDs: []string{"live"}}, nil); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	stopHub()
	<-hubDone
	until(t, func() bool { s, err := h.Snapshot(ctx); return err == nil && len(s.Devices) == 0 })
	if status, _ := transport.Request(ctx, client, srv.URL, c.Token, "GET", "/api/v1/snapshot", nil, nil); status != 503 {
		t.Fatal("断开后不能返回旧快照", status)
	}
}
