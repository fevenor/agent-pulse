package localipc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testAddress(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		address, err := DefaultAddress(fmt.Sprintf("ipc-test-%d", time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		return address
	}
	dir, err := os.MkdirTemp("", "pulse-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return "unix:" + filepath.Join(dir, "bridge.sock")
}
func TestLocalHTTPAndExclusiveEndpoint(t *testing.T) {
	address := testAddress(t)
	ln, err := Listen(address)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:1" {
			t.Error("本机HTTP Host错误")
		}
		_, _ = w.Write([]byte("本机IPC"))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	duplicate, err := Listen(address)
	if err == nil {
		_ = duplicate.Close()
		t.Fatal("重复接收器接管了现有端点")
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	client, err := Client(address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", BaseURL+"/healthz", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	_ = srv.Close()
	again, err := Listen(address)
	if err != nil {
		t.Fatal("关闭后端点未释放：", err)
	}
	_ = again.Close()
}
func TestLocalAddressRejectsTCPAndRemoteEndpoints(t *testing.T) {
	for _, address := range []string{"127.0.0.1:18702", "http://127.0.0.1:18702", `pipe:\\remote\pipe\agent-pulse`, "unix:relative.sock", "unix:/tmp/a\x00b"} {
		if Validate(address) == nil {
			t.Fatalf("接受了非本机IPC地址：%q", address)
		}
	}
	a, err := DefaultAddress("identity")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DefaultAddress("identity")
	if err != nil || a != b {
		t.Fatal("同一来源端点不稳定")
	}
	t.Setenv("TMPDIR", "/不用于本机端点的临时路径")
	t.Setenv("XDG_RUNTIME_DIR", "/不用于本机端点的运行路径")
	stable, err := DefaultAddress("identity")
	if err != nil || stable != a {
		t.Fatal("环境变量改变了同一来源的默认IPC端点")
	}
	c, err := DefaultAddress("another")
	if err != nil || a == c {
		t.Fatal("不同来源共用了端点")
	}
}
