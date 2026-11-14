package event

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var qoderSessionIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// qoderSessionTitle 只在真实事件到达时读取宿主明确保存的标题元数据，不轮询。
// 数据来源已核验于 Qoder 中国版 CLI 1.1.85；不把摘要、提示词或正文当作名称。
func qoderSessionTitle(sessionID, cwd, transcriptPath string) string {
	if !qoderSessionIdentifier.MatchString(sessionID) || strings.Contains(sessionID, "..") {
		return ""
	}
	if transcriptPath != "" {
		// 原生事件给出实际文件时优先使用；路径与会话不匹配时不另找历史文件。
		if !filepath.IsAbs(transcriptPath) || filepath.Base(transcriptPath) != sessionID+".jsonl" {
			return ""
		}
	} else {
		if !filepath.IsAbs(cwd) {
			return ""
		}
		root := strings.TrimSpace(os.Getenv("QODERCN_CONFIG_DIR"))
		if root == "" {
			home := strings.TrimSpace(os.Getenv("QODERCN_CLI_HOME"))
			if home == "" {
				var err error
				home, err = os.UserHomeDir()
				if err != nil {
					return ""
				}
			}
			name := os.Getenv("QODERCN_CONFIG_DIR_NAME")
			if name == "" {
				name = ".qoder-cn"
			}
			if name == "." || name == ".." || strings.ContainsAny(name, `/\`+"\x00") || !filepath.IsAbs(home) {
				return ""
			}
			root = filepath.Join(home, name)
		}
		if !filepath.IsAbs(root) {
			return ""
		}
		transcriptPath = filepath.Join(root, "projects", qoderProjectIdentifier(cwd), sessionID+".jsonl")
	}
	// 先检查类型，避免误开命名管道；不读取同名 state.json 或认证文件。
	info, err := os.Stat(transcriptPath)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	// 与宿主的会话列表读取边界一致：小文件完整读取，大文件只读首尾各 64 KiB。
	// 写入器会把缓存标题补到尾部；窗口没有标题时返回空，不扫描其他会话。
	const window = 64 << 10
	var head, tail []byte
	if info.Size() <= 2*window {
		head, err = io.ReadAll(io.LimitReader(file, info.Size()))
		if err != nil || int64(len(head)) != info.Size() {
			return ""
		}
	} else {
		head = make([]byte, window)
		tail = make([]byte, window)
		if _, err = file.ReadAt(head, 0); err != nil {
			return ""
		}
		if _, err = file.ReadAt(tail, info.Size()-window); err != nil {
			return ""
		}
		// 不解析窗口边缘截断的 JSON 行。
		head = head[:bytes.LastIndexByte(head, '\n')+1]
		if edge := bytes.IndexByte(tail, '\n'); edge >= 0 {
			tail = tail[edge+1:]
		} else {
			tail = nil
		}
	}
	custom, automatic := "", ""
	for _, part := range [][]byte{head, tail} {
		for _, line := range bytes.Split(part, []byte{'\n'}) {
			if !utf8.Valid(line) {
				continue
			}
			// 使用精确键名，避免 Go 结构体解码把 sessionid 等大小写变体当成原生字段。
			var row map[string]json.RawMessage
			var kind, id string
			if json.Unmarshal(line, &row) != nil || json.Unmarshal(row["type"], &kind) != nil ||
				json.Unmarshal(row["sessionId"], &id) != nil || id != sessionID {
				continue
			}
			field := ""
			switch kind {
			case "custom-title":
				field = "customTitle"
			case "ai-title":
				field = "aiTitle"
			default:
				continue
			}
			var title *string
			if json.Unmarshal(row[field], &title) != nil || title == nil {
				continue
			}
			if kind == "custom-title" {
				custom = *title
			} else {
				automatic = *title
			}
		}
	}
	name := strings.TrimSpace(custom)
	if name == "" {
		name = strings.TrimSpace(automatic)
	}
	// 采用 Bridge 现有标题长度边界，不截断 UTF-8 字符。
	if len(name) > 1024 {
		name = name[:1024]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name
}

// qoderProjectIdentifier 对齐宿主 un/Pe 函数，按 JavaScript UTF-16 码元编码目录。
func qoderProjectIdentifier(cwd string) string {
	units := utf16.Encode([]rune(cwd))
	encoded := make([]byte, len(units))
	var hash uint32 = 5381
	for i, unit := range units {
		hash = hash*33 ^ uint32(unit)
		if unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' {
			encoded[i] = byte(unit)
		} else {
			encoded[i] = '-'
		}
	}
	if len(encoded) <= 200 {
		return string(encoded)
	}
	signed := int64(int32(hash))
	if signed < 0 {
		signed = -signed
	}
	return string(encoded[:200]) + "-" + strconv.FormatInt(signed, 36)
}
