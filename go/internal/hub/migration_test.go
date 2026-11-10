package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/sqlitepath"
	"agent-pulse/protocol"
)

// 固定旧版结构，避免用当前迁移器创建样本而漏掉真实升级问题。
const schemaV2 = `
CREATE TABLE events (position INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
 source_id TEXT NOT NULL, agent TEXT NOT NULL, session_id TEXT NOT NULL, payload BLOB NOT NULL,
 notify INTEGER NOT NULL, valid INTEGER NOT NULL, received_at TEXT NOT NULL);
CREATE INDEX events_session ON events(source_id,agent,session_id);
CREATE TABLE sessions (source_id TEXT NOT NULL, agent TEXT NOT NULL, session_id TEXT NOT NULL,
 state TEXT NOT NULL, title TEXT NOT NULL, project TEXT NOT NULL, last_event_id TEXT NOT NULL,
 last_sequence INTEGER NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(source_id,agent,session_id));
CREATE TABLE reads (event_id TEXT PRIMARY KEY, read_at TEXT NOT NULL);
CREATE TABLE push_jobs (event_id TEXT NOT NULL, channel TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0, next_at TEXT NOT NULL, PRIMARY KEY(event_id,channel));
CREATE TABLE adapter_hosts (source_id TEXT PRIMARY KEY, report BLOB NOT NULL);
PRAGMA user_version=2;`

func TestVersion2UpgradePreservesDataAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "旧数据库 #%.db")
	db, err := sql.Open("sqlite", sqlitepath.URI(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(schemaV2); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	events := []protocol.Event{
		{EventID: "read", SourceID: "test-host", Agent: "test", SessionID: "done", Kind: "turn.completed", OccurredAt: at, Title: "已完成", Scope: "main", Sequence: 1},
		{EventID: "pending", SourceID: "test-host", Agent: "test", SessionID: "waiting", Kind: "approval.required", OccurredAt: at, Title: "需要批准", Scope: "main", Sequence: 1},
		{EventID: "sending", SourceID: "test-host", Agent: "test", SessionID: "uncertain", Kind: "turn.completed", OccurredAt: at, Title: "结果未知", Scope: "main", Sequence: 1},
	}
	for i, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO events VALUES(?,?,?,?,?,?,?,?,?)`, i+1, event.EventID, event.SourceID, event.Agent, event.SessionID, payload, true, true, timestamp(at)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO sessions VALUES(?,?,?,?,?,?,?,?,?)`, event.SourceID, event.Agent, event.SessionID, event.State(), "旧会话 "+event.SessionID, "旧项目", event.EventID, 1, timestamp(at)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO push_jobs VALUES(?,?,?,?,?)`, event.EventID, "test-phone", []string{"sent", "pending", "sending"}[i], i+1, timestamp(at)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO reads VALUES(?,?)`, "read", timestamp(at)); err != nil {
		t.Fatal(err)
	}
	report := `{"source_id":"test-host","items":[{"id":"opencode","target":"0.2.0","installed":"0.2.0","running":"0.2.0","agent_version":"2.0.20"}],"updated_at":"2026-01-01T00:00:00Z"}`
	if _, err = db.Exec(`INSERT INTO adapter_hosts VALUES(?,?)`, "test-host", report); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	// 备份以已停止写入、连接已关闭的数据库为前提。
	backup, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "hub-v2-backup.db")
	if err = os.WriteFile(backupPath, backup, 0600); err != nil {
		t.Fatal(err)
	}
	checkData := func(t *testing.T, h *Hub, managed bool) {
		t.Helper()
		var version int
		if err := h.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
			t.Fatal("迁移版本错误", version, err)
		}
		snapshot, err := h.Snapshot(ctx)
		if err != nil || len(snapshot.Sessions) != 3 || len(snapshot.Unread) != 2 {
			t.Fatal("会话或未读丢失", snapshot, err)
		}
		for _, session := range snapshot.Sessions {
			if session.Title != "旧会话 "+session.SessionID || session.Project != "旧项目" || session.LastSequence != 1 {
				t.Fatal("来源信息变化", session)
			}
			if session.SessionID == "waiting" {
				if session.State != "waiting" || (managed && (session.Alias != "升级后的别名" || session.OverrideState != "closed" || !session.Archived || session.ManagementVersion != 1)) || (!managed && session.ManagementVersion != 0) {
					t.Fatal("管理数据或来源状态错误", session)
				}
			}
		}
		if snapshot.Unread[0].EventID != "pending" || snapshot.Unread[0].Valid == managed || snapshot.Unread[1].EventID != "sending" || !snapshot.Unread[1].Valid {
			t.Fatal("未读及有效性未保留", snapshot.Unread)
		}
		history, err := h.History(ctx, 0, 10)
		if err != nil || len(history) != len(events) {
			t.Fatal("历史丢失", history, err)
		}
		for i, got := range history {
			want := events[len(events)-1-i]
			if !reflect.DeepEqual(got.Event, want) || got.Position != int64(len(events)-i) || got.Read != (want.EventID == "read") {
				t.Fatal("历史正文、序号或已读变化", got)
			}
		}
		for i, event := range events {
			var status, next string
			var attempts int
			if err := h.db.QueryRow(`SELECT status,attempts,next_at FROM push_jobs WHERE event_id=? AND channel='test-phone'`, event.EventID).Scan(&status, &attempts, &next); err != nil || status != []string{"sent", "pending", "unknown"}[i] || attempts != i+1 || next != timestamp(at) {
				t.Fatal("推送任务变化或发送中任务未转为未知", event.EventID, status, attempts, next, err)
			}
		}
		recorder := httptest.NewRecorder()
		h.Handler("").ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/adapters/status", nil))
		var reports []json.RawMessage
		if err := json.Unmarshal(recorder.Body.Bytes(), &reports); err != nil || recorder.Code != 200 || len(reports) != 1 {
			t.Fatal("适配器报告读取失败", recorder.Code, err)
		}
		var got, want any
		if err := json.Unmarshal(reports[0], &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(report), &want); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("适配器报告变化", got, err)
		}
		if duplicate, err := h.Accept(ctx, events[1]); err != nil || !duplicate {
			t.Fatal("升级后去重失效", duplicate, err)
		}
	}
	open := func(database string) *Hub {
		t.Helper()
		h, err := Open(config.Hub{Database: database})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		return h
	}
	h := open(path)
	checkData(t, h, false)
	alias, state, archived := "升级后的别名", "closed", true
	if err = h.ManageSession(ctx, protocol.SessionChange{SessionKey: protocol.SessionKey{SourceID: "test-host", Agent: "test", SessionID: "waiting"}, LastEventID: "pending", Alias: &alias, Archived: &archived, OverrideState: &state}); err != nil {
		t.Fatal(err)
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	checkData(t, open(path), true)
	// 还原独立备份再升级，确认回退来源仍保留旧数据而非新版修改。
	checkData(t, open(backupPath), false)
}
