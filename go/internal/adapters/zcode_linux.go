package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// WSL 与 SSH 都运行 Linux Server；只读取 current.json 选中的安装，不扫描历史版本或启动宿主。
func zcodeHostVersion() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return zcodeServerVersion(home, runtime.GOARCH)
}

func zcodeServerVersion(home, arch string) (string, error) {
	targets := map[string]string{"amd64": "linux-x64", "arm64": "linux-arm64"}
	target := targets[arch]
	if target == "" {
		return "", errors.New("ZCode Server 的当前 CPU 架构未支持")
	}
	root := filepath.Join(home, ".zcode", "server")
	var current struct {
		Version    string `json:"version"`
		AppVersion string `json:"appVersion"`
		ReleaseDir string `json:"releaseDir"`
		Target     string `json:"target"`
	}
	if err := readZCodeServerJSON(filepath.Join(root, "current.json"), &current); err != nil {
		return "", errors.New("未找到有效的 ZCode Server 当前安装记录")
	}
	releases := filepath.Join(root, "releases")
	if !filepath.IsAbs(current.ReleaseDir) || filepath.Dir(filepath.Clean(current.ReleaseDir)) != releases {
		return "", errors.New("ZCode Server 当前版本目录不在用户安装目录内")
	}
	if _, ok := version(current.Version); !ok || current.AppVersion != current.Version || current.Target != target {
		return "", errors.New("ZCode Server 当前版本或平台信息无效")
	}
	var manifest struct {
		Product    string `json:"product"`
		AppVersion string `json:"appVersion"`
		Target     string `json:"target"`
	}
	if err := readZCodeServerJSON(filepath.Join(current.ReleaseDir, "manifest.json"), &manifest); err != nil ||
		manifest.Product != "zcode-server" || manifest.AppVersion != current.Version || manifest.Target != target {
		return "", errors.New("ZCode Server 当前安装与产物清单不一致")
	}
	for _, name := range []string{"server-cli.js", "server-core.js", "zcode.cjs", "node"} {
		info, err := os.Stat(filepath.Join(current.ReleaseDir, "runtime", name))
		if err != nil || !info.Mode().IsRegular() || (name == "node" && info.Mode().Perm()&0111 == 0) {
			return "", errors.New("ZCode Server 当前安装缺少可用的运行产物")
		}
	}
	return current.Version, nil
}

// 安装记录是小型本地 JSON，限制读取大小；不读取配置、凭据或会话正文。
func readZCodeServerJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return errors.New("ZCode Server 安装记录过大或读取失败")
	}
	return json.Unmarshal(raw, value)
}
