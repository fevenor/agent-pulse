package event

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"agent-pulse/internal/sqlitepath"
	_ "modernc.org/sqlite"
)

// ZCode 原生 session 表保存标题与父会话；仅按精确 ID 只读查询，不读正文、凭据或桌面任务索引。
// 缺库、未知结构、锁竞争及无名称均不阻断生命周期事件，也不据此证明主会话身份。
func zcodeSessionMetadata(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	db, err := sql.Open("sqlite", sqlitepath.URI(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(50)")
	if err != nil {
		return "", false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var name, source string
	var parent sql.NullString
	if db.QueryRowContext(ctx, `SELECT title,title_source,parent_id FROM session WHERE id=?`, id).Scan(&name, &source, &parent) != nil {
		return "", false
	}
	if parent.Valid && parent.String != "" {
		return "", true
	}
	if source == "default" {
		return "", false
	}
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) {
		return "", false
	}
	if len(name) > 1024 {
		name = name[:1024]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name, false
}
