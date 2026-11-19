package event

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"agent-pulse/internal/sqlitepath"
	"agent-pulse/protocol"
)

type antigravityTerminal struct {
	SessionID string         `json:"session_id"`
	Root      string         `json:"root"`
	Turn      string         `json:"turn"`
	Event     protocol.Event `json:"event"`
}

type antigravityRunState struct {
	turn, status    string
	pending, killed bool
	parent          string
	depth           int64
}

func readAntigravityRunState(root, id string) (antigravityRunState, error) {
	var result antigravityRunState
	db, err := sql.Open("sqlite", sqlitepath.URI(filepath.Join(root, "conversation_summaries.db"))+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30)")
	if err != nil {
		return result, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	var at, status, parent sql.NullString
	var pending, killed sql.NullBool
	var index, depth sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT last_user_input_time,last_user_input_step_index,status,not_fully_idle,killed,parent_conversation_id,nesting_depth FROM conversation_summaries WHERE conversation_id=?`, id).Scan(&at, &index, &status, &pending, &killed, &parent, &depth)
	if err != nil {
		return result, err
	}
	if !at.Valid || !index.Valid || index.Int64 < 0 || !status.Valid || !pending.Valid || !killed.Valid || !parent.Valid || !depth.Valid {
		return result, errors.New("原生本轮状态字段不完整")
	}
	turn, _ := json.Marshal([]any{at.String, index.Int64})
	result = antigravityRunState{turn: string(turn), status: status.String, pending: pending.Bool, killed: killed.Bool, parent: parent.String, depth: depth.Int64}
	return result, nil
}

// 仅观察已确认主会话在正常Stop后仍有后台任务的那一轮，用户新提交会停止旧观察。
func AntigravityTerminalObservation(hook string, raw []byte, e *protocol.Event, metadata *AntigravityMetadata) (*protocol.Observation, string) {
	var h Hook
	if hook != "Stop" || e == nil || metadata == nil || metadata.Identity != "main" || filepath.Base(metadata.Root) != "antigravity" || json.Unmarshal(raw, &h) != nil {
		return nil, ""
	}
	state, err := readAntigravityRunState(metadata.Root, e.SessionID)
	if err != nil || state.parent != "" || state.depth != 0 {
		return nil, ""
	}
	sum := sha256.Sum256([]byte(state.turn))
	turn := hex.EncodeToString(sum[:])
	if e.Kind != "activity.observed" || (h.Termination != "NO_TOOL_CALL" && h.Termination != "model_stop") || h.FullyIdle == nil || *h.FullyIdle {
		return nil, turn
	}
	cursor := antigravityTerminal{SessionID: e.SessionID, Root: metadata.Root, Turn: state.turn, Event: *e}
	encoded, _ := json.Marshal(cursor)
	return &protocol.Observation{SessionID: e.SessionID, TurnID: turn, Cursor: encoded}, turn
}

func ObserveAntigravity(ctx context.Context, raw []byte, source string, emit func(protocol.LocalEvent) error) error {
	var cursor antigravityTerminal
	if len(raw) > 65536 || json.Unmarshal(raw, &cursor) != nil || cursor.SessionID == "" || cursor.Turn == "" || cursor.Event.SessionID != cursor.SessionID || cursor.Event.SourceID != source || cursor.Event.Agent != "antigravity" || cursor.Event.Validate() != nil || !filepath.IsAbs(cursor.Root) || filepath.Base(cursor.Root) != "antigravity" {
		return errors.New("本轮空闲观察游标无效")
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	unavailable := time.Time{}
	for {
		state, err := readAntigravityRunState(cursor.Root, cursor.SessionID)
		if err != nil {
			if unavailable.IsZero() {
				unavailable = time.Now()
			}
			if time.Since(unavailable) > 10*time.Second {
				return errors.New("本轮原生空闲状态持续不可读取")
			}
		} else {
			unavailable = time.Time{}
			if state.turn != cursor.Turn || state.parent != "" || state.depth != 0 {
				return nil
			}
			if state.status == "CASCADE_RUN_STATUS_IDLE" && !state.pending {
				e := cursor.Event
				e.EventID = protocol.NewID()
				e.OccurredAt = time.Now().UTC()
				e.Kind = "turn.completed"
				e.Title = "antigravity · 本轮完成"
				if state.killed {
					e.Kind = "run.interrupted"
					e.Title = "antigravity · 任务中断"
				}
				sum := sha256.Sum256([]byte(cursor.Turn))
				return emit(protocol.LocalEvent{Event: e, TurnID: hex.EncodeToString(sum[:])})
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
