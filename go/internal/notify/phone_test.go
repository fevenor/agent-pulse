package notify

import (
	"agent-pulse/protocol"
	"strings"
	"testing"
)

func TestPhoneMessageUsesSessionHostKindAndOriginalContent(t *testing.T) {
	original := protocol.Event{Title: "codex · 本轮完成", SessionTitle: "旧会话名", Kind: "turn.completed", Agent: "codex", Body: "原生回答"}
	phone := PhoneEvent(original, "新会话名", "主机友好名", "WSL Debian")
	if phone.Title != "新会话名" || !strings.Contains(phone.Body, "主机友好名 / WSL Debian") || !strings.Contains(phone.Body, "本轮完成") || !strings.HasSuffix(phone.Body, "原生回答") {
		t.Fatal(phone)
	}
	if strings.Count(phone.Body, "本轮完成") != 1 || original.Title != "codex · 本轮完成" || original.Body != "原生回答" {
		t.Fatal("手机重复状态或改写原始事件")
	}
	missing := PhoneEvent(protocol.Event{Kind: "turn.completed"}, "", "", "")
	if missing.Title != "未命名会话" || !strings.Contains(missing.Body, "未知主机") || !strings.Contains(missing.Body, "原生正文未取得") {
		t.Fatal(missing)
	}
}
