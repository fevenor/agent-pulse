package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushCharacterLimits(t *testing.T) {
	for _, tc := range []struct {
		title, body int
		valid       bool
	}{
		{0, 0, true}, {1, 1, true}, {40, 2000, true}, {-1, 0, false}, {0, -1, false},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.title, tc.body), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.yaml")
			body := fmt.Sprintf("push:\n  - name: phone\n    method: GET\n    url: https://example.invalid/push\n    max_title_chars: %d\n    max_body_chars: %d\n", tc.title, tc.body)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadHub(path)
			if (err == nil) != tc.valid {
				t.Fatalf("配置校验结果不符: %v", err)
			}
			if tc.valid && (cfg.Push[0].MaxTitleChars != tc.title || cfg.Push[0].MaxBodyChars != tc.body) {
				t.Fatal("未保留渠道字符上限")
			}
		})
	}
}

func TestStrictYAML(t *testing.T) {
	t.Setenv("PULSE_TEST_TOKEN", "secret: value\nnot_a_field: true")
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"有效", "source_id: desktop\ntoken: '${env:PULSE_TEST_TOKEN}'", true},
		{"重复键", "source_id: desktop\nsource_id: another", false},
		{"未知字段", "source_id: desktop\nqueue_event: 4", false},
		{"数字不能代替字符串", "source_id: 123", false},
		{"环境不存在", "source_id: '${env:PULSE_MISSING_FOR_TEST}'", false},
		{"多个文档", "source_id: desktop\n---\nsource_id: another", false},
		{"锚点", "source_id: &source desktop", false},
		{"合并", "source_id: desktop\n<<: {queue_events: 1}", false},
		{"标签", "source_id: !secret desktop", false},
		{"非字符串键", "source_id: desktop\n1: x", false},
		{"非回环", "source_id: desktop\nlisten: '0.0.0.0:18702'", false},
		{"格式错误不泄露值", "source_id: desktop\ntoken: [very-secret,", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadBridge(path)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if c.Token != os.Getenv("PULSE_TEST_TOKEN") {
					t.Fatal("环境替换改变了值")
				}
			} else {
				if err == nil {
					t.Fatal("错误配置被接收")
				}
				if strings.Contains(err.Error(), "very-secret") {
					t.Fatal("错误泄露敏感值")
				}
			}
		})
	}
}

func TestOptionalEnvironmentDefaults(t *testing.T) {
	const missing = "PULSE_OPTIONAL_MISSING_FOR_TEST"
	previous, existed := os.LookupEnv(missing)
	_ = os.Unsetenv(missing)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(missing, previous)
		} else {
			_ = os.Unsetenv(missing)
		}
	})
	t.Setenv("PULSE_OPTIONAL_VALUE", "")
	for _, tc := range []struct {
		name, token string
		want        string
	}{
		{"未设置使用默认空值", "${env:PULSE_OPTIONAL_MISSING_FOR_TEST:-}", ""},
		{"未设置使用指定默认值", "${env:PULSE_OPTIONAL_MISSING_FOR_TEST:-fallback}", "fallback"},
		{"已设置为空使用默认值", "${env:PULSE_OPTIONAL_VALUE:-fallback}", "fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bridge.yaml")
			body := fmt.Sprintf("source_id: desktop\ntoken: '%s'\n", tc.token)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadBridge(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.Token != tc.want {
				t.Fatalf("token = %q, want %q", c.Token, tc.want)
			}
		})
	}
}

func TestHubDefaultImageConfigLoadsWithoutOptionalEnvironment(t *testing.T) {
	t.Setenv("AGENT_PULSE_TOKEN", "")
	t.Setenv("AGENT_PULSE_RELAY_URL", "")
	t.Setenv("AGENT_PULSE_RELAY_TOKEN", "")
	path := filepath.Join("..", "..", "..", "deploy", "docker", "hub.default.yaml")
	c, err := LoadHub(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:18700" || c.Database != "/data/hub.db" || c.WebDirectory != "/web" || c.AdapterDirectory != "/repository" {
		t.Fatalf("容器默认路径或监听地址错误：%+v", c)
	}
	if c.Token != "" || c.RelayURL != "" || c.RelayToken != "" {
		t.Fatal("未设置可选凭据时配置必须保持可启动")
	}
}
