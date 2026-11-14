// Package event 实现独立 hook 适配程序的转换，Bridge 不链接此包。
package event

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"agent-pulse/protocol"
)

type Hook struct {
	SessionID      string   `json:"session_id"`
	Event          string   `json:"hook_event_name"`
	CWD            string   `json:"cwd"`
	AgentID        string   `json:"agent_id"`
	AgentType      string   `json:"agent_type"`
	AgentName      string   `json:"agentName"`
	ParentSession  string   `json:"parentSessionId"`
	TurnID         string   `json:"turn_id"`
	ToolID         string   `json:"tool_use_id"`
	Tool           string   `json:"tool_name"`
	Transcript     string   `json:"transcript_path"`
	Source         string   `json:"source"`
	Notification   string   `json:"notification_type"`
	ConversationID string   `json:"conversationId"`
	Workspaces     []string `json:"workspacePaths"`
	Termination    string   `json:"terminationReason"`
	FullyIdle      *bool    `json:"fullyIdle"`
	Execution      *int64   `json:"executionNum"`
	Step           *int64   `json:"stepIdx"`
	ToolCall       struct {
		Name string `json:"name"`
	} `json:"toolCall"`
	ParentID string `json:"parentConversationId"`
}

func Convert(agent, source, hook string, raw []byte, now time.Time) (*protocol.Event, error) {
	var h Hook
	if len(raw) > protocol.MaxHookBytes || json.Unmarshal(raw, &h) != nil {
		return nil, errors.New("hook JSON 无效")
	}
	if h.Event != "" && h.Event != hook {
		return nil, errors.New("hook 事件名不匹配")
	}
	if h.AgentID != "" || h.AgentType != "" || strings.HasPrefix(hook, "Subagent") {
		return nil, nil
	}
	// ZCode 3.17.2 默认主会话不带 agentName；子代理显式带 zcode-* 名称。
	// 工具钩子没有可靠归属字段，以下仅转换三个会话生命周期事件。
	if agent == "zcode" && (h.AgentName != "" || h.ParentSession != "" || (hook != "SessionStart" && hook != "UserPromptSubmit" && hook != "Stop")) {
		return nil, nil
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
			// Codex 工具 hook 可能沿用父会话 ID，公开契约没有稳定的主线程归属字段。
			if agent == "qoderclicn" {
				kind = "approval.required"
			}
		case "PreToolUse":
			if agent == "qoderclicn" && (h.Tool == "AskUserQuestion" || h.Tool == "ask_user_question") {
				kind = "input.required"
			}
		case "PostToolUse":
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
			}
		case "PostToolUse":
			kind = "activity.observed"
		case "Stop":
			switch h.Termination {
			case "model_stop":
				kind = "turn.completed"
			case "NO_TOOL_CALL":
				// agy 1.5.3 ACP 的真实终止值；仅完全空闲时确认完成。
				kind = "observation.lost"
				if h.FullyIdle != nil && *h.FullyIdle {
					kind = "turn.completed"
				}
			case "error", "max_steps_exceeded":
				kind = "run.failed"
			case "user_cancelled", "interrupted":
				kind = "run.interrupted"
			default:
				kind = "observation.lost"
			}
			if kind == "turn.completed" && h.FullyIdle != nil && !*h.FullyIdle {
				kind = "activity.observed"
			}
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
		e.SessionTitle = codexSessionTitle(h.SessionID)
	case "qoderclicn":
		e.SessionTitle = qoderSessionTitle(h.SessionID, h.CWD, h.Transcript)
	case "antigravity":
		name, internal := antigravitySessionMetadata(h.SessionID)
		if internal {
			return nil, nil
		}
		e.SessionTitle = name
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
