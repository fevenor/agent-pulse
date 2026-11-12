package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func mustJSON(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestHostManagementRequiresHubToken(t *testing.T) {
	h := testHub(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	response := httptest.NewRecorder()
	h.Handler("secret").ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("主机接口应受令牌保护，实际状态 %d", response.Code)
	}
}

func TestHostLifecycleAndTombstone(t *testing.T) {
	h, err := Open(config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), Push: []config.Channel{{Name: "phone"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx := context.Background()
	e := testEvent("host-event", "input.required", 1)
	if _, err = h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}

	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.Handler("").ServeHTTP(w, r)
		return w
	}
	if err := h.MarkRead(ctx, []string{e.EventID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO session_management(source_id,agent,session_id,alias) VALUES(?,?,?,?)`, e.SourceID, e.Agent, e.SessionID, "历史别名"); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodPost, "/api/v1/adapters/status", `{"source_id":"linux","items":[]}`); response.Code != 200 {
		t.Fatalf("适配器状态未保存：%d %s", response.Code, response.Body.String())
	}
	w := request(http.MethodPatch, "/api/v1/hosts/linux", `{"display_name":"开发机","archived":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("修改主机失败：%d %s", w.Code, w.Body.String())
	}
	s, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Sources["linux"]; got.Name != "开发机" || !got.Archived {
		t.Fatalf("快照中的主机资料错误：%+v", got)
	}

	// 默认客户端（Tray）不应再浏览归档主机的事件；Hub 管理页面可显式查看。
	hidden, err := h.History(ctx, 0, 20)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("归档主机事件应对客户端隐藏：%d %v", len(hidden), err)
	}
	visible, err := h.HistoryWithArchived(ctx, 0, 20, true)
	if err != nil || len(visible) != 1 || visible[0].EventID != e.EventID {
		t.Fatalf("Hub 应保留归档主机历史：%+v %v", visible, err)
	}
	var hosts []protocol.HostInfo
	w = request(http.MethodGet, "/api/v1/hosts", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &hosts) != nil || len(hosts) != 1 || hosts[0].Name != "开发机" || !hosts[0].Archived {
		t.Fatalf("主机列表响应错误：%d %s %#v", w.Code, w.Body.String(), hosts)
	}

	if err = h.deleteHost(ctx, "linux"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	err = h.registerPresenceLocked(ctx, protocol.Device{ID: "linux", Role: "bridge", HostName: "旧主机"})
	h.mu.Unlock()
	if !errors.Is(err, ErrHostDeleted) {
		t.Fatalf("已删除主机不得重新连接：%v", err)
	}
	if _, err = h.Accept(ctx, e); !errors.Is(err, ErrHostDeleted) {
		t.Fatalf("已删除主机的旧事件应被拒绝：%v", err)
	}
	// HTTP 层将删除标记返回为 410，Bridge/Relay 会清理这类旧队列。
	w = request(http.MethodPost, "/api/v1/events", mustJSON(e))
	if w.Code != http.StatusGone {
		t.Fatalf("已删除主机事件的响应错误：%d %s", w.Code, w.Body.String())
	}
	var count int
	for _, table := range []string{"events", "sessions", "adapter_hosts", "session_management", "hosts"} {
		if err = h.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE source_id=?`, "linux").Scan(&count); err != nil {
			t.Fatal(table, err)
		}
		if count != 0 {
			t.Fatalf("表 %s 未清理：%d", table, count)
		}
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM reads WHERE event_id=?`,
		`SELECT COUNT(*) FROM push_jobs WHERE event_id=?`,
	} {
		if err = h.db.QueryRow(query, e.EventID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("事件关联记录未清理：%d", count)
		}
	}
}

func TestBridgeHelloUnarchivesAndTrayDoesNot(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	archived := true
	if err := h.patchHost(ctx, "linux", hostChange{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	if err := h.registerPresenceLocked(ctx, protocol.Device{ID: "linux", Role: "bridge", HostName: "开发主机"}); err != nil {
		h.mu.Unlock()
		t.Fatal(err)
	}
	h.mu.Unlock()
	s, err := h.Snapshot(ctx)
	if err != nil || s.Sources["linux"].Archived || s.Sources["linux"].Name != "开发主机" {
		t.Fatalf("Bridge 重连未恢复主机：%+v %v", s.Sources["linux"], err)
	}
	if err := h.patchHost(ctx, "linux", hostChange{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	if err := h.registerPresenceLocked(ctx, protocol.Device{ID: "linux", Role: "tray", HostName: "Tray 名称不能覆盖主机"}); err != nil {
		h.mu.Unlock()
		t.Fatal(err)
	}
	h.mu.Unlock()
	// 同指纹 Tray 的 hello 不得覆盖 Bridge 的主机资料或归档状态。
	s, err = h.Snapshot(ctx)
	if err != nil || !s.Sources["linux"].Archived || s.Sources["linux"].Name != "开发主机" {
		t.Fatalf("Tray 不得恢复或重命名同指纹 Bridge：%+v %v", s.Sources["linux"], err)
	}
}
