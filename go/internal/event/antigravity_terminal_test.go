package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-pulse/protocol"
)

func TestAntigravityFinalIdleAfterSubagentAndNextTurnIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "antigravity")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE conversation_summaries(conversation_id TEXT,last_user_input_time TEXT,last_user_input_step_index INTEGER,status TEXT,not_fully_idle INTEGER,killed INTEGER,parent_conversation_id TEXT,nesting_depth INTEGER);
 INSERT INTO conversation_summaries VALUES('main','2026-11-19T10:00:00Z',7,'CASCADE_RUN_STATUS_IDLE',1,0,'',0)`)
	if err != nil {
		t.Fatal(err)
	}
	e := protocol.Event{EventID: "stop", SourceID: "host", Agent: "antigravity", SessionID: "main", Kind: "activity.observed", Title: "运行", Scope: "main", OccurredAt: time.Now()}
	raw := []byte(`{"conversationId":"main","terminationReason":"NO_TOOL_CALL","fullyIdle":false}`)
	obs, turn := AntigravityTerminalObservation("Stop", raw, &e, &AntigravityMetadata{Identity: "main", Root: root})
	if obs == nil {
		t.Fatal("主会话后台未结束时未登记观察")
	}
	terminal := e
	terminal.Kind = "turn.completed"
	finalObs, finalTurn := AntigravityTerminalObservation("Stop", []byte(`{"conversationId":"main","terminationReason":"NO_TOOL_CALL","fullyIdle":true}`), &terminal, &AntigravityMetadata{Identity: "main", Root: root})
	if finalObs != nil || finalTurn != turn || turn == "" {
		t.Fatal("原生终态与后台观察轮次不一致")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	emitted := make(chan protocol.Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- ObserveAntigravity(ctx, obs.Cursor, "host", func(e protocol.LocalEvent) error { emitted <- e.Event; return nil })
	}()
	select {
	case <-emitted:
		t.Fatal("后台仍运行时提前完成")
	case <-time.After(70 * time.Millisecond):
	}
	_, err = db.Exec("UPDATE conversation_summaries SET not_fully_idle=0")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-emitted:
		if result.Kind != "turn.completed" || result.SessionID != "main" {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("原生空闲未确认")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE conversation_summaries SET last_user_input_step_index=9")
	if err != nil {
		t.Fatal(err)
	}
	if err = ObserveAntigravity(ctx, obs.Cursor, "host", func(protocol.LocalEvent) error { t.Fatal("旧观察越过新用户轮次"); return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := AntigravityTerminalObservation("Stop", raw, &e, &AntigravityMetadata{Identity: "child", Root: root}); got != nil {
		t.Fatal("子会话被登记终态")
	}
	identity := AntigravityIdentityCursor("main", &AntigravityMetadata{Identity: "unknown", Root: root, RootSource: "transcript"})
	if len(identity) == 0 {
		t.Fatal("App未知身份未登记")
	}
	var cursor map[string]string
	_ = json.Unmarshal(identity, &cursor)
	// 父子识别使用独立最小表；错误结构必须返回unknown而非主身份。
	if got, err := CheckAntigravityIdentity(identity); err != nil || got != "unknown" {
		t.Fatal(got, err)
	}
}
