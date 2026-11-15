package adapters

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackageIgnoresBuildTimeButPreservesContentIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "原目录")
	write(t, filepath.Join(source, "index.js"), "export default 1;\n")
	manifest := Manifest{ID: "sample", Version: "0.3.3", Format: 1, Protocol: 1, Platform: "any", Kind: "opencode-plugin", Entry: "index.js", Command: "opencode"}
	build := func(dir string) Package {
		t.Helper()
		p, err := BuildPackage(dir, filepath.Join(root, "repository"), manifest)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := build(source)
	later := time.Date(2030, time.July, 9, 8, 7, 6, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(source, "index.js"), later, later); err != nil {
		t.Fatal(err)
	}
	second := build(source)
	if first.SHA256 != second.SHA256 || first.Size != second.Size {
		t.Fatal("仅修改时间导致包身份改变")
	}
	copied := filepath.Join(root, "另一个目录")
	write(t, filepath.Join(copied, "index.js"), "export default 1;\n")
	if third := build(copied); third.SHA256 != first.SHA256 {
		t.Fatal("源根目录不应进入包身份")
	}
	z, err := zip.OpenReader(filepath.Join(root, "repository", "packages", first.SHA256+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if len(z.File) != 1 || !z.File[0].Modified.Equal(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("包成员未采用固定时间")
	}
	write(t, filepath.Join(source, "index.js"), "export default 2;\n")
	if changed := build(source); changed.SHA256 == first.SHA256 {
		t.Fatal("实际内容变化必须改变包身份")
	}
}
