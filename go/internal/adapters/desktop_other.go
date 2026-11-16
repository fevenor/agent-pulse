//go:build !windows

package adapters

import (
	"errors"
	"os"
	"path/filepath"
)

func codexDesktopCommand() string { return "" }

// WSL桌面配套执行器有独立版本目录，不能用agy CLI版本冒充。
func antigravityDesktopVersion() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".antigravity-server", "bin")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", err
	}
	best := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := version(entry.Name()); !ok {
			continue
		}
		info, err := os.Stat(filepath.Join(base, entry.Name(), "language_server"))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		if best == "" || compare(entry.Name(), best) > 0 {
			best = entry.Name()
		}
	}
	if best == "" {
		return "", errors.New("未找到Antigravity桌面配套执行器")
	}
	return best, nil
}
