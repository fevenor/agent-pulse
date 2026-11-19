package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"agent-pulse/internal/config"
	"agent-pulse/protocol"
)

// 观察进程只执行当前已安装的签名包入口，整个本轮固定版本；退出或替换时取消。
func Observe(ctx context.Context, c config.Bridge, id string, cursor []byte, emit func(protocol.LocalEvent) error) error {
	if !identifier.MatchString(id) || len(cursor) > 65536 {
		return errors.New("本轮观察登记无效")
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		return err
	}
	installed, ok := state.Installed[id]
	if !ok || installed.Package.Kind != "executable" {
		return errors.New("本轮观察适配器未安装")
	}
	p := installed.Package
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := hostCommand(child, filepath.Join(packageDir(c.Adapters.Directory, p), filepath.FromSlash(p.Entry)), "--observe", "--source", c.SourceID, "--parent", strconv.Itoa(os.Getpid()))
	cmd.Stdin = bytes.NewReader(cursor)
	cmd.Stderr = io.Discard
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return errors.New("本轮观察入口无法启动")
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), protocol.MaxEventBytes+1)
	var readErr error
	count := 0
	for scanner.Scan() {
		var e protocol.LocalEvent
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Validate() != nil || e.SourceID != c.SourceID || e.Agent != id || count > 0 {
			readErr = errors.New("本轮观察输出无效")
			cancel()
			break
		}
		count++
		if err = emit(e); err != nil {
			readErr = err
			cancel()
			break
		}
	}
	if scanner.Err() != nil {
		readErr = errors.New("本轮观察输出超限或读取失败")
		cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return errors.New("本轮观察进程未正常完成")
	}
	return nil
}
