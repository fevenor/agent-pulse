package localipc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"net"
	"strings"
)

func currentSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
func DefaultAddress(source string) (string, error) {
	sid, err := currentSID()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(source))
	return fmt.Sprintf(`pipe:\\.\pipe\agent-pulse-%s-%x`, sid, hash[:12]), nil
}
func pipePath(address string) (string, error) {
	const prefix = `pipe:\\.\pipe\`
	if !strings.HasPrefix(address, prefix) {
		return "", errors.New("本机接入已使用命名管道；请删除Bridge配置的listen项采用默认地址，或填写pipe:\\\\.\\pipe\\本机名称")
	}
	name := strings.TrimPrefix(address, prefix)
	if len(name) == 0 || len(name) > 200 {
		return "", errors.New("本机命名管道名称长度无效")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return "", errors.New("本机命名管道名称仅允许字母、数字、点、横杠和下划线")
		}
	}
	return strings.TrimPrefix(address, "pipe:"), nil
}
func validateAddress(address string) error { _, err := pipePath(address); return err }
func dial(ctx context.Context, address string) (net.Conn, error) {
	p, err := pipePath(address)
	if err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, p)
}
func Listen(address string) (net.Listener, error) {
	p, err := pipePath(address)
	if err != nil {
		return nil, err
	}
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	// go-winio拒绝远程客户端；显式DACL仅授权当前用户，不沿用宽松默认权限。
	return winio.ListenPipe(p, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")"})
}
