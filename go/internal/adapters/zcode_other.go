//go:build !windows && !linux

package adapters

import "errors"

func zcodeHostVersion() (string, error) {
	return "", errors.New("ZCode 当前仅支持 Windows 桌面与 Linux Server 接入")
}
