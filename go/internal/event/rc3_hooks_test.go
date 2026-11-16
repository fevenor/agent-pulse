package event

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRC3NativeContentAndResolution(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	cases := []struct{ agent, hook, raw, kind, body string }{
		{"codex", "Stop", "{\"session_id\":\"s\",\"last_assistant_message\":\"原生回答\"}", "turn.completed", "原生回答"},
		{"codex", "PermissionRequest", "{\"session_id\":\"s\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"echo test\",\"description\":\"批准原因\"}}", "approval.required", "批准原因"},
		{"codex", "PreToolUse", "{\"session_id\":\"s\",\"tool_name\":\"request_user_input\",\"tool_input\":{\"questions\":[{\"question\":\"选哪个？\",\"options\":[{\"label\":\"A\",\"description\":\"方案A\"}]}]}}", "input.required", "方案A"},
		{"zcode", "PermissionRequest", "{\"session_id\":\"s\",\"tool_name\":\"Bash\",\"reason\":\"需要批准\",\"tool_input\":{\"command\":\"echo test\"}}", "approval.required", "需要批准"},
		{"zcode", "PostToolUseFailure", "{\"session_id\":\"s\",\"error\":\"工具失败\",\"error_details\":{\"secret\":\"禁止采集\"}}", "activity.observed", "工具失败"},
		{"qoderclicn", "PermissionDenied", "{\"session_id\":\"s\",\"tool_name\":\"Bash\",\"reason\":\"用户拒绝\"}", "activity.observed", "用户拒绝"},
		{"qoderclicn", "SessionEnd", "{\"session_id\":\"s\",\"reason\":\"prompt_input_exit\"}", "session.closed", "prompt_input_exit"},
		{"qoderclicn", "Elicitation", "{\"session_id\":\"s\",\"message\":\"请选择\",\"requested_schema\":{\"properties\":{\"choice\":{\"title\":\"选择\",\"enum\":[\"A\",\"B\"]}}}}", "input.required", "- A"},
		{"qoderclicn", "ElicitationResult", "{\"session_id\":\"s\",\"action\":\"cancel\",\"content\":{\"secret\":\"禁止采集\"}}", "activity.observed", "已取消"},
		{"antigravity", "PreToolUse", "{\"conversationId\":\"s\",\"toolCall\":{\"name\":\"ask_permission\",\"args\":{\"Action\":\"execute\",\"Target\":\"测试目标\",\"Reason\":\"请求原因\"}}}", "approval.required", "测试目标"},
		{"antigravity", "PreToolUse", "{\"conversationId\":\"s\",\"toolCall\":{\"name\":\"ask_question\",\"args\":{\"questions\":[{\"question\":\"多选？\",\"is_multi_select\":true,\"options\":[\"A\",\"B\"]}]}}}", "input.required", "可多选"},
		{"antigravity", "PostToolUse", "{\"conversationId\":\"s\",\"error\":\"工具错误\",\"toolCall\":{\"name\":\"run_command\"}}", "activity.observed", "工具错误"},
	}
	for _, tt := range cases {
		t.Run(tt.agent+tt.hook, func(t *testing.T) {
			e, err := Convert(tt.agent, "test-host", tt.hook, []byte(tt.raw), time.Now())
			if err != nil || e == nil || e.Kind != tt.kind || !strings.Contains(e.Body, tt.body) {
				t.Fatalf("事件/正文不符：%+v，%v", e, err)
			}
			if strings.Contains(e.Body, "禁止采集") {
				t.Fatal("用户答案或错误详情泄露")
			}
			if e.Kind == "tool.failed" && e.Notifies() {
				t.Fatal("工具失败被当作主任务提醒")
			}
		})
	}
}
func TestCodexFiltersInternalIdentityWithoutReadingRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec("CREATE TABLE threads(id TEXT,source TEXT,thread_source TEXT,rollout_path TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO threads VALUES(?,?,?,?),(?,?,?,?)", "main", "vscode", "user", "/main-rollout", "child", "{\"subagent\":\"memory_consolidation\"}", "memory_consolidation", "/child-rollout")
	if err != nil {
		t.Fatal(err)
	}
	if !codexInternalSession("child", "") || !codexInternalSession("main", "/child-rollout") || codexInternalSession("main", "/main-rollout") {
		t.Fatal("内部/主线程身份过滤不正确")
	}
}

func TestStructuredNativeErrorDoesNotUploadRawResponse(t *testing.T) {
	raw := []byte("{\"session_id\":\"s\",\"error\":{\"message\":\"可见错误\",\"response\":{\"token\":\"禁止上传\"}}}")
	e, err := Convert("qoderclicn", "host", "StopFailure", raw, time.Now())
	if err != nil || e == nil || e.Body != "可见错误" {
		t.Fatal(e, err)
	}
}
func TestInterruptedToolResolvesWaitingWithoutInventingTaskFailure(t *testing.T) {
	raw := []byte("{\"session_id\":\"s\",\"tool_name\":\"AskUserQuestion\",\"error\":\"工具取消\",\"is_interrupt\":true}")
	e, err := Convert("zcode", "host", "PostToolUseFailure", raw, time.Now())
	if err != nil || e == nil || e.Kind != "observation.lost" || e.Notifies() {
		t.Fatal(e, err)
	}
}

func TestAntigravityAppSandboxRequestAndNormalCommandAreDifferent(t *testing.T) {
	for _, tt := range []struct {
		raw     string
		waiting bool
	}{
		{"{\"conversationId\":\"main\",\"toolCall\":{\"name\":\"run_command\",\"args\":{\"BypassSandbox\":true,\"CommandLine\":\"printf test\",\"toolSummary\":\"权限原因\"}}}", true},
		{"{\"conversationId\":\"main\",\"toolCall\":{\"name\":\"run_command\",\"args\":{\"BypassSandbox\":false,\"CommandLine\":\"printf test\"}}}", false},
	} {
		e, err := Convert("antigravity", "host", "PreToolUse", []byte(tt.raw), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !tt.waiting {
			if e != nil {
				t.Fatal("普通命令误报批准", e)
			}
			continue
		}
		if e == nil || e.Kind != "approval.required" || !strings.Contains(e.Body, "printf test") || !strings.Contains(e.Body, "权限原因") {
			t.Fatal(e)
		}
	}
}
