package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

func TestThreeSessionIncrementalTrafficDoesNotScaleWithUnchangedHistory(t *testing.T) {
	sizes := []int{}
	for _, historyCount := range []int{0, 128} {
		h := testHub(t)
		ctx := context.Background()
		for i := 0; i < historyCount; i++ {
			e := testEvent(fmt.Sprintf("old-%d", i), "activity.observed", 1)
			e.SessionID = fmt.Sprintf("history-%d", i)
			if _, err := h.Accept(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		_, cursor := currentSync(t, h, protocol.SyncCursor{})
		traffic := 0
		upstream := 0
		for i := 0; i < 360; i++ {
			e := testEvent(fmt.Sprintf("event-%04d", i), "activity.observed", int64(i+2))
			e.SessionID = fmt.Sprintf("work-%d", i%3)
			e.Body = ""
			if i%12 == 0 {
				e.Kind = "turn.completed"
				e.Body = strings.Repeat("不同工作结果正文。", 100)
			}
			_, raw, err := transport.EncodeMessage(protocol.Message{Type: "events", RequestID: "batch", Payload: protocol.EventBatch{Events: []protocol.Event{e}}})
			if err != nil {
				t.Fatal(err)
			}
			upstream += len(raw)
			if _, err = h.Accept(ctx, e); err != nil {
				t.Fatal(err)
			}
			d, check := currentSync(t, h, cursor.SyncCursor)
			cursor = check
			for _, delta := range d {
				if delta.Reset != nil {
					t.Fatal("活动期间不应重建基准")
				}
				_, raw, err = transport.EncodeMessage(protocol.Message{Type: "sync", Payload: delta})
				if err != nil {
					t.Fatal(err)
				}
				traffic += len(raw)
			}
			// 每条新提醒完整正文只计一次；用未压缩大小保守计量。
			if e.Body != "" {
				body, _ := json.Marshal(protocol.EventBody{EventID: e.EventID, BodyHash: bodyHash(e.Body), Body: e.Body})
				traffic += len(body)
			}
		}
		sizes = append(sizes, traffic)
		t.Logf("历史=%d，3会话360事件：上行消息=%d字节，下行增量及30份完整正文=%d字节（不含保活、HTTP/TCP/VPN）", historyCount, upstream, traffic)
		if traffic > 600000 || upstream > 400000 {
			t.Fatal("增量流量超过测试预算")
		}
	}
	if sizes[1] > sizes[0]+1024 {
		t.Fatal("不变历史会话放大每次增量", sizes)
	}
}
