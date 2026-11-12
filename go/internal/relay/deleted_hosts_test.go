package relay_test

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/hub"
	"agent-pulse/internal/relay"
	"agent-pulse/internal/sqlitepath"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

func TestDeletedHostQueueIsRemovedIncludingBlockedEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Relay{Database: filepath.Join(t.TempDir(), "relay.db"), Token: "test-token", QueueEvents: 100, QueueBytes: 1 << 20}
	r, err := relay.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	relayServer := httptest.NewServer(r.Handler())
	defer relayServer.Close()
	client := transport.Client(2 * time.Second)
	events := []protocol.Event{}
	for index, id := range []string{"deleted-retry", "deleted-blocked", "active-event", "other-blocked"} {
		source := "deleted-host"
		if index >= 2 {
			source = "active-host"
		}
		event := protocol.Event{EventID: id, SourceID: source, Agent: "test", SessionID: id, Scope: "main", Kind: "input.required", Title: "主机事件", Body: "需要清理的正文", OccurredAt: time.Now().UTC()}
		events = append(events, event)
		if status, err := transport.Request(ctx, client, relayServer.URL, cfg.Token, "POST", "/api/v1/events", event, nil); err != nil || status != 202 {
			t.Fatalf("事件入队失败：%d %v", status, err)
		}
	}
	// 在发送线程启动前构造已有阻塞记录，覆盖删除前曾经遭遇永久错误的队列。
	db, err := sql.Open("sqlite", sqlitepath.URI(cfg.Database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE pending SET blocked=1 WHERE business_key IN (?,?)`, "event:deleted-blocked", "event:other-blocked"); err != nil {
		t.Fatal(err)
	}
	hubCfg := config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), RelayURL: relayServer.URL, RelayToken: cfg.Token}
	h, err := hub.Open(hubCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	hubServer := httptest.NewServer(h.Handler(""))
	defer hubServer.Close()
	if status, err := transport.Request(ctx, client, hubServer.URL, "", "DELETE", "/api/v1/hosts/deleted-host", nil, nil); err != nil || status != 200 {
		t.Fatalf("删除主机失败：%d %v", status, err)
	}
	runDone, hubDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(runDone); r.Run(ctx) }()
	go func() { defer close(hubDone); h.RunRelay(ctx, hubCfg) }()
	defer func() { cancel(); <-runDone; <-hubDone }()
	until(t, func() bool {
		var health struct {
			Pending   int  `json:"pending"`
			Blocked   int  `json:"blocked"`
			Connected bool `json:"hub_connected"`
		}
		status, err := transport.Request(ctx, client, relayServer.URL, cfg.Token, "GET", "/healthz", nil, &health)
		return err == nil && status == 200 && health.Connected && health.Pending == 1 && health.Blocked == 1
	})
	var deletedCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pending WHERE json_extract(payload,'$.source_id')='deleted-host'`).Scan(&deletedCount); err != nil || deletedCount != 0 {
		t.Fatalf("已删除主机的事件正文仍在队列：%d %v", deletedCount, err)
	}
	history, err := h.History(ctx, 0, 20)
	if err != nil || len(history) != 1 || history[0].EventID != "active-event" {
		t.Fatalf("删除处理干扰了其他主机投递：%+v %v", history, err)
	}
	report := map[string]any{"source_id": "deleted-host", "items": []any{}}
	if status, _ := transport.Request(ctx, client, relayServer.URL, cfg.Token, "POST", "/api/v1/adapters/status", report, nil); status != 410 {
		t.Fatalf("中继未透传已删除主机报告的拒绝状态：%d", status)
	}
	// 清理后再次送来旧队列也必须拒绝并移除，不得恢复主机。
	if status, err := transport.Request(ctx, client, relayServer.URL, cfg.Token, "POST", "/api/v1/events", events[0], nil); err != nil || status != 202 {
		t.Fatalf("旧事件重试未进入验证路径：%d %v", status, err)
	}
	until(t, func() bool {
		err := db.QueryRow(`SELECT COUNT(*) FROM pending WHERE json_extract(payload,'$.source_id')='deleted-host'`).Scan(&deletedCount)
		return err == nil && deletedCount == 0
	})
	snapshot, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Sources["deleted-host"]; exists {
		t.Fatal("Relay 旧事件恢复了已删除主机")
	}
}
