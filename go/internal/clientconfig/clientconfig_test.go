package clientconfig

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-pulse/internal/config"
)

func TestResolvePinsHubKeyAndRejectsReplacement(t *testing.T) {
	_, firstPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, secondPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	firstKey := base64.StdEncoding.EncodeToString(firstPrivate.Public().(ed25519.PublicKey))
	secondKey := base64.StdEncoding.EncodeToString(secondPrivate.Public().(ed25519.PublicKey))
	currentKey := firstKey
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/client-config" {
			t.Errorf("请求了非 Hub 配置接口：%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer hub-secret" {
			t.Errorf("未使用 Hub 鉴权：%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Settings{RelayURL: "http://127.0.0.1:18701", RelayToken: "relay-secret", PublicKey: currentKey})
	}))
	defer server.Close()

	c := config.Bridge{HubURL: server.URL, Token: "hub-secret", SourceID: "bridge-test-id", RelayURL: "http://relay.example.invalid", RelayToken: "manual-relay", Adapters: config.AdapterUpdates{Directory: t.TempDir()}}
	path := filepath.Join(t.TempDir(), "bridge.yaml")
	resolved, err := Resolve(context.Background(), c, path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Adapters.PublicKey != firstKey {
		t.Fatal("首次直连 Hub 公钥未被采用")
	}
	if resolved.RelayURL != c.RelayURL || resolved.RelayToken != c.RelayToken {
		t.Fatal("手工 Relay 配置未优先")
	}

	currentKey = secondKey
	_, err = Resolve(context.Background(), c, path)
	if err == nil || !strings.Contains(err.Error(), "首次信任记录") {
		t.Fatalf("期望拒绝 Hub 换钥，实际错误：%v", err)
	}
	if requests != 2 {
		t.Fatalf("Hub 直连次数为 %d，预期 2", requests)
	}

	// 更换主机指纹必须建立独立信任记录，不得复用旧主机缓存。
	c.SourceID = "bridge-other-id"
	resolved, err = Resolve(context.Background(), c, path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Adapters.PublicKey != secondKey {
		t.Fatal("新主机指纹未建立独立的首次信任记录")
	}
}

func TestResolveManualPublicKeyOverridesHub(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manualKey := base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	_, remotePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	remoteKey := base64.StdEncoding.EncodeToString(remotePrivate.Public().(ed25519.PublicKey))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Settings{PublicKey: remoteKey})
	}))
	defer server.Close()
	c := config.Bridge{HubURL: server.URL, SourceID: "bridge-test-id", Adapters: config.AdapterUpdates{PublicKey: manualKey}}
	resolved, err := Resolve(context.Background(), c, filepath.Join(t.TempDir(), "bridge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Adapters.PublicKey != manualKey {
		t.Fatal("手工公钥未覆盖 Hub 下发值")
	}
}

func TestResolveRejectsDamagedTrustCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("缓存损坏时不应重新向 Hub 建立首次信任")
		_ = json.NewEncoder(w).Encode(Settings{})
	}))
	defer server.Close()
	for _, raw := range []string{"not-json", "{}", `{ "hub_url":"` + server.URL + `", "source_id":"source", "settings":{"public_key":"bad-key"}}`} {
		path := filepath.Join(t.TempDir(), "bridge.yaml")
		cachePath := path + ".hub-client.json"
		if err := os.WriteFile(cachePath, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Resolve(context.Background(), config.Bridge{HubURL: server.URL, SourceID: "source"}, path)
		if err == nil || !strings.Contains(err.Error(), "未重新建立首次信任") {
			t.Fatalf("应明确拒绝损坏缓存：%v", err)
		}
		after, err := os.ReadFile(cachePath)
		if err != nil || string(after) != raw {
			t.Fatal("损坏缓存不应被静默替换")
		}
	}
}
