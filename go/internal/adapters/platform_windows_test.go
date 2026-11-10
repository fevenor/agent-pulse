package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 被复制的测试程序只在明确的测试环境变量下模拟宿主和适配包。
func TestMain(m *testing.M) {
	mode := os.Getenv("AGENT_PULSE_INSTALL_TEST_HELPER")
	if mode != "" {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			fmt.Print("test-agent 1.2.3")
			os.Exit(0)
		}
		if mode == "echo" {
			input, _ := io.ReadAll(os.Stdin)
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"args": os.Args[1:], "stdin": string(input)})
		} else {
			fmt.Print(`{}`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsHookQuotingAndUnicodeInput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "中文 空格 ' & %PULSE% ! $ (目录)")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	own, _ := os.Executable()
	raw, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "回显.exe")
	if err = os.WriteFile(exe, raw, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_PULSE_INSTALL_TEST_HELPER", "echo")
	config := filepath.Join(root, "bridge.local.yaml")
	prefix := quote(exe) + " dispatch --config " + quote(config) + " --adapter 'sample' --hook "
	command := hookCommand(prefix, "Stop")
	if !ownsHookCommand(command, prefix) || ownsHookCommand(command, prefix+"other") {
		t.Fatal("命令所有权识别错误")
	}
	cmd := exec.Command("cmd.exe", "/d", "/s", "/c", command)
	cmd.Stdin = strings.NewReader(`{"text":"中文与 emoji ✅"}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if err = json.Unmarshal(bytes.TrimPrefix(output, []byte{0xef, 0xbb, 0xbf}), &result); err != nil {
		t.Fatalf("输出无效：%v", err)
	}
	if len(result.Args) != 7 || result.Args[2] != config || result.Args[6] != "Stop" {
		t.Fatalf("参数被 shell 改写：%q", result.Args)
	}
	var payload map[string]string
	if json.Unmarshal([]byte(result.Stdin), &payload) != nil || payload["text"] != "中文与 emoji ✅" {
		t.Fatal("stdin 编码或转交错误")
	}
}

func TestWindowsBatchHostVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test-version.cmd"), []byte("@echo off\r\necho test-agent 1.2.3\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	version, err := Detect(context.Background(), Manifest{Command: "test-version"})
	if err != nil || version != "1.2.3" {
		t.Fatal("批处理宿主版本探测失败", version, err)
	}
}
