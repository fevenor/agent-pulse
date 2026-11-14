package event

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNativeHookSemantics(t *testing.T) {
	tests := []struct{ agent, hook, json, kind string }{
		{"codex", "Stop", `{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`, "turn.completed"},
		{"codex", "Interrupt", `{"session_id":"s"}`, "run.interrupted"},
		{"codex", "PostToolUse", `{"session_id":"s","tool_name":"Bash","tool_response":{"exit_code":1}}`, ""},
		{"codex", "SubagentStop", `{"session_id":"s","agent_id":"child"}`, ""},
		{"qoderclicn", "StopFailure", `{"session_id":"s","error_type":"rate_limit","error":"秘密"}`, "run.failed"},
		{"qoderclicn", "PermissionRequest", `{"session_id":"s","agent_id":"child"}`, ""},
		{"qoderclicn", "PermissionRequest", `{"session_id":"s","tool_name":"Bash"}`, "approval.required"},
		{"qoderclicn", "Notification", `{"session_id":"s","notification_type":"idle_prompt"}`, ""},
		{"zcode", "SessionStart", `{"session_id":"s","source":"startup","hook_event_name":"SessionStart"}`, "session.started"},
		{"zcode", "SessionStart", `{"session_id":"s","source":"resume"}`, "session.resumed"},
		{"zcode", "UserPromptSubmit", `{"session_id":"s","prompt":"私有提示词"}`, "session.resumed"},
		{"zcode", "Stop", `{"session_id":"s","last_assistant_message":"私有回答","stop_hook_active":false}`, "turn.completed"},
		{"zcode", "Stop", `{"session_id":"child","agent_type":"zcode-explore"}`, ""},
		{"zcode", "Stop", `{"session_id":"child","agentName":"zcode-explore"}`, ""},
		{"zcode", "Stop", `{"session_id":"child","parentSessionId":"main"}`, ""},
		{"zcode", "PermissionRequest", `{"session_id":"child","tool_name":"Bash"}`, ""},
		{"zcode", "PostToolUseFailure", `{"session_id":"s","error":"工具报错"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.agent+tt.hook, func(t *testing.T) {
			e, err := Convert(tt.agent, "host", tt.hook, []byte(tt.json), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if tt.kind == "" {
				if e != nil {
					t.Fatal("本应忽略", e)
				}
				return
			}
			if e == nil || e.Kind != tt.kind || e.Body != "" || e.Scope != "main" {
				t.Fatal(e)
			}
		})
	}
}

func TestLongNativeReplyIsDiscardedBeforeSending(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"session_id": "s", "hook_event_name": "Stop", "responseText": strings.Repeat("私有回复", 20000)})
	e, err := Convert("zcode", "host", "Stop", raw, time.Now())
	if err != nil || e == nil || e.Kind != "turn.completed" || e.Body != "" {
		t.Fatal("长回复导致完成事件丢失", err)
	}
	result, _ := json.Marshal(e)
	if len(result) > 1024 || strings.Contains(string(result), "私有回复") {
		t.Fatal("上传了原始回复")
	}
}
func TestAntigravityDirectStartMultipleSessionsAndUnknownStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	now := time.Now()
	for _, id := range []string{"first", "second", "resumed"} {
		e, err := Convert("antigravity", "host", "SessionStart", []byte(`{"conversationId":"`+id+`"}`), now)
		if err != nil || e == nil || e.SessionID != id {
			t.Fatal("直接启动与新会话不应依赖包装或旧绑定", e, err)
		}
	}
	for _, payload := range []string{`{"conversationId":"child","parentConversationId":"main"}`, `{"conversationId":"child","agent_id":"child"}`} {
		e, err := Convert("antigravity", "host", "Stop", []byte(payload), now)
		if err != nil || e != nil {
			t.Fatal("明确子代理必须过滤", e, err)
		}
	}
	for _, tt := range []struct{ payload, want string }{
		{`{"conversationId":"main","terminationReason":"model_stop"}`, "turn.completed"},
		{`{"conversationId":"main","terminationReason":"NO_TOOL_CALL","fullyIdle":true}`, "turn.completed"},
		{`{"conversationId":"main","terminationReason":"NO_TOOL_CALL","fullyIdle":false}`, "observation.lost"},
		{`{"conversationId":"main","terminationReason":"model_stop","fullyIdle":false}`, "activity.observed"},
		{`{"conversationId":"main","terminationReason":"new_reason"}`, "observation.lost"},
	} {
		e, err := Convert("antigravity", "host", "Stop", []byte(tt.payload), now)
		if err != nil || e == nil || e.Kind != tt.want {
			t.Fatal(e, err)
		}
	}
}
