// 本地推送接收器用于验证真实 HTTP 发送链路，不连接手机渠道。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
)

func main() {
	address := flag.String("listen", "127.0.0.1:18703", "仅回环监听")
	flag.Parse()
	if err := config.Loopback(*address); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	type received struct {
		Method string    `json:"method"`
		Title  string    `json:"title"`
		Body   string    `json:"body"`
		At     time.Time `json:"at"`
	}
	var mu sync.Mutex
	messages := []received{}
	mux := http.NewServeMux()
	mux.HandleFunc("/push", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		message := received{Method: r.Method, At: time.Now().UTC(), Title: r.URL.Query().Get("title"), Body: r.URL.Query().Get("body")}
		if r.Method == "POST" {
			if r.Header.Get("Content-Type") == "application/json" {
				var fields map[string]string
				raw, err := io.ReadAll(r.Body)
				if err != nil || json.Unmarshal(raw, &fields) != nil {
					w.WriteHeader(400)
					return
				}
				message.Title = fields["title"]
				message.Body = fields["body"]
			} else {
				if r.ParseForm() != nil {
					w.WriteHeader(400)
					return
				}
				message.Title = r.Form.Get("title")
				message.Body = r.Form.Get("body")
			}
		}
		mu.Lock()
		if len(messages) >= 1000 {
			messages = messages[1:]
		}
		messages = append(messages, message)
		mu.Unlock()
		transport.JSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		transport.JSON(w, 200, messages)
	})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fmt.Fprintln(os.Stderr, "本地推送测试接收器：http://"+*address+"/messages")
	if err := transport.Serve(ctx, *address, transport.Guard("", true, mux)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
