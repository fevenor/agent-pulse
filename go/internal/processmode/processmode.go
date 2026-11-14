// Package processmode 只在本机接入边界识别 ACP 启动；不读取或保存会话正文和进程环境。
package processmode

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// FromACP 共用同一祖先检查，支持命令型 hook、插件及新接入的已安装 Agent。
// 只根据已知 Agent 的实际启动命令肯定过滤；标识缺失不把普通运行阻断。
func FromACP(pid int, commands []string) bool {
	names := map[string]bool{}
	for _, command := range commands {
		if command == "" {
			continue
		}
		names[executableName(command)] = true
		if path, err := exec.LookPath(command); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				names[executableName(resolved)] = true
			}
		}
	}
	if len(names) == 0 {
		return false
	}
	for depth := 0; depth < 32 && pid > 0; depth++ {
		parent, executable, err := processInfo(pid)
		if err != nil {
			return false
		}
		if names[executableName(executable)] {
			args, err := processArguments(pid)
			if err == nil && acpArguments(args) {
				return true
			}
		}
		if parent == pid {
			break
		}
		pid = parent
	}
	return false
}
func executableName(path string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSuffix(path, " (deleted)"))), ".exe")
}

// 匹配真正的子命令/启动开关，不匹配 --print、提示词或其他参数值里的 acp 文本。
func acpArguments(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return args[1] == "acp" || args[1] == "--acp" || args[1] == "--acp=true"
}
