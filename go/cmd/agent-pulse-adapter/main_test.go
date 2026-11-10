package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"agent-pulse/internal/event"
)

// 测试程序作为假宿主执行，验证真实子进程的参数、绑定和清理，不调用模型。
func TestMain(m *testing.M) {
	if os.Getenv("AGENT_PULSE_TEST_HOST") == "1" {
		binding := os.Getenv("AGENT_PULSE_ANTIGRAVITY_SESSION")
		raw, err := os.ReadFile(binding)
		if err != nil || string(raw) != "resumed-main" || !reflect.DeepEqual(os.Args[1:], []string{"acp", "--stdio"}) {
			os.Exit(2)
		}
		for _, id := range []string{"resumed-main", "child"} {
			e, err := event.Convert("antigravity", "test-host", "PreInvocation", []byte(`{"conversationId":"`+id+`"}`), time.Now(), binding)
			if err != nil || (e != nil) != (id == "resumed-main") {
				os.Exit(3)
			}
		}
		if os.WriteFile(os.Getenv("AGENT_PULSE_TEST_BINDING_PATH"), []byte(binding), 0600) != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLaunchResumedMainSession(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	name := "agy"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err = os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
		t.Fatal(err)
	}
	pathRecord := filepath.Join(dir, "binding-path")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_PULSE_TEST_HOST", "1")
	t.Setenv("AGENT_PULSE_TEST_BINDING_PATH", pathRecord)
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"adapter", "run", "--", "--pulse-main-session", "resumed-main", "acp", "--stdio"}
	if err = launch(); err != nil {
		t.Fatal("续接包装器启动失败", err)
	}
	binding, err := os.ReadFile(pathRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(string(binding))); !os.IsNotExist(err) {
		t.Fatal("宿主退出后未清理临时绑定目录", err)
	}
	for _, args := range [][]string{{"--pulse-main-session"}, {"--pulse-main-session", "", "acp"}, {"--pulse-main-session", "bad\nid", "acp"}, {"--pulse-main-session", "--stdio", "acp"}} {
		os.Args = append([]string{"adapter", "run", "--"}, args...)
		if err = launch(); err == nil {
			t.Fatal("无效续接参数未拒绝", args)
		}
	}
}
