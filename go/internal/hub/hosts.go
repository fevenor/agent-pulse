package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

const hostNameLimit = 256

type hostChange struct {
	DisplayName *string `json:"display_name"`
	Archived    *bool   `json:"archived"`
}

func validHostID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= hostNameLimit && !strings.ContainsAny(id, "\x00\r\n")
}

func validHostName(name string) bool {
	return len(name) <= hostNameLimit && !strings.ContainsAny(name, "\x00\r\n")
}

// registerPresenceLocked 同时登记 Bridge 与 Tray；只有 Bridge 重新连接会解除归档。
func (h *Hub) registerPresenceLocked(ctx context.Context, device protocol.Device) error {
	if device.Role != "bridge" && device.Role != "tray" {
		return nil
	}
	if !validHostID(device.ID) || !validHostName(device.HostName) || !validHostName(device.Environment) {
		return fmt.Errorf("设备主机信息无效")
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deleted int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM deleted_hosts WHERE source_id=?`, device.ID).Scan(&deleted); err == nil {
		return ErrHostDeleted
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// 同指纹的 Tray 不能覆盖 Bridge 资料；Hub 自定义名称不受设备上报名影响。
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosts(source_id,default_name,environment,archived,last_seen,role) VALUES(?,?,?,0,?,?)
ON CONFLICT(source_id) DO UPDATE SET
 default_name=CASE WHEN excluded.default_name<>'' THEN excluded.default_name ELSE hosts.default_name END,
 environment=CASE WHEN excluded.environment<>'' THEN excluded.environment ELSE hosts.environment END,
 archived=CASE WHEN excluded.role='bridge' THEN 0 ELSE hosts.archived END,
 last_seen=excluded.last_seen,role=excluded.role
WHERE excluded.role='bridge' OR hosts.role='tray'`, device.ID, strings.TrimSpace(device.HostName), device.Environment, timestamp(time.Now()), device.Role); err != nil {
		return err
	}
	return tx.Commit()
}

func (h *Hub) listHostsLocked(ctx context.Context) ([]protocol.HostInfo, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT source_id,default_name,display_name,environment,archived,last_seen,role FROM hosts ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.HostInfo{}
	for rows.Next() {
		var v protocol.HostInfo
		var defaultName, displayName, at string
		if err = rows.Scan(&v.SourceID, &defaultName, &displayName, &v.Environment, &v.Archived, &at, &v.Role); err != nil {
			return nil, err
		}
		v.DefaultName = defaultName
		v.Name = displayName
		if v.Name == "" {
			v.Name = defaultName
		}
		if v.Name == "" {
			v.Name = v.SourceID
		}
		if at != "" {
			v.LastSeen, err = time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return nil, err
			}
		}
		c := h.devices[v.Role+":"+v.SourceID]
		v.Online = c != nil && time.Since(c.device.LastSeen) <= 35*time.Second
		result = append(result, v)
	}
	return result, rows.Err()
}

func (h *Hub) patchHost(ctx context.Context, id string, change hostChange) error {
	if !validHostID(id) || (change.DisplayName == nil && change.Archived == nil) {
		return fmt.Errorf("主机管理参数不完整")
	}
	if change.DisplayName != nil && !validHostName(*change.DisplayName) {
		return fmt.Errorf("显示名最长 256 字节且不能含换行")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deleted int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM deleted_hosts WHERE source_id=?`, id).Scan(&deleted); err == nil {
		return ErrHostDeleted
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM hosts WHERE source_id=?`, id).Scan(&exists); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hosts(source_id,default_name) VALUES(?,?)`, id, id); err != nil {
			return err
		}
	}
	if change.DisplayName != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE hosts SET display_name=? WHERE source_id=?`, strings.TrimSpace(*change.DisplayName), id); err != nil {
			return err
		}
	}
	if change.Archived != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE hosts SET archived=? WHERE source_id=?`, *change.Archived, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	h.changedLocked()
	return nil
}

func (h *Hub) deleteHost(ctx context.Context, id string) error {
	if !validHostID(id) {
		return fmt.Errorf("主机指纹无效")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO deleted_hosts(source_id,deleted_at) VALUES(?,?) ON CONFLICT(source_id) DO UPDATE SET deleted_at=excluded.deleted_at`, id, timestamp(time.Now())); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM push_jobs WHERE event_id IN (SELECT event_id FROM events WHERE source_id=?)`,
		`DELETE FROM reads WHERE event_id IN (SELECT event_id FROM events WHERE source_id=?)`,
		`DELETE FROM events WHERE source_id=?`, `DELETE FROM sessions WHERE source_id=?`,
		`DELETE FROM session_management WHERE source_id=?`, `DELETE FROM adapter_hosts WHERE source_id=?`, `DELETE FROM hosts WHERE source_id=?`,
	} {
		if _, err = tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for key, c := range h.devices {
		if (c.device.Role == "bridge" || c.device.Role == "tray") && c.device.ID == id {
			_ = c.ws.Close()
			delete(h.devices, key)
			delete(h.connections, c)
		}
	}
	h.changedLocked()
	return nil
}

func (h *Hub) hostRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		items, err := h.listHostsLocked(r.Context())
		if err != nil {
			transport.Error(w, 503, "主机列表读取失败")
			return
		}
		transport.JSON(w, 200, items)
	})
	mux.HandleFunc("PATCH /api/v1/hosts/{source_id}", func(w http.ResponseWriter, r *http.Request) {
		var change hostChange
		if !transport.Decode(w, r, &change) {
			return
		}
		err := h.patchHost(r.Context(), r.PathValue("source_id"), change)
		if errors.Is(err, ErrHostDeleted) {
			transport.Error(w, 410, err.Error())
			return
		}
		if err != nil {
			transport.Error(w, 400, err.Error())
			return
		}
		transport.JSON(w, 200, map[string]string{"accepted": "durable"})
	})
	mux.HandleFunc("DELETE /api/v1/hosts/{source_id}", func(w http.ResponseWriter, r *http.Request) {
		err := h.deleteHost(r.Context(), r.PathValue("source_id"))
		if err != nil {
			transport.Error(w, 400, err.Error())
			return
		}
		transport.JSON(w, 200, map[string]string{"accepted": "deleted"})
	})
}
