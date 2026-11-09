package adapters

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// 内核在进程退出后释放锁，崩溃不会留下阻止恢复的过期锁。
func lockInstall(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
