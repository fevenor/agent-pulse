package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-pulse/protocol"
)

// 模拟将来才出现的Agent：配置是普通文本，不使用Bridge已知的JSON hook格式。
// 控制和转换在同一个独立可执行包内，测试期间不更换Hub/Bridge代码。
func runControlFixture() bool {
	if os.Getenv("AGENT_PULSE_BUILTIN_CONTROL_FIXTURE") == "1" {
		if len(os.Args) > 1 && os.Args[1] == "--control" {
			raw, _ := io.ReadAll(os.Stdin)
			var req ControlRequest
			if json.Unmarshal(raw, &req) != nil {
				os.Exit(2)
			}
			reply, err := ExecuteControl(req)
			if err != nil {
				os.Exit(3)
			}
			json.NewEncoder(os.Stdout).Encode(reply)
		} else {
			fmt.Print(`{}`)
		}
		return true
	}
	if os.Getenv("AGENT_PULSE_CONTROL_FIXTURE") != "1" {
		return false
	}
	if len(os.Args) > 1 && os.Args[1] == "--control" {
		raw, _ := io.ReadAll(os.Stdin)
		var req ControlRequest
		if json.Unmarshal(raw, &req) != nil {
			os.Exit(2)
		}
		switch req.Operation {
		case "probe":
			json.NewEncoder(os.Stdout).Encode(ControlReply{Name: req.Package.Name, Version: "1.2.3", ConfigPath: filepath.Join(os.Getenv("AGENT_PULSE_CONTROL_ROOT"), req.Package.ID+".cfg"), Deferred: os.Getenv("AGENT_PULSE_CONTROL_DEFERRED") == "1", Notice: "外部环境管理hook"})
		case "configure", "verify":
			lines := []string{}
			for _, line := range strings.Split(strings.TrimSpace(string(req.Current)), "\n") {
				if line != "" && !strings.HasPrefix(line, "pulse_") {
					lines = append(lines, line)
				}
			}
			if req.Enable {
				hooks := []string{}
				for event := range req.Package.Hooks {
					hooks = append(hooks, event)
				}
				sort.Strings(hooks)
				for _, event := range hooks {
					lines = append(lines, fmt.Sprintf("pulse_%s=%s;%s;%s", event, req.BridgeExecutable, req.BridgeConfig, req.Package.Version))
				}
			}
			content := []byte(strings.Join(lines, "\n") + "\n")
			json.NewEncoder(os.Stdout).Encode(ControlReply{Content: content, Enabled: !strings.Contains(string(content), "hooks_enabled=false")})
		default:
			os.Exit(3)
		}
	} else {
		own, _ := os.Executable()
		idRaw, _ := os.ReadFile(filepath.Join(filepath.Dir(own), "identity.txt"))
		source := ""
		for i, arg := range os.Args {
			if arg == "--source" && i+1 < len(os.Args) {
				source = os.Args[i+1]
			}
		}
		event := protocol.Event{EventID: protocol.NewID(), SourceID: source, Agent: strings.TrimSpace(string(idRaw)), SessionID: "future-fixture-session", Scope: "main", Kind: "turn.completed", OccurredAt: time.Now().UTC(), Title: "未来Agent完成", Body: "中文完成 ✅"}
		json.NewEncoder(os.Stdout).Encode(protocol.AdapterResult{Event: &event})
	}
	return true
}
