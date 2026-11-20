package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

type syncRecord struct {
	Delta protocol.SyncDelta
	Bytes int
	At    time.Time
}
type syncState struct {
	Current *protocol.Snapshot
	Records []syncRecord
	Bytes   int
}

const syncWindowBytes = 8 << 20
const syncWindowRecords = 512

func sessionKey(s protocol.Session) protocol.SessionKey {
	return protocol.SessionKey{SourceID: s.SourceID, Agent: s.Agent, SessionID: s.SessionID}
}
func deviceKey(d protocol.Device) string { return d.Role + ":" + d.ID }
func bodyHash(body string) string        { s := sha256.Sum256([]byte(body)); return hex.EncodeToString(s[:]) }
func omitBodies(events []protocol.EventView) {
	for i := range events {
		events[i].BodyHash = bodyHash(events[i].Body)
		events[i].Body = ""
	}
}

func syncDifference(old, next protocol.Snapshot) protocol.SyncDelta {
	d := protocol.SyncDelta{SyncCursor: protocol.SyncCursor{RunID: next.RunID, Version: next.Version.Version}, From: old.Version.Version}
	priorSessions := map[protocol.SessionKey]protocol.Session{}
	for _, s := range old.Sessions {
		priorSessions[sessionKey(s)] = s
	}
	for _, s := range next.Sessions {
		k := sessionKey(s)
		if p, ok := priorSessions[k]; !ok || p != s {
			d.Sessions = append(d.Sessions, s)
		}
		delete(priorSessions, k)
	}
	for k := range priorSessions {
		d.SessionsRemoved = append(d.SessionsRemoved, k)
	}
	priorEvents := map[string]protocol.EventView{}
	for _, e := range old.Unread {
		priorEvents[e.EventID] = e
	}
	for _, e := range next.Unread {
		if p, ok := priorEvents[e.EventID]; !ok || !reflect.DeepEqual(p, e) {
			d.Unread = append(d.Unread, e)
		}
		delete(priorEvents, e.EventID)
	}
	for k := range priorEvents {
		d.UnreadRemoved = append(d.UnreadRemoved, k)
	}
	d.Sources = map[string]protocol.SourceLabel{}
	for k, v := range next.Sources {
		if p, ok := old.Sources[k]; !ok || p != v {
			d.Sources[k] = v
		}
	}
	for k := range old.Sources {
		if _, ok := next.Sources[k]; !ok {
			d.SourcesRemoved = append(d.SourcesRemoved, k)
		}
	}
	priorDevices := map[string]protocol.Device{}
	for _, v := range old.Devices {
		priorDevices[deviceKey(v)] = v
	}
	for _, v := range next.Devices {
		k := deviceKey(v)
		if p, ok := priorDevices[k]; !ok || !reflect.DeepEqual(p, v) {
			d.Devices = append(d.Devices, v)
		}
		delete(priorDevices, k)
	}
	for k := range priorDevices {
		d.DevicesRemoved = append(d.DevicesRemoved, k)
	}
	sort.Strings(d.UnreadRemoved)
	sort.Strings(d.SourcesRemoved)
	sort.Strings(d.DevicesRemoved)
	return d
}
func deltaEmpty(d protocol.SyncDelta) bool {
	return len(d.Sessions)+len(d.SessionsRemoved)+len(d.Unread)+len(d.UnreadRemoved)+len(d.Sources)+len(d.SourcesRemoved)+len(d.Devices)+len(d.DevicesRemoved) == 0
}

// 每次可信确认重新检查来源租期和提醒有效性；周期时间戳不进入业务增量。
func (h *Hub) syncCurrentLocked(ctx context.Context) (protocol.Snapshot, error) {
	s, err := h.snapshotModeLocked(ctx, true)
	if err != nil {
		return s, err
	}
	for i := range s.Devices {
		s.Devices[i].LastSeen = time.Time{}
	}
	sort.Slice(s.Devices, func(i, j int) bool { return deviceKey(s.Devices[i]) < deviceKey(s.Devices[j]) })
	s.Version.Version = 1
	if h.syncState.Current == nil {
		h.syncState.Current = &s
		return s, nil
	}
	old := *h.syncState.Current
	s.Version.Version = old.Version.Version + 1
	d := syncDifference(old, s)
	if deltaEmpty(d) {
		return old, nil
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return s, err
	}
	record := syncRecord{Delta: d, Bytes: len(raw), At: time.Now()}
	h.syncState.Records = append(h.syncState.Records, record)
	h.syncState.Bytes += record.Bytes
	h.syncState.Current = &s
	for len(h.syncState.Records) > 0 && (len(h.syncState.Records) > syncWindowRecords || h.syncState.Bytes > syncWindowBytes || time.Since(h.syncState.Records[0].At) > 15*time.Minute) {
		h.syncState.Bytes -= h.syncState.Records[0].Bytes
		h.syncState.Records = h.syncState.Records[1:]
	}
	return s, nil
}
func (h *Hub) syncSinceLocked(ctx context.Context, cursor protocol.SyncCursor) ([]protocol.SyncDelta, protocol.SyncCheckpoint, error) {
	s, err := h.syncCurrentLocked(ctx)
	if err != nil {
		return nil, protocol.SyncCheckpoint{}, err
	}
	check := protocol.SyncCheckpoint{SyncCursor: protocol.SyncCursor{RunID: h.runID, Version: s.Version.Version}}
	if cursor.RunID == h.runID && cursor.Version == s.Version.Version {
		return nil, check, nil
	}
	if cursor.RunID == h.runID && cursor.Version < s.Version.Version {
		result := []protocol.SyncDelta{}
		next := cursor.Version
		for _, r := range h.syncState.Records {
			if r.Delta.From == next {
				result = append(result, r.Delta)
				next = r.Delta.Version
			}
		}
		if next == s.Version.Version {
			return result, check, nil
		}
	}
	return []protocol.SyncDelta{{SyncCursor: check.SyncCursor, From: cursor.Version, Reset: &s}}, check, nil
}
func (h *Hub) adminChangedLocked() {
	h.adminVersion++
	for c := range h.connections {
		if c.admin {
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (h *Hub) syncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sync", func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{CheckOrigin: transport.OriginOK, HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"agent-pulse"}}
		ws, e := u.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		h.serveSync(&transport.JSONSocket{Conn: ws, Category: "sync"})
	})
	mux.HandleFunc("GET /api/v1/events/bodies", func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["id"]
		if len(ids) == 0 || len(ids) > 32 {
			transport.Error(w, 400, "正文ID数量须为1至32")
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		out := []protocol.EventBody{}
		for _, id := range ids {
			if id == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
				transport.Error(w, 400, "事件ID无效")
				return
			}
			var raw []byte
			err := h.db.QueryRowContext(r.Context(), "SELECT payload FROM events WHERE event_id=?", id).Scan(&raw)
			if err != nil {
				transport.Error(w, 404, "正文事件已不可用")
				return
			}
			var e protocol.Event
			if json.Unmarshal(raw, &e) != nil {
				transport.Error(w, 503, "正文读取失败")
				return
			}
			out = append(out, protocol.EventBody{EventID: id, BodyHash: bodyHash(e.Body), Body: e.Body})
		}
		raw, _ := json.Marshal(out)
		transport.WriteJSONBytes(w, r, 200, raw)
	})
	mux.HandleFunc("GET /api/v1/traffic", func(w http.ResponseWriter, r *http.Request) { transport.JSON(w, 200, transport.TrafficSnapshot()) })
}

func (h *Hub) serveSync(ws stateSocket) {
	defer ws.Close()
	ws.SetReadLimit(16 << 10)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var first struct {
		Type      string             `json:"type"`
		RequestID string             `json:"request_id"`
		Payload   protocol.SyncHello `json:"payload"`
	}
	if ws.ReadJSON(&first) != nil || first.Type != "sync_hello" || !validHostID(first.Payload.Device.ID) || len(first.RequestID) > 128 {
		return
	}
	hello := first.Payload
	if hello.Device.Role != "tray" && hello.Device.Role != "web" && hello.Device.Role != "client" {
		return
	}
	c := &connection{device: hello.Device, ws: ws, wake: make(chan struct{}, 1), admin: hello.Admin && hello.Device.Role == "web"}
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
	if h.registerPresenceLocked(context.Background(), c.device) != nil {
		h.mu.Unlock()
		return
	}
	key := deviceKey(c.device)
	if prior := h.devices[key]; prior != nil {
		_ = prior.ws.Close()
	}
	h.devices[key] = c
	h.connections[c] = true
	h.changedLocked()
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
	probe := first.RequestID
	done := make(chan struct{})
	defer func() { _ = ws.Close(); <-done }()
	go func() {
		defer close(done)
		defer ws.Close()
		for {
			_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
			var msg struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				Payload   struct {
					Locked     *bool  `json:"locked"`
					Fullscreen *bool  `json:"fullscreen"`
					RunID      string `json:"run_id"`
					Version    uint64 `json:"version"`
				} `json:"payload"`
			}
			if ws.ReadJSON(&msg) != nil {
				return
			}
			if len(msg.RequestID) > 128 || (msg.Type != "heartbeat" && msg.Type != "state" && msg.Type != "ack") {
				return
			}
			h.mu.Lock()
			c.device.LastSeen = time.Now().UTC()
			if msg.Type == "heartbeat" && msg.RequestID != "" {
				probe = msg.RequestID
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
			if msg.Type != "ack" {
				select {
				case c.wake <- struct{}{}:
				default:
				}
			}
			h.mu.Unlock()
		}
	}()
	cursor := hello.Cursor
	send := func() error {
		h.mu.Lock()
		deltas, checkpoint, err := h.syncSinceLocked(context.Background(), cursor)
		checkpoint.Probe = probe
		if c.admin {
			checkpoint.AdminVersion = h.adminVersion
		}
		h.mu.Unlock()
		if err != nil {
			return err
		}
		for _, delta := range deltas {
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err = ws.WriteJSON(protocol.Message{Type: "sync", Payload: delta}); err != nil {
				return err
			}
			cursor = delta.SyncCursor
		}
		_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return ws.WriteJSON(protocol.Message{Type: "checkpoint", Payload: checkpoint})
	}
	if send() != nil {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if send() != nil {
				return
			}
		case <-c.wake:
			// 合并短时间对象变化；关键提醒在下一小批及时交付，原历史不删除。
			timer := time.NewTimer(75 * time.Millisecond)
			select {
			case <-done:
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-c.wake:
			default:
			}
			if send() != nil {
				return
			}
		}
	}
}
