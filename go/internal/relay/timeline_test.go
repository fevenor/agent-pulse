package relay_test

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
)

func TestFilteredTimelineAndAnchorThroughRelay(t *testing.T) {
	root := t.TempDir()
	r, err := relay.Open(config.Relay{Database: filepath.Join(root, "relay.db"), Token: "test", QueueEvents: 100, QueueBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	server := httptest.NewServer(r.Handler())
	defer server.Close()
	cfg := config.Hub{Database: filepath.Join(root, "hub.db"), RelayURL: server.URL, RelayToken: "test"}
	h, err := hub.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, e := range []protocol.Event{
		{EventID: "unread", SourceID: "source", Agent: "test", SessionID: "main", Kind: "turn.completed", Title: "完成", Scope: "main", OccurredAt: time.Now()},
		{EventID: "activity", SourceID: "source", Agent: "test", SessionID: "main", Kind: "activity.observed", Title: "test · 正在运行", Scope: "main", OccurredAt: time.Now()},
	} {
		if _, err = h.Accept(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); h.RunRelay(ctx, cfg) }()
	defer func() { cancel(); <-done }()
	client := transport.Client(time.Second)
	path := "/api/v1/events?source_id=source&agent=test&session_id=main&view=important&anchor=unread&limit=30"
	var page hub.TimelinePage
	until(t, func() bool {
		status, _ := transport.Request(ctx, client, server.URL, "test", "GET", path, nil, &page)
		return status == 200
	})
	if len(page.Events) != 1 || page.Events[0].EventID != "unread" || page.Cursor <= page.Events[0].Position || page.RunID == "" {
		t.Fatal("中继丢失筛选/定位参数或扫描位置", page)
	}
}
