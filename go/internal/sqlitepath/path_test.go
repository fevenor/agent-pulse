package sqlitepath

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestFileNamesPersistInRequestedFile(t *testing.T) {
	for _, name := range []string{"database.db", "中文 空格 # %25.db"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			db, err := sql.Open("sqlite", URI(path))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE probe(value TEXT); INSERT INTO probe VALUES ('持久化成功');"); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(path); err != nil || info.Size() == 0 {
				t.Fatalf("目标数据库未写入：%v", err)
			}
			db, err = sql.Open("sqlite", URI(path))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var value string
			if err = db.QueryRow("SELECT value FROM probe").Scan(&value); err != nil || value != "持久化成功" {
				t.Fatalf("重新打开数据库失败：%q，%v", value, err)
			}
		})
	}
}
