package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agent-pulse/internal/config"
)

// ControlRequest 是固定的本机控制契约；配置路径由用户授权，包只生成内容，不能要求Bridge写任意文件。
type ControlRequest struct {
	Operation        string  `json:"operation"`
	Package          Package `json:"package"`
	BridgeExecutable string  `json:"bridge_executable"`
	BridgeConfig     string  `json:"bridge_config"`
	ConfigPath       string  `json:"config_path"`
	Directory        string  `json:"directory"`
	SourceID         string  `json:"source_id"`
	Address          string  `json:"address"`
	Current          []byte  `json:"current,omitempty"`
	Enable           bool    `json:"enable"`
}
type ControlReply struct {
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Deferred   bool   `json:"deferred,omitempty"`
	Notice     string `json:"notice,omitempty"`
	Content    []byte `json:"content,omitempty"`
	Enabled    bool   `json:"enabled"`
}

func control(ctx context.Context, c config.Bridge, configPath string, p Package, operation string, current []byte, enable bool) (ControlReply, error) {
	var reply ControlReply
	if p.Control == "" {
		return reply, errors.New("包缺少控制入口")
	}
	exe, err := os.Executable()
	if err != nil {
		return reply, err
	}
	cfg, err := filepath.Abs(configPath)
	if err != nil {
		return reply, err
	}
	raw, err := json.Marshal(ControlRequest{Operation: operation, Package: p, BridgeExecutable: exe, BridgeConfig: cfg, ConfigPath: c.Adapters.Managed[p.ID], Directory: c.Adapters.Directory, SourceID: c.SourceID, Address: c.Listen, Current: current, Enable: enable})
	if err != nil {
		return reply, err
	}
	if len(raw) > 2<<20 {
		return reply, errors.New("适配控制输入超过2MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := hostCommand(ctx, filepath.Join(packageDir(c.Adapters.Directory, p), filepath.FromSlash(p.Control)), "--control")
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	var output limitedBuffer
	output.limit = 2 << 20
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		return reply, errors.New("适配控制入口执行失败或超时")
	}
	if json.Unmarshal(output.Bytes(), &reply) != nil {
		return reply, errors.New("适配控制回复无效")
	}
	if len(reply.Name) > 256 || strings.ContainsAny(reply.Name, "\r\n\x00") {
		return reply, errors.New("适配名称过长或包含控制字符")
	}
	if reply.Deferred && strings.TrimSpace(reply.Notice) == "" {
		return reply, errors.New("外部管理提示缺失")
	}
	if operation == "probe" && reply.ConfigPath != "" && !filepath.IsAbs(reply.ConfigPath) {
		return reply, errors.New("适配器返回的配置路径不是绝对路径")
	}
	return reply, nil
}

type deferredPackage struct{ notice string }

func (e *deferredPackage) Error() string { return e.notice }

func detectPackage(ctx context.Context, c config.Bridge, p Package) (string, error) {
	if p.Control == "" {
		return Detect(ctx, p.Manifest)
	}
	reply, err := control(ctx, c, "bridge.yaml", p, "probe", nil, false)
	if err != nil {
		return "", err
	}
	if reply.Deferred {
		return reply.Version, &deferredPackage{notice: reply.Notice}
	}
	if _, ok := version(reply.Version); !ok {
		return "", errors.New("未检测到有效宿主版本")
	}
	return reply.Version, nil
}

// ExecuteControl 仅由适配器可执行文件调用。它不写文件、不访问Hub；产品细节随包发布。
func ExecuteControl(request ControlRequest) (ControlReply, error) {
	c := config.Bridge{SourceID: request.SourceID, Listen: request.Address, Adapters: config.AdapterUpdates{Directory: request.Directory, Managed: map[string]string{request.Package.ID: request.ConfigPath}}}
	switch request.Operation {
	case "probe":
		home, _ := os.UserHomeDir()
		catalog := setupAgentCatalog(home, runtime.GOOS)
		reply := ControlReply{Name: request.Package.Name, ConfigPath: request.ConfigPath}
		for _, item := range catalog {
			if item.ID == request.Package.ID {
				if reply.Name == "" {
					reply.Name = item.Name
				}
				if reply.ConfigPath == "" || item.ID == "codex" && os.Getenv("CODEX_HOME") != "" {
					reply.ConfigPath = item.ConfigPath
				}
				break
			}
		}
		if request.Package.ID == "codex" && windowsDefersCodex(reply.ConfigPath, runtime.GOOS) {
			reply.Deferred = true
			reply.Notice = "由WSL管理共享接入，保持现有钩子"
		}
		v, err := Detect(context.Background(), request.Package.Manifest)
		if err != nil {
			return reply, nil
		}
		reply.Version = v
		if request.Package.ID == "codex" && !reply.Deferred {
			reply.Notice = "请在 /hooks 中确认 AgentPulse 钩子已审核并信任；已信任的无需重复操作"
		}
		return reply, nil
	case "configure", "verify":
		data, err := updateHookConfigWithExecutable(c, request.BridgeConfig, request.Package, request.Current, request.Enable, request.BridgeExecutable)
		reply := ControlReply{Content: data, Enabled: true}
		if request.Package.ConfigStyle == "process-hooks" {
			var root struct {
				Hooks struct {
					Enabled bool `json:"enabled"`
				} `json:"hooks"`
			}
			if json.Unmarshal(data, &root) != nil || !root.Hooks.Enabled {
				reply.Enabled = false
			}
		}
		return reply, err
	}
	return ControlReply{}, errors.New("未知适配控制操作")
}
