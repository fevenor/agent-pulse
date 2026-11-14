// Package notify 将标题和正文编码到用户已有的 HTTP 推送接口。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

// Result 仅保存可安全展示的状态、错误类别和数值，不保存响应正文或含凭据的网络错误。
type Result struct {
	Status         string
	HTTPStatus     int
	BusinessStatus *int
	Reason         string
}

func Send(ctx context.Context, ch config.Channel, e protocol.Event) string {
	return SendResult(ctx, ch, e).Status
}

func SendResult(ctx context.Context, ch config.Channel, e protocol.Event) Result {
	u, err := url.Parse(ch.URL)
	if err != nil {
		return Result{Status: "failed", Reason: "推送地址无法解析"}
	}
	bodyText := e.Body
	// MeoW 的消息正文不能留空。只在该渠道发送边界用事件标题补齐，不改变 Hub 原事件。
	if strings.EqualFold(u.Hostname(), "api.chuckfang.com") && strings.TrimSpace(bodyText) == "" {
		bodyText = e.Title
	}
	replace := strings.NewReplacer("${title}", limitChars(e.Title, ch.MaxTitleChars), "${body}", limitChars(bodyText, ch.MaxBodyChars))
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
				return Result{Status: "failed", Reason: "推送内容无法编码"}
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
		return Result{Status: "failed", Reason: "推送请求无法构造"}
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
		return Result{Status: "unknown", Reason: "网络请求未取得可靠响应，不能确认送达"}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if strings.EqualFold(u.Hostname(), "api.chuckfang.com") {
			result := chuckfangResponse(resp.Body)
			result.HTTPStatus = resp.StatusCode
			return result
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return Result{Status: "sent", HTTPStatus: resp.StatusCode}
	}
	status := "failed"
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		status = "retry"
	}
	return Result{Status: status, HTTPStatus: resp.StatusCode, Reason: fmt.Sprintf("推送接口返回 HTTP %d", resp.StatusCode)}
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
	return chuckfangResponse(body).Status
}

func chuckfangResponse(body io.Reader) Result {
	raw, err := io.ReadAll(io.LimitReader(body, (64<<10)+1))
	var response struct {
		Status  *int   `json:"status"`
		Message string `json:"msg"`
	}
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &response) != nil || response.Status == nil {
		return Result{Status: "unknown", Reason: "手机接口响应不完整或缺少业务状态"}
	}
	result := Result{Status: "sent", BusinessStatus: response.Status}
	if *response.Status == 200 {
		return result
	}
	result.Status = "failed"
	result.Reason = fmt.Sprintf("手机接口返回业务失败（状态 %d）", *response.Status)
	// 只分类，不保存或显示服务端原始文本，避免昵称、密钥或正文泄露。
	message := strings.ToLower(response.Message)
	switch {
	case *response.Status == 404 && (strings.Contains(message, "昵称") || strings.Contains(message, "注册")):
		result.Reason = "手机推送目标不存在或未注册"
	case *response.Status == 403 && (strings.Contains(message, "ip") || strings.Contains(message, "封禁")):
		result.Reason = "手机接口拒绝来源 IP"
	case (strings.Contains(message, "msg") || strings.Contains(message, "正文")) && (strings.Contains(message, "空") || strings.Contains(message, "empty")):
		result.Reason = "手机接口拒绝空消息正文"
	case strings.Contains(message, "key") || strings.Contains(message, "密钥"):
		result.Reason = "手机接口拒绝渠道密钥"
	}
	return result
}
