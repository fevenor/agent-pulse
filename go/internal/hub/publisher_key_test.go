package hub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-pulse/internal/config"
)

func TestPublisherKeyFallbackAndExplicitPriority(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	fallback := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	explicit := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	if err := os.WriteFile(filepath.Join(repository, "publisher.pub"), []byte(fallback+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Open(config.Hub{Database: filepath.Join(root, "hub.db"), AdapterDirectory: repository})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	response := httptest.NewRecorder()
	h.Handler("").ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/client-config", nil))
	var result map[string]string
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || result["public_key"] != fallback {
		t.Fatal("客户端未获得仓库公钥")
	}
	// 显式配置必须优先，不能被仓库中另一把公钥或损坏文件覆盖。
	if err = os.WriteFile(filepath.Join(repository, "publisher.pub"), []byte("损坏文件"), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := resolvePublisherKey(repository, explicit)
	if err != nil || key != explicit {
		t.Fatalf("显式公钥未优先：%q %v", key, err)
	}
}

func TestPublisherKeyRejectsBrokenRepositoryAndAllowsBaseImage(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "未附加仓库")
	if key, err := resolvePublisherKey(missing, ""); err != nil || key != "" {
		t.Fatalf("基础镜像不能启动：%q %v", key, err)
	}
	for _, tc := range []struct {
		name, file, configured string
		exists                 bool
	}{
		{name: "仓库缺少公钥"},
		{name: "公钥为空", exists: true},
		{name: "公钥格式无效", file: "not-base64", exists: true},
		{name: "公钥长度无效", file: base64.StdEncoding.EncodeToString([]byte("短")), exists: true},
		{name: "显式公钥无效", configured: "bad-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := t.TempDir()
			if tc.exists {
				if err := os.WriteFile(filepath.Join(repository, "publisher.pub"), []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			database := filepath.Join(repository, "hub.db")
			h, err := Open(config.Hub{Database: database, AdapterDirectory: repository, AdapterPublicKey: tc.configured})
			if err == nil {
				h.Close()
				t.Fatal("错误公钥不应启动成功")
			}
			if !strings.Contains(err.Error(), "公钥") && !strings.Contains(err.Error(), "publisher.pub") {
				t.Fatalf("缺少明确原因：%v", err)
			}
			if _, err := os.Stat(database); !os.IsNotExist(err) {
				t.Fatal("公钥验证失败前不应创建数据库")
			}
		})
	}
}
