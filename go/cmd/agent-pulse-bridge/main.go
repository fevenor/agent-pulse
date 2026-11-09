package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/bridge"
	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Bridge 操作失败", "原因", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("用法：agent-pulse-bridge serve --config bridge.yaml 或 emit --address 127.0.0.1:18702")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	switch os.Args[1] {
	case "run-adapter":
		path := fs.String("config", "bridge.yaml", "Bridge 配置")
		id := fs.String("adapter", "", "适配包 ID")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		c, err := config.LoadBridge(*path)
		if err != nil {
			return err
		}
		return adapters.Launch(context.Background(), c, *id, fs.Args())
	case "dispatch":
		// hook 必须被动返回，通知故障不能拦截宿主操作或批准权限。
		reply := map[string]any{}
		defer func() { _ = json.NewEncoder(os.Stdout).Encode(reply) }()
		path := fs.String("config", "bridge.yaml", "Bridge 配置")
		id := fs.String("adapter", "", "适配器 ID")
		hook := fs.String("hook", "", "原生事件名")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return nil
		}
		c, err := config.LoadBridge(*path)
		if err != nil {
			slog.Warn("hook 未递交：配置不可用")
			return nil
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxEventBytes+1))
		if err != nil || len(input) > protocol.MaxEventBytes {
			slog.Warn("hook 未递交：输入超限")
			return nil
		}
		output, err := adapters.Dispatch(context.Background(), c, *id, *hook, input)
		if err == nil {
			var result protocol.AdapterResult
			if err = json.Unmarshal(output, &result); err == nil {
				if len(result.Reply) == 1 {
					if decision, ok := result.Reply["decision"].(string); ok && (decision == "allow" || decision == "stop") {
						reply = result.Reply
					}
				}
				if result.Event != nil {
					if err = result.Event.Validate(); err == nil {
						err = bridge.Emit(context.Background(), c.Listen, *result.Event)
					}
				}
			}
		}
		if err != nil {
			slog.Warn("hook 未递交，请检查 Bridge 或适配包")
		}
		return nil
	case "sync-adapters":
		path := fs.String("config", "bridge.yaml", "Bridge 配置")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		c, err := config.LoadBridge(*path)
		if err != nil {
			return err
		}
		return adapters.Sync(context.Background(), c, *path)
	case "emit":
		address := fs.String("address", "127.0.0.1:18702", "本地 Bridge 地址")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxEventBytes+1))
		if err != nil {
			return err
		}
		if len(data) > protocol.MaxEventBytes {
			return fmt.Errorf("事件超过 64 KiB")
		}
		var e protocol.Event
		if err = json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("stdin 必须为事件 JSON")
		}
		return bridge.Emit(context.Background(), *address, e)
	case "serve":
		path := fs.String("config", "bridge.yaml", "YAML 配置文件路径")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		c, err := config.LoadBridge(*path)
		if err != nil {
			return err
		}
		b := bridge.New(c)
		absolutePath, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); b.Run(ctx) }()
		go func() { defer wg.Done(); adapters.Run(ctx, c, absolutePath) }()
		slog.Info("Bridge 已启动", "listen", c.Listen, "source_id", c.SourceID)
		err = transport.Serve(ctx, c.Listen, b.Handler())
		cancel()
		wg.Wait()
		return err
	default:
		return fmt.Errorf("仅支持 serve、emit、dispatch 和 sync-adapters")
	}
}
