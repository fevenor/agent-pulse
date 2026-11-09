package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func testEvent(id, kind string, seq int64) protocol.Event {
	return protocol.Event{EventID: id, SourceID: "linux", Agent: "test", SessionID: "main", Kind: kind, OccurredAt: time.Now().UTC(), Title: "请查看", Body: "详细说明", Scope: "main", Sequence: seq}
}
func testHub(t *testing.T) *Hub {
	t.Helper()
	h, err := Open(config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), Push: []config.Channel{{Name: "phone"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestClosePreservesTerminalNoticeButClearsWaiting(t *testing.T) {
	for _, kind := range []string{"turn.completed", "run.failed", "run.interrupted", "input.required", "approval.required"} {
		t.Run(kind, func(t *testing.T) {
			h := testHub(t)
			ctx := context.Background()
			if _, err := h.Accept(ctx, testEvent("notice", kind, 1)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Accept(ctx, testEvent("close", "session.closed", 2)); err != nil {
				t.Fatal(err)
			}
			s, err := h.Snapshot(ctx)
			if err != nil || len(s.Unread) != 1 || s.Sessions[0].State != "closed" {
				t.Fatal(s, err)
			}
			want := kind != "input.required" && kind != "approval.required"
			if s.Unread[0].Valid != want {
				t.Fatal("关闭不应撤销结果通知，但应撤销等待", kind, s.Unread)
			}
			_, _, deliver := h.claimPush(ctx)
			if deliver != want {
				t.Fatal("关闭后的手机推送策略不符", kind, deliver)
			}
		})
	}
}

func TestDurableDedupReadAndOrdering(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	cfg := config.Hub{Database: path, Push: []config.Channel{{Name: "phone"}}}
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.MarkRead(ctx, []string{"early"}); err != nil {
		t.Fatal(err)
	}
	e := testEvent("early", "input.required", 1)
	if duplicate, err := h.Accept(ctx, e); err != nil || duplicate {
		t.Fatal(duplicate, err)
	}
	if duplicate, err := h.Accept(ctx, e); err != nil || !duplicate {
		t.Fatal(duplicate, err)
	}
	conflict := e
	conflict.Body = "不同正文"
	if _, err = h.Accept(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	later := testEvent("later", "turn.completed", 4)
	if _, err = h.Accept(ctx, later); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Accept(ctx, testEvent("old", "activity.observed", 2)); err != nil {
		t.Fatal(err)
	}
	history := testEvent("historical", "run.failed", 8)
	history.Historical = true
	if _, err = h.Accept(ctx, history); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Accept(ctx, testEvent("tool", "tool.failed", 5)); err != nil {
		t.Fatal(err)
	}
	s, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sessions) != 1 || s.Sessions[0].State != "idle" || s.Sessions[0].LastEventID != "later" {
		t.Fatalf("旧事件覆盖状态: %+v", s.Sessions)
	}
	if len(s.Unread) != 2 || !s.Unread[0].Valid || s.Unread[1].Valid {
		t.Fatalf("未读有效性错误: %+v", s.Unread)
	}
	var count int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM push_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("任务重复或已读仍建任务: %d %v", count, err)
	}
	previous := s.RunID
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	s, err = h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.RunID == previous || len(s.Unread) != 2 || len(s.Devices) != 0 {
		t.Fatal("恢复后的快照错误")
	}
	if duplicate, err := h.Accept(ctx, later); err != nil || !duplicate {
		t.Fatal("重启后去重失败", err)
	}
	if err = h.MarkRead(ctx, []string{"later", "historical", "later"}); err != nil {
		t.Fatal(err)
	}
	s, err = h.Snapshot(ctx)
	if err != nil || len(s.Unread) != 0 || len(s.Sessions) != 1 {
		t.Fatal("已读不应删除会话", err)
	}
}

func TestMainScopeAndReactivation(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	e := testEvent("child", "input.required", 1)
	e.Scope = "child"
	if _, err := h.Accept(ctx, e); err == nil {
		t.Fatal("子代理被接收")
	}
	e = testEvent("wait", "approval.required", 0)
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Accept(ctx, testEvent("resume", "session.resumed", 0)); err != nil {
		t.Fatal(err)
	}
	s, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Unread) != 1 || s.Unread[0].Valid || s.Sessions[0].State != "running" {
		t.Fatal("恢复活动未使等待提醒失效")
	}
}

func TestTransactionRollback(t *testing.T) {
	h := testHub(t)
	if _, err := h.db.Exec(`CREATE TRIGGER deny_push BEFORE INSERT ON push_jobs BEGIN SELECT RAISE(ABORT,'故障注入'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Accept(context.Background(), testEvent("atomic", "input.required", 1)); err == nil {
		t.Fatal("故障注入未触发")
	}
	for _, table := range []string{"events", "sessions", "push_jobs"} {
		var n int
		if err := h.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("事务留下部分数据 %s %d %v", table, n, err)
		}
	}
}

func TestUnsequencedLiveEventAndExpiry(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	if _, err := h.Accept(ctx, testEvent("start", "session.started", 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Accept(ctx, testEvent("permission", "approval.required", 0)); err != nil {
		t.Fatal(err)
	}
	s, err := h.Snapshot(ctx)
	if err != nil || s.Sessions[0].State != "waiting" || s.Sessions[0].LastSequence != 10 {
		t.Fatal("无序号事件应更新状态并保留原生序号", s, err)
	}
	if _, err := h.Accept(ctx, testEvent("stale", "session.resumed", 5)); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Snapshot(ctx)
	if s.Sessions[0].State != "waiting" {
		t.Fatal("旧序号覆盖状态")
	}
	e := testEvent("expired", "input.required", 11)
	e.OccurredAt = time.Now().Add(-25 * time.Hour)
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	before, _ := h.Snapshot(ctx)
	h.expireReminders(ctx)
	after, _ := h.Snapshot(ctx)
	if after.Version.Version <= before.Version.Version {
		t.Fatal("到期未发布变化通知")
	}
	if len(after.Unread) != 2 || after.Unread[1].Valid {
		t.Fatal("过期提醒应保留未读历史但停止提醒")
	}
}
