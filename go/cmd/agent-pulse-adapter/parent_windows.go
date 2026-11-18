package main

import (
	"errors"
	"golang.org/x/sys/windows"
)

// 缓存父进程句柄，PID复用也不能把已经停止的Bridge当成存活。
func observeParent(pid int) (func() bool, func(), error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, nil, errors.New("观察所属进程不可核对")
	}
	return func() bool {
		state, err := windows.WaitForSingleObject(handle, 0)
		return err == nil && state == uint32(windows.WAIT_TIMEOUT)
	}, func() { windows.CloseHandle(handle) }, nil
}
