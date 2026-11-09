// Package transport 提供服务共用的 HTTP 边界处理。
package transport

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-pulse/protocol"
)

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, protocol.Error{Error: message, Retryable: status == 429 || status >= 500})
}

func Decode(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		Error(w, 415, "请求必须使用 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxEventBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(target)
	if err == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			err = errors.New("多余内容")
		}
	}
	if err != nil {
		var big *http.MaxBytesError
		if errors.As(err, &big) {
			Error(w, 413, "请求体超过 64 KiB")
		} else {
			Error(w, 400, "JSON 格式或字段错误")
		}
		return false
	}
	return true
}

func OriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func Guard(token string, local bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local {
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
				Error(w, 403, "本地入口仅接受回环 Host")
				return
			}
		}
		if !OriginOK(r) {
			Error(w, 403, "不允许跨站请求")
			return
		}
		authorization := r.Header.Get("Authorization")
		if authorization == "" && r.URL.Path == "/api/v1/connect" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			for _, item := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
				item = strings.TrimSpace(item)
				if strings.HasPrefix(item, "pulse-token.") {
					if decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(item, "pulse-token.")); err == nil {
						authorization = "Bearer " + string(decoded)
					}
				}
			}
		}
		if token != "" && strings.HasPrefix(r.URL.Path, "/api/") && subtle.ConstantTimeCompare([]byte(authorization), []byte("Bearer "+token)) != 1 {
			Error(w, 401, "缺少有效令牌")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve 在信号取消时停止 HTTP 服务；WebSocket 由所属组件关闭。
func Serve(ctx context.Context, address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(stop)
		case <-done:
		}
	}()
	err = srv.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
