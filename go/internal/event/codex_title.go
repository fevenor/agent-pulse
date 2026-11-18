package event

import (
	"agent-pulse/internal/sqlitepath"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// Codex 本地标题索引是可选元数据，不是稳定的 hook 契约；只按精确会话标识取标题。
// 不读取 transcript、首条提示词或认证文件，索引不可用时不影响原生事件投递。
func codexSessionTitle(sessionID string) string { return codexSessionTitleAt(sessionID, "") }

func codexSessionTitleAt(sessionID, home string) string {
	name := ""
	if sessionID == "" {
		return name
	}
	const maxIndexBytes = 8 << 20
	if home == "" {
		home = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
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

// 按精确会话或宿主给出的日志路径查询身份，不打开日志正文，不按标题猜记忆任务。
func codexInternalSession(id, transcript string) bool {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		home = filepath.Join(user, ".codex")
	}
	databaseHome := strings.TrimSpace(os.Getenv("CODEX_SQLITE_HOME"))
	if databaseHome == "" {
		databaseHome = home
	}
	file := filepath.Join(databaseHome, "state_5.sqlite")
	if _, err := os.Stat(file); err != nil {
		return false
	}
	db, err := sql.Open("sqlite", sqlitepath.URI(file)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30)")
	if err != nil {
		return false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	var source string
	if transcript != "" {
		_ = db.QueryRowContext(ctx, "SELECT source FROM threads WHERE rollout_path=?", transcript).Scan(&source)
	}
	if source == "" {
		_ = db.QueryRowContext(ctx, "SELECT source FROM threads WHERE id=?", id).Scan(&source)
	}
	if strings.HasPrefix(source, "internal_") || strings.HasPrefix(source, "subagent_") {
		return true
	}
	var origin map[string]json.RawMessage
	if json.Unmarshal([]byte(source), &origin) == nil {
		if len(origin["subagent"]) > 0 || len(origin["internal"]) > 0 {
			return true
		}
	}
	var threadSource string
	_ = db.QueryRowContext(ctx, "SELECT thread_source FROM threads WHERE id=?", id).Scan(&threadSource)
	return threadSource == "memory_consolidation" || threadSource == "subagent" || threadSource == "guardian_review"
}

// 记忆整理线程不持久化；只认宿主专用目录，不按项目名或标题猜测。
func codexMemoryWorkspace(cwd string) bool {
	if cwd == "" {
		return false
	}
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		home = filepath.Join(user, ".codex")
	}
	canonical := func(path string) string {
		// 宿主可给出Windows加长路径；先规范命名空间，再处理WSL盘符表示。
		if strings.HasPrefix(path, `\\?\UNC\`) {
			path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
		} else {
			path = strings.TrimPrefix(path, `\\?\`)
		}
		if runtime.GOOS == "linux" && os.Getenv("WSL_DISTRO_NAME") != "" && len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
			path = "/mnt/" + strings.ToLower(path[:1]) + "/" + strings.ReplaceAll(path[3:], "\\", "/")
		}
		if !filepath.IsAbs(path) {
			return ""
		}
		path = filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return path
	}
	actual := canonical(cwd)
	if actual == "" {
		return false
	}
	// WSL中的Windows盘符目录沿用Windows大小写比较，普通Linux目录仍精确比较。
	caseFold := runtime.GOOS == "windows" || runtime.GOOS == "linux" && os.Getenv("WSL_DISTRO_NAME") != "" && len(actual) > 6 && strings.HasPrefix(actual, "/mnt/") && actual[6] == '/' && (actual[5] >= 'a' && actual[5] <= 'z' || actual[5] >= 'A' && actual[5] <= 'Z')
	for _, name := range []string{"memories", "memories_v2"} {
		expected := canonical(filepath.Join(home, name))
		if expected != "" && (actual == expected || caseFold && strings.EqualFold(actual, expected)) {
			return true
		}
	}
	return false
}
