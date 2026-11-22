package adapters

import (
	"context"
	"errors"
	"time"
)

// 下载/探测不占用此锁；hook只在短提交窗口等待，崩溃日志恢复后才选定版本。
func dispatchState(ctx context.Context, dir string) (State, error) {
	if dir == "" {
		return State{}, errors.New("适配目录未配置")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		unlock, err := lockInstall(dir)
		if err == nil {
			err = recoverInstall(dir)
			var state State
			if err == nil {
				state, err = loadState(dir)
			}
			unlock()
			return state, err
		}
		select {
		case <-ctx.Done():
			return State{}, errors.New("适配提交尚未完成")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
