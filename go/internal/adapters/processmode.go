package adapters

import (
	"agent-pulse/internal/config"
	"agent-pulse/internal/processmode"
)

// FromACP 在 Bridge 共用入口检查所有已安装 Agent；新来源只需现有清单的 command。
func FromACP(c config.Bridge, pid int) bool {
	if pid <= 0 || c.Adapters.Directory == "" {
		return false
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		return false
	}
	commands := []string{}
	for _, installed := range state.Installed {
		commands = append(commands, installed.Package.Command)
	}
	return processmode.FromACP(pid, commands)
}
