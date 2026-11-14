package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func antigravityTitleCache(t *testing.T, value string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "conversation_metadata.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAntigravityHookRefreshesExactNativeTitleOnNextEvent(t *testing.T) {
	first := `{"conversations":{"main":{"summary":{"ID":"main","Title":"原名称","Preview":"不能上传的内容"}},"other":{"summary":{"ID":"other","Title":"其他会话"}}}}`
	path := antigravityTitleCache(t, first)
	raw := []byte(`{"conversationId":"main","workspacePaths":["/项目"],"terminationReason":"model_stop"}`)
	e, err := Convert("antigravity", "host", "SessionStart", raw, time.Now())
	if err != nil || e == nil || e.SessionTitle != "原名称" || e.Body != "" {
		t.Fatal("启动应携带同一会话的原生名称", e, err)
	}
	if err = os.WriteFile(path, []byte(strings.Replace(first, "原名称", "用户改名后的名称", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	e, err = Convert("antigravity", "host", "Stop", raw, time.Now())
	if err != nil || e == nil || e.SessionTitle != "用户改名后的名称" || e.SessionID != "main" || e.Kind != "turn.completed" || e.Body != "" {
		t.Fatal("下一次真实事件应携带新名，不改变身份或丢失状态", e, err)
	}
	before, _ := os.ReadFile(path)
	_ = antigravitySessionTitle("main")
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("名称查询不应改写宿主缓存")
	}
}

func TestAntigravityOptionalMetadataCannotReplaceTitleWithPreviewOrDropEvent(t *testing.T) {
	for _, value := range []string{
		"invalid-json", `null`, `{"conversations":{}}`,
		`{"conversations":{"main":{"summary":"不能当作标题的摘要"}}}`,
		`{"conversations":{"main":{"summary":{"ID":"other","Title":"错误会话"}}}}`,
		`{"conversations":{"main":{"summary":{"ID":"main","Title":" ","Preview":"不能当作标题"}}}}`,
		`{"conversations":{"main":{"summary":{"ID":"main","title":"错误字段大小写"}}}}`,
		strings.Repeat("x", (8<<20)+1),
	} {
		antigravityTitleCache(t, value)
		e, err := Convert("antigravity", "host", "Stop", []byte(`{"conversationId":"main","terminationReason":"model_stop"}`), time.Now())
		if err != nil || e == nil || e.Kind != "turn.completed" || e.SessionTitle != "" {
			t.Fatal("标题缓存不可用不能丢掉真实事件或泄露摘要", e, err)
		}
	}
}

func TestAntigravityTitleMetadataUTF8AndMissingCache(t *testing.T) {
	row, _ := json.Marshal(map[string]any{"conversations": map[string]any{"main": map[string]any{"summary": map[string]string{"ID": "main", "Title": strings.Repeat("名称😀", 400)}}}})
	path := antigravityTitleCache(t, string(row))
	name := antigravitySessionTitle("main")
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) {
		t.Fatal("标题必须满足协议长度并保持 UTF-8")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if antigravitySessionTitle("main") != "" {
		t.Fatal("不存在缓存时应保持未知")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if antigravitySessionTitle("main") != "" {
		t.Fatal("目录不能作为缓存读取")
	}
}

func TestAntigravityPersistedRenameWinsOverStaleJSONCache(t *testing.T) {
	cache := antigravityTitleCache(t, `{"conversations":{"main":{"summary":{"ID":"main","Title":"尚未刷新的旧缓存名称"}}}}`)
	annotations := filepath.Join(filepath.Dir(filepath.Dir(cache)), "annotations", "main.pbtxt")
	if err := os.MkdirAll(filepath.Dir(annotations), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"用户第一次改名", "最新用户名称 ✅"} {
		if err := os.WriteFile(annotations, []byte("title: "+strconv.Quote(name)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		e, err := Convert("antigravity", "host", "PreInvocation", []byte(`{"conversationId":"main"}`), time.Now())
		if err != nil || e == nil || e.SessionTitle != name || e.SessionID != "main" || e.Body != "" {
			t.Fatal("下次真实事件应读取持久化改名，不依赖缓存刷新或上传正文", e, err)
		}
	}
	if got, err := os.ReadFile(cache); err != nil || !strings.Contains(string(got), "尚未刷新的旧缓存名称") {
		t.Fatal("不应改写宿主缓存", err)
	}
}

func TestAntigravityAnnotationStringBoundaryAndFallback(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"title: \"用户名称\"\n", "用户名称"},
		{"title: \"转义\\n引号\\\"名称\"\n", "转义\n引号\"名称"},
		{`title: "\344\270\255\xE6\x96\x87"`, "中文"},
		{`title: "\xff"`, "缓存名称"},
		{"", "缓存名称"},
		{"title: \"\"\n", "缓存名称"},
		{"  title: \"嵌套字段不是名称\"\n", "缓存名称"},
		{"Title: \"错误大小写\"\n", "缓存名称"},
		{"title: \"第一条\"\ntitle: \"第二条\"\n", "缓存名称"},
		{"title: 未加引号\n", "缓存名称"},
		{"title: \"半行", "缓存名称"},
		{strings.Repeat("x", (64<<10)+1), "缓存名称"},
	} {
		cache := antigravityTitleCache(t, `{"conversations":{"main":{"summary":{"ID":"main","Title":"缓存名称"}}}}`)
		path := filepath.Join(filepath.Dir(filepath.Dir(cache)), "annotations", "main.pbtxt")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
			t.Fatal(err)
		}
		if got := antigravitySessionTitle("main"); got != tc.want {
			t.Fatalf("注解标量读取 = %q，期望 %q", got, tc.want)
		}
		for _, id := range []string{"../main", "main/other", `main\other`, ".", "..", "main\x00"} {
			if antigravitySessionTitle(id) != "" {
				t.Fatal("不匹配会话不能读取当前注解")
			}
		}
	}
}

func TestAntigravityOnlyExplicitInternalMetadataFiltersNormalHooks(t *testing.T) {
	for _, tc := range []struct {
		input    string
		filtered bool
	}{
		{`{"conversations":{"main":{"is_internal":true}}}`, true},
		{`{"conversations":{"main":{"is_internal":false}}}`, false},
		{`{"conversations":{"other":{"is_internal":true}}}`, false},
		{`{"conversations":{"main":{"is_internal":"true"}}}`, false},
		{`{}`, false},
	} {
		antigravityTitleCache(t, tc.input)
		e, err := Convert("antigravity", "host", "Stop", []byte(`{"conversationId":"main","terminationReason":"model_stop"}`), time.Now())
		if err != nil || (e == nil) != tc.filtered {
			t.Fatal("只有明确且匹配ID的内部标记才否决普通会话", e, err)
		}
	}
}
