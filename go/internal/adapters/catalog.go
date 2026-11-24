package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
)

func publisherKey(c config.Bridge) (string, error) {
	key := strings.TrimSpace(c.Adapters.PublicKey)
	if key == "" {
		raw, err := os.ReadFile(filepath.Join(c.Adapters.Directory, "publisher.pub"))
		if err != nil {
			return "", fmt.Errorf("发布公钥未配置：%w", err)
		}
		key = strings.TrimSpace(string(raw))
	}
	return key, nil
}
func lockSync(dir string) (func(), error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("适配目录未配置")
	}
	directory := filepath.Join(dir, "sync")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	return lockInstall(directory)
}

// RefreshCatalog 只检查清单，不触碰宿主；最小间隔只限制网络清单检查。
// force 用于每次启动或用户强制同步，不绕过验签和序号检查。
func RefreshCatalog(ctx context.Context, c config.Bridge, force bool) error {
	if c.Adapters.Directory == "" {
		return nil
	}
	unlock, err := lockSync(c.Adapters.Directory)
	if err != nil {
		return errors.New("适配目录正在同步")
	}
	defer unlock()
	return refreshCatalog(ctx, c, force)
}
func cachedIndex(c config.Bridge) (Index, error) {
	key, err := publisherKey(c)
	if err != nil {
		return Index{}, err
	}
	raw, err := os.ReadFile(filepath.Join(c.Adapters.Directory, "index.cached.json"))
	if err != nil {
		return Index{}, err
	}
	index, err := Verify(raw, key)
	if err != nil {
		return Index{}, err
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		return Index{}, err
	}
	sum := sha256.Sum256(raw)
	if index.Sequence < state.Sequence || index.Sequence == state.Sequence && state.IndexHash != "" && state.IndexHash != hex.EncodeToString(sum[:]) {
		return Index{}, errors.New("本地索引回退或同序号内容变化")
	}
	return index, nil
}
func refreshCatalog(ctx context.Context, c config.Bridge, force bool) error {
	dir := c.Adapters.Directory
	state, err := loadState(dir)
	if err != nil {
		return err
	}
	key, err := publisherKey(c)
	if err != nil {
		return err
	}
	cached, _ := os.ReadFile(filepath.Join(dir, "index.cached.json"))
	var attempt struct {
		At      time.Time `json:"at"`
		HubURL  string    `json:"hub_url"`
		KeyHash string    `json:"key_hash"`
		Success time.Time `json:"success,omitempty"`
	}
	path := filepath.Join(dir, "check.json")
	if raw, e := os.ReadFile(path); e == nil {
		_ = json.Unmarshal(raw, &attempt)
	}
	keyDigest := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(keyDigest[:])
	interval := c.Adapters.MinCheckSeconds
	if interval == 0 {
		interval = 300
	}
	if !force && attempt.HubURL == c.HubURL && attempt.KeyHash == keyHash && !attempt.At.IsZero() && !attempt.At.After(time.Now()) && time.Since(attempt.At) < time.Duration(interval)*time.Second {
		if _, e := cachedIndex(c); e == nil {
			return nil
		}
		return errors.New("清单检查处于最小间隔，暂时没有可信本地清单；可使用 --force-sync")
	}
	attempt.At = time.Now().UTC()
	attempt.HubURL = c.HubURL
	attempt.KeyHash = keyHash
	if err := AtomicJSON(path, attempt); err != nil {
		return err
	}
	headers := map[string]string{}
	// 只用已经通过验签和序号检查的缓存请求304。
	if _, e := cachedIndex(c); e == nil {
		sum := sha256.Sum256(cached)
		headers["If-None-Match"] = `"` + hex.EncodeToString(sum[:]) + `"`
	}
	raw, status, err := get(ctx, transport.Client(10*time.Second), c.HubURL, c.Token, "/api/v1/adapters/index", headers, 1<<20)
	if err != nil && c.RelayURL != "" {
		raw, status, err = get(ctx, transport.Client(10*time.Second), c.RelayURL, c.RelayToken, "/api/v1/adapters/index", headers, 1<<20)
	}
	if err != nil {
		return err
	}
	if status == http.StatusNotModified {
		if _, e := cachedIndex(c); e != nil {
			return errors.New("304缺少可信索引缓存")
		}
		raw = cached
	}
	index, err := Verify(raw, key)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if index.Sequence < state.Sequence || index.Sequence == state.Sequence && state.IndexHash != "" && state.IndexHash != hash {
		return errors.New("发布索引回退或同序号内容发生变化")
	}
	if previous, e := Verify(cached, key); e == nil {
		known := map[string]Package{}
		for _, p := range previous.Packages {
			known[p.ID+"/"+p.Version+"/"+p.Platform] = p
		}
		for _, p := range index.Packages {
			if old, ok := known[p.ID+"/"+p.Version+"/"+p.Platform]; ok && !reflect.DeepEqual(old, p) {
				return errors.New("同一版本包内容不可修改")
			}
		}
	}
	if err = AtomicFile(filepath.Join(dir, "index.cached.json"), raw, 0600); err != nil {
		return err
	}
	if index.Sequence > state.Sequence {
		state.Rejected = map[string]string{}
	}
	state.Sequence = index.Sequence
	state.IndexHash = hash
	if err = AtomicJSON(filepath.Join(dir, "state.json"), state); err != nil {
		return err
	}
	attempt.Success = time.Now().UTC()
	return AtomicJSON(path, attempt)
}

// preparePackage 使用可信清单下载缺少的包；同摘要不重复下载，缺入口时从本地ZIP修复。
// 下载前先判定本机是否支持该运行方式：签名索引可能携带本机 Bridge 不认识的新形态，只拒绝本机安装。
func preparePackage(ctx context.Context, c config.Bridge, p Package) error {
	if err := p.Manifest.ValidateRuntime(); err != nil {
		return err
	}
	target := packageDir(c.Adapters.Directory, p)
	entry, err := os.Stat(filepath.Join(target, filepath.FromSlash(p.Entry)))
	controlOK := true
	if p.Control != "" {
		control, e := os.Stat(filepath.Join(target, filepath.FromSlash(p.Control)))
		controlOK = e == nil && control.Mode().IsRegular()
	}
	if err == nil && entry.Mode().IsRegular() && controlOK {
		return nil
	}
	data, err := download(ctx, c, p)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(c.Adapters.Directory, ".install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = Extract(data, staging, p.Manifest); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	// 内容寻址目录只属于本软件；缺失入口的目录以已校验ZIP修复。
	if _, e := os.Stat(target); e == nil {
		if err = os.RemoveAll(target); err != nil {
			return err
		}
	}
	return os.Rename(staging, target)
}
