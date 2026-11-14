package main

import (
	"agent-pulse/protocol"
	"encoding/json"
	"os"
	"path/filepath"
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
