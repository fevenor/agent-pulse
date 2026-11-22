package adapters

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// RepositorySource 只负责提供已签名清单和内容寻址的包，不建立新的信任。
type RepositorySource struct {
	Index   []byte
	Package func(context.Context, Package) ([]byte, error)
}

func LockRepository(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return lockInstall(dir)
}

func validatePackageData(data []byte, p Package) error {
	sum := sha256.Sum256(data)
	if int64(len(data)) != p.Size || hex.EncodeToString(sum[:]) != p.SHA256 {
		return errors.New("适配包大小或摘要不匹配")
	}
	return nil
}

// PromoteRepository 先核验并准备所有平台包，最后推广完整索引；旧包不删除，供在途下载。
// 调用方持仓库锁，在线和离线共用同一入口。
func PromoteRepository(ctx context.Context, dir, key string, source RepositorySource) error {
	next, err := Verify(source.Index, key)
	if err != nil {
		return err
	}
	previousRaw, readErr := os.ReadFile(filepath.Join(dir, "index.json"))
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	history := map[string]Package{}
	historyPath := filepath.Join(dir, "package-history.json")
	if raw, e := os.ReadFile(historyPath); e == nil {
		if json.Unmarshal(raw, &history) != nil || history == nil {
			return errors.New("仓库不可变版本记录损坏")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if len(previousRaw) > 0 {
		previous, e := Verify(previousRaw, key)
		if e != nil {
			return fmt.Errorf("当前仓库无法验签：%w", e)
		}
		if next.Sequence < previous.Sequence || next.Sequence == previous.Sequence && !bytes.Equal(previousRaw, source.Index) {
			return errors.New("仓库索引回退或同序号内容变化")
		}
		for _, p := range previous.Packages {
			history[p.ID+"/"+p.Version+"/"+p.Platform] = p
		}
	}
	for _, p := range next.Packages {
		identity := p.ID + "/" + p.Version + "/" + p.Platform
		if old, ok := history[identity]; ok && !reflect.DeepEqual(old, p) {
			return errors.New("同一适配器版本和平台的内容不可修改")
		}
		history[identity] = p
		path := filepath.Join(dir, "packages", p.SHA256+".zip")
		data, e := readBoundedFile(path, p.Size)
		cached := e == nil && validatePackageData(data, p) == nil
		if !cached {
			data, err = source.Package(ctx, p)
			if err != nil {
				return err
			}
		}
		if err = validatePackageData(data, p); err != nil {
			return err
		}
		// 包不执行；复用解包边界检查目录穿越、链接、入口和解压上限。
		staging, e := os.MkdirTemp(dir, ".verify-*")
		if e != nil {
			return e
		}
		e = Extract(data, staging, p.Manifest)
		_ = os.RemoveAll(staging)
		if e != nil {
			return e
		}
		if !cached {
			if err = AtomicFile(path, data, 0644); err != nil {
				return err
			}
		}
	}
	if err = AtomicJSON(historyPath, history); err != nil {
		return err
	}
	if bytes.Equal(previousRaw, source.Index) {
		return nil
	}
	return AtomicFile(filepath.Join(dir, "index.json"), source.Index, 0644)
}

// OfflineRepository 接受目录或完整ZIP包，包内不接受任意路径和链接。
func OfflineRepository(path string) (RepositorySource, func(), error) {
	info, err := os.Stat(path)
	if err != nil {
		return RepositorySource{}, nil, err
	}
	if info.IsDir() {
		raw, e := os.ReadFile(filepath.Join(path, "index.json"))
		return RepositorySource{Index: raw, Package: func(_ context.Context, p Package) ([]byte, error) {
			return readBoundedFile(filepath.Join(path, "packages", p.SHA256+".zip"), p.Size)
		}}, func() {}, e
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return RepositorySource{}, nil, err
	}
	fail := func(e error) (RepositorySource, func(), error) { z.Close(); return RepositorySource{}, nil, e }
	entries := map[string]*zip.File{}
	if len(z.File) > 131 {
		return fail(errors.New("离线包条目过多"))
	}
	for _, file := range z.File {
		name := file.Name
		if name == "packages/" && file.FileInfo().IsDir() {
			continue
		}
		if name != "index.json" && name != "publisher.pub" && !(strings.HasPrefix(name, "packages/") && strings.HasSuffix(name, ".zip") && hashPattern.MatchString(strings.TrimSuffix(strings.TrimPrefix(name, "packages/"), ".zip"))) {
			return fail(errors.New("离线包路径无效"))
		}
		if entries[name] != nil || !file.Mode().IsRegular() {
			return fail(errors.New("离线包包含重复项、链接或特殊文件"))
		}
		entries[name] = file
	}
	read := func(name string, limit int64) ([]byte, error) {
		file := entries[name]
		if file == nil || file.UncompressedSize64 > uint64(limit) {
			return nil, errors.New("离线包文件缺失或过大")
		}
		in, e := file.Open()
		if e != nil {
			return nil, e
		}
		defer in.Close()
		data, e := io.ReadAll(io.LimitReader(in, limit+1))
		if int64(len(data)) > limit {
			return nil, errors.New("离线包文件过大")
		}
		return data, e
	}
	raw, err := read("index.json", 1<<20)
	if err != nil {
		return fail(err)
	}
	return RepositorySource{Index: raw, Package: func(_ context.Context, p Package) ([]byte, error) { return read("packages/"+p.SHA256+".zip", p.Size) }}, func() { _ = z.Close() }, nil
}
func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, errors.New("文件超过上限")
	}
	return raw, err
}

// GitHubRepository 使用独立的 adapters-N 发布，不追随产品 latest 或短期CI产物。
func GitHubRepository(ctx context.Context, client *http.Client, api, repository string) (RepositorySource, error) {
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	type release struct {
		Tag    string  `json:"tag_name"`
		Draft  bool    `json:"draft"`
		Assets []asset `json:"assets"`
	}
	fetch := func(url string, limit int64) ([]byte, error) {
		req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "AgentPulse-adapters")
		resp, e := client.Do(req)
		if e != nil {
			return nil, errors.New("GitHub更新源不可达")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("GitHub更新源状态：%d", resp.StatusCode)
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if int64(len(data)) > limit {
			return nil, errors.New("GitHub响应超过上限")
		}
		return data, e
	}
	// 发布工具要求唯一递增序号；分页避免频繁产品发布遮住低频适配仓库发布。
	var chosen release
	var sequence uint64
	for page := 1; page <= 10; page++ {
		raw, e := fetch(strings.TrimRight(api, "/")+"/repos/"+repository+"/releases?per_page=100&page="+strconv.Itoa(page), 8<<20)
		if e != nil {
			return RepositorySource{}, e
		}
		var releases []release
		if json.Unmarshal(raw, &releases) != nil {
			return RepositorySource{}, errors.New("GitHub发布清单无效")
		}
		for _, r := range releases {
			n, e := strconv.ParseUint(strings.TrimPrefix(r.Tag, "adapters-"), 10, 64)
			if strings.HasPrefix(r.Tag, "adapters-") && e == nil && n > sequence && !r.Draft {
				chosen = r
				sequence = n
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	if sequence == 0 {
		return RepositorySource{}, errors.New("尚未公开签名适配仓库发布")
	}
	assets := map[string]string{}
	for _, a := range chosen.Assets {
		if assets[a.Name] != "" {
			return RepositorySource{}, errors.New("GitHub附件重复")
		}
		assets[a.Name] = a.URL
	}
	if assets["index.json"] == "" {
		return RepositorySource{}, errors.New("发布缺少签名索引")
	}
	raw, err := fetch(assets["index.json"], 1<<20)
	if err != nil {
		return RepositorySource{}, err
	}
	// 标签不作为信任，但必须与签名载荷的发布序号一致。
	var signed SignedIndex
	var index Index
	if json.Unmarshal(raw, &signed) != nil {
		return RepositorySource{}, errors.New("索引格式无效")
	}
	payload, e := base64.StdEncoding.DecodeString(signed.Payload)
	if e != nil || json.Unmarshal(payload, &index) != nil || index.Sequence != sequence {
		return RepositorySource{}, errors.New("发布标签与索引序号不一致")
	}
	return RepositorySource{Index: raw, Package: func(ctx context.Context, p Package) ([]byte, error) {
		if assets[p.SHA256+".zip"] == "" {
			return nil, errors.New("发布缺少平台包附件")
		}
		// 下载仍受整次更新的超时约束。
		return fetch(assets[p.SHA256+".zip"], p.Size)
	}}, nil
}
