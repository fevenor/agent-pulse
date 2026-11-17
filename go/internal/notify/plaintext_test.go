package notify

import (
	"strings"
	"testing"
)

func TestPlainTextKeepsVisibleContentAndCode(t *testing.T) {
	raw := "# 中文 ✅\n\n**重点** 与 [链接](https://example.invalid)\n\n- [x] 完成\n\n```sh\necho '# * _'\n```\n\n| 名称 | 值 |\n| --- | --- |\n| 项目 | 一 |\n"
	got := PlainText(raw)
	for _, v := range []string{"中文 ✅", "重点", "链接（https://example.invalid）", "[已完成]", "echo '# * _'", "名称 | 值", "项目 | 一"} {
		if !strings.Contains(got, v) {
			t.Fatalf("可见内容丢失 %q：%s", v, got)
		}
	}
	if strings.Contains(got, "**重点**") || strings.Contains(got, "```sh") {
		t.Fatal("Markdown标记未转换", got)
	}
}
