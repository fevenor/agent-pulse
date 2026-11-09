package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

// Frame 仅承载当前连接上的请求、回复和实时订阅，不形成持久消息日志。
type Frame struct {
	Type    string            `json:"type"`
	ID      string            `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

type Tunnel struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan Frame
	done    chan struct{}
	once    sync.Once
}

func NewTunnel(ws *websocket.Conn) *Tunnel {
	return &Tunnel{ws: ws, pending: map[string]chan Frame{}, done: make(chan struct{})}
}
func (p *Tunnel) Done() <-chan struct{} { return p.done }
func (p *Tunnel) Close()                { p.once.Do(func() { close(p.done); _ = p.ws.Close() }) }
func (p *Tunnel) Send(frame Frame) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := p.ws.WriteJSON(frame)
	if err != nil {
		p.Close()
	}
	return err
}
func (p *Tunnel) Request(ctx context.Context, frame Frame) (Frame, error) {
	frame.ID = protocol.NewID()
	frame.Type = "request"
	reply := make(chan Frame, 1)
	p.mu.Lock()
	if len(p.pending) >= 64 {
		p.mu.Unlock()
		return Frame{}, errors.New("中继并发请求过多")
	}
	p.pending[frame.ID] = reply
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, frame.ID); p.mu.Unlock() }()
	if err := p.Send(frame); err != nil {
		return Frame{}, err
	}
	select {
	case result := <-reply:
		return result, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-p.done:
		return Frame{}, errors.New("Hub 中继连接已断开")
	}
}

func (p *Tunnel) Read(ctx context.Context, handle func(Frame)) {
	defer p.Close()
	p.ws.SetReadLimit(12 << 20)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				p.Close()
				return
			case <-p.done:
				return
			case <-ticker.C:
				if p.Send(Frame{Type: "ping"}) != nil {
					return
				}
			}
		}
	}()
	for {
		_ = p.ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		var frame Frame
		if p.ws.ReadJSON(&frame) != nil {
			return
		}
		switch frame.Type {
		case "ping":
			if p.Send(Frame{Type: "pong"}) != nil {
				return
			}
		case "pong":
		case "response":
			p.mu.Lock()
			ch := p.pending[frame.ID]
			p.mu.Unlock()
			if ch != nil {
				select {
				case ch <- frame:
				default:
				}
			}
		default:
			handle(frame)
		}
	}
}

func DialTunnel(ctx context.Context, base, token string) (*Tunnel, error) {
	u := WebSocketURL(base, "/api/v1/relay")
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, resp, err := dialer.DialContext(ctx, u, http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, errors.New("连接 Relay 失败")
	}
	return NewTunnel(ws), nil
}

func Encode(value any) []byte { b, _ := json.Marshal(value); return b }
