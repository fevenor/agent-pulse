package event

import (
	"agent-pulse/internal/sqlitepath"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// antigravitySessionTitle 随真实hook读取宿主明确持久化的名称，不轮询。
// 用户名称优先读同 ID 的注解文件，避免依赖改名后 JSON 缓存何时刷新。
func antigravitySessionTitle(sessionID string) string {
	name, _ := antigravitySessionMetadata(sessionID)
	return name
}

// 普通运行不要求绑定文件；只用明确 is_internal=true 否决内部会话，false/缺失不作为认主证明。
func antigravitySessionMetadata(sessionID string) (string, bool) {
	return antigravityMetadataAt(sessionID, "")
}

func antigravityMetadataAt(sessionID, transcript string) (string, bool) {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, "/\\\x00\r\n") {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	for _, surface := range []string{"antigravity", "antigravity-cli", "antigravity-ide"} {
		candidate := filepath.Join(home, ".gemini", surface)
		relative, err := filepath.Rel(candidate, transcript)
		if transcript != "" && err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			root = candidate
			break
		}
	}
	cached, internal := antigravityCachedMetadata(filepath.Join(root, "cache", "conversation_metadata.json"), sessionID)
	if internal {
		return "", true
	}
	databaseName, child := antigravityDatabaseMetadata(filepath.Join(root, "conversation_summaries.db"), sessionID)
	if child {
		return "", true
	}
	name := antigravityAnnotationTitle(filepath.Join(root, "annotations", sessionID+".pbtxt"))
	if strings.TrimSpace(name) == "" {
		name = cached
	}
	if strings.TrimSpace(name) == "" {
		name = databaseName
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
		name, err = unquoteAnnotationTitle(quoted)
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

// App将title与最后查看时间写在同一行；只解析第一个原生引号字符串，不读取其余字段。
func unquoteAnnotationTitle(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", strconv.ErrSyntax
	}
	escaped := false
	for i := 1; i < len(raw); i++ {
		if escaped {
			escaped = false
			continue
		}
		if raw[i] == '\\' {
			escaped = true
			continue
		}
		if raw[i] == '"' {
			return strconv.Unquote(raw[:i+1])
		}
	}
	return "", strconv.ErrSyntax
}

// App持久化索引可提供标题及父子身份；只按精确ID取必要元数据，不读preview或全文索引。
func antigravityDatabaseMetadata(path, id string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	db, err := sql.Open("sqlite", sqlitepath.URI(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30)")
	if err != nil {
		return "", false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	var name, parent, source sql.NullString
	var depth sql.NullInt64
	err = db.QueryRowContext(ctx, "SELECT title,parent_conversation_id,nesting_depth,source FROM conversation_summaries WHERE conversation_id=?", id).Scan(&name, &parent, &depth, &source)
	if err != nil {
		return "", false
	}
	if parent.String != "" || depth.Int64 > 0 || source.String == "memory_consolidation" || source.String == "internal" {
		return "", true
	}
	return name.String, false
}
