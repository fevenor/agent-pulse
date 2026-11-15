package hub

import (
	"agent-pulse/internal/event"
	"agent-pulse/protocol"
	"context"
	"testing"
	"time"
)

func TestConvertedFinalReplyAndQuestionsRemainInHistory(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	now := time.Now()
	for _, agent := range []string{"zcode", "qoderclicn"} {
		for _, item := range []struct{ hook, raw, want string }{
			{"Stop", `{"session_id":"s","last_assistant_message":"最终回答：验证完成。"}`, "最终回答：验证完成。"},
			{"PreToolUse", `{"session_id":"s","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"选择饮品？","options":[{"label":"茶","description":"清香"}]}]}}`, "选择饮品？\n1. 茶 — 清香"},
		} {
			e, err := event.Convert(agent, "body-host", item.hook, []byte(item.raw), now)
			if err != nil || e == nil {
				t.Fatal(e, err)
			}
			if _, err = h.Accept(ctx, *e); err != nil {
				t.Fatal(err)
			}
			history, err := h.SessionHistory(ctx, protocol.SessionKey{SourceID: e.SourceID, Agent: e.Agent, SessionID: e.SessionID}, 0, 30)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, stored := range history {
				if stored.EventID == e.EventID {
					found = true
					if stored.Body != item.want {
						t.Fatal("入库或历史接口丢失正文", stored.Body)
					}
				}
			}
			if !found {
				t.Fatal("历史接口未返回转换事件")
			}
		}
	}
}
