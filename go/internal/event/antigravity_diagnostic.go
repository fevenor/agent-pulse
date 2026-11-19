package event

import (
	"encoding/json"
	"time"
	"unicode/utf8"

	"agent-pulse/protocol"
)

// 诊断仅包含身份与查询结果白名单，不含标题、提示词、正文、工具参数或原始错误文本。
type AntigravityDiagnostic struct {
	At             time.Time            `json:"at"`
	Hook           string               `json:"hook"`
	NativeIdentity map[string]string    `json:"native_identity"`
	Metadata       *AntigravityMetadata `json:"metadata,omitempty"`
	Termination    string               `json:"termination_reason,omitempty"`
	FullyIdle      *bool                `json:"fully_idle,omitempty"`
	Decision       string               `json:"decision"`
	Kind           string               `json:"kind,omitempty"`
}

func ConvertAntigravityDiagnostic(source, hook string, raw []byte, now time.Time) (*protocol.Event, AntigravityDiagnostic, error) {
	d := AntigravityDiagnostic{At: now.UTC(), Hook: hook, NativeIdentity: map[string]string{}}
	var values map[string]json.RawMessage
	if len(raw) <= protocol.MaxHookBytes && json.Unmarshal(raw, &values) == nil {
		for _, key := range []string{"conversationId", "parentConversationId", "parent_conversation_id", "parentSessionId", "rootConversationId", "root_conversation_id", "agentName", "agent_id", "agent_type"} {
			var value string
			if json.Unmarshal(values[key], &value) == nil && len(value) <= 256 && utf8.ValidString(value) {
				d.NativeIdentity[key] = value
			}
		}
		var reason string
		if json.Unmarshal(values["terminationReason"], &reason) == nil && len(reason) <= 128 {
			d.Termination = reason
		}
		_ = json.Unmarshal(values["fullyIdle"], &d.FullyIdle)
	}
	e, err := convert("antigravity", source, hook, raw, now, &d)
	d.Decision = "filtered_or_ignored"
	if e != nil {
		d.Decision = "emitted"
		d.Kind = e.Kind
	}
	if err != nil {
		d.Decision = "conversion_error"
	}
	return e, d, err
}
