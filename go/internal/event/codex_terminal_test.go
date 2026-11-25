package event

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-pulse/protocol"
)

func terminalFixture(t *testing.T) (codexCursor, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_SQLITE_HOME", home)
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-main.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return codexCursor{SessionID: "main", TurnID: "current", Path: path}, path
}
func finalLine(turn string, failure bool, body string) []byte {
	payload := map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": body}
	if failure {
		payload["error"] = map[string]any{"message": "模型最终报错", "codex_error_info": "other", "response_body": "不可采集", "credentials": "不可采集"}
	}
	raw, _ := json.Marshal(map[string]any{"type": "event_msg", "timestamp": "2026-11-18T00:00:00Z", "payload": payload})
	return append(raw, '\n')
}
func TestCodexOnlyConvertsFinalEventOfRegisteredTurn(t *testing.T) {
	cursor, _ := terminalFixture(t)
	for _, raw := range [][]byte{[]byte(`{"type":"event_msg","payload":{"type":"error","turn_id":"current","message":"网络重试"}}`), finalLine("other", true, "")} {
		if e, done, err := codexTerminal(raw, cursor, "source"); e != nil || done || err != nil {
			t.Fatalf("采集了非目标终态：%s", raw)
		}
	}
	e, done, err := codexTerminal(finalLine("current", true, "失败前正文"), cursor, "source")
	if err != nil {
		t.Fatal(err)
	}
	if !done || e == nil || e.Kind != "run.failed" || e.Body != "模型最终报错" || strings.Contains(e.Body, "不可采集") {
		t.Fatal(e, done)
	}
	repeated, _, _ := codexTerminal(finalLine("current", true, "不同非采集内容"), cursor, "source")
	if repeated.EventID != e.EventID {
		t.Fatal("最终事件身份不稳定")
	}
	e, done, _ = codexTerminal(finalLine("current", false, "最终可见回答"), cursor, "source")
	if !done || e == nil || e.Kind != "turn.completed" || e.Body != "最终可见回答" {
		t.Fatal(e, done)
	}
}
func TestCodexObservationDoesNotReplayHistoryAndWaitsForCompleteLine(t *testing.T) {
	cursor, path := terminalFixture(t)
	old := finalLine("current", true, "")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Hook{SessionID: "main", TurnID: "current", Transcript: path})
	req := CodexObservation("UserPromptSubmit", raw)
	if req == nil {
		t.Fatal("没有登记本轮")
	}
	if err := json.Unmarshal(req.Cursor, &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor.Offset != int64(len(old)) {
		t.Fatal("读取了登记前的历史")
	}
	if e, done, err := readCodexTerminal(&cursor, "source"); e != nil || done || err != nil {
		t.Fatal(e, done, err)
	}
	line := finalLine("current", true, "")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(line[:len(line)-1]); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before := cursor.Offset
	if e, done, err := readCodexTerminal(&cursor, "source"); e != nil || done || err != nil || cursor.Offset != before {
		t.Fatal("半行被丢失", e, done, err)
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{'\n'})
	f.Close()
	if e, done, err := readCodexTerminal(&cursor, "source"); e == nil || !done || err != nil {
		t.Fatal(e, done, err)
	}
}
func TestCodexTerminalReaderSkipsHugeUnrelatedRecordsAndKeepsLongFinal(t *testing.T) {
	cursor, path := terminalFixture(t)
	raw := []byte(strings.Repeat("x", 9<<20) + "\n")
	raw = append(raw, finalLine("current", false, strings.Repeat("回答", 50000))...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var e *protocol.Event
	for i := 0; i < 4; i++ {
		var done bool
		var err error
		e, done, err = readCodexTerminal(&cursor, "source")
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	if e == nil || e.Kind != "turn.completed" || len(e.Body) > 32<<10 || !strings.Contains(e.Body, "已截断") {
		t.Fatal("终态被超大普通记录或长回答吞掉")
	}
}
func TestCodexObserverCancellationAndUnsafeSource(t *testing.T) {
	cursor, _ := terminalFixture(t)
	if codexRollout("main", filepath.Join(t.TempDir(), "other.jsonl")) != "" {
		t.Fatal("允许任意文件作为终态来源")
	}
	raw, _ := json.Marshal(cursor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ObserveCodex(ctx, raw, "source", func(protocol.LocalEvent) error { t.Fatal("凭空发送结束"); return nil }); err != context.Canceled {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(Hook{SessionID: "main", TurnID: "current", AgentID: "child", Transcript: cursor.Path})
	if CodexObservation("UserPromptSubmit", raw) != nil {
		t.Fatal("登记了子代理")
	}
	raw, _ = json.Marshal(Hook{SessionID: "main", TurnID: "current", CWD: filepath.Join(os.Getenv("CODEX_HOME"), "memories_v2"), Transcript: cursor.Path})
	if CodexObservation("UserPromptSubmit", raw) != nil {
		t.Fatal("登记了记忆任务")
	}
	raw, _ = json.Marshal(Hook{SessionID: "main", TurnID: "current", Transcript: cursor.Path})
	if e, err := Convert("codex", "source", "Stop", raw, time.Now()); e != nil || err != nil {
		t.Fatal("Stop提前确认了整轮完成", e, err)
	}
}

func TestMalformedFinalIsUnavailableAndNotSilentlySkipped(t *testing.T) {
	cursor, path := terminalFixture(t)
	raw := []byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"current"}}` + "\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if e, done, err := readCodexTerminal(&cursor, "source"); e != nil || done || err == nil || cursor.Offset != 0 {
			t.Fatal("损坏终态被当成空闲而永久等待", e, done, err, cursor.Offset)
		}
	}
}

func TestCodexObservedFailureCarriesLocalTurnForDeduplication(t *testing.T) {
	cursor, path := terminalFixture(t)
	if err := os.WriteFile(path, finalLine("current", true, ""), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cursor)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	count := 0
	if err := ObserveCodex(ctx, raw, "source", func(e protocol.LocalEvent) error {
		count++
		if e.TurnID != "current" || e.Kind != "run.failed" || e.SessionID != "main" {
			t.Fatalf("原生终态未保留登记轮次：%+v", e)
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatal("最终错误没有且仅有一次交付", err, count)
	}
}
