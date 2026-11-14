package event

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Codex 本地标题索引是可选元数据，不是稳定的 hook 契约；只按精确会话标识取标题。
// 不读取 transcript、首条提示词或认证文件，索引不可用时不影响原生事件投递。
func codexSessionTitle(sessionID string) string {
	name := ""
	if sessionID == "" {
		return name
	}
	const maxIndexBytes = 8 << 20
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(userHome, ".codex")
	}
	path := filepath.Join(home, "session_index.jsonl")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxIndexBytes {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxIndexBytes {
		return ""
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maxIndexBytes+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var row struct {
			ID   string  `json:"id"`
			Name *string `json:"thread_name"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.ID != sessionID || row.Name == nil {
			continue
		}
		candidate := strings.TrimSpace(*row.Name)
		if !utf8.ValidString(candidate) {
			continue
		}
		if len(candidate) > 1024 {
			candidate = candidate[:1024]
			for !utf8.ValidString(candidate) {
				candidate = candidate[:len(candidate)-1]
			}
		}
		// 同一会话有多次缓存更新时使用最后一条有效标题，不修改本地索引。
		name = candidate
	}
	if scanner.Err() != nil {
		return ""
	}
	return name
}
