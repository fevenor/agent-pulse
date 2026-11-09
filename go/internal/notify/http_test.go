package notify

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func TestChuckfangBusinessResult(t *testing.T) {
	for _, tt := range []struct{ body, want string }{
		{`{"status":200,"message":"推送成功"}`, "sent"},
		{`{"status":400,"message":"参数错误"}`, "failed"},
		{`{"status":500}`, "failed"},
		{`{"status":"200"}`, "unknown"},
		{`{"status":null}`, "unknown"},
		{`{}`, "unknown"},
		{`<html>上游错误</html>`, "unknown"},
		{`{"status":200}尾部不完整`, "unknown"},
		{strings.Repeat(" ", 64<<10) + `{"status":200}`, "unknown"},
	} {
		if got := chuckfangResult(strings.NewReader(tt.body)); got != tt.want {
			t.Fatalf("业务结果为 %s，期望 %s", got, tt.want)
		}
	}
}

func TestSendSelectsChannelBusinessCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":400}`))
	}))
	defer server.Close()
	// 请求保留渠道域名，网络仅重定向到本地测试服务器，不连接真实渠道。
	previous := http.DefaultTransport
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	for _, tt := range []struct{ host, want string }{
		{"api.chuckfang.com", "failed"},
		{"API.CHUCKFANG.COM", "failed"},
		{"other.example.invalid", "sent"},
	} {
		ch := config.Channel{Method: "POST", Format: "json", URL: "http://" + tt.host + "/test", TimeoutSeconds: 1}
		if got := Send(context.Background(), ch, protocol.Event{}); got != tt.want {
			t.Fatalf("%s 返回 %s，期望 %s", tt.host, got, tt.want)
		}
	}
}

func TestHTTPEncoding(t *testing.T) {
	e := protocol.Event{Title: "中文 & + ?", Body: "换行\n\"引号\" & token=value"}
	for _, format := range []string{"get", "json", "form"} {
		t.Run(format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Key") != "secret" || r.URL.Query().Get("title") != e.Title {
					t.Error("查询或头部编码错误")
				}
				if format == "json" {
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["content"] != e.Body {
						t.Error("JSON 编码错误")
					}
				}
				if format == "form" {
					if r.ParseForm() != nil || r.PostForm.Get("content") != e.Body {
						t.Error("表单编码错误")
					}
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			ch := config.Channel{Method: "GET", URL: server.URL, Query: map[string]string{"title": "${title}"}, Headers: map[string]string{"X-Key": "secret"}, TimeoutSeconds: 1}
			if format != "get" {
				ch.Method = "POST"
				ch.Format = format
				ch.Fields = map[string]string{"content": "${body}"}
			}
			if got := Send(context.Background(), ch, e); got != "sent" {
				t.Fatal(got)
			}
		})
	}
}

func TestAmbiguousResponseAndRedirect(t *testing.T) {
	for _, code := range []int{0, 302, 429, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if code == 0 {
					c, _, _ := w.(http.Hijacker).Hijack()
					_ = c.Close()
					return
				}
				w.Header().Set("Location", "http://example.invalid")
				w.WriteHeader(code)
			}))
			defer s.Close()
			want := map[int]string{0: "unknown", 302: "failed", 429: "retry", 500: "retry"}[code]
			if got := Send(context.Background(), config.Channel{Method: "POST", Format: "json", URL: s.URL, TimeoutSeconds: 1}, protocol.Event{}); got != want {
				t.Fatal(got, want)
			}
		})
	}
}
