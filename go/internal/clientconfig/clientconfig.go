package clientconfig

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
)

// Settings 是 Hub 通过已认证的直连接口提供给 Bridge 的共享客户端配置。
type Settings struct {
	RelayURL   string `json:"relay_url"`
	RelayToken string `json:"relay_token"`
	PublicKey  string `json:"public_key"`
}

var errCacheIdentity = errors.New("Hub 客户端配置缓存身份不匹配")

type cache struct {
	HubURL   string   `json:"hub_url"`
	SourceID string   `json:"source_id"`
	Settings Settings `json:"settings"`
}

// Resolve 只直接连接 Hub；首次信任发布公钥成功后固定到配置旁的本地缓存。
// 手工 relay 配置优先，Hub 暂时不可达时可使用同一 Hub 和主机指纹的缓存。
func Resolve(ctx context.Context, c config.Bridge, configPath string) (config.Bridge, error) {
	if strings.TrimSpace(configPath) == "" {
		return c, errors.New("Hub 客户端配置缓存需要 Bridge 配置路径")
	}
	cachePath := configPath + ".hub-client.json"
	cached, cacheErr := readCache(cachePath, c.HubURL, c.SourceID)
	if cacheErr != nil && !os.IsNotExist(cacheErr) && !errors.Is(cacheErr, errCacheIdentity) {
		return c, fmt.Errorf("本地 Hub 信任缓存不可用；未重新建立首次信任，请核实缓存：%w", cacheErr)
	}
	var remote Settings
	_, requestErr := transport.Request(ctx, transport.Client(5*time.Second), c.HubURL, c.Token, http.MethodGet, "/api/v1/client-config", nil, &remote)
	if requestErr != nil {
		if cacheErr == nil {
			return apply(c, cached.Settings), nil
		}
		return c, fmt.Errorf("无法直连 Hub 读取客户端配置：%w", requestErr)
	}
	if err := config.ValidateRelaySettings(remote.RelayURL, remote.RelayToken); err != nil {
		return c, fmt.Errorf("Hub 返回的 Relay 配置无效：%w", err)
	}
	remote.PublicKey = strings.TrimSpace(remote.PublicKey)
	manualKey := strings.TrimSpace(c.Adapters.PublicKey) != ""
	if remote.PublicKey != "" {
		key, err := base64.StdEncoding.DecodeString(remote.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return c, errors.New("Hub 返回的适配包发布公钥格式无效")
		}
	}
	if cacheErr == nil && cached.Settings.PublicKey != "" {
		if remote.PublicKey != "" && cached.Settings.PublicKey != remote.PublicKey {
			if !manualKey {
				return c, errors.New("Hub 适配包发布公钥与首次信任记录不一致；为避免接受未验证的适配包，请人工核实后处理本地信任缓存")
			}
			// 手工公钥覆盖适配包验签信任；继续保留 Hub 的首次公钥记录。
			remote.PublicKey = cached.Settings.PublicKey
		}
		if remote.PublicKey == "" {
			// Hub 暂时未返回公钥时保留已经固定的信任值，避免配置意外降级。
			remote.PublicKey = cached.Settings.PublicKey
		}
	}
	if err := writeCache(cachePath, cache{HubURL: c.HubURL, SourceID: c.SourceID, Settings: remote}); err != nil {
		return c, fmt.Errorf("保存 Hub 客户端配置失败：%w", err)
	}
	return apply(c, remote), nil
}

func apply(c config.Bridge, value Settings) config.Bridge {
	if c.RelayURL == "" {
		c.RelayURL, c.RelayToken = value.RelayURL, value.RelayToken
	}
	if strings.TrimSpace(c.Adapters.PublicKey) == "" && value.PublicKey != "" {
		c.Adapters.PublicKey = value.PublicKey
	}
	return c
}

func readCache(path, hubURL, sourceID string) (cache, error) {
	var value cache
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	var fields map[string]json.RawMessage
	if len(data) > 1<<20 || json.Unmarshal(data, &fields) != nil || json.Unmarshal(data, &value) != nil ||
		value.HubURL == "" || value.SourceID == "" || len(fields["settings"]) == 0 || string(fields["settings"]) == "null" {
		return value, errors.New("Hub 客户端配置缓存无效")
	}
	if err := config.HTTPURL(value.HubURL); err != nil {
		return value, errors.New("Hub 客户端配置缓存中的 Hub 地址无效")
	}
	if err := config.ValidateRelaySettings(value.Settings.RelayURL, value.Settings.RelayToken); err != nil {
		return value, errors.New("Hub 客户端配置缓存中的 Relay 配置无效")
	}
	if value.Settings.PublicKey != "" {
		key, err := base64.StdEncoding.DecodeString(value.Settings.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return value, errors.New("Hub 客户端配置缓存中的发布公钥无效")
		}
	}
	if value.HubURL != hubURL || value.SourceID != sourceID {
		return value, errCacheIdentity
	}
	return value, nil
}

func writeCache(path string, value cache) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hub-client-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmpName, path)
}
