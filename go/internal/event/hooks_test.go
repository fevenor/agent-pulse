package event

import (
	"path/filepath"
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
	}
	for _, tt := range tests {
		t.Run(tt.agent+tt.hook, func(t *testing.T) {
			e, err := Convert(tt.agent, "host", tt.hook, []byte(tt.json), time.Now(), "")
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
func TestAntigravityBindingAndUnknownStop(t *testing.T) {
	binding := filepath.Join(t.TempDir(), "session")
	now := time.Now()
	e, err := Convert("antigravity", "host", "SessionStart", []byte(`{"conversationId":"main"}`), now, binding)
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	for _, hook := range []string{"SessionStart", "Stop"} {
		e, err = Convert("antigravity", "host", hook, []byte(`{"conversationId":"child","terminationReason":"model_stop"}`), now, binding)
		if err != nil || e != nil {
			t.Fatal("子会话误报", e, err)
		}
	}
	for _, tt := range []struct{ payload, want string }{
		{`{"conversationId":"main","terminationReason":"NO_TOOL_CALL","fullyIdle":true}`, "turn.completed"},
		{`{"conversationId":"main","terminationReason":"NO_TOOL_CALL","fullyIdle":false}`, "observation.lost"},
		{`{"conversationId":"main","terminationReason":"NO_TOOL_CALL"}`, "observation.lost"},
		{`{"conversationId":"main","terminationReason":"model_stop","fullyIdle":false}`, "activity.observed"},
	} {
		e, err = Convert("antigravity", "host", "Stop", []byte(tt.payload), now, binding)
		if err != nil || e == nil || e.Kind != tt.want {
			t.Fatal(tt.payload, e, err)
		}
	}
	e, err = Convert("antigravity", "host", "Stop", []byte(`{"conversationId":"main","terminationReason":"new_unknown_reason"}`), now, binding)
	if err != nil || e == nil || e.Kind != "observation.lost" {
		t.Fatal(e, err)
	}
	e, err = Convert("antigravity", "host", "Stop", []byte(`{"conversationId":"main","terminationReason":"model_stop"}`), now, "")
	if err != nil || e != nil {
		t.Fatal("未绑定不能假定主会话", e, err)
	}
}
