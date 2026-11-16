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

// 宿主直接调用预构建EXE，不为hook再套编码PowerShell。
func hookCommandPrefix(exe, cfg, id string) string {
	return quoteCMD(exe) + " dispatch --config " + quoteCMD(cfg) + " --adapter " + quoteCMD(id) + " --hook "
}
func quoteCMD(s string) string                { return "\"" + s + "\"" }
func hookCommand(prefix, event string) string { return prefix + quoteCMD(event) }
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
func ownsHookCommand(command, prefix string) bool { return strings.HasPrefix(command, prefix) }

// 命令字符串宿主会展开百分号/延迟变量；不允许把这类路径误解释成其他程序。
func validateHookPaths(exe, cfg string) error {
	if strings.ContainsAny(exe+cfg, "%!\r\n\x00") {
		return errors.New("宿主命令hook路径不能含百分号、感叹号或控制字符；请使用普通便携目录")
	}
	return nil
}
