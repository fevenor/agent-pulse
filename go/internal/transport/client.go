package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

func Request(ctx context.Context, client *http.Client, base, token, method, path string, body any, result any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, reader)
	if err != nil {
		return 0, fmt.Errorf("请求地址无效")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("连接失败或请求结果未知")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("服务返回 HTTP %d", resp.StatusCode)
	}
	if result != nil {
		data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		if err != nil || len(data) > 8<<20 {
			return resp.StatusCode, fmt.Errorf("响应读取失败或过大")
		}
		if err = json.Unmarshal(data, result); err != nil {
			return resp.StatusCode, fmt.Errorf("响应不是预期 JSON")
		}
	}
	return resp.StatusCode, nil
}

func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Watch 先建立订阅，再由回调读取快照；断线交给调用方重新连接。
func Watch(ctx context.Context, base, token string, device protocol.Device, onChange func(protocol.Version) error) error {
	wsURL := WebSocketURL(base, "/api/v1/connect")
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("实时连接失败")
	}
	defer ws.Close()
	ws.SetReadLimit(65536)
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err = ws.WriteJSON(protocol.Message{Type: "hello", Payload: device}); err != nil {
		return fmt.Errorf("实时连接握手失败")
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				_ = ws.Close()
				return
			case <-ticker.C:
				_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if ws.WriteJSON(protocol.Message{Type: "heartbeat"}) != nil {
					_ = ws.Close()
					return
				}
			}
		}
	}()
	for {
		_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		var msg struct {
			Type    string           `json:"type"`
			Payload protocol.Version `json:"payload"`
		}
		if err = ws.ReadJSON(&msg); err != nil {
			return fmt.Errorf("实时连接已断开")
		}
		if msg.Type == "hello" || msg.Type == "changed" {
			if err = onChange(msg.Payload); err != nil {
				return err
			}
		}
	}
}

func WebSocketURL(base, path string) string {
	return "ws" + strings.TrimPrefix(strings.TrimRight(base, "/"), "http") + path
}
