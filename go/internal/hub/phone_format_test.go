package hub

import (
	"agent-pulse/internal/config"
	"agent-pulse/protocol"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhoneFormattingUsesCurrentNamesAndDoesNotRewriteStoredEvent(t *testing.T) {
	messages := make(chan [2]string, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		messages <- [2]string{r.URL.Query().Get("title"), r.URL.Query().Get("body")}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	h, err := Open(config.Hub{Database: filepath.Join(t.TempDir(), "hub.db"), Push: []config.Channel{{Name: "phone", Method: "GET", URL: receiver.URL, TimeoutSeconds: 1, Query: map[string]string{"title": "${title}", "body": "${body}"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx := context.Background()
	e := testEvent("phone-format", "turn.completed", 1)
	e.Agent = "codex"
	e.Title = "codex · 本轮完成"
	e.SessionTitle = "原生会话名"
	e.Body = "原生回答"
	if _, err = h.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = h.registerPresenceLocked(ctx, protocol.Device{ID: e.SourceID, Role: "bridge", HostName: "默认名称", Environment: "Linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec("UPDATE hosts SET display_name=? WHERE source_id=?", "友好主机", e.SourceID); err != nil {
		t.Fatal(err)
	}
	h.pushOne(ctx)
	message := <-messages
	title, body := message[0], message[1]
	if title != "原生会话名" || !strings.Contains(body, "友好主机 / Linux") || !strings.Contains(body, "原生回答") || strings.Count(body, "本轮完成") != 1 {
		t.Fatal(title, body)
	}
	var stored string
	if err = h.db.QueryRow("SELECT payload FROM events WHERE event_id=?", e.EventID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "codex · 本轮完成") || strings.Contains(stored, "主机：") {
		t.Fatal("手机格式改写了原始事件", stored)
	}
}
