package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	"agent-pulse/internal/config"
)

// 身份检查仍调用已安装签名包，Bridge不解释任何产品数据库或文件路径。
func CheckIdentity(ctx context.Context, c config.Bridge, id string, cursor []byte) (string, error) {
	if !identifier.MatchString(id) || len(cursor) == 0 || len(cursor) > 8192 {
		return "", errors.New("身份观察请求无效")
	}
	state, err := dispatchState(ctx, c.Adapters.Directory)
	if err != nil {
		return "", err
	}
	installed, ok := state.Installed[id]
	if !ok || installed.Package.Kind != "executable" {
		return "", errors.New("身份观察适配器未安装")
	}
	p := installed.Package
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := hostCommand(ctx, filepath.Join(packageDir(c.Adapters.Directory, p), filepath.FromSlash(p.Entry)), "--check-identity")
	cmd.Stdin = bytes.NewReader(cursor)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	var output limitedBuffer
	output.limit = 1024
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		return "", errors.New("身份观察未完成")
	}
	var reply struct {
		Identity string `json:"identity"`
	}
	if json.Unmarshal(output.Bytes(), &reply) != nil {
		return "", errors.New("身份观察回复无效")
	}
	switch reply.Identity {
	case "main", "child", "unknown":
		return reply.Identity, nil
	}
	return "", errors.New("身份观察结论无效")
}
