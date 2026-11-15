package event

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFinalOutputAndQuestionContents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	for _, agent := range []string{"zcode", "qoderclicn"} {
		t.Run(agent, func(t *testing.T) {
			raw := `{"session_id":"main","last_assistant_message":"完成：结果是42。\n第二行🙂","prompt":"不应上传用户提示词","reasoning":"不应上传思考"}`
			e, err := Convert(agent, "host", "Stop", []byte(raw), time.Now())
			if err != nil || e == nil || e.Body != "完成：结果是42。\n第二行🙂" {
				t.Fatal(e, err)
			}
			for _, name := range []string{"AskUserQuestion", "ask_user_question"} {
				input := map[string]any{"session_id": "main", "hook_event_name": "PreToolUse", "tool_name": name, "tool_input": map[string]any{"questions": []any{
					map[string]any{"header": "饮品", "question": "你喜欢哪种饮品？", "multiSelect": false, "options": []any{map[string]any{"label": "咖啡", "description": "提神"}, map[string]any{"label": "茶", "description": "清香"}}},
					map[string]any{"question": "周末做什么？", "multiSelect": true, "options": []any{map[string]any{"label": "阅读"}}},
				}, "unrelated": "不应上传"}}
				data, _ := json.Marshal(input)
				e, err = Convert(agent, "host", "PreToolUse", data, time.Now())
				if err != nil || e == nil || e.Kind != "input.required" || !strings.Contains(e.Body, "你喜欢哪种饮品？\n1. 咖啡 — 提神\n2. 茶 — 清香") || !strings.Contains(e.Body, "周末做什么？（可多选）") || strings.Contains(e.Body, "不应上传") {
					t.Fatal(e, err)
				}
				input["agent_id"] = "child"
				data, _ = json.Marshal(input)
				e, err = Convert(agent, "host", "PreToolUse", data, time.Now())
				if err != nil || e != nil {
					t.Fatal("子代理问题泄露", e, err)
				}
			}
		})
	}
}
func TestQuestionsAreNotDoubleReportedAsApprovals(t *testing.T) {
	for _, agent := range []string{"qoderclicn", "zcode"} {
		e, err := Convert(agent, "host", "PermissionRequest", []byte(`{"session_id":"s","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"选择？"}]}}`), time.Now())
		if err != nil || e != nil {
			t.Fatal("提问不应再报告审批", agent, e, err)
		}
	}
	e, err := Convert("qoderclicn", "host", "PermissionRequest", []byte(`{"session_id":"s","tool_name":"Bash","tool_input":{"command":"敏感命令"}}`), time.Now())
	if err != nil || e == nil || e.Kind != "approval.required" || !strings.Contains(e.Body, "敏感命令") {
		t.Fatal("其他权限请求内容未按白名单展示", e, err)
	}
}
func TestBodyMissingMalformedAndBounded(t *testing.T) {
	for _, field := range []string{`null`, `42`, `{"text":"错误类型"}`, `[]`} {
		e, err := Convert("qoderclicn", "host", "Stop", []byte(`{"session_id":"s","last_assistant_message":`+field+`}`), time.Now())
		if err != nil || e == nil || e.Body != "" || e.Kind != "turn.completed" {
			t.Fatal(e, err)
		}
	}
	for _, raw := range []string{`null`, `{"questions":"错误类型"}`, `{"questions":[{"question":"","options":[{"label":"不能只有选项"}]}]}`} {
		e, err := Convert("zcode", "host", "PreToolUse", []byte(`{"session_id":"s","tool_name":"AskUserQuestion","tool_input":`+raw+`}`), time.Now())
		if err != nil || e == nil || e.Kind != "input.required" || e.Body != "" {
			t.Fatal(e, err)
		}
	}
	body := visibleBody(strings.Repeat("中文🙂", 20000))
	if len(body) > 32<<10 || !utf8.ValidString(body) || !strings.Contains(body, "已截断") {
		t.Fatal("正文上限或UTF-8截断错误")
	}
	e, err := Convert("zcode", "host", "PreToolUse", []byte(`{"session_id":"child","parentSessionId":"main","tool_name":"AskUserQuestion"}`), time.Now())
	if err != nil || e != nil {
		t.Fatal("父会话字段必须过滤", e, err)
	}
	e, err = Convert("zcode", "host", "PreToolUse", []byte(`{"session_id":"s","tool_name":"Bash"}`), time.Now())
	if err != nil || e != nil {
		t.Fatal("未授权的ZCode工具应忽略", e, err)
	}
}

func TestZCodeQuestionResultReturnsToActivity(t *testing.T) {
	e, err := Convert("zcode", "host", "PostToolUse", []byte(`{"session_id":"s","tool_name":"AskUserQuestion","tool_response":{"answers":"不采集用户答案"}}`), time.Now())
	if err != nil || e == nil || e.Kind != "activity.observed" || e.Body != "" {
		t.Fatal("提问工具返回后状态错误或上传用户答案", e, err)
	}
}

func TestMalformedNotificationContentPreservesWaitingState(t *testing.T) {
	e, err := Convert("qoderclicn", "host", "Notification", []byte(`{"session_id":"s","notification_type":"elicitation_dialog","message":{"unexpected":"object"}}`), time.Now())
	if err != nil || e == nil || e.Kind != "input.required" || e.Body != "" {
		t.Fatal("错误内容类型不应丢掉等待事件", e, err)
	}
}

func TestAntigravityNativeFinalOutputQuestionsAndPermissionContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	for _, sample := range []struct{ hook, input, kind, content string }{
		{"Stop", `{"conversationId":"main","terminationReason":"model_stop","fullyIdle":true,"finalModelOutput":"最后回答","modelThinking":"不采集思考"}`, "turn.completed", "最后回答"},
		{"Stop", `{"conversationId":"main","terminationReason":"error","error":"模型不可用"}`, "run.failed", "模型不可用"},
		{"PreToolUse", `{"conversationId":"main","toolCall":{"name":"ask_question","args":{"questions":[{"question":"哪种饮品？","options":["咖啡","茶"]}]}}}`, "input.required", "哪种饮品？\n1. 咖啡\n2. 茶"},
		{"PreToolUse", `{"conversationId":"main","toolCall":{"name":"ask_permission","args":{"command":"echo hello","reason":"检查输出","unrelated":"不采集"}}}`, "approval.required", "请求批准：ask_permission\n原因：检查输出\n命令：echo hello"},
	} {
		e, err := Convert("antigravity", "host", sample.hook, []byte(sample.input), time.Now())
		if err != nil || e == nil || e.Kind != sample.kind || e.Body != sample.content {
			t.Fatal(e, err)
		}
	}
	e, err := Convert("antigravity", "host", "Stop", []byte(`{"conversationId":"s","terminationReason":"model_stop"}`), time.Now())
	if err != nil || e == nil || e.Body != "" {
		t.Fatal("缺少原生输出不能猜历史回答", e, err)
	}
}

func TestQoderFailureAndApprovalKeepOnlyNativeVisibleFields(t *testing.T) {
	e, err := Convert("qoderclicn", "host", "StopFailure", []byte(`{"session_id":"s","error":"调用失败","error_details":{"headers":"不采集"}}`), time.Now())
	if err != nil || e == nil || e.Body != "调用失败" {
		t.Fatal(e, err)
	}
	e, err = Convert("qoderclicn", "host", "PermissionRequest", []byte(`{"session_id":"s","tool_name":"Bash","tool_input":{"command":"echo hello","description":"确认连通","private_context":"不采集"}}`), time.Now())
	if err != nil || e == nil || !strings.Contains(e.Body, "echo hello") || strings.Contains(e.Body, "不采集") {
		t.Fatal(e, err)
	}
}

func TestBodyJSONEscapingStillFitsNetworkEvent(t *testing.T) {
	for _, text := range []string{strings.Repeat("<>&", 10000), strings.Repeat("\x00", 32000), strings.Repeat("\\\"", 16000)} {
		body := visibleBody(text)
		raw, _ := json.Marshal(body)
		if len(body) > 32768 || len(raw) > 56<<10 || !utf8.ValidString(body) {
			t.Fatal("正文JSON转义超过网络余量", len(body), len(raw))
		}
	}
}
