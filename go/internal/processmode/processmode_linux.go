//go:build linux

package processmode

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processInfo(pid int) (int, string, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(root, "status"))
	if err != nil {
		return 0, "", err
	}
	parent := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			parent, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			break
		}
	}
	executable, err := os.Readlink(filepath.Join(root, "exe"))
	if err != nil {
		return 0, "", err
	}
	return parent, executable, nil
}
func processArguments(pid int) ([]string, error) {
	f, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// 只取程序名与第一个参数，不扫描后续提示词、配置或密钥参数。
	r := bufio.NewReaderSize(io.LimitReader(f, 8192), 4096)
	args := []string{}
	for len(args) < 2 {
		argument, err := r.ReadString(0)
		if len(argument) > 4096 {
			return nil, fmt.Errorf("启动参数前缀过长")
		}
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSuffix(argument, "\x00"))
	}
	return args, nil
}
