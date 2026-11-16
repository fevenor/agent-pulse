package adapters

import (
	"agent-pulse/internal/config"
	"bytes"
	"strings"
	"testing"
)

func TestOpenCodeJSONCPreservesPrivateSyntaxAndIsIdempotent(t *testing.T) {
	raw := []byte("// 原始说明\n{\n \"provider\": {\"number\": 900719925474099312345, \"url\":\"https://example.invalid/a//b\"},\n \"plugins\": [ {\"package\":\"third-party\", /* 插件说明 */ \"enabled\":true}, ],\n \"other\": \"保留\",\n}\n")
	c := config.Bridge{SourceID: "isolated", Listen: "127.0.0.1:19000"}
	c.Adapters.Directory = t.TempDir()
	p := Package{Manifest: Manifest{ID: "opencode", Kind: "opencode-plugin"}}
	after, err := updateOpenCodeJSONC(c, p, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"// 原始说明", "/* 插件说明 */", "900719925474099312345", "https://example.invalid/a//b", "third-party"} {
		if !strings.Contains(string(after), keep) {
			t.Fatal("私人内容被改写：", keep)
		}
	}
	again, err := updateOpenCodeJSONC(c, p, after, true)
	if err != nil || !bytes.Equal(after, again) {
		t.Fatal("重复安装不幂等", err, string(again))
	}
	disabled, err := updateOpenCodeJSONC(c, p, after, false)
	if err != nil || !strings.Contains(string(disabled), "third-party") || strings.Contains(string(disabled), "source_id") {
		t.Fatal("停用未保留其他插件", err)
	}
}
func TestOpenCodeJSONCRejectsInvalidAndDuplicateKeys(t *testing.T) {
	for _, raw := range []string{"[]", "null", "{\"plugins\":{}}", "{\"plugins\":[],\"plugins\":[]}", "{\"other\":{\"x\":1,\"x\":2}}", "{\"unterminated\":"} {
		if _, err := updateOpenCodeJSONC(config.Bridge{}, Package{Manifest: Manifest{ID: "opencode"}}, []byte(raw), true); err == nil {
			t.Fatal("无效配置被接受", raw)
		}
	}
}
