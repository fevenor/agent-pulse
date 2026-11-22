// Package bridge 负责本地快速接收和有界内存重试。
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	connectionChanges  chan struct{}
	connectedOnce      bool
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
	return &Bridge{connectionChanges: make(chan struct{}, 1), config: c, byID: map[string]*item{}, wake: make(chan struct{}, 1), collectorID: protocol.NewID(), observationContext: ctx, cancelObservations: cancel, observations: map[string]*observation{}, identities: map[string]*pendingIdentity{}, completedTurns: map[string]completedTurn{}}
}

func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		transport.JSON(w, 200, map[string]any{"traffic": transport.TrafficSnapshot(), "status": "ok", "pending": len(b.queue) + b.identityEvents, "bytes": b.bytes + b.identityBytes, "pending_identity": b.identityEvents, "rejected": b.rejected, "failed": b.failed, "last_failed_event_id": b.lastFailedID})
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
	if turn == "" {
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
	if e.Kind != "session.closed" {
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
	retry := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := b.runStream(ctx, b.config.HubURL, b.config.Token, false)
		if err == nil {
			retry = time.Second
			continue
		}
		if b.config.RelayURL != "" && ctx.Err() == nil {
			err = b.runStream(ctx, b.config.RelayURL, b.config.RelayToken, true)
			if err == nil {
				retry = time.Second
				continue
			}
		}
		if time.Since(started) > 35*time.Second {
			retry = time.Second
		}
		if !pause(ctx, retry) {
			return
		}
		retry *= 2
		if retry > 30*time.Second {
			retry = 30 * time.Second
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

// ConnectionChanges通知状态上报线程在重新接入后补报，不改变采集身份。
func (b *Bridge) ConnectionChanges() <-chan struct{} { return b.connectionChanges }
