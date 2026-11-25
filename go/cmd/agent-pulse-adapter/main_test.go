package main

import (
	"agent-pulse/protocol"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectAntigravityCommandConvertsWithoutBindings(t *testing.T) {
	oldArgs, oldID, oldIn, oldOut := os.Args, adapterID, os.Stdin, os.Stdout
	t.Cleanup(func() { os.Args, adapterID, os.Stdin, os.Stdout = oldArgs, oldID, oldIn, oldOut })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	adapterID = "antigravity"
	in, err := os.Create(filepath.Join(t.TempDir(), "hook.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = in.WriteString(`{"conversationId":"direct-main","terminationReason":"model_stop"}`)
	_, _ = in.Seek(0, 0)
	out, err := os.Create(filepath.Join(t.TempDir(), "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	os.Stdin, os.Stdout = in, out
	os.Args = []string{"adapter", "--hook", "Stop", "--source", "isolated-host"}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	_, _ = out.Seek(0, 0)
	var result protocol.AdapterResult
	if err = json.NewDecoder(out).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.SessionID != "direct-main" || result.Reply["decision"] != "stop" {
		t.Fatal("普通启动不应过滤，原生被动回复不变", result)
	}
}

func TestAntigravityDiagnosticOptInKeepsReplyAndOmitsContent(t *testing.T) {
	oldArgs, oldID, oldIn, oldOut := os.Args, adapterID, os.Stdin, os.Stdout
	t.Cleanup(func() { os.Args, adapterID, os.Stdin, os.Stdout = oldArgs, oldID, oldIn, oldOut })
	directory := t.TempDir()
	t.Setenv("AGENT_PULSE_IDENTITY_DIAGNOSTICS", directory)
	adapterID = "antigravity"
	in, err := os.Create(filepath.Join(t.TempDir(), "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = in.WriteString(`{"conversationId":"child","parentConversationId":"main","lastUserInput":"不可记录提示词","finalModelOutput":"不可记录正文","terminationReason":"NO_TOOL_CALL","fullyIdle":true}`)
	_, _ = in.Seek(0, 0)
	out, err := os.Create(filepath.Join(t.TempDir(), "output.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	os.Stdin, os.Stdout = in, out
	os.Args = []string{"adapter", "--hook", "Stop", "--source", "test"}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	_, _ = out.Seek(0, 0)
	var result protocol.AdapterResult
	if err = json.NewDecoder(out).Decode(&result); err != nil || result.Event != nil || result.Reply["decision"] != "stop" {
		t.Fatal(result, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic map[string]any
	if err = json.Unmarshal(raw, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic["decision"] != "filtered_or_ignored" || strings.Contains(string(raw), "不可记录") {
		t.Fatal("诊断改变决定或泄露正文", string(raw))
	}
}

func TestCodexDirectHooksCarryNativeTurnToLocalBridge(t *testing.T) {
	oldArgs, oldID, oldIn, oldOut := os.Args, adapterID, os.Stdin, os.Stdout
	t.Cleanup(func() { os.Args, adapterID, os.Stdin, os.Stdout = oldArgs, oldID, oldIn, oldOut })
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_SQLITE_HOME", t.TempDir())
	adapterID = "codex"
	for _, tc := range []struct{ hook, tool, kind string }{
		{"UserPromptSubmit", "", "session.resumed"},
		{"PostToolUse", "exec_command", "activity.observed"},
		{"PermissionRequest", "exec_command", "approval.required"},
		{"PreToolUse", "request_user_input", "input.required"},
		{"Interrupt", "", "run.interrupted"},
	} {
		t.Run(tc.hook, func(t *testing.T) {
			in, err := os.Create(filepath.Join(t.TempDir(), "hook.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			if err = json.NewEncoder(in).Encode(map[string]string{"session_id": "main", "turn_id": "native-turn", "tool_name": tc.tool}); err != nil {
				t.Fatal(err)
			}
			_, _ = in.Seek(0, 0)
			out, err := os.Create(filepath.Join(t.TempDir(), "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			os.Stdin, os.Stdout = in, out
			os.Args = []string{"adapter", "--hook", tc.hook, "--source", "isolated-host"}
			if err = run(); err != nil {
				t.Fatal(err)
			}
			_, _ = out.Seek(0, 0)
			var result protocol.AdapterResult
			if err = json.NewDecoder(out).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.Event == nil || result.Event.Kind != tc.kind || result.TurnID != "native-turn" {
				t.Fatalf("钩子丢失本机轮次：%+v", result)
			}
		})
	}
}
