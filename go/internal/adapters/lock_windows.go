package adapters

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

// 锁与句柄同寿命，进程异常退出后由系统释放。
func lockInstall(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped); _ = f.Close() }, nil
}
