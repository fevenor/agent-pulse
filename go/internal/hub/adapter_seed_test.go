package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/config"
)

func TestImageSeedOnlyInitializesOnceAndRetainsKey(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	active := filepath.Join(root, "active")
	source := filepath.Join(root, "source")
	os.MkdirAll(source, 0700)
	os.WriteFile(filepath.Join(source, "adapter"), []byte("fixture"), 0700)
	private := filepath.Join(root, "private")
	os.MkdirAll(seed, 0700)
	if err := adapters.GenerateKey(private, filepath.Join(seed, "publisher.pub")); err != nil {
		t.Fatal(err)
	}
	p, err := adapters.BuildPackage(source, seed, adapters.Manifest{ID: "seed", Version: "1.0.0", Format: 1, Protocol: 1, Platform: "linux/amd64", Kind: "executable", ConfigStyle: "hooks", Entry: "adapter", Command: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = adapters.SignIndex(seed, private, adapters.Index{Sequence: 1, Packages: []adapters.Package{p}}); err != nil {
		t.Fatal(err)
	}
	c := config.Hub{AdapterDirectory: active, AdapterSeedDirectory: seed}
	if err = seedAdapterRepository(c); err != nil {
		t.Fatal(err)
	}
	oldKey, _ := os.ReadFile(filepath.Join(active, "publisher.pub"))
	raw, _ := os.ReadFile(filepath.Join(active, "index.json"))
	if err = adapters.SignIndex(seed, private, adapters.Index{Sequence: 2, Packages: []adapters.Package{p}}); err != nil {
		t.Fatal(err)
	}
	if err = seedAdapterRepository(c); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(filepath.Join(active, "index.json"))
	if string(raw) != string(unchanged) {
		t.Fatal("镜像种子覆盖持久仓库")
	}
	if err = UpdateAdapters(context.Background(), c, seed); err != nil {
		t.Fatal(err)
	}
	nextKey, _ := os.ReadFile(filepath.Join(active, "publisher.pub"))
	if string(oldKey) != string(nextKey) {
		t.Fatal("更新替换已固定公钥")
	}
	index, err := adapters.VerifyRepository(active, string(oldKey))
	if err != nil || index.Sequence != 2 {
		t.Fatal(index, err)
	}
}
