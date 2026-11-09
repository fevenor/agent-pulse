package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
