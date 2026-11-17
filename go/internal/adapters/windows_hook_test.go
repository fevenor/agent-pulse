package adapters

import (
	"strings"
	"testing"
)

func TestEncodedWindowsHookOwnershipRejectsExtraCode(t *testing.T) {
	command := windowsProcessCommand(`D:\中文 空格\bridge.exe`, "dispatch", "--config", `D:\data\bridge.yaml`, "--adapter", "codex", "--hook", "Stop")
	exe, cfg, id, hook, ok := windowsHookParts(command)
	if !ok || exe != `D:\中文 空格\bridge.exe` || cfg != `D:\data\bridge.yaml` || id != "codex" || hook != "Stop" {
		t.Fatal("编码命令不能恢复固定参数")
	}
	for _, bad := range []string{command + " & echo extra", strings.Replace(command, "cmd.exe", "other.exe", 1), windowsProcessCommand(`D:\bridge.exe`, "dispatch", "--config", "other", "--adapter", "codex", "--hook", "Stop", "extra")} {
		if _, _, _, _, ok := windowsHookParts(bad); ok {
			t.Fatal("不能认领追加代码或其他命令")
		}
	}
}
