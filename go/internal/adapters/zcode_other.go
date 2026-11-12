//go:build !windows

package adapters

import "errors"

func zcodeDesktopVersion() (string, error) {
	return "", errors.New("ZCode 桌面接入当前仅验证 Windows 版本")
}
