package event

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAntigravityDiagnosticDistinguishesUnknownFromChildWithoutContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, ".gemini", "antigravity")
	transcript := filepath.Join(root, "brain", "child", ".system_generated", "logs", "transcript.jsonl")
	input := map[string]any{"conversationId": "child", "agentName": "browser", "transcriptPath": transcript, "lastUserInput": "禁止记录的用户输入", "finalModelOutput": "禁止记录的模型内容", "toolCall": map[string]any{"args": map[string]string{"secret": "禁止记录的工具参数"}}}
	raw, _ := json.Marshal(input)
	e, d, err := ConvertAntigravityDiagnostic("source", "SessionStart", raw, time.Now())
	if err != nil || e == nil || d.Metadata == nil || d.Metadata.Identity != "unknown" || d.Metadata.Database != "missing" || d.Metadata.RootSource != "transcript" {
		t.Fatal(e, d, err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "conversation_summaries.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec("CREATE TABLE conversation_summaries(conversation_id TEXT,title TEXT,parent_conversation_id TEXT,nesting_depth INTEGER,source TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	_, d, err = ConvertAntigravityDiagnostic("source", "PreInvocation", raw, time.Now())
	if err != nil || d.Metadata.Database != "no_row" {
		t.Fatal(d, err)
	}
	_, err = db.Exec("INSERT INTO conversation_summaries VALUES('child','不输出的会话标题','main',1,'')")
	if err != nil {
		t.Fatal(err)
	}
	e, d, err = ConvertAntigravityDiagnostic("source", "PreInvocation", raw, time.Now())
	if err != nil || e != nil || d.Metadata.Identity != "child" || d.Metadata.Database != "matched" {
		t.Fatal(e, d, err)
	}
	text, _ := json.Marshal(d)
	if strings.Contains(string(text), "禁止记录") || strings.Contains(string(text), "不输出的会话标题") || strings.Contains(string(text), "transcript.jsonl") {
		t.Fatal("诊断泄露非白名单内容", string(text))
	}
	delete(input, "transcriptPath")
	raw, _ = json.Marshal(input)
	e, d, err = ConvertAntigravityDiagnostic("source", "PreInvocation", raw, time.Now())
	if err != nil || e == nil || d.Metadata.RootSource != "default_cli" || d.Metadata.Identity != "unknown" {
		t.Fatal(e, d, err)
	}
	input["parentConversationId"] = "main"
	raw, _ = json.Marshal(input)
	e, d, err = ConvertAntigravityDiagnostic("source", "SessionStart", raw, time.Now())
	if err != nil || e != nil || d.Metadata != nil || d.NativeIdentity["parentConversationId"] != "main" {
		t.Fatal(e, d, err)
	}
}

func TestAntigravityNativeStopReasonsAndBackgroundWork(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		reason string
		idle   *bool
		kind   string
	}{
		{"NO_TOOL_CALL", &no, "activity.observed"}, {"model_stop", &no, "activity.observed"},
		{"NO_TOOL_CALL", &yes, "turn.completed"}, {"NO_TOOL_CALL", nil, "observation.lost"},
		{"ERROR", &no, "run.failed"}, {"MAX_INVOCATIONS", &yes, "run.failed"},
		{"MAX_FORCED_INVOCATIONS", &no, "run.failed"}, {"MAX_TOKEN_BUDGET_EXCEEDED", &yes, "run.failed"},
		{"USER_CANCELED", &no, "run.interrupted"}, {"TERMINAL_STEP_TYPE", &yes, "observation.lost"},
		{"TERMINAL_CUSTOM_HOOK", &yes, "observation.lost"}, {"INJECTED_RESPONSE", &yes, "observation.lost"},
	} {
		if got := antigravityStopKind(tc.reason, tc.idle); got != tc.kind {
			t.Fatal(tc.reason, got, tc.kind)
		}
	}
}
