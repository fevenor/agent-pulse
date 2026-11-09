package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"agent-pulse/internal/config"
	"agent-pulse/internal/hub"
	"agent-pulse/internal/transport"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Hub 启动或运行失败", "原因", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "hub.yaml", "YAML 配置文件路径")
	flag.Parse()
	c, err := config.LoadHub(*path)
	if err != nil {
		return err
	}
	h, err := hub.Open(c)
	if err != nil {
		return err
	}
	defer h.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); h.RunRelay(ctx, c) }()
	slog.Info("Hub 已启动", "listen", c.Listen)
	err = transport.Serve(ctx, c.Listen, h.Handler(c.Token))
	cancel()
	wg.Wait()
	return err
}
