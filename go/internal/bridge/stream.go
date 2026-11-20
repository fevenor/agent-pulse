package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"github.com/gorilla/websocket"
)

type upstreamMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"payload"`
}

func (b *Bridge) runStream(ctx context.Context, base, token string, relay bool) error {
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Proxy: http.ProxyFromEnvironment}
	raw, response, err := dialer.DialContext(ctx, transport.WebSocketURL(base, "/api/v1/connect"), headers)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return err
	}
	transport.RecordTraffic("upstream_connections", 0, 0, true)
	socket := &transport.JSONSocket{Conn: raw, Category: "upstream"}
	defer socket.Close()
	socket.SetReadLimit(256 << 10)
	b.mu.Lock()
	if b.connectedOnce {
		b.collectorID = protocol.NewID()
	}
	b.connectedOnce = true
	collector := b.collectorID
	b.mu.Unlock()
	name := b.config.HostName
	if name == "" {
		name, _ = os.Hostname()
	}
	send := func(msg protocol.Message) error {
		_ = socket.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return socket.WriteJSON(msg)
	}
	if err = send(protocol.Message{Type: "hello", Payload: protocol.Device{ID: b.config.SourceID, Role: "bridge", HostName: name, CollectorID: collector}}); err != nil {
		return err
	}
	localDone := make(chan struct{})
	incoming := make(chan upstreamMessage, 16)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_ = socket.SetReadDeadline(time.Now().Add(35 * time.Second))
			var msg upstreamMessage
			if socket.ReadJSON(&msg) != nil {
				return
			}
			select {
			case incoming <- msg:
			case <-localDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { close(localDone); _ = socket.Close(); <-readDone }()
	// 关闭连接与读取协程同生共死，避免输入缓冲满时退出阻塞。
	go func() {
		select {
		case <-ctx.Done():
			_ = socket.Close()
		case <-localDone:
		}
	}()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	lastHeartbeat := time.Now()
	handshake := false
	start := time.Now()
	pendingID := ""
	var pending []*item
	var sentAt time.Time
	probeResult := make(chan bool, 1)
	probing := false
	probeInterval := 30 * time.Second
	nextProbe := time.Now().Add(probeInterval)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readDone:
			return errors.New("上游实时连接结束")
		case msg := <-incoming:
			switch msg.Type {
			case "hello":
				if !handshake {
					select {
					case b.connectionChanges <- struct{}{}:
					default:
					}
				}
				handshake = true
			case "receipts":
				if msg.RequestID != pendingID || pendingID == "" {
					return errors.New("事件批次确认不匹配")
				}
				var receipt protocol.BatchReceipt
				if json.Unmarshal(msg.Payload, &receipt) != nil || len(receipt.Results) != len(pending) {
					return errors.New("事件确认数量无效")
				}
				b.mu.Lock()
				accepted := 0
				for i, result := range receipt.Results {
					entry := pending[i]
					if result.EventID != entry.event.EventID {
						b.mu.Unlock()
						return errors.New("事件确认顺序无效")
					}
					durable := result.Accepted == "durable" && ((!relay && result.Status == 200) || (relay && result.Status == 202))
					permanent := result.Status == 400 || result.Status == 409 || result.Status == 410 || result.Status == 413 || result.Status == 422
					if !durable && !permanent {
						break
					}
					if permanent {
						b.failed++
						b.lastFailedID = entry.event.EventID
					}
					b.bytes -= len(entry.payload)
					delete(b.byID, entry.event.EventID)
					accepted++
				}
				for i := 0; i < accepted; i++ {
					b.queue[i] = nil
				}
				b.queue = b.queue[accepted:]
				b.mu.Unlock()
				if accepted != len(pending) {
					return errors.New("批次部分事件尚未持久确认")
				}
				pendingID = ""
				pending = nil
			}
		case healthy := <-probeResult:
			probing = false
			if healthy {
				return nil
			}
			probeInterval *= 2
			if probeInterval > 5*time.Minute {
				probeInterval = 5 * time.Minute
			}
			nextProbe = time.Now().Add(probeInterval)
		case <-b.wake:
		case <-tick.C:
		}
		if !handshake && time.Since(start) > 10*time.Second {
			return errors.New("上游握手超时")
		}
		if time.Since(lastHeartbeat) >= 10*time.Second {
			if send(protocol.Message{Type: "heartbeat"}) != nil {
				return errors.New("上游心跳失败")
			}
			lastHeartbeat = time.Now()
		}
		if pendingID != "" && time.Since(sentAt) > 15*time.Second {
			return errors.New("持久确认超时")
		}
		if relay && !probing && time.Now().After(nextProbe) {
			probing = true
			go func() {
				probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				var value map[string]any
				status, e := transport.Request(probeCtx, transport.Client(3*time.Second), b.config.HubURL, b.config.Token, "GET", "/healthz", nil, &value)
				select {
				case probeResult <- e == nil && status == 200 && value["status"] == "ok":
				case <-ctx.Done():
				}
			}()
		}
		if handshake && pendingID == "" {
			b.mu.Lock()
			size := 0
			pending = nil
			for _, entry := range b.queue {
				if len(pending) >= 16 || size+len(entry.payload) > 128<<10 {
					break
				}
				pending = append(pending, entry)
				size += len(entry.payload)
			}
			b.mu.Unlock()
			if len(pending) > 0 {
				events := make([]protocol.Event, len(pending))
				for i, e := range pending {
					events[i] = e.event
				}
				pendingID = protocol.NewID()
				sentAt = time.Now()
				if send(protocol.Message{Type: "events", RequestID: pendingID, Payload: protocol.EventBatch{Events: events}}) != nil {
					return errors.New("事件批次发送失败")
				}
			}
		}
	}
}
