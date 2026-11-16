package event

import (
	"encoding/json"
	"sort"
	"strings"
)

// 只展示MCP原生提问的说明和表单，不传用户答复或认证URL。
func elicitationBody(message, schema json.RawMessage) string {
	var text string
	_ = json.Unmarshal(message, &text)
	var form struct {
		Title, Description string
		Properties         map[string]struct {
			Title, Description string
			Enum               []json.RawMessage
		}
	}
	_ = json.Unmarshal(schema, &form)
	parts := []string{text, form.Title, form.Description}
	keys := make([]string, 0, len(form.Properties))
	for key := range form.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field := form.Properties[key]
		label := field.Title
		if label == "" {
			label = key
		}
		parts = append(parts, label, field.Description)
		for _, raw := range field.Enum {
			var option string
			if json.Unmarshal(raw, &option) == nil {
				parts = append(parts, "- "+option)
			}
		}
	}
	return visibleBody(strings.Join(parts, "\n"))
}
func elicitationResultBody(action string) string {
	switch action {
	case "accept":
		return "输入请求已提交。"
	case "decline":
		return "输入请求已拒绝。"
	case "cancel":
		return "输入请求已取消。"
	default:
		return "输入请求已处理。"
	}
}
