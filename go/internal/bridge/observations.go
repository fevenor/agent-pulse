package bridge

import (
	"context"
	"errors"
	"strings"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/localipc"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
	"os"
)

type observation struct {
	turn   string
	cancel context.CancelFunc
	done   bool
	at     time.Time
}

func (b *Bridge) registerObservation(req protocol.LocalObservation) error {
	if req.Adapter == "" || req.SessionID == "" || req.TurnID == "" || len(req.Adapter) > 64 || len(req.SessionID) > 256 || len(req.TurnID) > 256 || len(req.Cursor) == 0 || len(req.Cursor) > 65536 || strings.ContainsAny(req.Adapter+req.SessionID+req.TurnID, "\x00\r\n") {
		return errors.New("本轮终态登记无效")
	}
	key := req.Adapter + "\x00" + req.SessionID
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.observationContext.Err() != nil {
		return errors.New("Bridge正在停止")
	}
	if previous := b.observations[key]; previous != nil {
		if previous.turn == req.TurnID {
			return nil
		}
		previous.cancel()
	} else if len(b.observations) >= 128 {
		oldestKey := ""
		oldest := time.Now()
		for k, v := range b.observations {
			if v.done && v.at.Before(oldest) {
				oldestKey = k
				oldest = v.at
			}
		}
		if oldestKey == "" {
			return errors.New("本轮终态观察容量已满")
		}
		delete(b.observations, oldestKey)
	}
	ctx, cancel := context.WithCancel(b.observationContext)
	current := &observation{turn: req.TurnID, cancel: cancel, at: time.Now()}
	b.observations[key] = current
	b.observationWG.Add(1)
	go func() {
		defer b.observationWG.Done()
		err := adapters.Observe(ctx, b.config, req.Adapter, req.Cursor, func(e protocol.Event) error {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.observations[key] != current || e.SessionID != req.SessionID {
				return context.Canceled
			}
			status, _ := b.enqueueLocked(e)
			if status != 202 {
				return errors.New("本轮终态未进入队列")
			}
			return nil
		})
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.observations[key] != current {
			return
		}
		current.done = true
		if err != nil && ctx.Err() == nil {
			// 观察失败只报未知，不把来源故障冒充模型失败；不会生成强提醒。
			b.enqueueLocked(protocol.Event{EventID: protocol.NewID(), SourceID: b.config.SourceID, Agent: req.Adapter, SessionID: req.SessionID, Kind: "observation.lost", OccurredAt: time.Now().UTC(), Scope: "main", Title: req.Adapter + " · 本轮状态待确认", Body: "本轮原生结束状态不可读取，不能确认任务仍在运行"})
		}
	}()
	return nil
}

func RegisterObservation(ctx context.Context, address string, req protocol.LocalObservation) error {
	client, err := localipc.Client(address, 300*time.Millisecond)
	if err != nil {
		return err
	}
	req.ProcessID = os.Getpid()
	var receipt struct {
		Accepted string `json:"accepted"`
	}
	status, err := transport.Request(ctx, client, localipc.BaseURL, "", "POST", "/api/v1/observations", req, &receipt)
	if err != nil {
		return err
	}
	if status != 202 || (receipt.Accepted != "memory" && receipt.Accepted != "filtered") {
		return errors.New("本轮终态登记未确认")
	}
	return nil
}

func (b *Bridge) stopObservations() {
	b.mu.Lock()
	b.cancelObservations()
	b.mu.Unlock()
	b.observationWG.Wait()
}
