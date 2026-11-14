//go:build !linux && !windows

package processmode

import "fmt"

func processInfo(pid int) (int, string, error) {
	return 0, "", fmt.Errorf("当前平台无启动信息查询")
}
func processArguments(pid int) ([]string, error) {
	return nil, fmt.Errorf("当前平台无启动信息查询")
}
