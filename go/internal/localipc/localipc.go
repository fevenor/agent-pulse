// Package localipc 只负责同机 HTTP 流的监听和拨号；Hub/Relay仍使用网络传输。
package localipc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// BaseURL 只是HTTP报文中的本机Host，不会拨号到TCP端口。
const BaseURL = "http://127.0.0.1:1"

func Validate(address string) error {
	if strings.IndexByte(address, 0) >= 0 {
		return errors.New("本机IPC地址含无效字符")
	}
	return validateAddress(address)
}

func unixPath(address string) (string, error) {
	if !strings.HasPrefix(address, "unix:") {
		return "", errors.New("本机接入已使用Unix套接字；请删除Bridge配置的listen项采用默认地址，或填写unix:/绝对路径")
	}
	p := strings.TrimPrefix(address, "unix:")
	if !filepath.IsAbs(p) || p != filepath.Clean(p) || len([]byte(p)) > 107 {
		return "", errors.New("Unix套接字必须为不超过107字节的规范绝对路径")
	}
	return p, nil
}

func Client(address string, timeout time.Duration) (*http.Client, error) {
	if err := Validate(address); err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, address) }, DisableKeepAlives: true}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("本机IPC不允许重定向") }}, nil
}
