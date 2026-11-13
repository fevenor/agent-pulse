package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-pulse/internal/adapters"
)

// 次版本兼容提示独立于失败，报告入库及读取均不能丢失或改写为错误。
func TestAdapterCompatibilityWarningRoundTrip(t *testing.T) {
	h := testHub(t)
	warning := "宿主次版本未验证：允许继续使用，但不承诺兼容性"
	report := adapters.Report{SourceID: "linux", Items: []adapters.Status{{ID: "sample", Installed: "1.0.0", Running: "1.0.0", AgentVersion: "1.3.0", Warning: warning}}}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/adapters/status", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	h.Handler("").ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("含兼容提示的报告被拒绝：%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	h.Handler("").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/adapters/status", nil))
	var reports []adapters.Report
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &reports) != nil || len(reports) != 1 || len(reports[0].Items) != 1 {
		t.Fatalf("兼容提示报告读取失败：%s", response.Body.String())
	}
	item := reports[0].Items[0]
	if item.Warning != warning || item.Error != "" || item.Installed != item.Running {
		t.Fatalf("兼容提示被丢弃或误报失败：%+v", item)
	}
}
