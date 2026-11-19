package hub

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

var ErrSessionChanged = errors.New("会话已变化，请刷新后重试")

func (h *Hub) SessionHistory(ctx context.Context, key protocol.SessionKey, before int64, limit int) ([]protocol.EventView, error) {
	return h.SessionHistoryWithArchived(ctx, key, before, limit, false)
}

func (h *Hub) SessionHistoryWithArchived(ctx context.Context, key protocol.SessionKey, before int64, limit int, includeArchived bool) ([]protocol.EventView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eventsLocked(ctx, `WHERE e.source_id=? AND e.agent=? AND e.session_id=? AND (?=0 OR e.position<?) ORDER BY e.position DESC LIMIT ?`, []any{key.SourceID, key.Agent, key.SessionID, before, before, limit}, includeArchived)
}

// 新事件按持久化位置升序分批读取，避免断线期间超过一页时漏掉中间记录。
func (h *Hub) SessionEventsAfter(ctx context.Context, key protocol.SessionKey, after int64, limit int) ([]protocol.EventView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eventsLocked(ctx, `WHERE e.source_id=? AND e.agent=? AND e.session_id=? AND e.position>? ORDER BY e.position ASC LIMIT ?`, []any{key.SourceID, key.Agent, key.SessionID, after, limit}, false)
}

// TimelineQuery 仅描述会话详情的筛选与位置，不改变完整事件存储。
type TimelineQuery struct {
	Before    int64
	After     *int64
	Anchor    string
	Important bool
	Limit     int
}

type TimelinePage struct {
	Events []protocol.EventView `json:"events"`
	More   bool                 `json:"more"`
	Newer  bool                 `json:"newer"`
	Cursor int64                `json:"cursor"`
	RunID  string               `json:"run_id"`
}

// 只隐藏无正文且仅表达运行的记录；工具错误及其他有意义的标题仍可见。
const importantEvents = ` AND NOT (trim(COALESCE(json_extract(e.payload,'$.body'),''),' ' || char(9) || char(10) || char(13))='' AND (
 json_extract(e.payload,'$.kind') IN ('session.started','session.resumed') OR
 (json_extract(e.payload,'$.kind')='activity.observed' AND json_extract(e.payload,'$.title')=json_extract(e.payload,'$.agent') || ' · 正在运行')))`

func (h *Hub) SessionTimeline(ctx context.Context, key protocol.SessionKey, q TimelineQuery) (TimelinePage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	page := TimelinePage{Events: []protocol.EventView{}, RunID: h.runID}
	if q.Limit < 1 || q.Limit > 200 || q.Before < 0 || (q.After != nil && (*q.After < 0 || q.Before != 0 || q.Anchor != "")) || (q.Anchor != "" && q.Before != 0) {
		return page, errors.New("时间线分页参数无效")
	}
	base := `WHERE e.source_id=? AND e.agent=? AND e.session_id=?`
	args := []any{key.SourceID, key.Agent, key.SessionID}
	if err := h.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0) FROM events WHERE source_id=? AND agent=? AND session_id=?`, args...).Scan(&page.Cursor); err != nil {
		return page, err
	}
	if q.Important {
		base += importantEvents
	}
	tail := base
	values := append([]any{}, args...)
	if q.Anchor != "" {
		var position int64
		if err := h.db.QueryRowContext(ctx, `SELECT position FROM events WHERE source_id=? AND agent=? AND session_id=? AND event_id=?`, append(args, q.Anchor)...).Scan(&position); err != nil {
			return page, err
		}
		tail += ` AND e.position<=?`
		values = append(values, position)
	} else if q.Before != 0 {
		tail += ` AND e.position<?`
		values = append(values, q.Before)
	}
	if q.After != nil {
		tail += ` AND e.position>? AND e.position<=? ORDER BY e.position ASC LIMIT ?`
		values = append(values, *q.After, page.Cursor, q.Limit+1)
	} else {
		tail += ` ORDER BY e.position DESC LIMIT ?`
		values = append(values, q.Limit+1)
	}
	events, err := h.eventsLocked(ctx, tail, values, false)
	if err != nil {
		return page, err
	}
	page.More = len(events) > q.Limit
	if page.More {
		events = events[:q.Limit]
	}
	page.Events = events
	if q.After != nil {
		// 一页未扫完时只推进到最后已返回记录；全为隐藏活动时也推进扫描位置。
		if page.More {
			page.Cursor = events[len(events)-1].Position
		}
	} else if len(events) > 0 {
		newer, err := h.eventsLocked(ctx, base+` AND e.position>? ORDER BY e.position ASC LIMIT 1`, append(args, events[0].Position), false)
		if err != nil {
			return page, err
		}
		page.Newer = len(newer) > 0
	}
	return page, nil
}

func (h *Hub) ManageSession(ctx context.Context, req protocol.SessionChange) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var last string
	if err = tx.QueryRowContext(ctx, `SELECT last_event_id FROM sessions WHERE source_id=? AND agent=? AND session_id=?`, req.SourceID, req.Agent, req.SessionID).Scan(&last); err != nil {
		return err
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM session_management WHERE source_id=? AND agent=? AND session_id=?`, req.SourceID, req.Agent, req.SessionID).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last != req.LastEventID || revision != req.ManagementVersion {
		return ErrSessionChanged
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO session_management(source_id,agent,session_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, req.SourceID, req.Agent, req.SessionID); err != nil {
		return err
	}
	for _, field := range []struct {
		name    string
		value   any
		enabled bool
	}{
		{"archived", req.Archived, req.Archived != nil}, {"alias", req.Alias, req.Alias != nil}, {"override_state", req.OverrideState, req.OverrideState != nil},
	} {
		if field.enabled {
			if _, err = tx.ExecContext(ctx, `UPDATE session_management SET `+field.name+`=? WHERE source_id=? AND agent=? AND session_id=?`, field.value, req.SourceID, req.Agent, req.SessionID); err != nil {
				return err
			}
		}
	}
	if req.OverrideState != nil {
		at := ""
		if *req.OverrideState != "" {
			at = timestamp(time.Now())
		}
		if _, err = tx.ExecContext(ctx, `UPDATE session_management SET override_at=? WHERE source_id=? AND agent=? AND session_id=?`, at, req.SourceID, req.Agent, req.SessionID); err != nil {
			return err
		}
		if *req.OverrideState == "closed" || *req.OverrideState == "interrupted" {
			if _, err = tx.ExecContext(ctx, `UPDATE events SET valid=0 WHERE source_id=? AND agent=? AND session_id=? AND json_extract(payload,'$.kind') IN ('input.required','approval.required')`, req.SourceID, req.Agent, req.SessionID); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_management SET revision=revision+1 WHERE source_id=? AND agent=? AND session_id=?`, req.SourceID, req.Agent, req.SessionID); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		h.changedLocked()
	}
	return err
}

func (h *Hub) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/sessions/manage", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SessionChange
		if !transport.Decode(w, r, &req) {
			return
		}
		if req.SourceID == "" || req.Agent == "" || req.SessionID == "" || req.LastEventID == "" || req.ManagementVersion < 0 || (req.Archived == nil && req.Alias == nil && req.OverrideState == nil) {
			transport.Error(w, 400, "会话管理参数不完整")
			return
		}
		if req.Alias != nil && (len(*req.Alias) > 256 || strings.ContainsAny(*req.Alias, "\x00\r\n")) {
			transport.Error(w, 400, "别名无效")
			return
		}
		if req.OverrideState != nil {
			switch *req.OverrideState {
			case "", "idle", "closed", "interrupted", "unknown":
			default:
				transport.Error(w, 400, "不支持的人工状态")
				return
			}
		}
		err := h.ManageSession(r.Context(), req)
		switch {
		case errors.Is(err, ErrSessionChanged):
			transport.Error(w, 409, err.Error())
		case errors.Is(err, sql.ErrNoRows):
			transport.Error(w, 404, "会话不存在")
		case err != nil:
			transport.Error(w, 503, "管理操作未确认，请刷新后重试")
		default:
			transport.JSON(w, 200, map[string]string{"accepted": "durable"})
		}
	})
}
