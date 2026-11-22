package adapters

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func repositoryFixture(t *testing.T) (string, string, string, Package) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "repo")
	priv := filepath.Join(root, "key")
	pub := filepath.Join(root, "pub")
	if err := GenerateKey(priv, pub); err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(pub)
	source := filepath.Join(root, "source")
	write(t, filepath.Join(source, "adapter"), "fixture-data")
	p, err := BuildPackage(source, dir, Manifest{ID: "repo-fixture", Name: "仓库测试", Version: "1.0.0", Format: 1, Protocol: 1, Platform: "windows/amd64", Kind: "executable", Entry: "adapter", Command: "fixture", ConfigStyle: "hooks"})
	if err != nil {
		t.Fatal(err)
	}
	if err = SignIndex(dir, priv, Index{Sequence: 1, Packages: []Package{p}}); err != nil {
		t.Fatal(err)
	}
	return dir, priv, string(key), p
}
func directorySource(t *testing.T, dir string) RepositorySource {
	t.Helper()
	source, closeSource, err := OfflineRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeSource)
	return source
}
func TestRepositoryPromotionIsCompleteImmutableAndIncremental(t *testing.T) {
	repo, priv, key, p := repositoryFixture(t)
	target := t.TempDir()
	ctx := context.Background()
	if err := PromoteRepository(ctx, target, key, directorySource(t, repo)); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(target, "index.json"))
	path := filepath.Join(target, "packages", p.SHA256+".zip")
	before, _ := os.Stat(path)
	source := directorySource(t, repo)
	source.Package = func(context.Context, Package) ([]byte, error) { t.Fatal("同内容不应获取包"); return nil, nil }
	if err := PromoteRepository(ctx, target, key, source); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("同包被重复写入")
	}
	modified := p
	modified.SHA256 = fmt.Sprintf("%064d", 1)
	if err := SignIndex(repo, priv, Index{Sequence: 2, Packages: []Package{p}}); err != nil {
		t.Fatal(err)
	}
	next := directorySource(t, repo)
	// 元数据同版本也不能悄悄改写。
	changed := p
	changed.Name = "被修改的名称"
	payload := Index{Sequence: 2, Packages: []Package{changed}}
	if err := SignIndex(repo, priv, payload); err != nil {
		t.Fatal(err)
	}
	if err := PromoteRepository(ctx, target, key, directorySource(t, repo)); err == nil {
		t.Fatal("接受不可变版本变化")
	}
	if got, _ := os.ReadFile(filepath.Join(target, "index.json")); !bytes.Equal(got, old) {
		t.Fatal("失败改变活动索引")
	}
	p2 := p
	p2.Version = "1.0.1"
	if err := SignIndex(repo, priv, Index{Sequence: 3, Packages: []Package{p2}}); err != nil {
		t.Fatal(err)
	}
	if err := PromoteRepository(ctx, target, key, directorySource(t, repo)); err != nil {
		t.Fatal(err)
	}
	if err := PromoteRepository(ctx, target, key, next); err == nil {
		t.Fatal("接受旧索引回放")
	}
	// 同序号不同内容不允许，即便仍由有效私钥签名。
	if err := SignIndex(repo, priv, Index{Sequence: 3, Packages: []Package{p}}); err != nil {
		t.Fatal(err)
	}
	if err := PromoteRepository(ctx, target, key, directorySource(t, repo)); err == nil {
		t.Fatal("接受同序号变化")
	}
	// 缺包与错误摘要不能推广；已缓存旧包保留供在途读取。
	source.Index = old
	source.Index[20] ^= 1
	if err := PromoteRepository(ctx, target, key, source); err == nil {
		t.Fatal("接受坏签名")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("删除旧包")
	}
	_ = modified
}
func TestMissingAndCorruptPackagesNeverPromote(t *testing.T) {
	repo, priv, key, p := repositoryFixture(t)
	target := t.TempDir()
	source := directorySource(t, repo)
	source.Package = func(context.Context, Package) ([]byte, error) { return nil, errors.New("离线缺包") }
	if err := PromoteRepository(context.Background(), target, key, source); err == nil {
		t.Fatal("缺包仍推广")
	}
	if _, err := os.Stat(filepath.Join(target, "index.json")); !os.IsNotExist(err) {
		t.Fatal("半完成索引可见")
	}
	source = directorySource(t, repo)
	source.Package = func(context.Context, Package) ([]byte, error) { return []byte("corrupt"), nil }
	if err := PromoteRepository(context.Background(), target, key, source); err == nil {
		t.Fatal("接受损坏包")
	}
	// 即使摘要被合法签名，不安全的包内路径仍必须被拒绝。
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	f, _ := z.Create("../escape")
	f.Write([]byte("x"))
	z.Close()
	sum := sha256.Sum256(data.Bytes())
	p.SHA256 = hex.EncodeToString(sum[:])
	p.Size = int64(data.Len())
	os.MkdirAll(filepath.Join(repo, "packages"), 0700)
	os.WriteFile(filepath.Join(repo, "packages", p.SHA256+".zip"), data.Bytes(), 0600)
	if err := SignIndex(repo, priv, Index{Sequence: 2, Packages: []Package{p}}); err != nil {
		t.Fatal(err)
	}
	if err := PromoteRepository(context.Background(), target, key, directorySource(t, repo)); err == nil {
		t.Fatal("接受合法签名的不安全解包路径")
	}
}
func TestGitHubAndOfflineUseSameRepository(t *testing.T) {
	repo, _, key, p := repositoryFixture(t)
	var server *httptest.Server
	downloads := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/repo/releases":
			json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v99.0.0", "assets": []any{}}, {"tag_name": "adapters-100", "draft": true}, {"tag_name": "adapters-1", "assets": []map[string]string{{"name": "index.json", "browser_download_url": server.URL + "/index"}, {"name": p.SHA256 + ".zip", "browser_download_url": server.URL + "/package"}}}})
		case "/index":
			http.ServeFile(w, r, filepath.Join(repo, "index.json"))
		case "/package":
			downloads++
			http.ServeFile(w, r, filepath.Join(repo, "packages", p.SHA256+".zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	source, err := GitHubRepository(context.Background(), server.Client(), server.URL, "example/repo")
	if err != nil {
		t.Fatal(err)
	}
	online := t.TempDir()
	offline := t.TempDir()
	if err = PromoteRepository(context.Background(), online, key, source); err != nil {
		t.Fatal(err)
	}
	if err = PromoteRepository(context.Background(), online, key, source); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatalf("重复下载：%d", downloads)
	}
	// 外层ZIP不携带公钥也能导入，但信任必须由管理员另行配置。
	bundle := filepath.Join(t.TempDir(), "offline.zip")
	out, _ := os.Create(bundle)
	archive := zip.NewWriter(out)
	for _, name := range []string{"index.json", "packages/" + p.SHA256 + ".zip"} {
		file, _ := archive.Create(name)
		data, _ := os.ReadFile(filepath.Join(repo, filepath.FromSlash(name)))
		file.Write(data)
	}
	archive.Close()
	out.Close()
	source, closeSource, err := OfflineRepository(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSource()
	if err = PromoteRepository(context.Background(), offline, key, source); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(online, "index.json"))
	b, _ := os.ReadFile(filepath.Join(offline, "index.json"))
	if !bytes.Equal(a, b) {
		t.Fatal("在线离线索引不同")
	}
}
func TestOfflineRejectsPathsAndRepositoryLock(t *testing.T) {
	for _, name := range []string{"../index.json", "packages/../../escape", "absolute/escape", "publisher.key"} {
		path := filepath.Join(t.TempDir(), "bundle.zip")
		f, _ := os.Create(path)
		z := zip.NewWriter(f)
		entry, _ := z.Create(name)
		entry.Write([]byte("x"))
		z.Close()
		f.Close()
		if _, closeSource, err := OfflineRepository(path); err == nil {
			closeSource()
			t.Fatal("接受外层非法路径", name)
		}
	}
	dir := t.TempDir()
	unlock, err := LockRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if second, err := LockRepository(dir); err == nil {
		second()
		t.Fatal("更新锁允许并行推广")
	}
}
