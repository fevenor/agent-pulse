//go:build linux || windows

package adapters

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallLockAndReplacement(t *testing.T) {
	dir := t.TempDir()
	release, err := lockInstall(dir)
	if err != nil {
		t.Fatal(err)
	}
	if release2, err := lockInstall(dir); err == nil {
		release2()
		release()
		t.Fatal("并发安装没有被阻止")
	}
	release()
	release, err = lockInstall(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := filepath.Join(dir, "中文 配置.json")
	for _, raw := range [][]byte{[]byte(`{"旧":true}`), []byte(`{"新":true}`)} {
		if err = AtomicFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, raw) {
			t.Fatal("原子替换未保存完整内容", err)
		}
	}
}
