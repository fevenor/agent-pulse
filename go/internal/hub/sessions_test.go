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

func TestRenameKeepsIdentityStateUnreadAndRejectsOlderName(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	start := testEvent("start-name", "session.started", 1)
	start.SessionTitle = "原名称"
	if _, err := h.Accept(ctx, start); err != nil {
		t.Fatal(err)
	}
	waiting := testEvent("waiting-name", "approval.required", 2)
	waiting.SessionTitle = "旧任务携带的标题"
	if _, err := h.Accept(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	before, _ := h.Snapshot(ctx)
	override := "interrupted"
	if err := h.ManageSession(ctx, protocol.SessionChange{SessionKey: protocol.SessionKey{SourceID: waiting.SourceID, Agent: waiting.Agent, SessionID: waiting.SessionID}, LastEventID: waiting.EventID, OverrideState: &override}); err != nil {
		t.Fatal(err)
	}
	before, _ = h.Snapshot(ctx)
	rename := testEvent("rename-latest", "session.renamed", 30)
	rename.SessionTitle = "改名后的最新名称"
	if _, err := h.Accept(ctx, rename); err != nil {
		t.Fatal(err)
	}
	older := rename
	older.EventID, older.Sequence, older.SessionTitle = "rename-old", 20, "较旧名称"
	if _, err := h.Accept(ctx, older); err != nil {
		t.Fatal(err)
	}
	after, _ := h.Snapshot(ctx)
	if len(after.Sessions) != 1 || after.Sessions[0].Title != rename.SessionTitle || after.Sessions[0].State != before.Sessions[0].State || after.Sessions[0].OverrideState != override || after.Sessions[0].LastEventID != waiting.EventID || after.Sessions[0].NameSequence != 30 {
		t.Fatal("改名应只更新同一会话名称，不改变活动与人工状态", after)
	}
	if len(after.Unread) != len(before.Unread) || after.Unread[0].Valid != before.Unread[0].Valid || after.Unread[0].Read != before.Unread[0].Read {
		t.Fatal("改名不应产生或清除提醒", after.Unread)
	}
	late := testEvent("late-status-name", "activity.observed", 3)
	late.SessionTitle = "任务补报中的旧标题"
	if _, err := h.Accept(ctx, late); err != nil {
		t.Fatal(err)
	}
	after, _ = h.Snapshot(ctx)
	if after.Sessions[0].Title != rename.SessionTitle {
		t.Fatal("任务中的旧标题不能覆盖改名")
	}
	unknown := testEvent("unknown-name", "session.renamed", 1)
	unknown.SessionID = "未观察到的会话"
	if _, err := h.Accept(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	unnamed := testEvent("unnamed", "session.started", 1)
	unnamed.SessionID, unnamed.Project = "不应显示的后台ID", "项目名"
	if _, err := h.Accept(ctx, unnamed); err != nil {
		t.Fatal(err)
	}
	after, _ = h.Snapshot(ctx)
	if len(after.Sessions) != 2 {
		t.Fatal("改名不能创建未观察的会话", after.Sessions)
	}
	for _, session := range after.Sessions {
		if session.SessionID == unnamed.SessionID && session.Title != "" {
			t.Fatal("无原生名称时不得把项目或会话ID当标题")
		}
	}
}

func TestUnsequencedNamesCannotRollbackKnownNativeRename(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	start := testEvent("unordered-start", "session.started", 0)
	start.SessionTitle = "原名称"
	if _, err := h.Accept(ctx, start); err != nil {
		t.Fatal(err)
	}
	live := testEvent("unordered-live", "activity.observed", 0)
	live.SessionTitle = "下一次事件中的新名称"
	if _, err := h.Accept(ctx, live); err != nil {
		t.Fatal(err)
	}
	s, _ := h.Snapshot(ctx)
	if s.Sessions[0].Title != live.SessionTitle {
		t.Fatal("无原生序号来源应在下一次事件更新名称")
	}
	rename := testEvent("ordered-rename", "session.renamed", 30)
	rename.SessionTitle = "最新原生改名"
	if _, err := h.Accept(ctx, rename); err != nil {
		t.Fatal(err)
	}
	live.EventID, live.SessionTitle = "late-unordered", "旧无序号载荷"
	if _, err := h.Accept(ctx, live); err != nil {
		t.Fatal(err)
	}
	older := testEvent("late-unordered-rename", "session.renamed", 0)
	older.SessionTitle = "旧无序号改名"
	if _, err := h.Accept(ctx, older); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Snapshot(ctx)
	if s.Sessions[0].Title != rename.SessionTitle || s.Sessions[0].LastEventID != live.EventID {
		t.Fatal("缺少序号不能回滚已知新名，但仍处理实时状态", s)
	}
	latest := testEvent("ordered-latest", "activity.observed", 31)
	latest.SessionTitle = "后续有序事件的名称"
	if _, err := h.Accept(ctx, latest); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Snapshot(ctx)
	if s.Sessions[0].Title != latest.SessionTitle || s.Sessions[0].NameSequence != 31 {
		t.Fatal("较新原生序号应允许更新名称", s)
	}
}
