package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

func TestMain(m *testing.M) {
	if os.Getenv("PULSE_BRIDGE_FAKE_HOST") == "1" {
		e := protocol.Event{EventID: "mode-event", SourceID: "mode-source", Agent: "opencode", SessionID: "mode-session", Kind: "turn.completed", OccurredAt: time.Now().UTC(), Title: "完成", Scope: "main"}
		raw, _ := json.Marshal(protocol.LocalEvent{Event: e, ProcessID: os.Getpid()})
		r, err := http.Post(os.Getenv("PULSE_BRIDGE_TEST_URL")+"/api/v1/events", "application/json", bytes.NewReader(raw))
		if err != nil {
			os.Exit(2)
		}
		defer r.Body.Close()
		var receipt protocol.Receipt
		if json.NewDecoder(r.Body).Decode(&receipt) != nil || r.StatusCode != 202 {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString(receipt.Accepted)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestSharedBridgeIngressFiltersACPAndStripsLocalProcessMetadata(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("当前交付平台")
	}
	dir := t.TempDir()
	state := []byte(`{"installed":{"sample":{"package":{"command":"sample-agent"}}},"rejected":{}}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "sample-agent")
	if runtime.GOOS == "windows" {
		host += ".exe"
	}
	if err = os.WriteFile(host, raw, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args     []string
		accepted string
		queued   int
	}{{[]string{"acp", "--stdio"}, "filtered", 0}, {nil, "memory", 1}, {[]string{"--print", "acp"}, "memory", 1}} {
		cfg := config.Bridge{SourceID: "mode-source", QueueEvents: 10, QueueBytes: 1 << 20}
		cfg.Adapters.Directory = dir
		b := New(cfg)
		server := httptest.NewServer(b.Handler())
		c := exec.Command(host, tc.args...)
		c.Env = append(os.Environ(), "PULSE_BRIDGE_FAKE_HOST=1", "PULSE_BRIDGE_TEST_URL="+server.URL, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := c.CombinedOutput()
		server.Close()
		if err != nil || string(output) != tc.accepted || len(b.queue) != tc.queued {
			t.Fatal("过滤必须作用于共用入口，普通启动不能阻断", tc.args, string(output), err, len(b.queue))
		}
		if len(b.queue) > 0 && bytes.Contains(b.queue[0].payload, []byte("process_id")) {
			t.Fatal("本地进程信息不应转发Hub或进入持久数据")
		}
	}
}
