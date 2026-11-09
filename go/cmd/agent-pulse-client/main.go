// 测试客户端用于 Linux 验证，不承担 Tray 的本地缓存或自动提醒职责。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
	"agent-pulse/protocol"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	base := flag.String("hub", "http://127.0.0.1:18700", "Hub 根地址")
	watch := flag.Bool("watch", false, "订阅变化并输出完整快照")
	read := flag.String("read", "", "明确标记一个事件已读")
	flag.Parse()
	if err := config.HTTPURL(*base); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	token := os.Getenv("AGENT_PULSE_TOKEN")
	client := transport.Client(10 * time.Second)
	if *read != "" {
		_, err := transport.Request(ctx, client, *base, token, http.MethodPost, "/api/v1/read", protocol.ReadRequest{EventIDs: []string{*read}}, nil)
		return err
	}
	var last protocol.Version
	refresh := func(version protocol.Version) error {
		if last.RunID == version.RunID && last.Version >= version.Version {
			return nil
		}
		var s protocol.Snapshot
		_, err := transport.Request(ctx, client, *base, token, http.MethodGet, "/api/v1/snapshot", nil, &s)
		if err != nil {
			return err
		}
		last = s.Version
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	if !*watch {
		return refresh(protocol.Version{RunID: "initial"})
	}
	for ctx.Err() == nil {
		err := transport.Watch(ctx, *base, token, protocol.Device{ID: protocol.NewID(), Role: "client"}, refresh)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintln(os.Stderr, "连接中断，重新同步：", err)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
