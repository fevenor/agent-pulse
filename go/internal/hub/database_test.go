package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

// 只验证当前数据库的重启和备份恢复，不构造旧版数据库迁移场景。
func TestCurrentDatabasePersistsNamesReadsManagementAndUncertainPush(t *testing.T) {
	cfg := config.Hub{Database: filepath.Join(t.TempDir(), "当前数据库 #%.db")}
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := testEvent("current-format-event", "turn.completed", 1)
	e.SessionTitle = "当前会话名称"
	if _, err = h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = h.MarkRead(ctx, []string{e.EventID}); err != nil {
		t.Fatal(err)
	}
	alias, archived := "本地备注", true
	if err = h.ManageSession(ctx, protocol.SessionChange{SessionKey: protocol.SessionKey{SourceID: e.SourceID, Agent: e.Agent, SessionID: e.SessionID}, LastEventID: e.EventID, Alias: &alias, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec(`INSERT INTO push_jobs(event_id,channel,status,attempts,next_at) VALUES(?,?,'sending',1,?)`, e.EventID, "test-phone", timestamp(e.OccurredAt)); err != nil {
		t.Fatal(err)
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "当前格式备份.db")
	if err = os.WriteFile(backup, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cfg.Database, backup} {
		reopened, err := Open(config.Hub{Database: path})
		if err != nil {
			t.Fatal(err)
		}
		s, err := reopened.Snapshot(ctx)
		if err != nil || len(s.Sessions) != 1 || s.Sessions[0].Title != e.SessionTitle || s.Sessions[0].Alias != alias || !s.Sessions[0].Archived || len(s.Unread) != 0 {
			t.Fatal("当前格式重开不得丢失名称、管理或已读", s, err)
		}
		var state string
		if err = reopened.db.QueryRow(`SELECT status FROM push_jobs WHERE event_id=?`, e.EventID).Scan(&state); err != nil || state != "unknown" {
			t.Fatal("发送中断后不得假定失败并盲目重发", state, err)
		}
		if duplicate, err := reopened.Accept(ctx, e); err != nil || !duplicate {
			t.Fatal("当前格式恢复后去重失效", duplicate, err)
		}
		if err = reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
