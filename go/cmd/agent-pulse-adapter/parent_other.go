//go:build !windows

package main

import "os"

// 观察器是Bridge直接启动的子进程；父进程退出后会被重新托管，及时退出而不成为孤儿。
func observeParent(pid int) (func() bool, func(), error) {
	return func() bool { return os.Getppid() == pid }, func() {}, nil
}
