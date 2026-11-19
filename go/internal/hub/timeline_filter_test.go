package hub

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

func TestTimelineFiltersBeforePagingAndLocatesUnreadAcrossActivity(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	key := protocol.SessionKey{SourceID: "source", Agent: "antigravity", SessionID: "main"}
	add := func(id, kind, body string) {
		t.Helper()
		e := protocol.Event{EventID: id, SourceID: key.SourceID, Agent: key.Agent, SessionID: key.SessionID, Kind: kind, Title: "antigravity · 正在运行", Body: body, Scope: "main", OccurredAt: time.Now()}
		if _, err := h.Accept(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	add("old", "turn.completed", "旧未读")
	for i := 0; i < 500; i++ {
		add(fmt.Sprintf("activity-%d", i), "activity.observed", "")
	}
	add("warning", "activity.observed", "工具错误，不能隐藏")
	add("unknown", "observation.lost", "")
	add("latest", "turn.completed", "最新已读")
	if err := h.MarkRead(ctx, []string{"latest"}); err != nil {
		t.Fatal(err)
	}
	page, err := h.SessionTimeline(ctx, key, TimelineQuery{Important: true, Limit: 30})
	if err != nil || len(page.Events) != 4 || page.More || page.Events[3].EventID != "old" || !page.Events[0].Read {
		t.Fatal(page, err)
	}
	anchor, err := h.SessionTimeline(ctx, key, TimelineQuery{Important: true, Limit: 30, Anchor: "old"})
	if err != nil || len(anchor.Events) != 1 || !anchor.Newer || anchor.Events[0].Read || anchor.Cursor != page.Cursor {
		t.Fatal(anchor, err)
	}
	full, err := h.SessionTimeline(ctx, key, TimelineQuery{Limit: 30})
	if err != nil || len(full.Events) != 30 || !full.More {
		t.Fatal(full, err)
	}
	var count int
	before := int64(0)
	for {
		p, err := h.SessionTimeline(ctx, key, TimelineQuery{Before: before, Limit: 30})
		if err != nil {
			t.Fatal(err)
		}
		count += len(p.Events)
		if !p.More {
			break
		}
		before = p.Events[len(p.Events)-1].Position
	}
	if count != 504 {
		t.Fatal("完整原始历史丢失", count)
	}
	after := page.Cursor
	for i := 0; i < 100; i++ {
		add(fmt.Sprintf("later-activity-%d", i), "activity.observed", "")
	}
	empty, err := h.SessionTimeline(ctx, key, TimelineQuery{Important: true, Limit: 30, After: &after})
	if err != nil || len(empty.Events) != 0 || empty.More || empty.Cursor <= after {
		t.Fatal("隐藏事件必须推进扫描位置", empty, err)
	}
	after = empty.Cursor
	for i := 0; i < 65; i++ {
		add(fmt.Sprintf("later-result-%d", i), "turn.completed", "完成")
	}
	seen := map[string]bool{}
	for {
		p, err := h.SessionTimeline(ctx, key, TimelineQuery{Important: true, Limit: 30, After: &after})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Events {
			if seen[e.EventID] || e.Position <= after {
				t.Fatal("重复或位置回退", e)
			}
			seen[e.EventID] = true
		}
		after = p.Cursor
		if !p.More {
			break
		}
	}
	if len(seen) != 65 {
		t.Fatal("跨页增量遗漏", len(seen))
	}
	server := httptest.NewServer(h.Handler(""))
	defer server.Close()
	client := transport.Client(time.Second)
	for _, query := range []string{"view=bad", "view=important", "view=important&source_id=source&agent=antigravity&session_id=main&anchor=old&after=0", "view=important&source_id=source&agent=antigravity&session_id=main&anchor=old&before=2"} {
		if status, _ := transport.Request(ctx, client, server.URL, "", "GET", "/api/v1/events?"+query, nil, nil); status != 400 {
			t.Fatal("无效查询未拒绝", query, status)
		}
	}
	var got TimelinePage
	status, err := transport.Request(ctx, client, server.URL, "", "GET", "/api/v1/events?view=important&source_id=source&agent=antigravity&session_id=main&anchor=old&limit=30", nil, &got)
	if status != 200 || err != nil || len(got.Events) != 1 || got.RunID == "" || !got.Newer {
		t.Fatal(status, got, err)
	}
	status, _ = transport.Request(ctx, client, server.URL, "", "GET", "/api/v1/events?view=important&source_id=source&agent=antigravity&session_id=other&anchor=old", nil, nil)
	if status != 404 {
		t.Fatal("不能跨会话定位", status)
	}
}

func TestTimelineKeepsMeaningfulEmptyBodyActivity(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	e := testEvent("tool-error", "activity.observed", 1)
	e.Body = ""
	e.Title = "工具执行失败"
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	p, err := h.SessionTimeline(ctx, protocol.SessionKey{SourceID: e.SourceID, Agent: e.Agent, SessionID: e.SessionID}, TimelineQuery{Important: true, Limit: 30})
	if err != nil || len(p.Events) != 1 {
		t.Fatal("不能隐藏无正文但有错误标题的记录", p, err)
	}
}
