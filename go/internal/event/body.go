package event

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func isQuestionTool(name string) bool {
	return name == "AskUserQuestion" || name == "ask_user_question" || name == "request_user_input" || name == "functions.request_user_input"
}

// 保留事件的可见正文；超出既有上限时明确截断，不让长回答吞掉完成状态。
func visibleBody(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	const limit = 32 << 10
	const wireLimit = 56 << 10 // 为64KiB事件的身份、标题、项目和JSON字段保留空间。
	const suffix = "\n\n[内容超过事件长度上限，已截断]"
	encoded, _ := json.Marshal(value)
	if len(value) <= limit && len(encoded) <= wireLimit {
		return value
	}
	cut := min(len(value), limit-len(suffix))
	for {
		prefix := value[:cut]
		for !utf8.ValidString(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
		encoded, _ = json.Marshal(prefix + suffix)
		if len(encoded) <= wireLimit {
			return prefix + suffix
		}
		cut = max(0, len(prefix)*wireLimit/len(encoded)-16)
	}
}

// 仅白名单提取原生问题与选项，兼顾 Qoder/ZCode 对象选项和 Agy 原生字符串选项。
func questionBody(raw json.RawMessage) string {
	var input struct {
		Questions []struct {
			Question    string            `json:"question"`
			MultiSelect bool              `json:"multiSelect"`
			NativeMulti bool              `json:"is_multi_select"`
			Header      string            `json:"header"`
			Options     []json.RawMessage `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	var parts []string
	for i, q := range input.Questions {
		if i >= 32 {
			parts = append(parts, "[其余问题已省略]")
			break
		}
		text := strings.TrimSpace(q.Question)
		if text == "" {
			continue
		}
		if q.Header != "" {
			text = q.Header + "\n" + text
		}
		if q.MultiSelect || q.NativeMulti {
			text += "（可多选）"
		}
		for n, raw := range q.Options {
			if n >= 64 {
				text += "\n[其余选项已省略]"
				break
			}
			var label string
			var option struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			}
			if json.Unmarshal(raw, &label) != nil {
				if json.Unmarshal(raw, &option) != nil {
					continue
				}
				label = option.Label
			}
			if strings.TrimSpace(label) == "" {
				continue
			}
			text += fmt.Sprintf("\n%d. %s", n+1, strings.TrimSpace(label))
			if description := strings.TrimSpace(option.Description); description != "" {
				text += " — " + description
			}
		}
		parts = append(parts, text)
	}
	return visibleBody(strings.Join(parts, "\n\n"))
}

// 批准只展示明确的动作摘要，不序列化全部工具参数、权限元数据或模型上下文。
func approvalBody(tool string, raw json.RawMessage) string {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(raw, &input)
	parts := []string{"请求批准：" + tool}
	for _, field := range []struct{ key, label string }{
		{"description", "说明"}, {"CommandLine", "命令"}, {"toolSummary", "说明"}, {"Action", "动作"}, {"Target", "目标"}, {"Reason", "原因"}, {"action", "动作"}, {"target", "目标"}, {"reason", "原因"}, {"command", "命令"}, {"file_path", "文件"}, {"path", "路径"},
	} {
		var value string
		if json.Unmarshal(input[field.key], &value) == nil && strings.TrimSpace(value) != "" {
			parts = append(parts, field.label+"："+value)
		}
	}
	return visibleBody(strings.Join(parts, "\n"))
}

// 错误可能是原生字符串或结构化message，仅取可见摘要，不展开堆栈、响应或凭据。
func nativeFailureBody(raw json.RawMessage, errorType string) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var value struct{ Message string }
		if json.Unmarshal(raw, &value) == nil {
			text = value.Message
		}
	}
	if strings.TrimSpace(text) == "" {
		text = errorType
	}
	return visibleBody(text)
}
