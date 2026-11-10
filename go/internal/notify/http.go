// Package notify 将标题和正文编码到用户已有的 HTTP 推送接口。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

// Send 返回 sent、retry、failed 或 unknown，不返回含凭据的网络错误。
func Send(ctx context.Context, ch config.Channel, e protocol.Event) string {
	replace := strings.NewReplacer("${title}", limitChars(e.Title, ch.MaxTitleChars), "${body}", limitChars(e.Body, ch.MaxBodyChars))
	u, err := url.Parse(ch.URL)
	if err != nil {
		return "failed"
	}
	query := u.Query()
	for k, v := range ch.Query {
		query.Set(k, replace.Replace(v))
	}
	u.RawQuery = query.Encode()
	fields := map[string]string{}
	for k, v := range ch.Fields {
		fields[k] = replace.Replace(v)
	}
	var body io.Reader
	contentType := ""
	if ch.Method == "POST" {
		if ch.Format == "json" {
			data, err := json.Marshal(fields)
			if err != nil {
				return "failed"
			}
			body = bytes.NewReader(data)
			contentType = "application/json"
		} else {
			values := url.Values{}
			for k, v := range fields {
				values.Set(k, v)
			}
			body = strings.NewReader(values.Encode())
			contentType = "application/x-www-form-urlencoded"
		}
	}
	req, err := http.NewRequestWithContext(ctx, ch.Method, u.String(), body)
	if err != nil {
		return "failed"
	}
	for k, v := range ch.Headers {
		req.Header.Set(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := &http.Client{Timeout: time.Duration(ch.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if strings.EqualFold(u.Hostname(), "api.chuckfang.com") {
			return chuckfangResult(resp.Body)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "sent"
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return "retry"
	}
	return "failed"
}

// 按 Unicode 码点计数，省略号也占一个字符；不改写 Hub 保存的原始事件。
func limitChars(text string, maximum int) string {
	if maximum <= 0 || utf8.RuneCountInString(text) <= maximum {
		return text
	}
	runes := []rune(text)
	return string(runes[:maximum-1]) + "…"
}

// 当前实际渠道在 HTTP 200 中另返回业务 status；只做该接口需要的局部判断。
func chuckfangResult(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, (64<<10)+1))
	var result struct {
		Status *int `json:"status"`
	}
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &result) != nil || result.Status == nil {
		return "unknown"
	}
	if *result.Status == 200 {
		return "sent"
	}
	return "failed"
}
