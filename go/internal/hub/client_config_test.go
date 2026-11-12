package hub

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestClientConfigFollowsHubAuthentication(t *testing.T) {
	h := testHub(t)
	h.config.RelayURL = "https://relay.example.invalid"
	h.config.RelayToken = "isolated-relay-test-token"
	h.config.AdapterPublicKey = "test-publisher-public-key"
	for _, tc := range []struct {
		token, header string
		status        int
	}{
		{"", "", 200}, {"test-hub-token", "", 401},
		{"test-hub-token", "Bearer wrong", 401}, {"test-hub-token", "Bearer test-hub-token", 200},
	} {
		request := httptest.NewRequest("GET", "/api/v1/client-config", nil)
		request.Header.Set("Authorization", tc.header)
		response := httptest.NewRecorder()
		h.Handler(tc.token).ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("响应状态 %d，预期 %d", response.Code, tc.status)
		}
		if tc.status == 200 {
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 3 || body["relay_token"] != h.config.RelayToken || body["relay_url"] != h.config.RelayURL || body["public_key"] != h.config.AdapterPublicKey {
				t.Fatal("中继发现内容不符")
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("不得缓存凭据响应")
			}
		}
	}
}
