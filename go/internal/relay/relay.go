// Package relay 只持久保存上行待发送数据，查询和实时状态均由 Hub 提供。
package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/sqlitepath"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

type Relay struct {
	mu      sync.Mutex
	db      *sql.DB
	config  config.Relay
	peer    *transport.Tunnel
	clients map[string]*socket
	closed  bool
}
type socket struct {
	ws   *websocket.Conn
	send chan []byte
}

func Open(c config.Relay) (*Relay, error) {
	path, err := filepath.Abs(c.Database)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
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
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS pending(position INTEGER PRIMARY KEY AUTOINCREMENT,business_key TEXT NOT NULL UNIQUE,path TEXT NOT NULL,payload BLOB NOT NULL,blocked INTEGER NOT NULL DEFAULT 0);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Relay{db: db, config: c, clients: map[string]*socket{}}, nil
}
func (r *Relay) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.peer != nil {
		r.peer.Close()
	}
	for _, c := range r.clients {
		_ = c.ws.Close()
	}
	return r.db.Close()
}
func (r *Relay) current() *transport.Tunnel { r.mu.Lock(); defer r.mu.Unlock(); return r.peer }

func (r *Relay) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		var n, blocked int
		err := r.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(blocked),0) FROM pending`).Scan(&n, &blocked)
		if err != nil {
			transport.Error(w, 503, "队列不可读")
			return
		}
		transport.JSON(w, 200, map[string]any{"status": "ok", "hub_connected": r.peer != nil, "pending": n, "blocked": blocked})
	})
	m.HandleFunc("GET /api/v1/relay", r.connectHub)
	m.HandleFunc("GET /api/v1/connect", r.connectClient)
	m.HandleFunc("POST /api/v1/adapters/status", r.ForwardStatus)
	m.HandleFunc("POST /api/v1/sessions/manage", func(w http.ResponseWriter, req *http.Request) {
		var change protocol.SessionChange
		if !transport.Decode(w, req, &change) {
			return
		}
		reply, err := r.forward(req.Context(), transport.Frame{Method: "POST", Path: "/api/v1/sessions/manage", Body: transport.Encode(change)})
		if err != nil {
			transport.Error(w, 503, "Hub 离线，会话管理未提交")
			return
		}
		writeReply(w, reply)
	})
	m.HandleFunc("POST /api/v1/events", func(w http.ResponseWriter, req *http.Request) {
		var e protocol.Event
		if !transport.Decode(w, req, &e) {
			return
		}
		if err := e.Validate(); err != nil {
			transport.Error(w, 400, err.Error())
			return
		}
		e.OccurredAt = e.OccurredAt.UTC()
		duplicate, status := r.enqueue(req.Context(), "event:"+e.EventID, "/api/v1/events", transport.Encode(e))
		if status != 202 {
			transport.Error(w, status, "事件未落盘，检查冲突或队列容量")
			return
		}
		transport.JSON(w, 202, protocol.Receipt{EventID: e.EventID, Accepted: "durable", Duplicate: duplicate})
	})
	m.HandleFunc("POST /api/v1/read", func(w http.ResponseWriter, req *http.Request) {
		var read protocol.ReadRequest
		if !transport.Decode(w, req, &read) {
			return
		}
		if len(read.EventIDs) < 1 || len(read.EventIDs) > 256 {
			transport.Error(w, 400, "已读 ID 数量无效")
			return
		}
		for _, id := range read.EventIDs {
			if id == "" || len(id) > 256 {
				transport.Error(w, 400, "已读 ID 无效")
				return
			}
		}
		sort.Strings(read.EventIDs)
		payload := transport.Encode(read)
		if reply, err := r.forward(req.Context(), transport.Frame{Method: "POST", Path: "/api/v1/read", Body: payload}); err == nil && reply.Status == 200 {
			writeReply(w, reply)
			return
		}
		hash := sha256.Sum256(payload)
		_, status := r.enqueue(req.Context(), "read:"+hex.EncodeToString(hash[:]), "/api/v1/read", payload)
		if status != 202 {
			transport.Error(w, status, "已读未落盘")
			return
		}
		transport.JSON(w, 202, map[string]string{"accepted": "relay", "message": "已暂存，客户端须保留到 Hub 200 确认"})
	})
	m.HandleFunc("GET /api/v1/", func(w http.ResponseWriter, req *http.Request) {
		if !Allowed("GET", req.URL.Path) {
			transport.Error(w, 404, "接口不存在")
			return
		}
		reply, err := r.forward(req.Context(), transport.Frame{Method: "GET", Path: req.URL.RequestURI(), Headers: map[string]string{"Range": req.Header.Get("Range")}})
		if err != nil {
			transport.Error(w, 503, "Hub 暂不可达，不能返回实时数据")
			return
		}
		writeReply(w, reply)
	})
	return transport.Guard(r.config.Token, false, m)
}

func Allowed(method, path string) bool {
	if method == "POST" {
		return path == "/api/v1/sessions/manage" || path == "/api/v1/events" || path == "/api/v1/read" || path == "/api/v1/adapters/status"
	}
	return method == "GET" && (path == "/api/v1/snapshot" || path == "/api/v1/events" || path == "/api/v1/push-jobs" || (path == "/api/v1/adapters/index" || path == "/api/v1/adapters/status") || strings.HasPrefix(path, "/api/v1/adapters/packages/"))
}

func (r *Relay) forward(ctx context.Context, frame transport.Frame) (transport.Frame, error) {
	p := r.current()
	if p == nil {
		return transport.Frame{}, errors.New("Hub 离线")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return p.Request(ctx, frame)
}
func writeReply(w http.ResponseWriter, f transport.Frame) {
	for k, v := range f.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(f.Status)
	_, _ = w.Write(f.Body)
}

func (r *Relay) enqueue(ctx context.Context, key, path string, payload []byte) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 503
	}
	defer tx.Rollback()
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM pending WHERE business_key=?`, key).Scan(&previous)
	if err == nil {
		if !bytes.Equal(previous, payload) {
			return false, 409
		}
		return true, 202
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, 503
	}
	var n int
	var size int64
	if tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM pending`).Scan(&n, &size) != nil {
		return false, 503
	}
	if n >= r.config.QueueEvents || size+int64(len(payload)) > r.config.QueueBytes {
		return false, 503
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pending(business_key,path,payload) VALUES(?,?,?)`, key, path, payload); err != nil {
		return false, 503
	}
	if tx.Commit() != nil {
		return false, 503
	}
	return false, 202
}

func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 64; i++ {
				if !r.flushOne(ctx) {
					break
				}
			}
		}
	}
}
func (r *Relay) flushOne(ctx context.Context) bool {
	if r.current() == nil {
		return false
	}
	var pos int64
	var path string
	var payload []byte
	r.mu.Lock()
	err := r.db.QueryRowContext(ctx, `SELECT position,path,payload FROM pending WHERE blocked=0 ORDER BY position LIMIT 1`).Scan(&pos, &path, &payload)
	r.mu.Unlock()
	if err != nil {
		return false
	}
	reply, err := r.forward(ctx, transport.Frame{Method: "POST", Path: path, Body: payload})
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if reply.Status == http.StatusGone && path == "/api/v1/events" {
		// Hub 已删除该主机；清理其全部旧事件，包括此前因其他错误而阻塞的记录。
		var event protocol.Event
		if json.Unmarshal(payload, &event) != nil || event.SourceID == "" {
			return false
		}
		_, err = r.db.ExecContext(ctx, `DELETE FROM pending WHERE path='/api/v1/events' AND json_extract(payload,'$.source_id')=?`, event.SourceID)
		return err == nil
	}
	if reply.Status == http.StatusOK || reply.Status == http.StatusGone {
		_, err = r.db.ExecContext(ctx, `DELETE FROM pending WHERE position=?`, pos)
		return err == nil
	}
	if reply.Status == 400 || reply.Status == 409 || reply.Status == 413 {
		_, err = r.db.ExecContext(ctx, `UPDATE pending SET blocked=1 WHERE position=?`, pos)
		return err == nil
	}
	return false
}

func (r *Relay) connectHub(w http.ResponseWriter, req *http.Request) {
	u := websocket.Upgrader{CheckOrigin: transport.OriginOK}
	ws, err := u.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	peer := transport.NewTunnel(ws)
	defer peer.Close()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.peer != nil {
		r.peer.Close()
	}
	for _, c := range r.clients {
		_ = c.ws.Close()
	}
	r.peer = peer
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.peer == peer {
			r.peer = nil
			for _, c := range r.clients {
				_ = c.ws.Close()
			}
		}
		r.mu.Unlock()
	}()
	peer.Read(req.Context(), func(f transport.Frame) {
		if f.Type != "stream_data" && f.Type != "stream_close" {
			return
		}
		r.mu.Lock()
		c := r.clients[f.ID]
		r.mu.Unlock()
		if c == nil {
			return
		}
		if f.Type == "stream_close" {
			_ = c.ws.Close()
			return
		}
		select {
		case c.send <- f.Body:
		default:
			_ = c.ws.Close()
		}
	})
}

func (r *Relay) connectClient(w http.ResponseWriter, req *http.Request) {
	peer := r.current()
	if peer == nil {
		transport.Error(w, 503, "Hub 暂不可达")
		return
	}
	u := websocket.Upgrader{CheckOrigin: transport.OriginOK}
	ws, err := u.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(4096)
	id := protocol.NewID()
	c := &socket{ws: ws, send: make(chan []byte, 8)}
	r.mu.Lock()
	if r.peer != peer || r.closed {
		r.mu.Unlock()
		return
	}
	r.clients[id] = c
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.clients, id)
		r.mu.Unlock()
		_ = peer.Send(transport.Frame{Type: "stream_close", ID: id})
	}()
	done := make(chan struct{})
	defer func() { _ = ws.Close(); <-done }()
	go func() {
		defer close(done)
		defer ws.Close()
		for {
			_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
			kind, data, err := ws.ReadMessage()
			if err != nil || kind != websocket.TextMessage {
				return
			}
			if peer.Send(transport.Frame{Type: "stream_input", ID: id, Body: data}) != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-peer.Done():
			return
		case <-done:
			return
		case data := <-c.send:
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if ws.WriteMessage(websocket.TextMessage, data) != nil {
				return
			}
		}
	}
}

// ForwardStatus 将状态上报透传，离线时不保存过期的主机状态。
func (r *Relay) ForwardStatus(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 65536))
	if err != nil {
		transport.Error(w, 413, "状态过大")
		return
	}
	if !json.Valid(body) {
		transport.Error(w, 400, "状态必须为 JSON")
		return
	}
	reply, err := r.forward(req.Context(), transport.Frame{Method: "POST", Path: "/api/v1/adapters/status", Body: body})
	if err != nil {
		transport.Error(w, 503, "Hub 离线")
		return
	}
	writeReply(w, reply)
}
