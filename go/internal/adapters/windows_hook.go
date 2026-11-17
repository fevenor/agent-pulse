package adapters

import (
	"encoding/base64"
	"encoding/binary"
	"regexp"
	"strings"
	"unicode/utf16"
)

const windowsHookLauncher = "cmd.exe /d /s /c powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand "
const windowsHookPreamble = "[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false); & "

// 固定cmd边界避免PowerShell将嵌套PowerShell当作特殊管道；参数编码防二次解释，stdin保留原字节。
func windowsProcessCommand(exe string, args ...string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := windowsHookPreamble + quote(exe)
	for _, arg := range args {
		script += " " + quote(arg)
	}
	script += "; exit $LASTEXITCODE"
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	return windowsHookLauncher + base64.StdEncoding.EncodeToString(raw)
}

var quotedWindowsHook = regexp.MustCompile(`^"([^"\r\n]+)" dispatch --config "([^"\r\n]+)" --adapter "([^"\r\n]+)" --hook "([A-Za-z]+)"$`)
var powershellLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)

// 只解析本软件生成且能逐字重建的命令；不执行、不认领追加脚本或其他参数。
func windowsHookParts(command string) (exe, cfg, id, event string, ok bool) {
	if m := quotedWindowsHook.FindStringSubmatch(command); m != nil {
		return m[1], m[2], m[3], m[4], true
	}
	if !strings.HasPrefix(command, windowsHookLauncher) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, windowsHookLauncher))
	if err != nil || len(raw)%2 != 0 || len(raw) > 32768 {
		return
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	script := string(utf16.Decode(units))
	if !strings.HasPrefix(script, windowsHookPreamble) || !strings.HasSuffix(script, "; exit $LASTEXITCODE") {
		return
	}
	args := []string{}
	for _, part := range powershellLiteral.FindAllString(strings.TrimSuffix(strings.TrimPrefix(script, windowsHookPreamble), "; exit $LASTEXITCODE"), -1) {
		args = append(args, strings.ReplaceAll(part[1:len(part)-1], "''", "'"))
	}
	if len(args) != 8 || args[1] != "dispatch" || args[2] != "--config" || args[4] != "--adapter" || args[6] != "--hook" || windowsProcessCommand(args[0], args[1:]...) != command {
		return
	}
	return args[0], args[3], args[5], args[7], true
}
func ownedWindowsCodex(command, event string) bool {
	exe, _, id, hook, ok := windowsHookParts(command)
	name := strings.ToLower(exe)
	at := strings.LastIndexAny(name, `\/`)
	name = name[at+1:]
	return ok && id == "codex" && hook == event && (name == "bridge.exe" || name == "agent-pulse-bridge.exe")
}
