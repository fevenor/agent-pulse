package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func currentSync(t *testing.T, h *Hub, c protocol.SyncCursor) ([]protocol.SyncDelta, protocol.SyncCheckpoint) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	d, v, e := h.syncSinceLocked(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	return d, v
}
func TestIncrementalUnreadRemovalValidityAndBodyIdentity(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	e := testEvent("result", "turn.completed", 1)
	e.Body = strings.Repeat("正文", 3000)
	if _, err := h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	baseline, check := currentSync(t, h, protocol.SyncCursor{})
	if len(baseline) != 1 || baseline[0].Reset == nil {
		t.Fatal("首次必须取得基准")
	}
	initial := baseline[0].Reset
	if len(initial.Unread) != 1 || initial.Unread[0].Body != "" || initial.Unread[0].BodyHash != bodyHash(e.Body) {
		t.Fatal("基准不应重传正文")
	}
	h.mu.Lock()
	h.changedLocked()
	h.mu.Unlock()
	empty, next := currentSync(t, h, check.SyncCursor)
	if len(empty) != 0 || next.Version != check.Version {
		t.Fatal("无业务变化不得增加同步位置")
	}
	if _, err := h.Accept(ctx, testEvent("resume", "session.resumed", 2)); err != nil {
		t.Fatal(err)
	}
	d, check2 := currentSync(t, h, check.SyncCursor)
	if len(d) != 1 || d[0].Reset != nil || len(d[0].Unread) != 1 || d[0].Unread[0].Valid || d[0].Unread[0].Body != "" {
		t.Fatal("失效应只更新标记", d)
	}
	if err := h.MarkRead(ctx, []string{e.EventID}); err != nil {
		t.Fatal(err)
	}
	d, check3 := currentSync(t, h, check2.SyncCursor)
	if len(d) != 1 || !reflect.DeepEqual(d[0].UnreadRemoved, []string{e.EventID}) {
		t.Fatal("已读必须明确移除未读", d)
	}
	if err := h.deleteHost(ctx, e.SourceID); err != nil {
		t.Fatal(err)
	}
	d, _ = currentSync(t, h, check3.SyncCursor)
	if len(d) != 1 || len(d[0].SessionsRemoved) != 1 || len(d[0].SourcesRemoved) != 1 {
		t.Fatal("删除必须移除来源和会话", d)
	}
}
func TestIncrementalWindowAndIdleVolume(t *testing.T) {
	h := testHub(t)
	_, check := currentSync(t, h, protocol.SyncCursor{})
	original := check.SyncCursor
	for i := 0; i < syncWindowRecords+2; i++ {
		e := testEvent(protocol.NewID(), "activity.observed", int64(i+1))
		if _, err := h.Accept(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		_, check = currentSync(t, h, check.SyncCursor)
	}
	d, _ := currentSync(t, h, original)
	if len(d) != 1 || d[0].Reset == nil {
		t.Fatal("恢复窗口不足时必须重建基准")
	}
	d, _ = currentSync(t, h, protocol.SyncCursor{RunID: "other-run", Version: check.Version})
	if len(d) != 1 || d[0].Reset == nil {
		t.Fatal("运行代次变更必须重建基准")
	}
	var hourly int
	for i := 0; i < 360; i++ {
		d, v := currentSync(t, h, check.SyncCursor)
		if len(d) != 0 {
			t.Fatal("空闲不可重复发送基准")
		}
		_, raw, err := transport.EncodeMessage(protocol.Message{Type: "checkpoint", Payload: v})
		if err != nil {
			t.Fatal(err)
		}
		hourly += len(raw)
	}
	if hourly > 100000 {
		t.Fatal("空闲状态确认超过预算", hourly)
	}
	t.Logf("无变化时每小时360次权威确认应用载荷=%d字节，无基准重传", hourly)
}
func TestSyncSocketRestoresCursorAndRejectsPhantomRead(t *testing.T) {
	h := testHub(t)
	server := httptest.NewServer(h.Handler("test"))
	defer server.Close()
	dial := func(cursor protocol.SyncCursor) *transport.JSONSocket {
		t.Helper()
		ws, _, err := websocket.DefaultDialer.Dial(transport.WebSocketURL(server.URL, "/api/v1/sync"), map[string][]string{"Authorization": {"Bearer test"}})
		if err != nil {
			t.Fatal(err)
		}
		s := &transport.JSONSocket{Conn: ws}
		if err = s.WriteJSON(protocol.Message{Type: "sync_hello", RequestID: "fresh-probe", Payload: protocol.SyncHello{Device: protocol.Device{ID: "view", Role: "tray"}, Cursor: cursor}}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	read := func(s *transport.JSONSocket) (string, json.RawMessage) {
		t.Helper()
		_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
		var m struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := s.ReadJSON(&m); err != nil {
			t.Fatal(err)
		}
		return m.Type, m.Payload
	}
	s := dial(protocol.SyncCursor{})
	defer s.Close()
	kind, raw := read(s)
	var d protocol.SyncDelta
	if json.Unmarshal(raw, &d) != nil || kind != "sync" || d.Reset == nil {
		t.Fatal("同步基准缺失")
	}
	kind, raw = read(s)
	var check protocol.SyncCheckpoint
	if json.Unmarshal(raw, &check) != nil || check.Probe != "fresh-probe" {
		t.Fatal("必须回显本次客户端探测")
	}
	if kind != "checkpoint" {
		t.Fatal("基准后缺确认")
	}
	e := testEvent("notice", "input.required", 1)
	if _, err := h.Accept(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for {
		kind, raw = read(s)
		if kind == "sync" {
			break
		}
	}
	d = protocol.SyncDelta{}
	if json.Unmarshal(raw, &d) != nil || d.Reset != nil || len(d.Unread) != 1 {
		t.Fatal("实时变更不应重发基准")
	}
	cursor := d.SyncCursor
	_ = s.Close()
	if err := h.MarkRead(context.Background(), []string{"notice"}); err != nil {
		t.Fatal(err)
	}
	restored := dial(cursor)
	defer restored.Close()
	removed := false
	for !removed {
		kind, raw = read(restored)
		if kind != "sync" {
			continue
		}
		d = protocol.SyncDelta{}
		if json.Unmarshal(raw, &d) != nil {
			t.Fatal("恢复增量无效")
		}
		if d.Reset != nil {
			removed = len(d.Reset.Unread) == 0
		} else {
			removed = len(d.UnreadRemoved) == 1
		}
	}
}
