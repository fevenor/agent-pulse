package adapters

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const powershellCommand = "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand "
const powershellInput = "$OutputEncoding = [Console]::InputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false); $payload = [Console]::In.ReadToEnd(); $payload | & "

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// 外层命令只有固定开关和 Base64，避免 cmd、PowerShell 或 Bash 再次解释路径。
func hookCommand(prefix, event string) string {
	return powershellCommand + encodePowerShell(powershellInput+prefix+quote(event))
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
func ownsHookCommand(command, prefix string) bool {
	if !strings.HasPrefix(command, powershellCommand) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, powershellCommand))
	if err != nil || len(raw)%2 != 0 {
		return false
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	return strings.HasPrefix(string(utf16.Decode(units)), powershellInput+prefix)
}
