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
	"agent-pulse/internal/clientconfig"
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
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
		printHelp(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	switch os.Args[1] {
	case "setup":
		path := fs.String("config", defaultConfigPath(), "Bridge YAML 配置路径")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		return adapters.SetupInteractive(context.Background(), *path, os.Stdin, os.Stdout)
	case "run-adapter":
		path := fs.String("config", defaultConfigPath(), "Bridge 配置")
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
		reply := map[string]any{}
		defer func() { _ = json.NewEncoder(os.Stdout).Encode(reply) }()
		path := fs.String("config", defaultConfigPath(), "Bridge 配置")
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
		input, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxHookBytes+1))
		if err != nil || len(input) > protocol.MaxHookBytes {
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
		path := fs.String("config", defaultConfigPath(), "Bridge 配置")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		c, err := config.LoadBridge(*path)
		if err != nil {
			return err
		}
		resolved, err := clientconfig.Resolve(context.Background(), c, *path)
		if err != nil {
			slog.Warn("Hub 客户端配置读取失败，将使用本地可用配置", "原因", err)
		}
		return adapters.Sync(context.Background(), resolved, *path)
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
		path := fs.String("config", defaultConfigPath(), "YAML 配置文件路径")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		c, err := config.LoadBridge(*path)
		if err != nil {
			return err
		}
		absolutePath, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		resolved, resolveErr := clientconfig.Resolve(context.Background(), c, absolutePath)
		if resolveErr != nil {
			slog.Warn("Hub 客户端配置读取失败，将使用本地可用配置", "原因", resolveErr)
		}
		c = resolved
		b := bridge.New(c)
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
		printHelp(os.Stderr)
		return fmt.Errorf("未知命令：%s", os.Args[1])
	}
}
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "bridge.yaml"
	}
	return filepath.Join(home, ".config", "agent-pulse", "bridge.yaml")
}
func printHelp(w io.Writer) {
	fmt.Fprintln(w, `AgentPulse Bridge：将本机 Agent 事件发送到 Hub。

用法：
  agent-pulse-bridge setup [--config 路径]       扫描已安装 Agent 并交互配置钩子
  agent-pulse-bridge serve [--config 路径]       启动 Bridge 服务
  agent-pulse-bridge sync-adapters [--config 路径] 手动检查并更新适配包
  agent-pulse-bridge run-adapter --adapter ID [--config 路径] [参数...] 启动适配器
  agent-pulse-bridge emit [--address host:port]   从标准输入发送事件 JSON

setup 会逐个询问是否启用或禁用支持的 Agent，只修改所选 Agent 配置。
完成后按提示重启或重新加载 Agent；Codex 可能还需要额外确认项目和命令信任。
Relay 地址、令牌和适配包发布公钥默认从 Hub 的认证接口获取；首次直连指定 Hub 获取的发布公钥会按 Hub 地址与主机指纹固定保存；公钥发生变化时需人工核实。也可在 YAML 手动填写 adapters.public_key 覆盖公钥来源。
源代码构建安装：bash scripts/install-bridge.sh
发布版安装：bash scripts/install-bridge-release.sh（需提供版本、二进制 URL 和 SHA-256）。`)
}
