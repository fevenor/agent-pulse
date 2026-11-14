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

const qoderTestSessionID = "11111111-1111-4111-8111-111111111111"

func writeQoderTitleFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func qoderTitleRow(kind, id, title string) string {
	field := "customTitle"
	if kind == "ai-title" {
		field = "aiTitle"
	}
	row, _ := json.Marshal(map[string]string{"type": kind, "sessionId": id, field: title})
	return string(row) + "\n"
}

func TestQoderSessionTitleMetadata(t *testing.T) {
	custom := qoderTitleRow("custom-title", qoderTestSessionID, "用户名称")
	auto := qoderTitleRow("ai-title", qoderTestSessionID, "原生自动标题")
	prefix := `{"type":"custom-title","sessionId":"` + qoderTestSessionID + `"`
	cases := []struct{ name, contents, want string }{
		{"用户改名", custom, "用户名称"},
		{"原生自动标题", auto, "原生自动标题"},
		{"用户名称优先", custom + auto, "用户名称"},
		{"连续改名取最后一条", custom + qoderTitleRow("custom-title", qoderTestSessionID, "最新名称 ✅"), "最新名称 ✅"},
		{"自动标题也取最后一条", auto + qoderTitleRow("ai-title", qoderTestSessionID, "最新自动标题"), "最新自动标题"},
		{"其他会话不串入", custom + qoderTitleRow("custom-title", "other-session", "别的会话"), "用户名称"},
		{"会话标识区分大小写", qoderTitleRow("custom-title", "SESSION-A", "别的会话"), ""},
		{"空元数据", "", ""},
		{"原生字段区分大小写", `{"Type":"custom-title","SessionId":"` + qoderTestSessionID + `","CustomTitle":"非原生键名"}`, ""},
		{"名称字段区分大小写", prefix + `,"CustomTitle":"非原生键名"}`, ""},
		{"空值不覆盖", custom + prefix + `,"customTitle":null}`, "用户名称"},
		{"摘要不是名称", `{"type":"summary","sessionId":"` + qoderTestSessionID + `","summary":"不是名称","customTitle":"不能猜测"}`, ""},
		{"正文不是名称", `{"type":"user","sessionId":"` + qoderTestSessionID + `","message":{"content":"不是名称"},"title":"猜测字段"}`, ""},
		{"最近提示词不是名称", `{"type":"last-prompt","sessionId":"` + qoderTestSessionID + `","lastPrompt":"不是名称"}`, ""},
		{"未知类型不冒充标题", qoderTitleRow("session-title", qoderTestSessionID, "未知字段"), ""},
		{"无会话标识不接入", qoderTitleRow("custom-title", "", "无主名称"), ""},
		{"缺失名称不覆盖", custom + prefix + `}`, "用户名称"},
		{"错误名称类型不覆盖", custom + prefix + `,"customTitle":2}`, "用户名称"},
		{"损坏行不覆盖", custom + "{损坏行}\n", "用户名称"},
		{"未完成写入不覆盖", custom + prefix + `,"customTitle":"半行`, "用户名称"},
		{"空用户标题回退原生自动标题", custom + auto + qoderTitleRow("custom-title", qoderTestSessionID, "  "), "原生自动标题"},
		{"无效UTF8不传播", custom + prefix + `,"customTitle":"` + string([]byte{255}) + `"}`, "用户名称"},
		{"裁剪首尾空白", qoderTitleRow("custom-title", qoderTestSessionID, "  用户名称  "), "用户名称"},
		{"末行无换行", strings.TrimSuffix(custom, "\n"), "用户名称"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), qoderTestSessionID+".jsonl")
			writeQoderTitleFixture(t, path, tc.contents)
			if got := qoderSessionTitle(qoderTestSessionID, "", path); got != tc.want {
				t.Fatalf("名称 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestQoderSessionTitleFileSelection(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("QODERCN_CONFIG_DIR", root)
	t.Setenv("QODERCN_CONFIG_DIR_NAME", "")
	t.Setenv("QODERCN_CLI_HOME", "")
	path := filepath.Join(root, "projects", qoderProjectIdentifier(cwd), qoderTestSessionID+".jsonl")
	writeQoderTitleFixture(t, path, qoderTitleRow("custom-title", qoderTestSessionID, "默认位置"))
	if got := qoderSessionTitle(qoderTestSessionID, cwd, ""); got != "默认位置" {
		t.Fatalf("默认位置 = %q", got)
	}
	actual := filepath.Join(t.TempDir(), qoderTestSessionID+".jsonl")
	writeQoderTitleFixture(t, actual, qoderTitleRow("custom-title", qoderTestSessionID, "事件实际路径"))
	if got := qoderSessionTitle(qoderTestSessionID, cwd, actual); got != "事件实际路径" {
		t.Fatalf("实际路径优先 = %q", got)
	}
	for _, invalid := range []string{"other-session.jsonl", filepath.Join(t.TempDir(), "other-session.jsonl"), filepath.Dir(actual)} {
		if got := qoderSessionTitle(qoderTestSessionID, cwd, invalid); got != "" {
			t.Fatalf("不匹配路径不得回退：%q => %q", invalid, got)
		}
	}
	for _, id := range []string{"", "../session", "session..other", "/session", "session\\other"} {
		if got := qoderSessionTitle(id, cwd, ""); got != "" {
			t.Fatalf("非法标识不得读取：%q => %q", id, got)
		}
	}
	if got := qoderSessionTitle(qoderTestSessionID, "relative", ""); got != "" {
		t.Fatalf("相对目录不得猜测：%q", got)
	}
	missing := filepath.Join(t.TempDir(), qoderTestSessionID+".jsonl")
	writeQoderTitleFixture(t, filepath.Join(filepath.Dir(missing), qoderTestSessionID, "state.json"), `{"data":{"title":"不支持的猜测"}}`)
	if got := qoderSessionTitle(qoderTestSessionID, cwd, missing); got != "" {
		t.Fatalf("不读取 state.json：%q", got)
	}
	if err := os.Mkdir(missing, 0700); err != nil {
		t.Fatal(err)
	}
	if got := qoderSessionTitle(qoderTestSessionID, cwd, missing); got != "" {
		t.Fatalf("不读取目录：%q", got)
	}
}

func TestQoderSessionTitleConfigRoot(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("QODERCN_CONFIG_DIR", "")
	t.Setenv("QODERCN_CONFIG_DIR_NAME", "")
	t.Setenv("QODERCN_CLI_HOME", "")
	check := func(root, name string) {
		t.Helper()
		path := filepath.Join(root, "projects", qoderProjectIdentifier(cwd), qoderTestSessionID+".jsonl")
		writeQoderTitleFixture(t, path, qoderTitleRow("custom-title", qoderTestSessionID, name))
		if got := qoderSessionTitle(qoderTestSessionID, cwd, ""); got != name {
			t.Fatalf("根目录名称 = %q，期望 %q", got, name)
		}
	}
	check(filepath.Join(home, ".qoder-cn"), "默认主目录")
	t.Setenv("QODERCN_CONFIG_DIR_NAME", "qoder-test")
	check(filepath.Join(home, "qoder-test"), "自定义目录名")
	other := t.TempDir()
	t.Setenv("QODERCN_CLI_HOME", other)
	check(filepath.Join(other, "qoder-test"), "宿主主目录覆盖")
	t.Setenv("QODERCN_CONFIG_DIR_NAME", "../escape")
	if got := qoderSessionTitle(qoderTestSessionID, cwd, ""); got != "" {
		t.Fatalf("目录名不得越界：%q", got)
	}
	t.Setenv("QODERCN_CONFIG_DIR", "relative")
	if got := qoderSessionTitle(qoderTestSessionID, cwd, ""); got != "" {
		t.Fatalf("不猜测相对配置根目录：%q", got)
	}
}

func TestQoderSessionTitleBoundedRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), qoderTestSessionID+".jsonl")
	old := qoderTitleRow("custom-title", qoderTestSessionID, "旧名称")
	latest := qoderTitleRow("custom-title", qoderTestSessionID, "最新名称")
	body := `{"type":"user","message":{"content":"` + strings.Repeat("正文不是名称", 40000) + `"}}` + "\n"
	writeQoderTitleFixture(t, path, old+body+latest)
	if got := qoderSessionTitle(qoderTestSessionID, "", path); got != "最新名称" {
		t.Fatalf("大文件尾部名称 = %q", got)
	}
	writeQoderTitleFixture(t, path, body+latest+body)
	if got := qoderSessionTitle(qoderTestSessionID, "", path); got != "" {
		t.Fatalf("窗口无标题应返回空：%q", got)
	}
	writeQoderTitleFixture(t, path, qoderTitleRow("custom-title", qoderTestSessionID, strings.Repeat("汉", 500)))
	got := qoderSessionTitle(qoderTestSessionID, "", path)
	if len(got) > 1024 || !utf8.ValidString(got) || got != strings.Repeat("汉", 341) {
		t.Fatalf("UTF-8 长度边界有误：%d", len(got))
	}
}

func TestQoderProjectIdentifier(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/tmp/project", "-tmp-project"},
		{"/tmp/中文😀", "-tmp-----"},
		{`C:\Users\tester\repo`, "C--Users-tester-repo"},
		{strings.Repeat("a", 201), strings.Repeat("a", 200) + "-x54x4s"},
	}
	for _, tc := range cases {
		if got := qoderProjectIdentifier(tc.cwd); got != tc.want {
			t.Fatalf("目录编码 %q = %q，期望 %q", tc.cwd, got, tc.want)
		}
	}
}

func TestQoderHookRefreshesTitleOnNextRealEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), qoderTestSessionID+".jsonl")
	writeQoderTitleFixture(t, path, qoderTitleRow("custom-title", qoderTestSessionID, "原名称"))
	raw, _ := json.Marshal(map[string]string{"session_id": qoderTestSessionID, "cwd": "/项目", "transcript_path": path})
	first, err := Convert("qoderclicn", "host", "SessionStart", raw, time.Now())
	if err != nil || first == nil || first.SessionTitle != "原名称" {
		t.Fatal("真实事件转换应读取 Qoder 原生名称", first, err)
	}
	writeQoderTitleFixture(t, path, qoderTitleRow("custom-title", qoderTestSessionID, "原名称")+qoderTitleRow("custom-title", qoderTestSessionID, "最新用户名称"))
	latest, err := Convert("qoderclicn", "host", "Stop", raw, time.Now())
	if err != nil || latest == nil || latest.SessionID != first.SessionID || latest.SessionTitle != "最新用户名称" || latest.Kind != "turn.completed" || latest.Body != "" {
		t.Fatal("下次真实事件应刷新名称而非创建新会话或上传正文", latest, err)
	}
}
