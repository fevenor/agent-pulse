// Package protocol 定义各语言实现共用的 JSON 协议。
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const MaxEventBytes = 64 << 10

// AdapterResult 仅在本机适配包与 dispatch 间使用；Reply 必须是宿主的被动回复。
type AdapterResult struct {
	Event *Event         `json:"event,omitempty"`
	Reply map[string]any `json:"reply"`
}

type Event struct {
	EventID    string    `json:"event_id"`
	SourceID   string    `json:"source_id"`
	Agent      string    `json:"agent"`
	SessionID  string    `json:"session_id"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Project    string    `json:"project,omitempty"`
	Scope      string    `json:"scope"`
	Sequence   int64     `json:"sequence,omitempty"`
	Historical bool      `json:"historical,omitempty"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (e Event) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"event_id", e.EventID}, {"source_id", e.SourceID}, {"agent", e.Agent}, {"session_id", e.SessionID},
	} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > 256 || strings.ContainsAny(field.value, "\x00\r\n") {
			return fmt.Errorf("%s 必须是 1 至 256 字节的标识", field.name)
		}
	}
	if e.Scope != "main" {
		return fmt.Errorf("scope 必须明确为 main；子代理和未知归属不接收")
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("occurred_at 必填")
	}
	if e.Sequence < 0 {
		return fmt.Errorf("sequence 不能为负数")
	}
	if strings.TrimSpace(e.Title) == "" || len(e.Title) > 1024 || len(e.Body) > 32<<10 || len(e.Project) > 1024 {
		return fmt.Errorf("title 必填且最长 1024 字节，body 最长 32768 字节，project 最长 1024 字节")
	}
	if _, ok := states[e.Kind]; !ok && e.Kind != "tool.failed" {
		return fmt.Errorf("不支持的 kind")
	}
	return nil
}

var states = map[string]string{
	"session.started": "running", "session.resumed": "running", "activity.observed": "running",
	"turn.completed": "idle", "task.completed": "idle", "input.required": "waiting", "approval.required": "waiting",
	"run.failed": "failed", "run.interrupted": "interrupted", "session.closed": "closed", "observation.lost": "unknown",
}

func (e Event) State() string { return states[e.Kind] }
func (e Event) Notifies() bool {
	switch e.Kind {
	case "turn.completed", "task.completed", "input.required", "approval.required", "run.failed", "run.interrupted":
		return true
	}
	return false
}

type Session struct {
	SourceID     string    `json:"source_id"`
	Agent        string    `json:"agent"`
	SessionID    string    `json:"session_id"`
	State        string    `json:"state"`
	Title        string    `json:"title"`
	Project      string    `json:"project,omitempty"`
	LastEventID  string    `json:"last_event_id"`
	LastSequence int64     `json:"last_sequence,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type EventView struct {
	Event
	Position   int64     `json:"position"`
	Read       bool      `json:"read"`
	Valid      bool      `json:"valid"`
	ReceivedAt time.Time `json:"received_at"`
}

type Version struct {
	RunID   string `json:"run_id"`
	Version uint64 `json:"version"`
}

type Device struct {
	Path       string    `json:"path"`
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	Locked     *bool     `json:"locked,omitempty"`
	Fullscreen *bool     `json:"fullscreen,omitempty"`
	LastSeen   time.Time `json:"last_seen"`
}

type Snapshot struct {
	Version
	Sessions []Session   `json:"sessions"`
	Unread   []EventView `json:"unread"`
	Devices  []Device    `json:"devices"`
}

type ReadRequest struct {
	EventIDs []string `json:"event_ids"`
}
type Receipt struct {
	EventID   string `json:"event_id"`
	Accepted  string `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
}

type Error struct {
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

type Message struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}
