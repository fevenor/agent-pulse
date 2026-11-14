package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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
	fs := flag.NewFlagSet("adapter", flag.ContinueOnError)
	hook := fs.String("hook", "", "原生 hook 名")
	source := fs.String("source", "", "主机 ID")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxHookBytes+1))
	if err != nil {
		return err
	}
	result, err := event.Convert(adapterID, *source, *hook, raw, time.Now())
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
