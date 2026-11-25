package bridge

import (
	"context"

	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

// 失败的观察可以由后续 hook 重新登记；每次失败只产生一个非通知的未知事件。
// 等待真实工作结束后再断言，且不能在持锁时 Fatal，否则清理 stopObservations 会死锁。
func TestObserverFailureReportsUnknownAndAllowsReregistration(t *testing.T) {
	b := New(config.Bridge{SourceID: "test", QueueEvents: 10, QueueBytes: 1 << 20})
	defer b.stopObservations()
	req := protocol.LocalObservation{Adapter: "codex", Observation: protocol.Observation{SessionID: "main", TurnID: "turn", Cursor: []byte(`{}`)}}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := b.registerObservation(req); err != nil {
			t.Fatal(err)
		}
		// 未配置适配目录的 Observe 必定立即返回错误，不调用真实宿主。
		b.observationWG.Wait()
		b.mu.Lock()
		count := len(b.queue)
		valid := count == attempt && b.queue[count-1].event.Kind == "observation.lost" && !b.queue[count-1].event.Notifies()
		b.mu.Unlock()
		if !valid {
			t.Fatalf("观察失败应每次报告一次未知：尝试=%d 事件=%d", attempt, count)
		}
	}
	b.stopObservations()
	req.TurnID = "new"
	if err := b.registerObservation(req); err == nil {
		t.Fatal("停止后仍启动观察")
	}
}

func TestActiveObservationRegistrationIsIdempotent(t *testing.T) {
	b := New(config.Bridge{SourceID: "test", QueueEvents: 10, QueueBytes: 1 << 20})
	defer b.stopObservations()
	_, cancel := context.WithCancel(b.observationContext)
	current := &observation{turn: "turn", cancel: cancel, at: time.Now()}
	b.observations["codex\x00main"] = current
	req := protocol.LocalObservation{Adapter: "codex", Observation: protocol.Observation{SessionID: "main", TurnID: "turn", Cursor: []byte(`{}`)}}
	if err := b.registerObservation(req); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	unchanged := b.observations["codex\x00main"] == current && len(b.queue) == 0
	b.mu.Unlock()
	if !unchanged {
		t.Fatal("同轮进行中的观察被重复启动")
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

func TestSameTurnActivityKeepsObservationUntilFinalFailure(t *testing.T) {
	b := New(config.Bridge{SourceID: "test", QueueEvents: 10, QueueBytes: 1 << 20})
	defer b.stopObservations()
	ctx, cancel := context.WithCancel(b.observationContext)
	defer cancel()
	current := &observation{turn: "current", cancel: cancel, at: time.Now()}
	b.observations["codex\x00main"] = current
	for _, kind := range []string{"session.resumed", "input.required", "approval.required", "activity.observed"} {
		e := event(kind)
		e.Agent, e.Kind = "codex", kind
		if status, duplicate := b.enqueueTurn(e, "current"); status != 202 || duplicate {
			t.Fatal(status, duplicate)
		}
		if ctx.Err() != nil || current.done {
			t.Fatalf("同轮%s取消了最终模型错误观察", kind)
		}
	}
	failed := event("observed-final")
	failed.Agent, failed.Kind, failed.Body = "codex", "run.failed", "429 Too Many Requests"
	if status, duplicate := b.enqueueTurn(failed, "current"); status != 202 || duplicate {
		t.Fatal(status, duplicate)
	}
	if ctx.Err() == nil || !current.done || len(b.queue) != 5 || b.queue[4].event.Kind != "run.failed" {
		t.Fatal("最终错误未收尾观察或未进入队列")
	}
	failed.EventID = "duplicate-final"
	if status, duplicate := b.enqueueTurn(failed, "current"); status != 202 || !duplicate || len(b.queue) != 5 {
		t.Fatal("同轮最终错误重复提醒", status, duplicate)
	}
}

func TestObservationRetiresForNewTurnUnknownTurnAndTerminal(t *testing.T) {
	for _, tc := range []struct{ name, kind, turn string }{
		{"新轮次", "session.resumed", "next"},
		{"无轮次活动", "activity.observed", ""},
		{"最终完成", "turn.completed", "current"},
		{"中断", "run.interrupted", "current"},
		{"来源失效", "observation.lost", "current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New(config.Bridge{QueueEvents: 10, QueueBytes: 1 << 20})
			defer b.stopObservations()
			ctx, cancel := context.WithCancel(b.observationContext)
			defer cancel()
			current := &observation{turn: "current", cancel: cancel, at: time.Now()}
			b.observations["test\x00main"] = current
			e := event("new-event")
			e.Kind = tc.kind
			if status, _ := b.enqueueTurn(e, tc.turn); status != 202 {
				t.Fatal(status)
			}
			if ctx.Err() == nil || !current.done {
				t.Fatal("旧观察未回收")
			}
		})
	}
}
