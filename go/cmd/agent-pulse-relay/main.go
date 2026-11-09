package main

import (
	"agent-pulse/internal/config"
	"agent-pulse/internal/relay"
	"agent-pulse/internal/transport"
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Relay 运行失败", "原因", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "relay.yaml", "YAML 配置文件")
	flag.Parse()
	c, err := config.LoadRelay(*path)
	if err != nil {
		return err
	}
	r, err := relay.Open(c)
	if err != nil {
		return err
	}
	defer r.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.Run(ctx) }()
	slog.Info("Relay 已启动", "listen", c.Listen)
	err = transport.Serve(ctx, c.Listen, r.Handler())
	cancel()
	wg.Wait()
	return err
}
