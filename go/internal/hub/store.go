// Package hub 保存权威会话、事件、已读和推送任务。
package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/sqlitepath"
	"agent-pulse/protocol"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("event_id 已存在且内容不同")
var ErrHostDeleted = errors.New("主机已删除，拒绝其待处理事件")

type connection struct {
	device protocol.Device
	ws     stateSocket
	wake   chan struct{}
}

type Hub struct {
	config      config.Hub
	mu          sync.Mutex
	db          *sql.DB
	runID       string
	version     uint64
	ttl         time.Duration
	channels    []config.Channel
	connections map[*connection]bool
	devices     map[string]*connection
	closed      bool
	pushAfter   time.Time
}

func Open(c config.Hub) (*Hub, error) {
	publisherKey, err := resolvePublisherKey(c.AdapterDirectory, c.AdapterPublicKey)
	if err != nil {
		return nil, err
	}
	c.AdapterPublicKey = publisherKey
	path, err := filepath.Abs(c.Database)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	db, err := sql.Open("sqlite", sqlitepath.URI(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	h := &Hub{config: c, db: db, runID: protocol.NewID(), ttl: time.Duration(c.ReminderTTLSeconds) * time.Second, channels: c.Push, connections: map[*connection]bool{}, devices: map[string]*connection{}, pushAfter: time.Now().Add(35 * time.Second)}
	if h.ttl <= 0 {
		h.ttl = 24 * time.Hour
	}
	if err = h.initializeDatabase(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return h, nil
}

func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.connections {
		_ = c.ws.Close()
	}
	return h.db.Close()
}

// 开发期只创建和读取当前结构，不识别、迁移或转换旧数据库。
// 已有结构不匹配时事务回滚并要求显式重建，不自动清空用户数据。
func (h *Hub) initializeDatabase() error {
	if _, err := h.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;`); err != nil {
		return err
	}
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS events (
 position INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
 source_id TEXT NOT NULL, agent TEXT NOT NULL, session_id TEXT NOT NULL,
 payload BLOB NOT NULL, notify INTEGER NOT NULL, valid INTEGER NOT NULL, received_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_session ON events(source_id,agent,session_id);
CREATE TABLE IF NOT EXISTS sessions (
 source_id TEXT NOT NULL, agent TEXT NOT NULL, session_id TEXT NOT NULL, state TEXT NOT NULL,
 title TEXT NOT NULL, project TEXT NOT NULL, last_event_id TEXT NOT NULL, last_sequence INTEGER NOT NULL,
 updated_at TEXT NOT NULL, name_sequence INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(source_id,agent,session_id)
);
CREATE TABLE IF NOT EXISTS reads (event_id TEXT PRIMARY KEY, read_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS push_jobs (
 event_id TEXT NOT NULL, channel TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0, next_at TEXT NOT NULL,
 http_status INTEGER NOT NULL DEFAULT 0, business_status INTEGER, failure_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(event_id,channel)
);
CREATE TABLE IF NOT EXISTS adapter_hosts (source_id TEXT PRIMARY KEY, report BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS session_management (
 source_id TEXT NOT NULL, agent TEXT NOT NULL, session_id TEXT NOT NULL,
 alias TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0,
 override_state TEXT NOT NULL DEFAULT '', override_at TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(source_id,agent,session_id)
);
CREATE TABLE IF NOT EXISTS hosts (
 source_id TEXT PRIMARY KEY, default_name TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
 environment TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0, last_seen TEXT NOT NULL DEFAULT '',
 role TEXT NOT NULL DEFAULT 'bridge'
);
CREATE TABLE IF NOT EXISTS deleted_hosts (source_id TEXT PRIMARY KEY, deleted_at TEXT NOT NULL);
`); err != nil {
		return err
	}
	for _, query := range []string{
		`SELECT name_sequence FROM sessions LIMIT 0`,
		`SELECT role FROM hosts LIMIT 0`,
		`SELECT http_status,business_status,failure_reason FROM push_jobs LIMIT 0`,
	} {
		rows, err := tx.Query(query)
		if err != nil {
			return errors.New("数据库不符合当前开发结构，请先备份并显式重建数据库")
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	for id, label := range h.config.Sources {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO hosts(source_id,default_name,environment) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM deleted_hosts WHERE source_id=?)`, id, label.Name, label.Environment, id); err != nil {
			return err
		}
	}
	// 崩溃前的发送可能已经到达手机；当前格式重启后仍不能盲目补发。
	if _, err = tx.Exec(`UPDATE push_jobs SET status='unknown' WHERE status='sending'`); err != nil {
		return err
	}
	return tx.Commit()
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func (h *Hub) changedLocked() {
	h.version++
	for c := range h.connections {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) versionLocked() protocol.Version {
	return protocol.Version{RunID: h.runID, Version: h.version}
}

func (h *Hub) Accept(ctx context.Context, e protocol.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	e.OccurredAt = e.OccurredAt.UTC()
	payload, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var deleted int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM deleted_hosts WHERE source_id=?`, e.SourceID).Scan(&deleted); err == nil {
		return false, ErrHostDeleted
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hosts(source_id,default_name) VALUES(?,?)`, e.SourceID, e.SourceID); err != nil {
		return false, err
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM events WHERE event_id=?`, e.EventID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, payload) {
			return false, ErrConflict
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var seq, nameSequence int64
	err = tx.QueryRowContext(ctx, `SELECT last_sequence,name_sequence FROM sessions WHERE source_id=? AND agent=? AND session_id=?`, e.SourceID, e.Agent, e.SessionID).Scan(&seq, &nameSequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	apply := e.State() != "" && !e.Historical && (e.Sequence == 0 || seq == 0 || e.Sequence > seq)
	// 某些 CLI 退出时还会关闭未投入使用的内部会话；只存历史，不凭空新增已关闭会话。
	if errors.Is(err, sql.ErrNoRows) && e.Kind == "session.closed" {
		apply = false
	}
	now := timestamp(time.Now())
	title := strings.TrimSpace(e.SessionTitle)
	// 改名只更新已存在会话的名称，独立排序，不产生运行状态、已读或通知变化。
	if e.Kind == "session.renamed" && !e.Historical && err == nil && (e.Sequence == 0 && nameSequence == 0 || e.Sequence > nameSequence) {
		if e.Sequence > nameSequence {
			nameSequence = e.Sequence
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET title=?,name_sequence=? WHERE source_id=? AND agent=? AND session_id=?`, title, nameSequence, e.SourceID, e.Agent, e.SessionID); err != nil {
			return false, err
		}
	}

	if apply {
		// 无序号的实时事件按到达顺序处理，但不能抹掉已知原生序号。
		if e.Sequence > seq {
			seq = e.Sequence
		}
		// 关闭会话解除等待，但不会撤销刚产生的完成、失败或中断通知。
		if _, err = tx.ExecContext(ctx, `UPDATE events SET valid=0 WHERE source_id=? AND agent=? AND session_id=? AND valid=1 AND (? <> 'session.closed' OR json_extract(payload,'$.kind') IN ('input.required','approval.required'))`, e.SourceID, e.Agent, e.SessionID, e.Kind); err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sessions(source_id,agent,session_id,state,title,project,last_event_id,last_sequence,updated_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(source_id,agent,session_id) DO UPDATE SET state=excluded.state,project=excluded.project,last_event_id=excluded.last_event_id,last_sequence=excluded.last_sequence,updated_at=excluded.updated_at`, e.SourceID, e.Agent, e.SessionID, e.State(), title, e.Project, e.EventID, seq, now)
		if err != nil {
			return false, err
		}

		// 无改名事件的来源在下一次真实事件携带最新名称；旧原生序号不回滚名称。
		if title != "" && (e.Sequence == 0 && nameSequence == 0 || e.Sequence > nameSequence) {
			if e.Sequence > nameSequence {
				nameSequence = e.Sequence
			}
			if _, err = tx.ExecContext(ctx, `UPDATE sessions SET title=?,name_sequence=? WHERE source_id=? AND agent=? AND session_id=?`, title, nameSequence, e.SourceID, e.Agent, e.SessionID); err != nil {
				return false, err
			}
		}

		if _, err = tx.ExecContext(ctx, `UPDATE session_management SET override_state='',override_at='',revision=revision+1 WHERE source_id=? AND agent=? AND session_id=? AND override_state<>''`, e.SourceID, e.Agent, e.SessionID); err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(event_id,source_id,agent,session_id,payload,notify,valid,received_at) VALUES(?,?,?,?,?,?,?,?)`, e.EventID, e.SourceID, e.Agent, e.SessionID, payload, e.Notifies(), apply && e.Notifies(), now)
	if err != nil {
		return false, err
	}
	if apply && e.Notifies() {
		for _, ch := range h.channels {
			_, err = tx.ExecContext(ctx, `INSERT INTO push_jobs(event_id,channel,next_at) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM reads WHERE event_id=?)`, e.EventID, ch.Name, now, e.EventID)
			if err != nil {
				return false, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	h.changedLocked()
	return false, nil
}

func (h *Hub) MarkRead(ctx context.Context, ids []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := false
	for _, id := range ids {
		r, err := tx.ExecContext(ctx, `INSERT INTO reads(event_id,read_at) VALUES(?,?) ON CONFLICT DO NOTHING`, id, timestamp(time.Now()))
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		changed = changed || n > 0
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if changed {
		h.changedLocked()
	}
	return nil
}

func (h *Hub) Snapshot(ctx context.Context) (protocol.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := protocol.Snapshot{Version: h.versionLocked(), Sessions: []protocol.Session{}, Unread: []protocol.EventView{}, Devices: []protocol.Device{}}
	s.Sources = map[string]protocol.SourceLabel{}
	for id, label := range h.config.Sources {
		s.Sources[id] = protocol.SourceLabel{Name: label.Name, Environment: label.Environment}
	}
	hostRows, err := h.db.QueryContext(ctx, `SELECT source_id,default_name,display_name,environment,archived FROM hosts`)
	if err != nil {
		return s, err
	}
	for hostRows.Next() {
		var id, defaultName, displayName, environment string
		var archived bool
		if err = hostRows.Scan(&id, &defaultName, &displayName, &environment, &archived); err != nil {
			break
		}
		name := displayName
		if name == "" {
			name = defaultName
		}
		if name == "" {
			name = id
		}
		s.Sources[id] = protocol.SourceLabel{Name: name, Environment: environment, Archived: archived}
	}
	hostRowErr := hostRows.Err()
	_ = hostRows.Close()
	if err != nil {
		return s, err
	}
	if hostRowErr != nil {
		return s, hostRowErr
	}
	// 静态来源名称只作为回退，不能让已经删除的主机再次出现在客户端。
	deletedRows, err := h.db.QueryContext(ctx, `SELECT source_id FROM deleted_hosts`)
	if err != nil {
		return s, err
	}
	for deletedRows.Next() {
		var id string
		if err = deletedRows.Scan(&id); err != nil {
			break
		}
		delete(s.Sources, id)
	}
	deletedRowsErr := deletedRows.Err()
	_ = deletedRows.Close()
	if err != nil {
		return s, err
	}
	if deletedRowsErr != nil {
		return s, deletedRowsErr
	}
	rows, err := h.db.QueryContext(ctx, `SELECT source_id,agent,session_id,state,title,project,last_event_id,last_sequence,updated_at,name_sequence,COALESCE((SELECT json_extract(payload,'$.kind') FROM events WHERE event_id=sessions.last_event_id),'') FROM sessions ORDER BY source_id,agent,session_id`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var v protocol.Session
		var at string
		if err = rows.Scan(&v.SourceID, &v.Agent, &v.SessionID, &v.State, &v.Title, &v.Project, &v.LastEventID, &v.LastSequence, &at, &v.NameSequence, &v.LastKind); err != nil {
			break
		}
		v.UpdatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			break
		}
		s.Sessions = append(s.Sessions, v)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return s, err
	}
	if rowErr != nil {
		return s, rowErr
	}
	for i := range s.Sessions {
		v := &s.Sessions[i]
		err := h.db.QueryRowContext(ctx, `SELECT alias,archived,override_state,override_at,revision FROM session_management WHERE source_id=? AND agent=? AND session_id=?`, v.SourceID, v.Agent, v.SessionID).Scan(&v.Alias, &v.Archived, &v.OverrideState, &v.OverrideAt, &v.ManagementVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return s, err
		}
	}
	s.Unread, err = h.eventsLocked(ctx, `WHERE e.notify=1 AND r.event_id IS NULL ORDER BY e.position`, nil, false)
	if err != nil {
		return s, err
	}
	for _, c := range h.devices {
		if time.Since(c.device.LastSeen) <= 35*time.Second {
			device := c.device
			if device.Role == "bridge" || device.Role == "tray" {
				if label, exists := s.Sources[device.ID]; exists {
					device.HostName = label.Name
					device.Environment = label.Environment
				}
			}
			s.Devices = append(s.Devices, device)
		}
	}
	return s, nil
}

func (h *Hub) History(ctx context.Context, before int64, limit int) ([]protocol.EventView, error) {
	return h.HistoryWithArchived(ctx, before, limit, false)
}

func (h *Hub) HistoryWithArchived(ctx context.Context, before int64, limit int, includeArchived bool) ([]protocol.EventView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eventsLocked(ctx, `WHERE (?=0 OR e.position<?) ORDER BY e.position DESC LIMIT ?`, []any{before, before, limit}, includeArchived)
}

func (h *Hub) eventsLocked(ctx context.Context, tail string, args []any, includeArchived bool) ([]protocol.EventView, error) {
	if !includeArchived {
		tail = strings.Replace(tail, "WHERE ", "WHERE NOT EXISTS (SELECT 1 FROM hosts archived_host WHERE archived_host.source_id=e.source_id AND archived_host.archived=1) AND ", 1)
	}
	rows, err := h.db.QueryContext(ctx, `SELECT e.position,e.payload,e.valid,e.received_at,r.event_id IS NOT NULL FROM events e LEFT JOIN reads r ON e.event_id=r.event_id `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.EventView{}
	size := 0
	for rows.Next() {
		var v protocol.EventView
		var payload []byte
		var at string
		if err = rows.Scan(&v.Position, &payload, &v.Valid, &at, &v.Read); err != nil {
			return nil, err
		}
		size += len(payload)
		if size > 8<<20 {
			return nil, fmt.Errorf("快照超过 8 MiB；未截断，请减少查询范围或扩展快照协议")
		}
		if err = json.Unmarshal(payload, &v.Event); err != nil {
			return nil, err
		}
		v.ReceivedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		v.Valid = v.Valid && !v.Read && time.Since(v.OccurredAt) <= h.ttl
		result = append(result, v)
	}
	return result, rows.Err()
}
