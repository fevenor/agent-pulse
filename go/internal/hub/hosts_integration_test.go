package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func TestDeletedHostReportsAndStaticLabelsStayDeletedAfterRestart(t *testing.T) {
	cfg := config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), Sources: map[string]config.SourceLabel{"linux": {Name: "静态主机名", Environment: "Linux"}}}
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.Handler("test-token"))
	defer func() { server.Close(); _ = h.Close() }()
	ctx := context.Background()
	client := transport.Client(2 * time.Second)
	request := func(method, path string, body, result any, want int) {
		t.Helper()
		status, err := transport.Request(ctx, client, server.URL, "test-token", method, path, body, result)
		if status != want || (want == 200 && err != nil) {
			t.Fatalf("请求 %s %s：状态 %d，错误 %v", method, path, status, err)
		}
	}
	report := map[string]any{"source_id": "linux", "items": []any{}}
	request("POST", "/api/v1/adapters/status", report, nil, 200)
	request("DELETE", "/api/v1/hosts/linux", nil, nil, 200)
	for _, phase := range []string{"删除后", "重启后"} {
		if phase == "重启后" {
			server.Close()
			if err = h.Close(); err != nil {
				t.Fatal(err)
			}
			h, err = Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server = httptest.NewServer(h.Handler("test-token"))
		}
		request("POST", "/api/v1/adapters/status", report, nil, http.StatusGone)
		var reports []any
		request("GET", "/api/v1/adapters/status", nil, &reports, 200)
		if len(reports) != 0 {
			t.Fatalf("%s旧报告重新创建了适配器记录：%+v", phase, reports)
		}
		var snapshot protocol.Snapshot
		request("GET", "/api/v1/snapshot", nil, &snapshot, 200)
		if _, exists := snapshot.Sources["linux"]; exists {
			t.Fatalf("%s静态配置重新显示了已删除主机：%+v", phase, snapshot.Sources)
		}
		var hosts []protocol.HostInfo
		request("GET", "/api/v1/hosts", nil, &hosts, 200)
		if len(hosts) != 0 {
			t.Fatalf("%s主机管理记录重新出现：%+v", phase, hosts)
		}
	}
}

func TestTwoTrayClientsObserveHostNamesAndTrayLifecycle(t *testing.T) {
	h := testHub(t)
	server := httptest.NewServer(h.Handler("test-token"))
	defer server.Close()
	ctx := context.Background()
	request := func(method, path string, body, result any) {
		t.Helper()
		status, err := transport.Request(ctx, transport.Client(2*time.Second), server.URL, "test-token", method, path, body, result)
		if err != nil || status != 200 {
			t.Fatalf("请求 %s %s 失败：%d %v", method, path, status, err)
		}
	}
	connect := func(device protocol.Device, accepted bool) *websocket.Conn {
		t.Helper()
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/connect", http.Header{"Authorization": []string{"Bearer test-token"}})
		if err != nil {
			t.Fatal("设备连接失败", err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		if err = ws.WriteJSON(protocol.Message{Type: "hello", Payload: device}); err != nil {
			t.Fatal("发送设备登记失败", err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var hello protocol.Message
		err = ws.ReadJSON(&hello)
		if accepted && (err != nil || hello.Type != "hello") {
			t.Fatalf("设备登记未确认：%+v %v", hello, err)
		}
		if !accepted && err == nil {
			t.Fatalf("已删除设备被错误接纳：%+v", hello)
		}
		return ws
	}
	bridgeDevice := protocol.Device{ID: "linux", Role: "bridge", HostName: "开发机", Environment: "Linux"}
	bridge := connect(bridgeDevice, true)
	trays := []*websocket.Conn{}
	for _, id := range []string{"tray-one", "tray-two"} {
		trays = append(trays, connect(protocol.Device{ID: id, Role: "tray", HostName: "本地桌面"}, true))
	}
	request("PATCH", "/api/v1/hosts/linux", map[string]any{"display_name": "集中维护的开发机", "archived": true}, nil)
	request("PATCH", "/api/v1/hosts/tray-one", map[string]any{"display_name": "办公桌面", "archived": true}, nil)
	var target protocol.Snapshot
	request("GET", "/api/v1/snapshot", nil, &target)
	for index, ws := range trays {
		for {
			var message struct {
				Type    string           `json:"type"`
				Payload protocol.Version `json:"payload"`
			}
			if err := ws.ReadJSON(&message); err != nil {
				t.Fatalf("第 %d 个 Tray 未收到改名通知：%v", index+1, err)
			}
			if message.Type == "changed" && message.Payload.RunID == target.RunID && message.Payload.Version >= target.Version.Version {
				break
			}
		}
		var snapshot protocol.Snapshot
		request("GET", "/api/v1/snapshot", nil, &snapshot)
		if snapshot.Sources["linux"].Name != "集中维护的开发机" || snapshot.Sources["tray-one"].Name != "办公桌面" {
			t.Fatalf("第 %d 个 Tray 获取的名称不一致：%+v", index+1, snapshot.Sources)
		}
		found := false
		for _, device := range snapshot.Devices {
			if device.ID == "tray-one" {
				found = device.HostName == "办公桌面"
			}
		}
		if !found {
			t.Fatalf("第 %d 个 Tray 获取的设备列表未使用集中名称：%+v", index+1, snapshot.Devices)
		}
	}
	// 旧连接继续心跳、收到事件或适配器报告，都不应被当成主机重新上线。
	if err := bridge.WriteJSON(protocol.Message{Type: "heartbeat", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/events", testEvent("archived-live-event", "input.required", 1), nil)
	request("POST", "/api/v1/adapters/status", map[string]any{"source_id": "linux", "items": []any{}}, nil)
	tray := connect(protocol.Device{ID: "tray-one", Role: "tray", HostName: "客户端本地新名称"}, true)
	var snapshot protocol.Snapshot
	request("GET", "/api/v1/snapshot", nil, &snapshot)
	if !snapshot.Sources["linux"].Archived || !snapshot.Sources["tray-one"].Archived || snapshot.Sources["tray-one"].Name != "办公桌面" {
		t.Fatalf("非 Bridge 重连不应解档或覆盖集中名称：%+v", snapshot.Sources)
	}
	var history []protocol.EventView
	request("GET", "/api/v1/events", nil, &history)
	if len(history) != 0 {
		t.Fatalf("Tray 仍可读取归档事件：%+v", history)
	}
	request("GET", "/api/v1/events?include_archived=true", nil, &history)
	if len(history) != 1 {
		t.Fatalf("Hub 未保留归档事件：%+v", history)
	}
	var hosts []protocol.HostInfo
	request("GET", "/api/v1/hosts", nil, &hosts)
	found := false
	for _, host := range hosts {
		if host.SourceID == "tray-one" {
			found = host.Role == "tray" && host.Online && host.Name == "办公桌面" && host.DefaultName == "客户端本地新名称"
		}
	}
	if !found {
		t.Fatalf("Tray 的主机资料或在线状态不正确：%+v", hosts)
	}
	// 真正重新建立 Bridge 连接才解除归档，集中名称仍优先。
	_ = connect(bridgeDevice, true)
	request("GET", "/api/v1/snapshot", nil, &snapshot)
	if snapshot.Sources["linux"].Archived || snapshot.Sources["linux"].Name != "集中维护的开发机" {
		t.Fatalf("Bridge 重连解档或名称规则错误：%+v", snapshot.Sources["linux"])
	}
	request("DELETE", "/api/v1/hosts/tray-one", nil, nil)
	snapshot = protocol.Snapshot{}
	for {
		var message protocol.Message
		if err := tray.ReadJSON(&message); err != nil {
			break
		}
	}
	request("GET", "/api/v1/snapshot", nil, &snapshot)
	if _, exists := snapshot.Sources["tray-one"]; exists {
		t.Fatal("已删除 Tray 仍出现在主机名称中")
	}
	for _, device := range snapshot.Devices {
		if device.ID == "tray-one" {
			t.Fatal("已删除 Tray 仍保持连接")
		}
	}
	_ = connect(protocol.Device{ID: "tray-one", Role: "tray", HostName: "已删除桌面"}, false)
}
