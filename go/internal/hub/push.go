package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/notify"
	"agent-pulse/protocol"
)

func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.expireReminders(ctx)
			if time.Now().After(h.pushAfter) {
				h.pushOne(ctx)
			}
		}
	}
}

func (h *Hub) expireReminders(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	result, err := h.db.ExecContext(ctx, `UPDATE events SET valid=0 WHERE valid=1 AND julianday(json_extract(payload,'$.occurred_at'))<=julianday(?)`, timestamp(time.Now().Add(-h.ttl)))
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("提醒有效期更新失败")
		}
		return
	}
	if count, err := result.RowsAffected(); err == nil && count > 0 {
		h.changedLocked()
	}
}

func (h *Hub) pushOne(ctx context.Context) {
	e, ch, ok := h.claimPush(ctx)
	if !ok {
		return
	}
	status := notify.Send(ctx, ch, e)
	h.mu.Lock()
	defer h.mu.Unlock()
	// attempts 在发送前递增；结果写入失败保留 sending，重启转为 unknown。
	_, err := h.db.ExecContext(ctx, `UPDATE push_jobs SET status=CASE WHEN ?='retry' AND attempts<3 THEN 'pending' WHEN ?='retry' THEN 'failed' ELSE ? END,next_at=? WHERE event_id=? AND channel=?`, status, status, status, timestamp(time.Now().Add(10*time.Second)), e.EventID, ch.Name)
	if err != nil {
		slog.Error("推送结果未保存", "event_id", e.EventID)
	} else {
		slog.Info("推送处理完成", "event_id", e.EventID, "channel", ch.Name, "result", status)
	}
}

func (h *Hub) claimPush(ctx context.Context) (protocol.Event, config.Channel, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var e protocol.Event
	var ch config.Channel
	var payload []byte
	var channel string
	var valid, read bool
	err := h.db.QueryRowContext(ctx, `SELECT e.payload,p.channel,e.valid,r.event_id IS NOT NULL FROM push_jobs p JOIN events e ON e.event_id=p.event_id LEFT JOIN reads r ON r.event_id=p.event_id WHERE p.status='pending' AND p.next_at<=? ORDER BY e.position LIMIT 1`, timestamp(time.Now())).Scan(&payload, &channel, &valid, &read)
	if err != nil {
		if err != sql.ErrNoRows && ctx.Err() == nil {
			slog.Error("待推送任务读取失败")
		}
		return e, ch, false
	}
	if json.Unmarshal(payload, &e) != nil {
		return e, ch, false
	}
	for _, candidate := range h.channels {
		if candidate.Name == channel {
			ch = candidate
			break
		}
	}
	skipped := read || !valid || time.Since(e.OccurredAt) > h.ttl || ch.Name == ""
	for _, c := range h.devices {
		if c.device.Role == "tray" && time.Since(c.device.LastSeen) < 35*time.Second && c.device.Locked != nil && !*c.device.Locked {
			skipped = true
		}
	}
	status := "sending"
	if skipped {
		status = "skipped"
	}
	_, err = h.db.ExecContext(ctx, `UPDATE push_jobs SET status=?,attempts=attempts+1 WHERE event_id=? AND channel=?`, status, e.EventID, channel)
	return e, ch, err == nil && !skipped
}
