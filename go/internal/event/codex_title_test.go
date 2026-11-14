package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCodexHookUsesExactNativeTitleWithoutReadingConversationBody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	index := "{\"id\":\"other\",\"thread_name\":\"其他会话标题\"}\n" +
		"{\"id\":\"main\",\"thread_name\":\"旧标题\"}\n" +
		"{\"id\":\"main\",\"thread_name\":\"部署并优化通知服务\"}\n"
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"session_id":"main","cwd":"/工作目录/agent-pulse","prompt":"不要作为标题或正文上传","transcript_path":"/不应读取的文件"}`)
	event, err := Convert("codex", "host", "UserPromptSubmit", raw, time.Now())
	if err != nil || event == nil {
		t.Fatal(err)
	}
	if event.SessionTitle != "部署并优化通知服务" || event.Project != "agent-pulse" || event.Body != "" || event.Title != "codex · 正在运行" {
		t.Fatalf("会话标题、项目和通知标题不应混用：%+v", event)
	}
	other, err := Convert("qoderclicn", "host", "UserPromptSubmit", raw, time.Now())
	if err != nil || other == nil || other.SessionTitle != "" {
		t.Fatal("其他 Agent 不应读取 Codex 的本地标题")
	}
	after, err := os.ReadFile(filepath.Join(home, "session_index.jsonl"))
	if err != nil || string(after) != index {
		t.Fatal("读取标题不应改写原索引")
	}
}

func TestCodexMissingInvalidAndOversizedTitleMetadataDoesNotDropEvent(t *testing.T) {
	for _, value := range []string{"", "invalid-json\n", `{"id":"other","thread_name":"不匹配"}`, `{"id":"s","thread_name":"  "}`, strings.Repeat("x", (8<<20)+1)} {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		if value != "" {
			if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
		event, err := Convert("codex", "host", "Stop", []byte(`{"session_id":"s","cwd":"/项目"}`), time.Now())
		if err != nil || event == nil || event.Kind != "turn.completed" || event.SessionTitle != "" {
			t.Fatal("可选标题不可用时仍需投递原事件", err)
		}
	}
}

func TestCodexTitleFitsProtocolByteLimitAndKeepsUTF8(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	row, _ := json.Marshal(map[string]string{"id": "s", "thread_name": strings.Repeat("会话😀", 400)})
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), row, 0600); err != nil {
		t.Fatal(err)
	}
	title := codexSessionTitle("s")
	if title == "" || len(title) > 1024 || !utf8.ValidString(title) {
		t.Fatal("标题必须保持有效 UTF-8 并满足协议字节限制")
	}
}
