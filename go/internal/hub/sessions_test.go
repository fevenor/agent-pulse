package hub

import (
	"agent-pulse/protocol"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionManagementPreservesFactsAndRejectsStaleChanges(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	start := testEvent("start", "session.started", 1)
	start.SessionTitle = "真实会话标题"
	if _, err := h.Accept(ctx, start); err != nil {
		t.Fatal(err)
	}
	notice := testEvent("waiting", "approval.required", 2)
	notice.Title = "请求批准某操作"
	if _, err := h.Accept(ctx, notice); err != nil {
		t.Fatal(err)
	}
	key := protocol.SessionKey{SourceID: notice.SourceID, Agent: notice.Agent, SessionID: notice.SessionID}
	closed := "closed"
	archived := true
	change := protocol.SessionChange{SessionKey: key, LastEventID: "waiting", OverrideState: &closed, Archived: &archived}
	if err := h.ManageSession(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err := h.ManageSession(ctx, change); !errors.Is(err, ErrSessionChanged) {
		t.Fatal("旧版本应拒绝", err)
	}
	s, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sessions[0].Title != "真实会话标题" || s.Sessions[0].State != "waiting" || s.Sessions[0].OverrideState != "closed" || !s.Sessions[0].Archived || s.Unread[0].Read || s.Unread[0].Valid {
		t.Fatal("人工修正不得覆盖来源事实或清除未读", s)
	}
	old := testEvent("old", "activity.observed", 1)
	if _, err = h.Accept(ctx, old); err != nil {
		t.Fatal(err)
	}
	historical := testEvent("history", "activity.observed", 10)
	historical.Historical = true
	if _, err = h.Accept(ctx, historical); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Snapshot(ctx)
	if s.Sessions[0].OverrideState != "closed" {
		t.Fatal("迟到历史清除了人工覆盖")
	}
	activity := testEvent("new", "activity.observed", 3)
	if _, err = h.Accept(ctx, activity); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Snapshot(ctx)
	if s.Sessions[0].OverrideState != "" || !s.Sessions[0].Archived || s.Sessions[0].State != "running" {
		t.Fatal("有效活动应只解除状态覆盖", s)
	}
	other := testEvent("other", "session.started", 1)
	other.SourceID = "other-host"
	if _, err = h.Accept(ctx, other); err != nil {
		t.Fatal(err)
	}
	events, err := h.SessionHistory(ctx, key, 0, 2)
	if err != nil || len(events) != 2 || events[0].SourceID != key.SourceID {
		t.Fatal(events, err)
	}
	earlier, err := h.SessionHistory(ctx, key, events[1].Position, 2)
	if err != nil || len(earlier) != 2 || earlier[0].Position >= events[1].Position {
		t.Fatal("会话分页边界错误", earlier, err)
	}
}

func TestUnsequencedHistoryAndClockRollbackPreserveOrderingContract(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	e := testEvent("now", "input.required", 0)
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	state := "interrupted"
	key := protocol.SessionKey{SourceID: e.SourceID, Agent: e.Agent, SessionID: e.SessionID}
	if err := h.ManageSession(ctx, protocol.SessionChange{SessionKey: key, LastEventID: e.EventID, OverrideState: &state}); err != nil {
		t.Fatal(err)
	}
	e.EventID = "late"
	e.Kind = "activity.observed"
	e.OccurredAt = e.OccurredAt.Add(-time.Hour)
	e.Historical = true
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	s, _ := h.Snapshot(ctx)
	if s.Sessions[0].OverrideState != "interrupted" {
		t.Fatal("历史补报不应恢复自动跟随")
	}
	e.EventID = "live-with-old-clock"
	e.Historical = false
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Snapshot(ctx)
	if s.Sessions[0].OverrideState != "" || s.Sessions[0].State != "running" {
		t.Fatal("无序号实时事件应按到达顺序处理，不得因来源时钟回拨吞掉进展")
	}
}
