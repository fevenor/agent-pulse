//go:build !linux && !windows

package adapters

import "errors"

func lockInstall(dir string) (func(), error) {
	return nil, errors.New("此平台的自动安装尚未验证；请在 Linux 上使用")
}
