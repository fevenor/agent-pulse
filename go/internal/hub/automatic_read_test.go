package hub

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"agent-pulse/protocol"
)

func TestRunningAutomaticallyReadsPreviousNoticesInSameSession(t *testing.T) {
	for _, kind := range []string{"session.started", "session.resumed", "activity.observed"} {
		t.Run(kind, func(t *testing.T) {
			h := testHub(t)
			makeTestBridgeOnline(h, "linux", "")
			ctx := context.Background()
			notices := []string{"turn.completed", "task.completed", "input.required", "approval.required", "run.failed", "run.interrupted"}
			for i, notice := range notices {
				if _, err := h.Accept(ctx, testEvent(notice, notice, int64(i+1))); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.MarkRead(ctx, []string{notices[0]}); err != nil {
				t.Fatal(err)
			}
			var manualReadAt string
			if err := h.db.QueryRow(`SELECT read_at FROM reads WHERE event_id=?`, notices[0]).Scan(&manualReadAt); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"source", "agent", "session"} {
				e := testEvent("other-"+field, "turn.completed", 1)
				switch field {
				case "source":
					e.SourceID = "other-source"
				case "agent":
					e.Agent = "other-agent"
				case "session":
					e.SessionID = "other-session"
				}
				if _, err := h.Accept(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.Accept(ctx, testEvent("continue", kind, 10)); err != nil {
				t.Fatal(err)
			}
			s, err := h.Snapshot(ctx)
			if err != nil || len(s.Unread) != 3 {
				t.Fatal("应只清除同一来源、Agent及会话的旧未读", s, err)
			}
			for _, e := range s.Unread {
				if e.SourceID == "linux" && e.Agent == "test" && e.SessionID == "main" {
					t.Fatal("同会话旧未读未清除", e)
				}
			}
			key := protocol.SessionKey{SourceID: "linux", Agent: "test", SessionID: "main"}
			history, err := h.SessionHistory(ctx, key, 0, 20)
			if err != nil || len(history) != len(notices)+1 {
				t.Fatal("自动已读不得删除历史", history, err)
			}
			for _, e := range history {
				if e.Notifies() && (!e.Read || e.Valid || e.Body != "详细说明") {
					t.Fatal("有效及失效旧提醒都应已读并保留原文", e)
				}
			}
			var afterReadAt string
			if err := h.db.QueryRow(`SELECT read_at FROM reads WHERE event_id=?`, notices[0]).Scan(&afterReadAt); err != nil || afterReadAt != manualReadAt {
				t.Fatal("不得覆盖既有手工已读时间", afterReadAt, err)
			}
			// 旧手机任务复用既有已读/失效检查，不重新发送。
			for range notices {
				if _, _, deliver := h.claimPush(ctx); deliver {
					t.Fatal("旧提醒已读后仍触发手机投递")
				}
			}
			if _, err := h.Accept(ctx, testEvent("next-result", "turn.completed", 11)); err != nil {
				t.Fatal(err)
			}
			s, err = h.Snapshot(ctx)
			if err != nil || len(s.Unread) != 4 || s.Unread[3].EventID != "next-result" || !s.Unread[3].Valid {
				t.Fatal("下一轮结果必须保持新的未读", s, err)
			}
			cfg := h.config
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restored, err := reopened.Snapshot(ctx)
			if err != nil || !reflect.DeepEqual(restored.Unread, s.Unread) {
				t.Fatal("重启应保留自动已读及下一轮未读", restored, err)
			}
		})
	}
}

func TestRunningReadsOldNoticesEvenWhenAlreadyRunningOrUnknown(t *testing.T) {
	for _, previousState := range []string{"running", "unknown"} {
		t.Run(previousState, func(t *testing.T) {
			h := testHub(t)
			makeTestBridgeOnline(h, "linux", "")
			ctx := context.Background()
			if _, err := h.Accept(ctx, testEvent("start", "session.started", 1)); err != nil {
				t.Fatal(err)
			}
			if previousState == "unknown" {
				if _, err := h.Accept(ctx, testEvent("lost", "observation.lost", 2)); err != nil {
					t.Fatal(err)
				}
			}
			// 历史补报形成失效未读；不要求上一状态必须是停止或等待。
			e := testEvent("old-result", "turn.completed", 2)
			e.Historical = true
			if _, err := h.Accept(ctx, e); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Accept(ctx, testEvent("progress", "activity.observed", 3)); err != nil {
				t.Fatal(err)
			}
			s, err := h.Snapshot(ctx)
			if err != nil || len(s.Unread) != 0 || len(s.Sessions) != 1 || s.Sessions[0].State != "running" {
				t.Fatal("有效运行事件应清除旧未读，不额外限制上一状态", s, err)
			}
		})
	}
}

func TestRejectedOrNonRunningEventsNeverAutomaticallyRead(t *testing.T) {
	for _, name := range []string{"duplicate", "conflict", "old-sequence", "equal-sequence", "historical", "old-collector", "child", "renamed", "closed", "lost", "retry", "new-result", "new-wait"} {
		t.Run(name, func(t *testing.T) {
			h := testHub(t)
			makeTestBridgeOnline(h, "linux", "")
			ctx := context.Background()
			start := testEvent("start", "session.resumed", 1)
			if _, err := h.Accept(ctx, start); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Accept(ctx, testEvent("notice", "input.required", 10)); err != nil {
				t.Fatal(err)
			}
			e := testEvent("incoming", "activity.observed", 11)
			switch name {
			case "duplicate", "conflict":
				e = start
				if name == "conflict" {
					e.Body = "冲突内容"
				}
			case "old-sequence":
				e.Sequence = 9
			case "equal-sequence":
				e.Sequence = 10
			case "historical":
				e.Historical = true
			case "old-collector":
				e.CollectorID = "old"
			case "child":
				e.Scope = "child"
			case "renamed":
				e.Kind = "session.renamed"
			case "closed":
				e.Kind = "session.closed"
			case "lost":
				e.Kind = "observation.lost"
			case "retry":
				e.Kind = "tool.failed"
			case "new-result":
				e.Kind = "turn.completed"
			case "new-wait":
				e.Kind = "approval.required"
			}
			duplicate, err := h.Accept(ctx, e)
			switch name {
			case "conflict":
				if !errors.Is(err, ErrConflict) {
					t.Fatal("事件冲突应拒绝", err)
				}
			case "child":
				if err == nil {
					t.Fatal("子代理应拒绝")
				}
			default:
				if err != nil || duplicate != (name == "duplicate") {
					t.Fatal(duplicate, err)
				}
			}
			s, err := h.Snapshot(ctx)
			if err != nil || len(s.Unread) == 0 || s.Unread[0].EventID != "notice" {
				t.Fatal("无效运行事件及非运行事件不得清除旧未读", s, err)
			}
		})
	}
}

func TestAutomaticReadRollsBackWithRunningEvent(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	if _, err := h.Accept(ctx, testEvent("notice", "input.required", 1)); err != nil {
		t.Fatal(err)
	}
	before, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 在写入已读和会话之后让新事件入库失败，验证整个事务一起回滚。
	if _, err := h.db.Exec(`CREATE TRIGGER deny_running BEFORE INSERT ON events
WHEN NEW.event_id='resume' BEGIN SELECT RAISE(ABORT,'故障注入'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Accept(ctx, testEvent("resume", "session.resumed", 2)); err == nil {
		t.Fatal("故障注入未触发")
	}
	after, err := h.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("运行事件失败不得留下已读或状态变化", after, err)
	}
	var reads int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM reads`).Scan(&reads); err != nil || reads != 0 {
		t.Fatal("事务失败仍保留自动已读", reads, err)
	}
}
