//go:build !windows

package localipc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

func DefaultAddress(source string) (string, error) {
	// 固定用户运行目录不依赖HOME、TMPDIR或systemd环境，且保持套接字路径足够短。
	base := filepath.Join("/tmp", fmt.Sprintf("agent-pulse-%d", os.Getuid()))
	if !filepath.IsAbs(base) {
		return "", errors.New("本机运行目录必须为绝对路径")
	}
	hash := sha256.Sum256([]byte(source))
	address := "unix:" + filepath.Join(base, fmt.Sprintf("bridge-%x.sock", hash[:12]))
	return address, Validate(address)
}
func validateAddress(address string) error { _, err := unixPath(address); return err }
func dial(ctx context.Context, address string) (net.Conn, error) {
	p, err := unixPath(address)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", p)
}

// 私有目录和进程锁保证不接管其他用户的端点，也不在并发启动时误删活跃套接字。
func Listen(address string) (net.Listener, error) {
	p, err := unixPath(address)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(p)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("Unix套接字目录必须由当前用户拥有，且仅当前用户可访问（权限700）")
	}
	fd, err := unix.Open(p+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), p+".lock")
	locked := false
	defer func() {
		if !locked {
			_ = lock.Close()
		}
	}()
	lockInfo, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	owner, ok := lockInfo.Sys().(*syscall.Stat_t)
	if !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 || !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, errors.New("本机IPC锁文件的类型或权限无效")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.New("同一本机接入已运行，不能重复启动")
	}
	if old, statErr := os.Lstat(p); statErr == nil {
		owner, ok := old.Sys().(*syscall.Stat_t)
		if old.Mode()&os.ModeSocket == 0 || !ok || owner.Uid != uint32(os.Getuid()) {
			return nil, errors.New("IPC地址已有非本用户套接字，拒绝覆盖")
		}
		conn, dialErr := net.DialTimeout("unix", p, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("IPC地址已有运行中的接收器，拒绝接管")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, errors.New("无法确认旧IPC端点已停止，未清理")
		}
		current, checkErr := os.Lstat(p)
		if checkErr != nil || !os.SameFile(old, current) {
			return nil, errors.New("IPC端点在检查时发生变化，未清理")
		}
		if err = os.Remove(p); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(p, 0600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	locked = true
	return &lockedListener{Listener: ln, lock: lock}, nil
}

type lockedListener struct {
	net.Listener
	lock *os.File
	once sync.Once
	err  error
}

func (l *lockedListener) Close() error {
	l.once.Do(func() { l.err = l.Listener.Close(); _ = unix.Flock(int(l.lock.Fd()), unix.LOCK_UN); _ = l.lock.Close() })
	return l.err
}
