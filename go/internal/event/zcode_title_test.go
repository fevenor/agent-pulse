package event

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-pulse/internal/sqlitepath"
)

func TestZCodeReadsNativeNameByIDAndKeepsUserDatabaseReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqlitepath.URI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY,title TEXT,title_source TEXT,parent_id TEXT); INSERT INTO session VALUES('main','原生名称🙂','custom',NULL),('child','子会话','generated','main'),('default','默认占位','default',NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, name string
		internal bool
	}{{"main", "原生名称🙂", false}, {"child", "", true}, {"default", "", false}, {"missing", "", false}, {"main' OR 1=1--", "", false}} {
		name, internal := zcodeSessionMetadata(tc.id)
		if name != tc.name || internal != tc.internal {
			t.Fatal(tc.id, name, internal)
		}
	}
	_, err = db.Exec(`UPDATE session SET title='改名后的原生名称' WHERE id='main'`)
	if err != nil {
		t.Fatal(err)
	}
	event, err := Convert("zcode", "source", "UserPromptSubmit", []byte(`{"session_id":"main"}`), time.Now())
	if err != nil || event == nil || event.SessionTitle != "改名后的原生名称" || event.SessionID != "main" {
		t.Fatal(event, err)
	}
	child, err := Convert("zcode", "source", "Stop", []byte(`{"session_id":"child"}`), time.Now())
	if err != nil || child != nil {
		t.Fatal("明确父会话不得成为用户提醒", child, err)
	}
	var title string
	if db.QueryRow(`SELECT title FROM session WHERE id='main'`).Scan(&title) != nil || title != "改名后的原生名称" {
		t.Fatal("读取改变了宿主数据")
	}
}

func TestZCodeMissingDatabaseDoesNotCreateFilesOrBlockLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	event, err := Convert("zcode", "source", "Stop", []byte(`{"session_id":"unavailable"}`), time.Now())
	if err != nil || event == nil || event.SessionTitle != "" {
		t.Fatal(event, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatal("元数据缺失时不得创建宿主目录")
	}
}
