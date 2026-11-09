package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"agent-pulse/internal/event"
	"agent-pulse/protocol"
)

// 构建时注入，产物与 Bridge 分别发布。
var adapterID = ""

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if adapterID == "antigravity" && len(os.Args) > 1 && os.Args[1] == "run" {
		return launch()
	}
	fs := flag.NewFlagSet("adapter", flag.ContinueOnError)
	hook := fs.String("hook", "", "原生 hook 名")
	source := fs.String("source", "", "主机 ID")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxEventBytes+1))
	if err != nil {
		return err
	}
	result, err := event.Convert(adapterID, *source, *hook, raw, time.Now(), os.Getenv("AGENT_PULSE_ANTIGRAVITY_SESSION"))
	if err != nil {
		return err
	}
	reply := map[string]any{}
	if adapterID == "antigravity" {
		if *hook == "PreToolUse" {
			reply["decision"] = "allow"
		}
		if *hook == "Stop" {
			reply["decision"] = "stop"
		}
	}
	return json.NewEncoder(os.Stdout).Encode(protocol.AdapterResult{Event: result, Reply: reply})
}
func launch() error {
	dir, err := os.MkdirTemp("", "agent-pulse-agy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	args := os.Args[2:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	cmd := exec.CommandContext(context.Background(), "agy", args...)
	cmd.Env = append(os.Environ(), "AGENT_PULSE_ANTIGRAVITY_SESSION="+filepath.Join(dir, "main-session"))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
