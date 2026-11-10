package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func (h *Hub) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/push-jobs", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		rows, err := h.db.QueryContext(r.Context(), `SELECT event_id,channel,status,attempts,next_at FROM push_jobs ORDER BY next_at DESC LIMIT 200`)
		if err != nil {
			transport.Error(w, 503, "推送记录读取失败")
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, channel, status, next string
			var attempts int
			if rows.Scan(&id, &channel, &status, &attempts, &next) != nil {
				transport.Error(w, 503, "推送记录读取失败")
				return
			}
			items = append(items, map[string]any{"event_id": id, "channel": channel, "status": status, "attempts": attempts, "next_at": next})
		}
		if rows.Err() != nil {
			transport.Error(w, 503, "推送记录读取失败")
			return
		}
		transport.JSON(w, 200, items)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		transport.JSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		var e protocol.Event
		if !transport.Decode(w, r, &e) {
			return
		}
		if err := e.Validate(); err != nil {
			transport.Error(w, 400, err.Error())
			return
		}
		duplicate, err := h.Accept(r.Context(), e)
		if errors.Is(err, ErrConflict) {
			transport.Error(w, 409, err.Error())
			return
		}
		if err != nil {
			transport.Error(w, 503, "事件未提交，请稍后重试")
			return
		}
		transport.JSON(w, 200, protocol.Receipt{EventID: e.EventID, Accepted: "durable", Duplicate: duplicate})
	})
	mux.HandleFunc("POST /api/v1/read", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ReadRequest
		if !transport.Decode(w, r, &req) {
			return
		}
		if len(req.EventIDs) == 0 || len(req.EventIDs) > 256 {
			transport.Error(w, 400, "event_ids 需要 1 至 256 个标识")
			return
		}
		for _, id := range req.EventIDs {
			if id == "" || len(id) > 256 {
				transport.Error(w, 400, "event_ids 含无效标识")
				return
			}
		}
		if err := h.MarkRead(r.Context(), req.EventIDs); err != nil {
			transport.Error(w, 503, "已读未保存，请重试")
			return
		}
		transport.JSON(w, 200, map[string]string{"accepted": "durable"})
	})
	mux.HandleFunc("GET /api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		s, err := h.Snapshot(r.Context())
		if err != nil {
			transport.Error(w, 503, "快照读取失败或超过容量限制")
			return
		}
		data, err := json.Marshal(s)
		if err != nil || len(data) > 8<<20 {
			transport.Error(w, 503, "快照超过 8 MiB，未截断")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		before := int64(0)
		limit := 50
		var err error
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || before < 0 {
				transport.Error(w, 400, "before 必须为非负整数")
				return
			}
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 200 {
				transport.Error(w, 400, "limit 范围为 1 至 200")
				return
			}
		}
		key := protocol.SessionKey{SourceID: r.URL.Query().Get("source_id"), Agent: r.URL.Query().Get("agent"), SessionID: r.URL.Query().Get("session_id")}
		var items []protocol.EventView
		if key != (protocol.SessionKey{}) {
			if key.SourceID == "" || key.Agent == "" || key.SessionID == "" {
				transport.Error(w, 400, "会话筛选必须包含来源、Agent 和会话标识")
				return
			}
			items, err = h.SessionHistory(r.Context(), key, before, limit)
		} else {
			items, err = h.History(r.Context(), before, limit)
		}
		if err != nil {
			transport.Error(w, 503, "历史读取失败")
			return
		}
		transport.JSON(w, 200, items)
	})
	mux.HandleFunc("GET /api/v1/connect", h.connect)
	h.sessionRoutes(mux)
	h.adapterRoutes(mux)
	return transport.Guard(token, false, mux)
}

func (h *Hub) connect(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: transport.OriginOK, HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"agent-pulse"}}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.serveSocket(ws)
}

type stateSocket interface {
	Close() error
	SetReadLimit(int64)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	ReadJSON(any) error
	WriteJSON(any) error
}

func (h *Hub) serveSocket(ws stateSocket) {
	defer ws.Close()
	ws.SetReadLimit(4096)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello struct {
		Type    string          `json:"type"`
		Payload protocol.Device `json:"payload"`
	}
	if ws.ReadJSON(&hello) != nil || hello.Type != "hello" || hello.Payload.ID == "" || len(hello.Payload.ID) > 256 {
		return
	}
	if hello.Payload.Role != "bridge" && hello.Payload.Role != "tray" && hello.Payload.Role != "client" && hello.Payload.Role != "web" {
		return
	}
	c := &connection{device: hello.Payload, ws: ws, wake: make(chan struct{}, 1)}
	c.device.LastSeen = time.Now().UTC()
	c.device.Path = "direct"
	if _, ok := ws.(*virtualSocket); ok {
		c.device.Path = "relay"
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	key := c.device.Role + ":" + c.device.ID
	if previous := h.devices[key]; previous != nil {
		_ = previous.ws.Close()
	}
	h.devices[key] = c
	h.connections[c] = true
	h.changedLocked()
	v := h.versionLocked()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.connections, c)
		if h.devices[key] == c {
			delete(h.devices, key)
			h.changedLocked()
		}
		h.mu.Unlock()
	}()
	write := func(msg protocol.Message) error {
		_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return ws.WriteJSON(msg)
	}
	if write(protocol.Message{Type: "hello", Payload: v}) != nil {
		return
	}
	finished := make(chan struct{})
	defer func() { _ = ws.Close(); <-finished }()
	go func() {
		defer close(finished)
		defer ws.Close()
		for {
			_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
			var msg struct {
				Type    string `json:"type"`
				Payload struct {
					Locked     *bool `json:"locked"`
					Fullscreen *bool `json:"fullscreen"`
				} `json:"payload"`
			}
			if ws.ReadJSON(&msg) != nil {
				return
			}
			if msg.Type != "heartbeat" && msg.Type != "state" {
				return
			}
			h.mu.Lock()
			c.device.LastSeen = time.Now().UTC()
			c.device.Path = "direct"
			if _, ok := ws.(*virtualSocket); ok {
				c.device.Path = "relay"
			}
			changed := false
			if msg.Payload.Locked != nil {
				changed = c.device.Locked == nil || *c.device.Locked != *msg.Payload.Locked
				c.device.Locked = msg.Payload.Locked
			}
			if msg.Payload.Fullscreen != nil {
				changed = changed || c.device.Fullscreen == nil || *c.device.Fullscreen != *msg.Payload.Fullscreen
				c.device.Fullscreen = msg.Payload.Fullscreen
			}
			if changed {
				h.changedLocked()
			}
			// 同版本回包同时作为连接存活确认，客户端可跳过无变化的快照。
			select {
			case c.wake <- struct{}{}:
			default:
			}
			h.mu.Unlock()
		}
	}()
	for {
		select {
		case <-finished:
			return
		case <-c.wake:
			h.mu.Lock()
			v := h.versionLocked()
			h.mu.Unlock()
			if write(protocol.Message{Type: "changed", Payload: v}) != nil {
				return
			}
		}
	}
}
