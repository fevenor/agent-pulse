// Package bridge 负责本地快速接收和有界内存重试。
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/config"
	"agent-pulse/internal/localipc"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

type item struct {
	event   protocol.Event
	payload []byte
	turnID  string
}

type Bridge struct {
	mu                 sync.Mutex
	config             config.Bridge
	queue              []*item
	byID               map[string]*item
	bytes              int
	rejected           uint64
	failed             uint64
	lastFailedID       string
	wake               chan struct{}
	collectorID        string
	observationContext context.Context
	cancelObservations context.CancelFunc
	observations       map[string]*observation
	observationWG      sync.WaitGroup
	identities         map[string]*pendingIdentity
	identityEvents     int
	identityBytes      int
	completedTurns     map[string]completedTurn
}

func New(c config.Bridge) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{config: c, byID: map[string]*item{}, wake: make(chan struct{}, 1), collectorID: protocol.NewID(), observationContext: ctx, cancelObservations: cancel, observations: map[string]*observation{}, identities: map[string]*pendingIdentity{}, completedTurns: map[string]completedTurn{}}
}

func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		transport.JSON(w, 200, map[string]any{"status": "ok", "pending": len(b.queue) + b.identityEvents, "bytes": b.bytes + b.identityBytes, "pending_identity": b.identityEvents, "rejected": b.rejected, "failed": b.failed, "last_failed_event_id": b.lastFailedID})
	})
	mux.HandleFunc("POST /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		var input protocol.LocalEvent
		if !transport.Decode(w, r, &input) {
			return
		}
		e := input.Event
		if e.SourceID == "" {
			e.SourceID = b.config.SourceID
		}
		if e.SourceID != b.config.SourceID {
			transport.Error(w, 400, "source_id 与本地 Bridge 不一致")
			return
		}
		if err := e.Validate(); err != nil {
			transport.Error(w, 400, err.Error())
			return
		}
		if adapters.FromACP(b.config, input.ProcessID) {
			transport.JSON(w, 202, protocol.Receipt{EventID: e.EventID, Accepted: "filtered"})
			return
		}
		var status int
		var duplicate bool
		if len(input.Identity) > 0 {
			status, duplicate = b.enqueueIdentity(e, input.Identity, input.TurnID)
		} else {
			status, duplicate = b.enqueueTurn(e, input.TurnID)
		}
		if status == 409 {
			transport.Error(w, status, "队列内已有相同 event_id 的不同内容")
			return
		}
		if status == 503 {
			transport.Error(w, status, "内存队列已满，事件未接收")
			return
		}
		transport.JSON(w, 202, protocol.Receipt{EventID: e.EventID, Accepted: "memory", Duplicate: duplicate})
	})
	mux.HandleFunc("POST /api/v1/observations", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.LocalObservation
		if !transport.Decode(w, r, &req) {
			return
		}
		if adapters.FromACP(b.config, req.ProcessID) {
			transport.JSON(w, 202, map[string]string{"accepted": "filtered"})
			return
		}
		if err := b.registerObservation(req); err != nil {
			transport.Error(w, 503, "本轮终态未登记")
			return
		}
		transport.JSON(w, 202, map[string]string{"accepted": "memory"})
	})
	return transport.Guard("", true, mux)
}

func (b *Bridge) enqueue(e protocol.Event) (int, bool) { return b.enqueueTurn(e, "") }
func (b *Bridge) enqueueTurn(e protocol.Event, turn string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if pending := b.identities[e.Agent+"\x00"+e.SessionID]; pending != nil {
		// 后续缺少路径的hook也不能证明主身份，共用已登记的实际数据根继续等待。
		return b.enqueueIdentityLocked(e, pending.cursor, turn)
	}
	return b.enqueueTurnLocked(e, turn)
}

type completedTurn struct {
	turn string
	at   time.Time
}

func (b *Bridge) enqueueTurnLocked(e protocol.Event, turn string) (int, bool) {
	return b.enqueueTurnWithCollectorLocked(e, turn, b.collectorID)
}
func (b *Bridge) enqueueTurnWithCollectorLocked(e protocol.Event, turn, collector string) (int, bool) {
	key := e.Agent + "\x00" + e.SessionID
	if e.Agent != "antigravity" || turn == "" {
		return b.enqueueWithCollectorLocked(e, collector)
	}
	if len(turn) > 256 {
		return 409, false
	}
	if done, ok := b.completedTurns[key]; ok && done.turn == turn {
		return 202, true
	}
	status, duplicate := b.enqueueWithCollectorLocked(e, collector)
	if status == 202 && (e.Kind == "turn.completed" || e.Kind == "run.failed" || e.Kind == "run.interrupted") {
		if len(b.completedTurns) >= 128 {
			var oldest string
			at := time.Now()
			for id, item := range b.completedTurns {
				if item.at.Before(at) {
					oldest = id
					at = item.at
				}
			}
			delete(b.completedTurns, oldest)
		}
		b.completedTurns[key] = completedTurn{turn: turn, at: time.Now()}
	}
	return status, duplicate
}

func (b *Bridge) enqueueLocked(e protocol.Event) (int, bool) {
	return b.enqueueWithCollectorLocked(e, b.collectorID)
}
func (b *Bridge) enqueueWithCollectorLocked(e protocol.Event, collector string) (int, bool) {
	e.OccurredAt = e.OccurredAt.UTC()
	if previous := b.byID[e.EventID]; previous != nil {
		e.CollectorID = previous.event.CollectorID
	} else {
		e.CollectorID = collector
	}
	payload, _ := json.Marshal(e)
	if previous := b.byID[e.EventID]; previous != nil {
		if bytes.Equal(previous.payload, payload) {
			return 202, true
		}
		return 409, false
	}
	if len(b.queue)+b.identityEvents >= b.config.QueueEvents || b.bytes+b.identityBytes+len(payload) > b.config.QueueBytes {
		b.rejected++
		return 503, false
	}
	// 明确关闭会话后给最终记录一个短交付窗口，再回收未结束的观察进程；不使用任务空闲超时。
	if e.Agent == "antigravity" {
		if current := b.observations[e.Agent+"\x00"+e.SessionID]; current != nil && !current.done {
			current.cancel()
			current.done = true
		}
	}
	if e.Kind == "session.closed" {
		if current := b.observations[e.Agent+"\x00"+e.SessionID]; current != nil && !current.done {
			time.AfterFunc(2*time.Second, current.cancel)
		}
	}
	entry := &item{event: e, payload: payload}
	b.queue = append(b.queue, entry)
	b.byID[e.EventID] = entry
	b.bytes += len(payload)
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return 202, false
}

func (b *Bridge) Run(ctx context.Context) {
	defer b.stopObservations()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); b.presence(ctx) }()
	defer wg.Wait()
	client := transport.Client(5 * time.Second)
	retry := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		var entry *item
		if len(b.queue) > 0 {
			entry = b.queue[0]
		}
		b.mu.Unlock()
		if entry == nil {
			select {
			case <-ctx.Done():
				return
			case <-b.wake:
			}
			continue
		}
		var receipt protocol.Receipt
		status, err := transport.Request(ctx, client, b.config.HubURL, b.config.Token, http.MethodPost, "/api/v1/events", entry.event, &receipt)
		accepted := err == nil && status == 200 && receipt.EventID == entry.event.EventID && receipt.Accepted == "durable"
		if !accepted && (err != nil && (status == 0 || status >= 500)) && b.config.RelayURL != "" {
			status, err = transport.Request(ctx, client, b.config.RelayURL, b.config.RelayToken, http.MethodPost, "/api/v1/events", entry.event, &receipt)
			accepted = err == nil && status == 202 && receipt.EventID == entry.event.EventID && receipt.Accepted == "durable"
		}
		// 明确无效的单条事件不阻塞后续事件；保留失败计数和标识供诊断。
		permanent := status == 400 || status == 409 || status == 410 || status == 413 || status == 422
		if accepted || permanent {
			b.mu.Lock()
			b.queue[0] = nil
			b.queue = b.queue[1:]
			delete(b.byID, entry.event.EventID)
			b.bytes -= len(entry.payload)
			if permanent {
				b.failed++
				b.lastFailedID = entry.event.EventID
			}
			b.mu.Unlock()
			if permanent {
				slog.Error("上游拒绝事件", "event_id", entry.event.EventID, "status", status)
			}
			retry = time.Second
			continue
		}
		slog.Warn("上游暂不可用，事件保留在内存", "event_id", entry.event.EventID, "status", status)
		if !pause(ctx, retry) {
			return
		}
		if retry < 30*time.Second {
			retry *= 2
			if retry > 30*time.Second {
				retry = 30 * time.Second
			}
		}
	}
}

func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (b *Bridge) presence(ctx context.Context) {
	hostName := b.config.HostName
	if hostName == "" {
		hostName, _ = os.Hostname()
	}
	device := protocol.Device{ID: b.config.SourceID, Role: "bridge", HostName: hostName}
	first := true
	connect := func(connectCtx context.Context, base, token string) error {
		b.mu.Lock()
		if !first {
			b.collectorID = protocol.NewID()
		}
		first = false
		device.CollectorID = b.collectorID
		b.mu.Unlock()
		return transport.Watch(connectCtx, base, token, device, func(protocol.Version) error { return nil })
	}
	for ctx.Err() == nil {
		err := connect(ctx, b.config.HubURL, b.config.Token)
		if err != nil && b.config.RelayURL != "" && ctx.Err() == nil {
			// 定期重建回退连接，以重新探测直连；不让回退路径永久黏住。
			fallback, cancel := context.WithTimeout(ctx, 30*time.Second)
			_ = connect(fallback, b.config.RelayURL, b.config.RelayToken)
			cancel()
		}
		if !pause(ctx, 2*time.Second) {
			return
		}
	}
}

func Emit(ctx context.Context, address string, e protocol.Event) error {
	return EmitWithIdentity(ctx, address, e, nil, "")
}
func EmitWithIdentity(ctx context.Context, address string, e protocol.Event, identity json.RawMessage, turn string) error {
	client, err := localipc.Client(address, 200*time.Millisecond)
	if err != nil {
		return err
	}
	if e.EventID == "" {
		e.EventID = protocol.NewID()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	var receipt protocol.Receipt
	status, err := transport.Request(ctx, client, localipc.BaseURL, "", http.MethodPost, "/api/v1/events", protocol.LocalEvent{Event: e, Identity: identity, TurnID: turn, ProcessID: os.Getpid()}, &receipt)
	if err != nil {
		return err
	}
	if status != 202 || (receipt.Accepted != "memory" && receipt.Accepted != "filtered") || receipt.EventID != e.EventID {
		return fmt.Errorf("本地确认格式错误")
	}
	return nil
}
