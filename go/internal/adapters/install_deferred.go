package adapters

import (
	"agent-pulse/internal/config"
	"bytes"
	"errors"
	"os"
	"path/filepath"
)

// 外部环境拥有hook时只切换本机包选择；不创建或改写该环境的文件。
func installPackageOnly(c config.Bridge, p Package, before, after State) error {
	path := c.Adapters.Managed[p.ID]
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	unlock, err := lockInstall(c.Adapters.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, current) {
		return errors.New("外部管理的hook已变化，未切换包")
	}
	installed := after.Installed[p.ID]
	installed.ConfigBefore = raw
	installed.ConfigAfter = raw
	after.Installed[p.ID] = installed
	journal := filepath.Join(c.Adapters.Directory, "pending.json")
	if err = AtomicJSON(journal, installJournal{Path: path, Before: raw, After: raw, Existed: true, State: before}); err != nil {
		return err
	}
	if err = AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), after); err != nil {
		if recovered := recoverInstall(c.Adapters.Directory); recovered != nil {
			return recovered
		}
		return err
	}
	return os.Remove(journal)
}
