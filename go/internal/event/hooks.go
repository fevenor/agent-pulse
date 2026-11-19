// Package event 实现独立 hook 适配程序的转换，Bridge 不链接此包。
package event

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-pulse/protocol"
)

type Hook struct {
	SessionID        string          `json:"session_id"`
	Event            string          `json:"hook_event_name"`
	CWD              string          `json:"cwd"`
	AgentID          string          `json:"agent_id"`
	AgentType        string          `json:"agent_type"`
	AgentName        string          `json:"agentName"`
	ParentSession    string          `json:"parentSessionId"`
	TurnID           string          `json:"turn_id"`
	ToolID           string          `json:"tool_use_id"`
	Tool             string          `json:"tool_name"`
	Transcript       string          `json:"transcript_path"`
	Source           string          `json:"source"`
	Notification     string          `json:"notification_type"`
	ConversationID   string          `json:"conversationId"`
	Workspaces       []string        `json:"workspacePaths"`
	Termination      string          `json:"terminationReason"`
	FullyIdle        *bool           `json:"fullyIdle"`
	Execution        *int64          `json:"executionNum"`
	Step             *int64          `json:"stepIdx"`
	FinalModelOutput json.RawMessage `json:"finalModelOutput"`
	ErrorContent     json.RawMessage `json:"error"`
	ErrorType        string          `json:"error_type"`
	LastAssistant    json.RawMessage `json:"last_assistant_message"`
	ToolInput        json.RawMessage `json:"tool_input"`
	Message          json.RawMessage `json:"message"`
	Reason           string          `json:"reason"`
	Action           string          `json:"action"`
	RequestID        string          `json:"requestId"`
	ElicitationID    string          `json:"elicitation_id"`
	RequestedSchema  json.RawMessage `json:"requested_schema"`
	TranscriptCamel  string          `json:"transcriptPath"`
	ThreadSource     string          `json:"thread_source"`
	IsInterrupt      bool            `json:"is_interrupt"`
	ToolCall         struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"args"`
	} `json:"toolCall"`
	ParentID string `json:"parentConversationId"`
}

func Convert(agent, source, hook string, raw []byte, now time.Time) (*protocol.Event, error) {
	return convert(agent, source, hook, raw, now, nil)
}

func convert(agent, source, hook string, raw []byte, now time.Time, diagnostic *AntigravityDiagnostic) (*protocol.Event, error) {
	var h Hook
	if len(raw) > protocol.MaxHookBytes || json.Unmarshal(raw, &h) != nil {
		return nil, errors.New("hook JSON 无效")
	}
	if h.Event != "" && h.Event != hook {
		return nil, errors.New("hook 事件名不匹配")
	}
	if h.AgentID != "" || h.AgentType != "" || h.ParentSession != "" || h.ParentID != "" || h.ThreadSource == "memory_consolidation" || h.ThreadSource == "subagent" || strings.HasPrefix(hook, "Subagent") {
		return nil, nil
	}
	// ZCode主会话不带agentName；权限和工具错误仍结合本地父会话元数据过滤。
	if agent == "zcode" && (h.AgentName != "" || h.ParentSession != "") {
		return nil, nil
	}
	// Stop仍可要求宿主继续处理；有原生轮次时以最终task_complete为准，不提前报完成。
	if agent == "codex" && hook == "Stop" && h.TurnID != "" {
		if info, err := os.Stat(codexRollout(h.SessionID, h.Transcript)); err == nil && info.Mode().IsRegular() {
			return nil, nil
		}
	}
	kind := ""
	switch agent {
	case "codex", "qoderclicn", "zcode":
		if h.SessionID == "" {
			return nil, errors.New("缺少会话标识")
		}
		switch hook {
		case "SessionStart":
			kind = "session.started"
			if h.Source == "resume" {
				kind = "session.resumed"
			}
		case "UserPromptSubmit":
			kind = "session.resumed"
		case "SessionEnd":
			kind = "session.closed"
		case "Stop":
			kind = "turn.completed"
		case "Interrupt":
			if agent == "codex" {
				kind = "run.interrupted"
			}
		case "StopFailure":
			if agent == "qoderclicn" {
				kind = "run.failed"
			}
		case "PermissionRequest":
			// 真实请求才记等待批准，提问工具不重复报批准。
			if !isQuestionTool(h.Tool) {
				kind = "approval.required"
			}
		case "PreToolUse":
			if isQuestionTool(h.Tool) {
				kind = "input.required"
			}
		case "PostToolUse":
			if h.Tool != "" {
				kind = "activity.observed"
			}
		case "PostToolUseFailure":
			if agent != "codex" {
				kind = "tool.failed"
			}
		case "PermissionDenied":
			if agent == "qoderclicn" {
				kind = "activity.observed"
			}
		case "Elicitation":
			if agent == "qoderclicn" {
				kind = "input.required"
			}
		case "ElicitationResult":
			if agent == "qoderclicn" {
				kind = "activity.observed"
			}
		case "Notification":
			if agent == "qoderclicn" && h.Notification == "elicitation_dialog" {
				kind = "input.required"
			}
		}
	case "antigravity":
		if h.ConversationID == "" {
			return nil, errors.New("缺少会话标识")
		}
		if h.ParentID != "" || h.ParentSession != "" {
			return nil, nil
		}
		h.SessionID = h.ConversationID
		if len(h.Workspaces) > 0 {
			h.CWD = h.Workspaces[0]
		}
		switch hook {
		case "SessionStart":
			kind = "session.started"
		case "PreInvocation":
			kind = "activity.observed"
		case "PreToolUse":
			switch h.ToolCall.Name {
			case "ask_question":
				kind = "input.required"
			case "ask_permission":
				kind = "approval.required"
			case "run_command":
				// App在执行前hook明确发送脱离沙箱请求；不是所有命令都需要批准。
				var request struct{ BypassSandbox bool }
				if json.Unmarshal(h.ToolCall.Arguments, &request) == nil && request.BypassSandbox {
					kind = "approval.required"
				}
			}
		case "PostToolUse":
			kind = "activity.observed"
		case "Stop":
			kind = antigravityStopKind(h.Termination, h.FullyIdle)
		}
	default:
		return nil, errors.New("不支持的来源")
	}
	if kind == "" {
		return nil, nil
	}
	// 每次原生调用分配 ID，Bridge 的网络重试保留同一 ID、时间和载荷。
	id := protocol.NewID()
	e := &protocol.Event{EventID: id, SourceID: source, Agent: agent, SessionID: h.SessionID, Kind: kind, OccurredAt: now.UTC(), Scope: "main", Title: agent + " · " + title(kind), Body: "", Project: filepath.Base(h.CWD)}
	if e.Project == "." || h.CWD == "" {
		e.Project = ""
	}
	switch agent {
	case "zcode":
		name, internal := zcodeSessionMetadata(h.SessionID)
		if internal {
			return nil, nil
		}
		e.SessionTitle = name
	case "codex":
		if codexMemoryWorkspace(h.CWD) || codexInternalSession(h.SessionID, h.Transcript) {
			return nil, nil
		}
		e.SessionTitle = codexSessionTitle(h.SessionID)
	case "qoderclicn":
		e.SessionTitle = qoderSessionTitle(h.SessionID, h.CWD, h.Transcript)
	case "antigravity":
		metadata := inspectAntigravityMetadata(h.SessionID, h.TranscriptCamel)
		if diagnostic != nil {
			diagnostic.Metadata = &metadata
		}
		if metadata.Identity == "child" {
			return nil, nil
		}
		e.SessionTitle = metadata.Title
	}
	// 仅采集本事件的最终回答或提问字段，不读思考、用户提示词、历史正文或任意工具参数。
	{
		switch kind {
		case "turn.completed":
			var reply string
			rawReply := h.LastAssistant
			if agent == "antigravity" {
				rawReply = h.FinalModelOutput
			}
			if json.Unmarshal(rawReply, &reply) == nil {
				e.Body = visibleBody(reply)
			}
		case "run.failed", "tool.failed":
			e.Body = nativeFailureBody(h.ErrorContent, h.ErrorType)
		case "approval.required":
			input := h.ToolInput
			name := h.Tool
			if agent == "antigravity" {
				input = h.ToolCall.Arguments
				name = h.ToolCall.Name
			}
			e.Body = approvalBody(name, input)
			if h.Reason != "" {
				e.Body = visibleBody(e.Body + "\n原因：" + h.Reason)
			}
		case "input.required":
			if hook == "PreToolUse" {
				input := h.ToolInput
				if agent == "antigravity" {
					input = h.ToolCall.Arguments
				}
				e.Body = questionBody(input)
			} else if hook == "Elicitation" {
				e.Body = elicitationBody(h.Message, h.RequestedSchema)
			} else {
				var message string
				if json.Unmarshal(h.Message, &message) == nil {
					e.Body = visibleBody(message)
				}
			}
		}
	}
	if hook == "SessionEnd" && h.Reason != "" {
		e.Body = visibleBody("结束原因：" + h.Reason)
	}
	if hook == "PermissionDenied" {
		e.Title = agent + " · 授权已拒绝"
		e.Body = visibleBody("授权已拒绝：" + h.Tool + "\n" + h.Reason)
	}
	if hook == "ElicitationResult" {
		e.Title = agent + " · 输入请求已处理"
		e.Body = elicitationResultBody(h.Action)
	}
	if hook == "PostToolUseFailure" {
		// 工具返回会结束其等待；不把局部错误当作主任务最终失败。
		e.Kind = "activity.observed"
		e.Title = agent + " · 工具执行失败"
		if h.IsInterrupt {
			e.Kind = "observation.lost"
			e.Title = agent + " · 工具中断，主任务状态待确认"
		}
	}
	if hook == "PostToolUse" && agent == "antigravity" {
		var failure string
		if json.Unmarshal(h.ErrorContent, &failure) == nil && failure != "" {
			e.Kind = "activity.observed"
			e.Title = agent + " · 工具执行失败"
			e.Body = visibleBody(failure)
		}
	}
	if hook == "StopFailure" && len(h.LastAssistant) > 0 {
		var reply string
		if json.Unmarshal(h.LastAssistant, &reply) == nil && reply != "" {
			e.Body = visibleBody(e.Body + "\n\n失败前的可见输出：\n" + reply)
		}
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}
func title(kind string) string {
	switch kind {
	case "turn.completed":
		return "本轮完成"
	case "run.failed":
		return "任务失败"
	case "run.interrupted":
		return "任务中断"
	case "input.required":
		return "等待输入"
	case "approval.required":
		return "等待批准"
	case "session.closed":
		return "会话已关闭"
	case "observation.lost":
		return "运行状态待确认"
	default:
		return "正在运行"
	}
}

// App与CLI的停止名称不同；正常停止仍须等待后台任务空闲，错误和取消不被空闲标志吞掉。
func antigravityStopKind(reason string, fullyIdle *bool) string {
	switch reason {
	case "model_stop", "NO_TOOL_CALL":
		if fullyIdle != nil && !*fullyIdle {
			return "activity.observed"
		}
		if fullyIdle == nil && reason == "NO_TOOL_CALL" {
			return "observation.lost"
		}
		return "turn.completed"
	case "error", "max_steps_exceeded", "ERROR", "MAX_INVOCATIONS", "MAX_FORCED_INVOCATIONS", "MAX_TOKEN_BUDGET_EXCEEDED":
		return "run.failed"
	case "user_cancelled", "interrupted", "USER_CANCELED":
		return "run.interrupted"
	default:
		return "observation.lost"
	}
}
