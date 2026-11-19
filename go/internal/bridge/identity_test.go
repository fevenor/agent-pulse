package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"agent-pulse/internal/config"
)

func TestPendingIdentityNeverLeaksChildAndSharesQueueCapacity(t *testing.T) {
	b := New(config.Bridge{QueueEvents: 3, QueueBytes: 1 << 20})
	defer b.stopObservations()
	cursor := json.RawMessage(`{"session_id":"main","root":"/test/antigravity"}`)
	e := event("first")
	e.Agent = "antigravity"
	e.Kind = "session.started"
	if code, _ := b.enqueueIdentity(e, cursor, ""); code != 202 {
		t.Fatal(code)
	}
	if code, duplicate := b.enqueueIdentity(e, cursor, ""); code != 202 || !duplicate {
		t.Fatal(code, duplicate)
	}
	e.EventID = "second"
	e.Kind = "activity.observed"
	if code, _ := b.enqueueIdentity(e, cursor, ""); code != 202 {
		t.Fatal(code)
	}
	b.mu.Lock()
	if len(b.queue) != 0 || b.identityEvents != 2 || b.identityBytes == 0 {
		t.Fatal("身份未确认即进入网络队列")
	}
	if !b.resolveIdentityLocked("antigravity\x00main", b.identities["antigravity\x00main"], "child") || len(b.queue) != 0 || b.identityEvents != 0 || b.identityBytes != 0 {
		t.Fatal("子任务未丢弃")
	}
	b.mu.Unlock()
}

func TestPendingMainReleasedBeforeNewTerminalKeepsCollector(t *testing.T) {
	b := New(config.Bridge{QueueEvents: 4, QueueBytes: 1 << 20})
	defer b.stopObservations()
	cursor := json.RawMessage(`{"session_id":"main","root":"/test/antigravity"}`)
	firstCollector := b.collectorID
	for _, id := range []string{"first", "second"} {
		e := event(id)
		e.Agent = "antigravity"
		e.Kind = "activity.observed"
		if code, _ := b.enqueueIdentity(e, cursor, ""); code != 202 {
			t.Fatal(code)
		}
	}
	b.mu.Lock()
	b.collectorID = "new-connection"
	b.mu.Unlock()
	done := event("done")
	done.Agent = "antigravity"
	done.Kind = "turn.completed"
	if code, _ := b.enqueue(done); code != 202 {
		t.Fatal(code)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) != 0 || b.identityEvents != 3 {
		t.Fatal("缺少身份的后续事件提前放行")
	}
	if !b.resolveIdentityLocked("antigravity\x00main", b.identities["antigravity\x00main"], "main") {
		t.Fatal("主身份确认后未释放")
	}
	if len(b.queue) != 3 || b.queue[0].event.EventID != "first" || b.queue[1].event.EventID != "second" || b.queue[2].event.EventID != "done" {
		t.Fatal("身份延后破坏了终态顺序")
	}
	if b.queue[0].event.CollectorID != firstCollector || b.queue[2].event.CollectorID != "new-connection" {
		t.Fatal("身份核对后改写首次采集代次")
	}
	if b.identityEvents != 0 || b.identityBytes != 0 || len(b.identities) != 0 {
		t.Fatal("身份队列未释放")
	}
}

func TestPendingIdentityUnavailableDoesNotPretendMainOrLoseEvent(t *testing.T) {
	b := New(config.Bridge{QueueEvents: 1, QueueBytes: 1 << 20})
	defer b.stopObservations()
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	p := &pendingIdentity{cursor: []byte(`{}`), cancel: cancel}
	e := event("first")
	e.Agent = "antigravity"
	e.CollectorID = b.collectorID
	payload, _ := json.Marshal(e)
	p.events = []*item{{event: e, payload: payload}}
	b.identities["antigravity\x00main"] = p
	b.identityEvents = 1
	b.identityBytes = len(payload)
	if code, _ := b.enqueue(event("other")); code != 503 {
		t.Fatal("身份队列绕过总容量", code)
	}
	if len(b.queue) != 0 || b.identityEvents != 1 {
		t.Fatal("不可确认身份被放行或丢弃")
	}
	b.stopObservations()
	if code, _ := b.enqueueIdentity(e, []byte(`{}`), ""); code != 503 {
		t.Fatal("停止后仍接收身份等待", code)
	}
}

func TestAntigravityNativeAndObservedFinalOnlyEnqueueOnce(t *testing.T) {
	b := New(config.Bridge{QueueEvents: 10, QueueBytes: 1 << 20})
	defer b.stopObservations()
	first := event("observer-result")
	first.Agent = "antigravity"
	first.Kind = "turn.completed"
	if code, duplicate := b.enqueueTurn(first, "turn-1"); code != 202 || duplicate {
		t.Fatal(code, duplicate)
	}
	next := first
	next.EventID = "native-result"
	if code, duplicate := b.enqueueTurn(next, "turn-1"); code != 202 || !duplicate {
		t.Fatal("原生与观察重复提醒", code, duplicate)
	}
	if len(b.queue) != 1 {
		t.Fatal(len(b.queue))
	}
	next.EventID = "next-turn"
	if code, duplicate := b.enqueueTurn(next, "turn-2"); code != 202 || duplicate {
		t.Fatal("新轮次被误去重", code, duplicate)
	}
	if len(b.queue) != 2 {
		t.Fatal(len(b.queue))
	}
}

func TestPendingIdentityWorkerResolvesWithInstalledAdapter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("此隔离入口用Linux脚本模拟身份查询，不触发原生模型")
	}
	for _, identity := range []string{"main", "child"} {
		t.Run(identity, func(t *testing.T) {
			root := t.TempDir()
			selection := filepath.Join(root, "identity.json")
			if err := os.WriteFile(selection, []byte(`{"identity":"unknown"}`), 0600); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, "packages", "antigravity", "0.3.6", "test")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "adapter"), []byte("#!/bin/sh\ncat '"+selection+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			state := map[string]any{"installed": map[string]any{"antigravity": map[string]any{"package": map[string]any{"id": "antigravity", "version": "0.3.6", "sha256": "test", "entry": "adapter", "kind": "executable"}}}, "rejected": map[string]string{}}
			raw, _ := json.Marshal(state)
			if err := os.WriteFile(filepath.Join(root, "state.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			b := New(config.Bridge{QueueEvents: 10, QueueBytes: 1 << 20, Adapters: config.AdapterUpdates{Directory: root}})
			defer b.stopObservations()
			e := event("early")
			e.Agent = "antigravity"
			if code, _ := b.enqueueIdentity(e, []byte(`{"session_id":"main","root":"/sample/antigravity"}`), ""); code != 202 {
				t.Fatal(code)
			}
			time.Sleep(250 * time.Millisecond)
			b.mu.Lock()
			pending := b.identityEvents
			queued := len(b.queue)
			b.mu.Unlock()
			if pending != 1 || queued != 0 {
				t.Fatal("未知身份被提前放行", pending, queued)
			}
			if err := os.WriteFile(selection, []byte(`{"identity":"`+identity+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				b.mu.Lock()
				pending = b.identityEvents
				queued = len(b.queue)
				b.mu.Unlock()
				if pending == 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			expected := 0
			if identity == "main" {
				expected = 1
			}
			if pending != 0 || queued != expected {
				t.Fatal("已安装适配器身份检查未收敛", identity, pending, queued)
			}
		})
	}
}
