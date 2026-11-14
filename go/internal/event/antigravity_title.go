package event

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// antigravitySessionTitle 随真实 hook 读取默认 CLI 明确持久化的名称，不轮询。
// 用户名称优先读同 ID 的注解文件，避免依赖改名后 JSON 缓存何时刷新。
func antigravitySessionTitle(sessionID string) string {
	name, _ := antigravitySessionMetadata(sessionID)
	return name
}

// 普通运行不要求绑定文件；只用明确 is_internal=true 否决内部会话，false/缺失不作为认主证明。
func antigravitySessionMetadata(sessionID string) (string, bool) {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, "/\\\x00\r\n") {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	cached, internal := antigravityCachedMetadata(filepath.Join(root, "cache", "conversation_metadata.json"), sessionID)
	if internal {
		return "", true
	}
	name := antigravityAnnotationTitle(filepath.Join(root, "annotations", sessionID+".pbtxt"))
	if strings.TrimSpace(name) == "" {
		name = cached
	}
	name = strings.TrimSpace(name)
	if len(name) > 1024 {
		name = name[:1024]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name, false
}

// 只读取宿主序列化器输出的顶层 title 字符串，不实现通用 protobuf 文本解析。
// strconv.Unquote 支持名称中的换行、引号、十六进制和八进制转义；格式变化返回未知。
func antigravityAnnotationTitle(path string) string {
	const maxBytes = 64 << 10
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(raw) > maxBytes || !utf8.Valid(raw) {
		return ""
	}
	name, found := "", false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "title:") {
			continue
		}
		if found {
			return ""
		}
		found = true
		quoted := strings.TrimSpace(strings.TrimPrefix(line, "title:"))
		if !strings.HasPrefix(quoted, `"`) {
			return ""
		}
		name, err = strconv.Unquote(quoted)
		if err != nil || !utf8.ValidString(name) {
			return ""
		}
	}
	return name
}

// JSON 缓存是可选回退；只按精确键及 ID 读取 Title，不把 Preview 或摘要对象当名称。
func antigravityCachedMetadata(path, sessionID string) (string, bool) {
	const maxBytes = 8 << 20
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(raw) > maxBytes || !utf8.Valid(raw) {
		return "", false
	}
	var root, conversations, entry, summary map[string]json.RawMessage
	var id, name string
	var internal bool
	if json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["conversations"], &conversations) != nil || json.Unmarshal(conversations[sessionID], &entry) != nil {
		return "", false
	}
	if json.Unmarshal(entry["is_internal"], &internal) == nil && internal {
		return "", true
	}
	if json.Unmarshal(entry["summary"], &summary) != nil || json.Unmarshal(summary["ID"], &id) != nil || id != sessionID || json.Unmarshal(summary["Title"], &name) != nil {
		return "", false
	}

	return name, false
}
