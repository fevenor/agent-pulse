package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/relay"
	"agent-pulse/internal/transport"
)

type virtualSocket struct {
	peer     *transport.Tunnel
	id       string
	in       chan []byte
	done     chan struct{}
	once     sync.Once
	deadline time.Time
}

func (v *virtualSocket) Close() error {
	v.once.Do(func() { close(v.done); _ = v.peer.Send(transport.Frame{Type: "stream_close", ID: v.id}) })
	return nil
}
func (v *virtualSocket) SetReadLimit(int64)                {}
func (v *virtualSocket) SetReadDeadline(t time.Time) error { v.deadline = t; return nil }
func (v *virtualSocket) SetWriteDeadline(time.Time) error  { return nil }
func (v *virtualSocket) WriteJSON(data any) error {
	return v.peer.Send(transport.Frame{Type: "stream_data", ID: v.id, Body: transport.Encode(data)})
}
func (v *virtualSocket) ReadJSON(data any) error {
	timer := time.NewTimer(time.Until(v.deadline))
	defer timer.Stop()
	select {
	case body := <-v.in:
		return json.Unmarshal(body, data)
	case <-v.done:
		return errors.New("连接关闭")
	case <-v.peer.Done():
		return errors.New("Relay 失联")
	case <-timer.C:
		return errors.New("心跳超时")
	}
}

type responseBuffer struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (r *responseBuffer) Header() http.Header { return r.header }
func (r *responseBuffer) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *responseBuffer) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	if r.body.Len()+len(b) > 8<<20 {
		r.overflow = true
		return 0, errors.New("中继响应过大")
	}
	return r.body.Write(b)
}

func (h *Hub) RunRelay(ctx context.Context, c config.Hub) {
	if c.RelayURL == "" {
		return
	}
	for ctx.Err() == nil {
		peer, err := transport.DialTunnel(ctx, c.RelayURL, c.RelayToken)
		if err == nil {
			h.handleRelay(ctx, peer, c.Token)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (h *Hub) handleRelay(ctx context.Context, p *transport.Tunnel, token string) {
	defer p.Close()
	handler := h.Handler(token)
	var mu sync.Mutex
	streams := map[string]*virtualSocket{}
	var wg sync.WaitGroup
	limit := make(chan struct{}, 32)
	p.Read(ctx, func(f transport.Frame) {
		switch f.Type {
		case "request":
			select {
			case limit <- struct{}{}:
			default:
				_ = p.Send(transport.Frame{Type: "response", ID: f.ID, Status: 503, Body: transport.Encode(map[string]string{"error": "并发请求过多"})})
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-limit }()
				reply := transport.Frame{Type: "response", ID: f.ID, Status: 400}
				u, err := url.ParseRequestURI(f.Path)
				if err == nil && u.Host == "" && relay.Allowed(f.Method, u.Path) && len(f.Body) <= 65536 {
					req, err := http.NewRequestWithContext(ctx, f.Method, "http://hub.internal"+f.Path, bytes.NewReader(f.Body))
					if err == nil {
						req.Header.Set("Content-Type", "application/json")
						if token != "" {
							req.Header.Set("Authorization", "Bearer "+token)
						}
						if f.Headers["Range"] != "" {
							req.Header.Set("Range", f.Headers["Range"])
						}
						out := &responseBuffer{header: make(http.Header)}
						handler.ServeHTTP(out, req)
						reply.Status = out.status
						if reply.Status == 0 {
							reply.Status = 200
						}
						reply.Body = out.body.Bytes()
						reply.Headers = map[string]string{}
						for _, key := range []string{"Content-Type", "Content-Range", "Accept-Ranges", "ETag"} {
							if value := out.header.Get(key); value != "" {
								reply.Headers[key] = value
							}
						}
						if out.overflow {
							reply.Status = 413
							reply.Body = nil
						}
					}
				}
				_ = p.Send(reply)
			}()
		case "stream_input":
			if len(f.Body) > 4096 || f.ID == "" {
				return
			}
			mu.Lock()
			v := streams[f.ID]
			if v == nil {
				var msg struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(f.Body, &msg) != nil || msg.Type != "hello" || len(streams) >= 128 {
					mu.Unlock()
					_ = p.Send(transport.Frame{Type: "stream_close", ID: f.ID})
					return
				}
				v = &virtualSocket{peer: p, id: f.ID, in: make(chan []byte, 8), done: make(chan struct{})}
				streams[f.ID] = v
				wg.Add(1)
				go func() { defer wg.Done(); h.serveSocket(v); mu.Lock(); delete(streams, v.id); mu.Unlock() }()
			}
			mu.Unlock()
			select {
			case v.in <- f.Body:
			default:
				_ = v.Close()
			}
		case "stream_close":
			mu.Lock()
			v := streams[f.ID]
			mu.Unlock()
			if v != nil {
				_ = v.Close()
			}
		}
	})
	mu.Lock()
	for _, v := range streams {
		_ = v.Close()
	}
	mu.Unlock()
	wg.Wait()
}
