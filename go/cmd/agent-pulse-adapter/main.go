package main

import (
	"context"
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
	observe := fs.Bool("observe", false, "观察已登记本轮的原生终态")
	parent := fs.Int("parent", 0, "观察进程所属Bridge的进程号")
	source := fs.String("source", "", "主机 ID")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxHookBytes+1))
	if err != nil {
		return err
	}
	if *observe {
		if adapterID != "codex" {
			return fmt.Errorf("适配器不支持本轮终态观察")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if *parent > 0 {
			alive, closeParent, parentErr := observeParent(*parent)
			if parentErr != nil {
				return parentErr
			}
			defer closeParent()
			go func() {
				ticker := time.NewTicker(500 * time.Millisecond)
				defer ticker.Stop()
				for {
					if !alive() {
						cancel()
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		return event.ObserveCodex(ctx, raw, *source, func(e protocol.Event) error { return json.NewEncoder(os.Stdout).Encode(e) })
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
	output := protocol.AdapterResult{Event: result, Reply: reply}
	if adapterID == "codex" {
		output.Observation = event.CodexObservation(*hook, raw)
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}
