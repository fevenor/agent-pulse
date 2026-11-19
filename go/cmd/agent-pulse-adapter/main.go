package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
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
	fs := flag.NewFlagSet("adapter", flag.ContinueOnError)
	hook := fs.String("hook", "", "原生 hook 名")
	identityCheck := fs.Bool("check-identity", false, "检查待确认会话身份，仅返回main/child/unknown")
	observe := fs.Bool("observe", false, "观察已登记本轮的原生终态")
	parent := fs.Int("parent", 0, "观察进程所属Bridge的进程号")
	source := fs.String("source", "", "主机 ID")
	diagnosticDir := fs.String("identity-diagnostics", os.Getenv("AGENT_PULSE_IDENTITY_DIAGNOSTICS"), "仅Antigravity：将本次身份判断白名单写入已存在的私有诊断目录")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxHookBytes+1))
	if err != nil {
		return err
	}
	if *identityCheck {
		if adapterID != "antigravity" {
			return fmt.Errorf("适配器不支持身份观察")
		}
		identity, err := event.CheckAntigravityIdentity(raw)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"identity": identity})
	}
	if *observe {
		if adapterID != "codex" && adapterID != "antigravity" {
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
		emit := func(e protocol.Event) error { return json.NewEncoder(os.Stdout).Encode(e) }
		if adapterID == "antigravity" {
			return event.ObserveAntigravity(ctx, raw, *source, func(e protocol.LocalEvent) error { return json.NewEncoder(os.Stdout).Encode(e) })
		}
		return event.ObserveCodex(ctx, raw, *source, emit)
	}
	var result *protocol.Event
	var identity json.RawMessage
	var observation *protocol.Observation
	var turnID string
	if adapterID == "antigravity" {
		var diagnostic event.AntigravityDiagnostic
		result, diagnostic, err = event.ConvertAntigravityDiagnostic(*source, *hook, raw, time.Now())
		observation, turnID = event.AntigravityTerminalObservation(*hook, raw, result, diagnostic.Metadata)
		if result != nil && *hook == "Stop" && result.Kind == "activity.observed" && diagnostic.Metadata != nil && diagnostic.Metadata.Identity == "main" && filepath.Base(diagnostic.Metadata.Root) == "antigravity" && observation == nil && turnID == "" {
			result.Kind = "observation.lost"
			result.Title = "antigravity · 本轮状态待确认"
			result.Body = "后台任务尚未结束，但本轮原生空闲状态不可读取"
			diagnostic.Kind = result.Kind
		}

		if result != nil {
			identity = event.AntigravityIdentityCursor(result.SessionID, diagnostic.Metadata)
			if len(identity) > 0 {
				diagnostic.Decision = "pending_identity"
			}
		}
		// 每调用一文件，避免并发追加交错；目录已有256份时停止新增，不覆盖已有诊断或影响hook回复。
		if filepath.IsAbs(*diagnosticDir) {
			if entries, readErr := os.ReadDir(*diagnosticDir); readErr == nil && len(entries) < 256 {
				if file, openErr := os.CreateTemp(*diagnosticDir, "identity-*.json"); openErr == nil {
					_ = json.NewEncoder(file).Encode(diagnostic)
					_ = file.Close()
				}
			}
		}
	} else {
		result, err = event.Convert(adapterID, *source, *hook, raw, time.Now())
	}
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
	output := protocol.AdapterResult{Event: result, Identity: identity, TurnID: turnID, Observation: observation, Reply: reply}
	if adapterID == "codex" {
		output.Observation = event.CodexObservation(*hook, raw)
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}
