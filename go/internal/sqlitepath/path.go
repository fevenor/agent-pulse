// Package sqlitepath 将本地数据库文件路径转换为 SQLite 文件地址。
package sqlitepath

import (
	"net/url"
	"path/filepath"
	"strings"
)

// URI 接收绝对文件路径，保留文件名中的查询符号，并兼容 Windows 盘符。
func URI(path string) string {
	p := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}
