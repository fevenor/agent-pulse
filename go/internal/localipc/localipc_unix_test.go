//go:build !windows

package localipc

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixSocketRecoveryAndPrivatePermissions(t *testing.T) {
	address := testAddress(t)
	p := strings.TrimPrefix(address, "unix:")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	ln, err := Listen(address)
	if err != nil {
		t.Fatal("无法恢复自身残留套接字：", err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("套接字权限不是600")
	}
	_ = ln.Close()
	if err = os.WriteFile(p, []byte("不可覆盖"), 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := Listen(address); err == nil {
		_ = other.Close()
		t.Fatal("覆盖了普通文件")
	}
	data, _ := os.ReadFile(p)
	if string(data) != "不可覆盖" {
		t.Fatal("普通文件被改写")
	}
	_ = os.Remove(p)
	if err = os.Chmod(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if other, err := Listen(address); err == nil {
		_ = other.Close()
		t.Fatal("接受了非私有目录")
	}
}
