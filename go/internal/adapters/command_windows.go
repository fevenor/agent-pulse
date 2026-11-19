package adapters

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// 保留规范化的归属前缀；生成命令时显式选择Windows执行器。
func hookCommandPrefix(exe, cfg, id string) string {
	return quoteCMD(normalizeHookPath(exe)) + " dispatch --config " + quoteCMD(normalizeHookPath(cfg)) + " --adapter " + quoteCMD(id) + " --hook "
}
func quoteCMD(s string) string { return "\"" + s + "\"" }
func hookCommand(prefix, event string) string {
	direct := prefix + quoteCMD(event)
	exe, cfg, id, hook, ok := windowsHookParts(direct)
	if !ok {
		return direct
	}
	return windowsProcessCommand(exe, "dispatch", "--config", cfg, "--adapter", id, "--hook", hook)
}
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[2*i:], u)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
func hostCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat", ".ps1":
		script := "[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false); & " + quote(path)
		for _, arg := range args {
			script += " " + quote(arg)
		}
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(script))
	default:
		cmd = exec.CommandContext(ctx, path, args...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// Rust的路径规范化会产生Windows加长路径；这与普通盘符/UNC路径指向同一文件。
func normalizeHookPath(path string) string {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + path[len(`\\?\UNC\`):]
	}
	if strings.HasPrefix(path, `\\?\`) && len(path) >= 7 && path[5] == ':' && path[6] == '\\' {
		return path[4:]
	}
	return path
}

func ownsHookCommand(command, prefix string) bool {
	exe, cfg, id, _, ok := windowsHookParts(command)
	wantedExe, wantedCfg, wantedID, _, expected := windowsHookParts(prefix + quoteCMD("Stop"))
	if !ok || !expected {
		return false
	}
	// 内置Bridge随工程版本更换目录；以同一配置、Agent及程序名确认归属，避免升级后重复执行。
	return id == wantedID &&
		strings.EqualFold(filepath.Base(normalizeHookPath(exe)), filepath.Base(normalizeHookPath(wantedExe))) &&
		sameHookConfigFile(normalizeHookPath(cfg), normalizeHookPath(wantedCfg))
}

// 命令字符串宿主会展开百分号/延迟变量；不允许把这类路径误解释成其他程序。
func validateHookPaths(exe, cfg string) error {
	if strings.ContainsAny(exe+cfg, "%!\r\n\x00") {
		return errors.New("宿主命令hook路径不能含百分号、感叹号或控制字符；请使用普通便携目录")
	}
	return nil
}
