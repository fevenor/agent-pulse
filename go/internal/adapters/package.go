// Package adapters 管理独立适配包。签名只授权本项目固定的包与安装格式。
package adapters

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const BridgeVersion = "0.5.0"
const MaxPackageBytes = 32 << 20

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Manifest struct {
	ID          string            `json:"id"`
	Version     string            `json:"version"`
	Format      int               `json:"format"`
	Protocol    int               `json:"protocol"`
	Platform    string            `json:"platform"`
	Kind        string            `json:"kind"`
	Entry       string            `json:"entry"`
	Command     string            `json:"command"`
	AgentMin    string            `json:"agent_min"`
	AgentMax    string            `json:"agent_max"`
	Hooks       map[string]string `json:"hooks,omitempty"`
	ConfigStyle string            `json:"config_style,omitempty"`
	DirectHooks []string          `json:"direct_hooks,omitempty"`
}
type Package struct {
	Manifest
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Index struct {
	Sequence uint64    `json:"sequence"`
	Packages []Package `json:"packages"`
}
type SignedIndex struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func (m Manifest) Validate() error {
	if !identifier.MatchString(m.ID) || !identifier.MatchString(m.Version) || m.Format != 1 || m.Protocol != 1 {
		return errors.New("适配包标识或协议版本无效")
	}
	if m.Kind != "executable" && m.Kind != "opencode-plugin" {
		return errors.New("不支持的适配包运行方式")
	}
	if m.Kind == "executable" && m.ConfigStyle != "hooks" && m.ConfigStyle != "named-hooks" && m.ConfigStyle != "process-hooks" {
		return errors.New("不支持的宿主配置格式")
	}
	for _, value := range []string{m.Version, m.AgentMin, m.AgentMax} {
		if value != "" {
			if _, ok := version(value); !ok {
				return errors.New("包与兼容版本必须为数字版本号")
			}
		}
	}
	if m.AgentMin != "" && m.AgentMax != "" && compare(m.AgentMin, m.AgentMax) > 0 {
		return errors.New("宿主兼容版本下限不得高于上限")
	}
	if !fs.ValidPath(m.Entry) || m.Entry == "." || strings.Contains(m.Entry, "\\") || filepath.IsAbs(m.Entry) {
		return errors.New("入口路径无效")
	}
	if m.Platform == "" || m.Command == "" || strings.ContainsAny(m.Command, "/\\ \t\n") {
		return errors.New("适配包平台或探测命令缺失")
	}
	for event := range m.Hooks {
		if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`).MatchString(event) {
			return errors.New("hook 事件名无效")
		}
	}
	return nil
}

func Verify(raw []byte, publicKey string) (Index, error) {
	var index Index
	var signed SignedIndex
	if len(raw) > 1<<20 || json.Unmarshal(raw, &signed) != nil {
		return index, errors.New("索引格式无效")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return index, errors.New("发布公钥无效")
	}
	payload, err := base64.StdEncoding.DecodeString(signed.Payload)
	if err != nil {
		return index, errors.New("索引载荷无效")
	}
	sig, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil || !ed25519.Verify(key, payload, sig) {
		return index, errors.New("发布索引签名错误")
	}
	if json.Unmarshal(payload, &index) != nil || index.Sequence == 0 || len(index.Packages) > 128 {
		return index, errors.New("索引序号或内容无效")
	}
	seen := map[string]bool{}
	for _, p := range index.Packages {
		if err = p.Manifest.Validate(); err != nil {
			return index, err
		}
		key := p.ID + "/" + p.Version + "/" + p.Platform
		if seen[key] || !hashPattern.MatchString(p.SHA256) || p.Size < 1 || p.Size > MaxPackageBytes {
			return index, errors.New("包重复或摘要/大小无效")
		}
		seen[key] = true
	}
	return index, nil
}

func AtomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return AtomicFile(path, append(data, '\n'), 0600)
}
func AtomicFile(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pulse-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func GenerateKey(privatePath, publicPath string) error {
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		return errors.New("私钥路径已存在，不覆盖")
	}
	if _, err := os.Stat(publicPath); !os.IsNotExist(err) {
		return errors.New("公钥路径已存在，不覆盖")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err = AtomicFile(privatePath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0600); err != nil {
		return err
	}
	return AtomicFile(publicPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0644)
}

func BuildPackage(source, repository string, m Manifest) (Package, error) {
	p := Package{Manifest: m}
	if err := m.Validate(); err != nil {
		return p, err
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	total := int64(0)
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("包内不允许链接：%s", rel)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("包内只允许普通文件")
		}
		total += info.Size()
		if total > MaxPackageBytes {
			return errors.New("解压后包超过 32 MiB")
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		out, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		_ = in.Close()
		return err
	})
	if err != nil {
		return p, err
	}
	if err = writer.Close(); err != nil {
		return p, err
	}
	if _, err = os.Stat(filepath.Join(source, filepath.FromSlash(m.Entry))); err != nil {
		return p, errors.New("包入口不存在")
	}
	sum := sha256.Sum256(buf.Bytes())
	p.SHA256 = hex.EncodeToString(sum[:])
	p.Size = int64(buf.Len())
	return p, AtomicFile(filepath.Join(repository, "packages", p.SHA256+".zip"), buf.Bytes(), 0644)
}

func SignIndex(repository, privatePath string, index Index) error {
	keyRaw, err := os.ReadFile(privatePath)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyRaw)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return errors.New("私钥无效")
	}
	payload, err := json.Marshal(index)
	if err != nil {
		return err
	}
	signed := SignedIndex{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))}
	if _, err = Verify(transportJSON(signed), base64.StdEncoding.EncodeToString(ed25519.PrivateKey(key).Public().(ed25519.PublicKey))); err != nil {
		return err
	}
	if err := verifyRepositoryPackages(repository, index); err != nil {
		return err
	}
	return AtomicJSON(filepath.Join(repository, "index.json"), signed)
}

// VerifyRepository 复用客户端验签规则，并验证索引引用的每个包，不修改仓库。
func VerifyRepository(repository, publicKey string) (Index, error) {
	raw, err := os.ReadFile(filepath.Join(repository, "index.json"))
	if err != nil {
		return Index{}, err
	}
	index, err := Verify(raw, publicKey)
	if err != nil {
		return Index{}, err
	}
	return index, verifyRepositoryPackages(repository, index)
}

func verifyRepositoryPackages(repository string, index Index) error {
	for _, p := range index.Packages {
		data, err := os.ReadFile(filepath.Join(repository, "packages", p.SHA256+".zip"))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != p.Size || hex.EncodeToString(sum[:]) != p.SHA256 {
			return errors.New("仓库包大小或摘要不匹配")
		}
	}
	return nil
}
func transportJSON(value any) []byte { data, _ := json.Marshal(value); return data }

func Extract(data []byte, directory string, m Manifest) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	var total uint64
	seen := map[string]bool{}
	if len(r.File) > 4096 {
		return errors.New("包内文件数量过多")
	}
	for _, file := range r.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:") || seen[name] {
			return errors.New("包含不安全或重复路径")
		}
		seen[name] = true
		if file.Mode()&os.ModeSymlink != 0 || (!file.FileInfo().IsDir() && !file.Mode().IsRegular()) {
			return errors.New("包内只允许普通文件和目录")
		}
		total += file.UncompressedSize64
		if total > MaxPackageBytes {
			return errors.New("包解压大小超过上限")
		}
		target := filepath.Join(directory, filepath.FromSlash(name))
		if file.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		content, err := io.ReadAll(io.LimitReader(in, MaxPackageBytes+1))
		_ = in.Close()
		if err != nil || len(content) > MaxPackageBytes {
			return errors.New("解压失败")
		}
		mode := fs.FileMode(0600)
		if name == m.Entry && m.Kind == "executable" {
			mode = 0700
		}
		if err = os.WriteFile(target, content, mode); err != nil {
			return err
		}
	}
	info, err := os.Stat(filepath.Join(directory, filepath.FromSlash(m.Entry)))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("适配包入口缺失")
	}
	return nil
}
