package transport

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserTokenAndOrigin(t *testing.T) {
	handler := Guard("临时令牌", false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tt := range []struct {
		name, path, auth, protocol, origin string
		upgrade                            bool
		status                             int
	}{
		{name: "页面公开", path: "/", status: 204},
		{name: "API 需要令牌", path: "/api/v1/snapshot", status: 401},
		{name: "HTTP 头令牌", path: "/api/v1/snapshot", auth: "Bearer 临时令牌", status: 204},
		{name: "WS 子协议令牌", path: "/api/v1/connect", protocol: "agent-pulse, pulse-token." + base64.RawURLEncoding.EncodeToString([]byte("临时令牌")), origin: "http://hub.local", upgrade: true, status: 204},
		{name: "拒绝跨站", path: "/api/v1/connect", auth: "Bearer 临时令牌", origin: "http://external.invalid", upgrade: true, status: 403},
		{name: "子协议不能用于普通请求", path: "/api/v1/snapshot", protocol: "pulse-token." + base64.RawURLEncoding.EncodeToString([]byte("临时令牌")), status: 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://hub.local"+tt.path, nil)
			req.Header.Set("Authorization", tt.auth)
			req.Header.Set("Sec-WebSocket-Protocol", tt.protocol)
			req.Header.Set("Origin", tt.origin)
			if tt.upgrade {
				req.Header.Set("Upgrade", "websocket")
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != tt.status {
				t.Fatal(out.Code, tt.status)
			}
		})
	}
}
