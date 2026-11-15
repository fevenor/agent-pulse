package hub

import (
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewSessionEventsAfterPagesDoNotSkipOrMixSessions(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	key := protocol.SessionKey{SourceID: "timeline", Agent: "qoderclicn", SessionID: "main"}
	for i := 0; i < 75; i++ {
		e := protocol.Event{EventID: fmt.Sprintf("new-%d", i), SourceID: key.SourceID, Agent: key.Agent, SessionID: key.SessionID, Kind: "turn.completed", OccurredAt: time.Now(), Scope: "main", Title: "测试", Body: fmt.Sprintf("第%d次输出", i)}
		if _, err := h.Accept(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	other := protocol.Event{EventID: "other", SourceID: key.SourceID, Agent: key.Agent, SessionID: "other", Kind: "turn.completed", OccurredAt: time.Now(), Scope: "main", Title: "其他会话"}
	if _, err := h.Accept(ctx, other); err != nil {
		t.Fatal(err)
	}
	after := int64(0)
	seen := map[string]bool{}
	for {
		page, err := h.SessionEventsAfter(ctx, key, after, 30)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if e.Position <= after || e.SessionID != key.SessionID || seen[e.EventID] {
				t.Fatal("分页重复、逆序或混入其他会话", e)
			}
			after = e.Position
			seen[e.EventID] = true
		}
	}
	if len(seen) != 75 {
		t.Fatal("丢失跨页的新事件", len(seen))
	}
	server := httptest.NewServer(h.Handler(""))
	defer server.Close()
	for _, query := range []string{"after=-1", "after=broken", "after=1&before=2&source_id=timeline&agent=qoderclicn&session_id=main", "after=1"} {
		code, _ := transport.Request(ctx, transport.Client(time.Second), server.URL, "", "GET", "/api/v1/events?"+query, nil, nil)
		if code != 400 {
			t.Fatal("不完整查询未被拒绝", query, code)
		}
	}
	var result []protocol.EventView
	code, err := transport.Request(ctx, transport.Client(time.Second), server.URL, "", "GET", "/api/v1/events?after=60&limit=30&source_id=timeline&agent=qoderclicn&session_id=main", nil, &result)
	if code != 200 || err != nil || len(result) != 15 {
		t.Fatal("实际接口未按会话分页", code, err, len(result))
	}
}
