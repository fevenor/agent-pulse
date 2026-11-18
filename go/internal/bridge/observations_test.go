package bridge

import (
	"context"

	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func TestObserverFailureReportsUnknownOnceAndCancellationRejectsNewWork(t *testing.T) {
	b := New(config.Bridge{SourceID: "test", QueueEvents: 10, QueueBytes: 1 << 20})
	defer b.stopObservations()
	req := protocol.LocalObservation{Adapter: "codex", Observation: protocol.Observation{SessionID: "main", TurnID: "turn", Cursor: []byte(`{}`)}}
	if err := b.registerObservation(req); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		done := b.observations["codex\x00main"].done
		b.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	b.mu.Lock()
	if len(b.queue) != 1 || b.queue[0].event.Kind != "observation.lost" || b.queue[0].event.Notifies() {
		t.Fatal("来源故障被当成任务失败或未降为未知")
	}
	b.mu.Unlock()
	if err := b.registerObservation(req); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	if len(b.queue) != 1 {
		t.Fatal("重复登记重复产生事件")
	}
	b.mu.Unlock()
	b.stopObservations()
	req.TurnID = "new"
	if err := b.registerObservation(req); err == nil {
		t.Fatal("停止后仍启动观察")
	}
}

func TestObservationCapacityAndQueueRetryKeepFirstCollector(t *testing.T) {
	b := New(config.Bridge{SourceID: "test", QueueEvents: 10, QueueBytes: 1 << 20})
	defer b.stopObservations()
	for i := 0; i < 128; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		_ = ctx
		b.observations[string(rune(i+100))+"active"] = &observation{turn: "t", cancel: cancel, at: time.Now()}
	}
	req := protocol.LocalObservation{Adapter: "codex", Observation: protocol.Observation{SessionID: "main", TurnID: "turn", Cursor: []byte(`{}`)}}
	if err := b.registerObservation(req); err == nil {
		t.Fatal("突破观察容量")
	}
	e := protocol.Event{EventID: "event", SourceID: "test", Agent: "codex", SessionID: "main", Kind: "session.started", Title: "运行", Scope: "main", OccurredAt: time.Now()}
	if status, duplicate := b.enqueue(e); status != 202 || duplicate {
		t.Fatal(status, duplicate)
	}
	b.mu.Lock()
	first := b.queue[0].event.CollectorID
	b.collectorID = "new-connection"
	b.mu.Unlock()
	if status, duplicate := b.enqueue(e); status != 202 || !duplicate {
		t.Fatal("连接切换破坏了首次入队身份", status, duplicate)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.queue[0].event.CollectorID != first {
		t.Fatal("缓存事件被改写为新连接的真实进展")
	}
}
