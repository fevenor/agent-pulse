package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/protocol"
)

type pendingIdentity struct {
	cursor []byte
	events []*item
	cancel context.CancelFunc
}

func (b *Bridge) enqueueIdentity(e protocol.Event, cursor []byte, turn string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enqueueIdentityLocked(e, cursor, turn)
}
func (b *Bridge) enqueueIdentityLocked(e protocol.Event, cursor []byte, turn string) (int, bool) {
	if len(cursor) == 0 || len(cursor) > 8192 || len(turn) > 256 {
		return 409, false
	}
	if b.observationContext.Err() != nil {
		return 503, false
	}
	e.CollectorID = b.collectorID
	payload, err := json.Marshal(e)
	if err != nil {
		return 409, false
	}
	if previous := b.byID[e.EventID]; previous != nil {
		e.CollectorID = previous.event.CollectorID
		payload, _ = json.Marshal(e)
		if bytes.Equal(previous.payload, payload) {
			return 202, true
		}
		return 409, false
	}
	key := e.Agent + "\x00" + e.SessionID
	pending := b.identities[key]
	if pending != nil {
		if !bytes.Equal(pending.cursor, cursor) {
			return 409, false
		}
		for _, previous := range pending.events {
			if previous.event.EventID == e.EventID {
				e.CollectorID = previous.event.CollectorID
				payload, _ = json.Marshal(e)
				if bytes.Equal(previous.payload, payload) {
					return 202, true
				}
				return 409, false
			}
		}
	}
	if len(b.queue)+b.identityEvents >= b.config.QueueEvents || b.bytes+b.identityBytes+len(payload) > b.config.QueueBytes || (pending == nil && len(b.identities) >= 128) {
		b.rejected++
		return 503, false
	}
	if pending == nil {
		ctx, cancel := context.WithCancel(b.observationContext)
		pending = &pendingIdentity{cursor: append([]byte{}, cursor...), cancel: cancel}
		b.identities[key] = pending
		b.observationWG.Add(1)
		go b.observeIdentity(ctx, key, pending)
	}
	pending.events = append(pending.events, &item{event: e, payload: payload, turnID: turn})
	b.identityEvents++
	b.identityBytes += len(payload)
	return 202, false
}

func (b *Bridge) observeIdentity(ctx context.Context, key string, pending *pendingIdentity) {
	defer b.observationWG.Done()
	delay := 200 * time.Millisecond
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		identity, err := adapters.CheckIdentity(ctx, b.config, strings.SplitN(key, "\x00", 2)[0], pending.cursor)
		b.mu.Lock()
		if b.identities[key] != pending {
			b.mu.Unlock()
			return
		}
		if err == nil && (identity == "main" || identity == "child") {
			done := b.resolveIdentityLocked(key, pending, identity)
			b.mu.Unlock()
			if done {
				return
			}
		} else {
			b.mu.Unlock()
		}
		// 长时间不可读取时继续保留有界内存中的事件，降低检查频率；不猜身份、不生成提醒。
		if time.Since(started) > 10*time.Second {
			delay = 2 * time.Second
		}
	}
}

func (b *Bridge) resolveIdentityLocked(key string, pending *pendingIdentity, identity string) bool {
	if b.identities[key] != pending {
		return true
	}
	for len(pending.events) > 0 {
		first := pending.events[0]
		b.identityEvents--
		b.identityBytes -= len(first.payload)
		if identity == "main" {
			if status, _ := b.enqueueTurnWithCollectorLocked(first.event, first.turnID, first.event.CollectorID); status != 202 {
				b.identityEvents++
				b.identityBytes += len(first.payload)
				return false
			}
		}
		pending.events = pending.events[1:]
	}
	delete(b.identities, key)
	pending.cancel()
	return true
}
