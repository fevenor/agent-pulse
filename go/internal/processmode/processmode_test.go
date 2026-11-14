package processmode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 假宿主仅生成真实父子进程，用来核验启动方式，不调用 Agent 或模型。
func TestMain(m *testing.M) {
	switch os.Getenv("PULSE_MODE_TEST_STAGE") {
	case "host":
		c := exec.Command(os.Getenv("PULSE_MODE_TEST_BINARY"))
		c.Env = append(os.Environ(), "PULSE_MODE_TEST_STAGE=child")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if c.Run() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "child":
		fmt.Print(FromACP(os.Getpid(), []string{"test-agent"}))
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestACPCommandMatchesOnlyLaunchPrefix(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"agy", "acp", "--stdio"}, true},
		{[]string{"qoder", "--acp"}, true},
		{[]string{"opencode", "--acp=true"}, true},
		{[]string{"agy"}, false},
		{[]string{"agy", "--print", "acp"}, false},
		{[]string{"agy", "--print", "请执行 acp"}, false},
		{[]string{"agy", "exec", "acp"}, false},
	} {
		if got := acpArguments(tc.args); got != tc.want {
			t.Fatal(tc.args, got)
		}
	}
}
func TestRealProcessAncestryFiltersACPWithoutWrapperOrBindings(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("本机启动信息查询只交付 Linux/Windows")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	host := filepath.Join(dir, "test-agent")
	if runtime.GOOS == "windows" {
		host += ".exe"
	}
	if err = os.WriteFile(host, raw, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{{nil, "false"}, {[]string{"acp", "--stdio"}, "true"}, {[]string{"--acp"}, "true"}, {[]string{"--print", "acp"}, "false"}} {
		c := exec.Command(host, tc.args...)
		c.Env = append(os.Environ(), "PULSE_MODE_TEST_STAGE=host", "PULSE_MODE_TEST_BINARY="+binary, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := c.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != tc.want {
			t.Fatal("启动方式不需要包装器或绑定", tc.args, string(output), err)
		}
	}
}
