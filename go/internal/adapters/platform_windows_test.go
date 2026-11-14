package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-pulse/internal/config"
)

// 被复制的测试程序只在明确的测试环境变量下模拟宿主和适配包。
func TestMain(m *testing.M) {
	mode := os.Getenv("AGENT_PULSE_INSTALL_TEST_HELPER")
	if mode == "hold-output" {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}
	if mode == "inherited-output" {
		own, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		child := hostCommand(context.Background(), own)
		child.Env = append(os.Environ(), "AGENT_PULSE_INSTALL_TEST_HELPER=hold-output")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err = child.Start(); err != nil {
			os.Exit(3)
		}
		if err = os.WriteFile(os.Getenv("AGENT_PULSE_HELPER_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(4)
		}
		fmt.Print("test-agent 1.2.3")
		os.Exit(0)
	}
	if mode != "" {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			fmt.Print("test-agent 1.2.3")
			os.Exit(0)
		}
		if mode == "echo" {
			input, _ := io.ReadAll(os.Stdin)
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"args": os.Args[1:], "stdin": string(input)})
		} else {
			fmt.Print(`{}`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// 本轮 Windows RC 仅支持 ZCode 原生进程钩子；旧命令/批处理执行测试随该范围后置，不以跳过它们替代 ZCode 验收。
// 仅验证原生 Windows 参数和 stdin 字节，不覆盖旧 shell 钩子或真实宿主防护兼容。
func TestWindowsNativeArgumentsAndUnicodeInput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "中文 空格 ' & %PULSE% !PULSE! $PULSE (目录)")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatalf("创建原生传参测试目录失败：%v", err)
	}
	own, err := os.Executable()
	if err != nil {
		t.Fatalf("获取测试辅助程序路径失败：%v", err)
	}
	raw, err := os.ReadFile(own)
	if err != nil {
		t.Fatalf("读取测试辅助程序失败：%v", err)
	}
	exe := filepath.Join(root, "原生回显.exe")
	if err = os.WriteFile(exe, raw, 0700); err != nil {
		t.Fatalf("复制测试辅助程序失败：%v", err)
	}
	t.Setenv("AGENT_PULSE_INSTALL_TEST_HELPER", "echo")
	t.Setenv("PULSE", "禁止展开_明确非空的环境变量值")
	config := filepath.Join(root, "配置文件.yaml")
	wantArgs := []string{"dispatch", "--config", config, "--adapter", "zcode", "--hook", "Stop"}
	// 使用实际 ZCode 配置生成入口，再执行其 command/args，不只测试手写参数数组。
	rawConfig, err := processHookConfig(map[string]any{}, exe, config, Package{Manifest: Manifest{ID: "zcode", Hooks: map[string]string{"Stop": ""}}})
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if err = json.Unmarshal(rawConfig, &generated); err != nil {
		t.Fatal(err)
	}
	handler := generated["hooks"].(map[string]any)["events"].(map[string]any)["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if handler["type"] != "process" || handler["command"] != exe {
		t.Fatal("ZCode 配置未使用原生进程")
	}
	args := make([]string, 0, 7)
	for _, value := range handler["args"].([]any) {
		args = append(args, value.(string))
	}
	if len(args) != len(wantArgs) {
		t.Fatal("ZCode 配置参数数量错误")
	}
	for i, want := range wantArgs {
		if args[i] != want {
			t.Fatalf("ZCode 配置参数错误：%q", args)
		}
	}
	input := []byte(" \t{\"text\":\"中文与 emoji ✅🚀\"}\r\n")
	// 直接启动复制的测试 EXE，不经过 cmd 或 PowerShell。
	const nativeTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), nativeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	// 限制退出或取消后继承管道未关闭造成的等待，收尾错误仍按失败处理。
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now()
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Windows 原生传参执行失败（耗时 %s，局部超时 %s，管道收尾上限 %s）：执行错误=%v；上下文错误=%v；进程状态=%v；标准输出=%q；标准错误=%q",
			time.Since(started).Round(time.Millisecond), nativeTimeout, cmd.WaitDelay, err, ctx.Err(), cmd.ProcessState, output, stderr.Bytes())
	}
	var result struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if err = json.Unmarshal(output, &result); err != nil {
		t.Fatalf("原生回显输出无效：%v；标准输出=%q；标准错误=%q", err, output, stderr.Bytes())
	}
	if len(result.Args) != 7 {
		t.Fatalf("原生 dispatch 参数数量错误：期望 7 个，收到 %d 个；参数=%q；标准错误=%q", len(result.Args), result.Args, stderr.Bytes())
	}
	for i, want := range args {
		if result.Args[i] != want {
			t.Fatalf("原生参数被改写或发生环境展开：第 %d 个参数期望=%q；收到=%q；全部参数=%q；标准错误=%q", i+1, want, result.Args[i], result.Args, stderr.Bytes())
		}
	}
	if !bytes.Equal([]byte(result.Stdin), input) {
		t.Fatalf("原生 stdin 字节被改写：期望=%q；收到=%q；标准错误=%q", input, result.Stdin, stderr.Bytes())
	}
	var payload map[string]string
	if err = json.Unmarshal([]byte(result.Stdin), &payload); err != nil || payload["text"] != "中文与 emoji ✅🚀" {
		t.Fatalf("原生 stdin JSON 或 Unicode 内容错误：%v；收到输入=%q；标准错误=%q", err, result.Stdin, stderr.Bytes())
	}
	t.Logf("已逐项核对 7 个原生参数和 %d 字节中文/emoji JSON 输入；耗时=%s；标准错误=%q", len(input), time.Since(started).Round(time.Millisecond), stderr.Bytes())
}

// 仅检查旧命令构造及所有权，不创建或启动任何 shell 进程。
func TestWindowsHookConstructionDoesNotExecuteShell(t *testing.T) {
	exe := filepath.Join("测试目录 中文 & %PULSE% ! $", "bridge.exe")
	config := filepath.Join("配置目录 中文 ' & %PULSE%", "bridge.yaml")
	prefix := quote(exe) + " dispatch --config " + quote(config) + " --adapter 'sample' --hook "
	command := hookCommand(prefix, "Stop")
	expected := powershellCommand + encodePowerShell(powershellInput+prefix+quote("Stop"))
	if command != expected {
		t.Fatal("钩子构造未保留规定的命令与编码边界")
	}
	if !ownsHookCommand(command, prefix) || ownsHookCommand(command, prefix+"other") {
		t.Fatal("钩子所有权边界错误")
	}
	for _, invalid := range []string{"echo keep", powershellCommand + "invalid-base64", powershellCommand + "AA=="} {
		if ownsHookCommand(invalid, prefix) {
			t.Fatalf("误认其他或损坏命令为本机钩子：%q", invalid)
		}
	}
}

// 宿主退出后，其后代仍持有输出管道时必须限时返回，不把不完整探测当成功。
func TestWindowsHostVersionInheritedOutputIsBounded(t *testing.T) {
	own, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("AGENT_PULSE_INSTALL_TEST_HELPER", "inherited-output")
	t.Setenv("AGENT_PULSE_HELPER_PID_FILE", pidFile)
	// 只等待本测试明确记录的辅助后代，避免回归结束后留下进程。
	defer func() {
		raw, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			t.Errorf("读取测试后代 PID 失败：%v", readErr)
			return
		}
		pid, parseErr := strconv.Atoi(string(raw))
		if parseErr != nil {
			t.Errorf("解析测试后代 PID 失败：%v", parseErr)
			return
		}
		process, findErr := os.FindProcess(pid)
		if findErr != nil {
			t.Errorf("查找测试后代失败：%v", findErr)
			return
		}
		_, _ = process.Wait()
	}()
	started := time.Now()
	detected, err := Detect(context.Background(), Manifest{Command: own})
	elapsed := time.Since(started)
	if !errors.Is(err, exec.ErrWaitDelay) || detected != "" {
		t.Fatalf("输出管道未完整关闭时应返回收尾超时：版本=%q，错误=%v，耗时=%s", detected, err, elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("宿主已退出后仍等待输出后代：耗时=%s，期望不超过 2 秒", elapsed)
	}
	t.Logf("宿主已退出、输出后代仍存活时限时失败，未误报版本：耗时=%s", elapsed.Round(time.Millisecond))
}

// 旧授权登记保留，但本轮 Windows 不探测、安装或重写后置的命令与插件来源。
func TestWindowsSyncPreservesDeferredSourcesWithoutInstalling(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	private, public := filepath.Join(root, "publisher.key"), filepath.Join(root, "publisher.pub")
	if err := GenerateKey(private, public); err != nil {
		t.Fatal(err)
	}
	packages := []Package{
		{Manifest: Manifest{ID: "codex", Version: "0.2.1", Format: 1, Protocol: 1, Platform: "windows/amd64", Kind: "executable", Entry: "adapter.exe", Command: "must-not-run", ConfigStyle: "hooks"}, SHA256: strings.Repeat("a", 64), Size: 1},
		{Manifest: Manifest{ID: "opencode", Version: "0.3.0", Format: 1, Protocol: 1, Platform: "any", Kind: "opencode-plugin", Entry: "index.js", Command: "must-not-run"}, SHA256: strings.Repeat("b", 64), Size: 1},
	}
	// 签名流程验证真实包文件，使用合法辅助 EXE 而非伪装可执行文件；本测试不启动它。
	own, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	for i := range packages {
		directory := filepath.Join(root, "input", packages[i].ID)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		payload := executable
		if packages[i].Kind == "opencode-plugin" {
			payload = []byte("export default {};\n")
		}
		if err := os.WriteFile(filepath.Join(directory, packages[i].Entry), payload, 0600); err != nil {
			t.Fatal(err)
		}
		packaged, err := BuildPackage(directory, repository, packages[i].Manifest)
		if err != nil {
			t.Fatal(err)
		}
		packages[i] = packaged
	}
	if err := SignIndex(repository, private, Index{Sequence: 1, Packages: packages}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(repository, "index.json"))
	}))
	defer server.Close()
	key, err := os.ReadFile(public)
	if err != nil {
		t.Fatal(err)
	}
	codexPath, pluginPath := filepath.Join(root, "codex.json"), filepath.Join(root, "opencode.json")
	before := []byte(`{"keep":true}`)
	for _, path := range []string{codexPath, pluginPath} {
		if err := os.WriteFile(path, before, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := config.Bridge{SourceID: "scope-test", HubURL: server.URL, Adapters: config.AdapterUpdates{Directory: filepath.Join(root, "state"), PublicKey: string(key), Managed: map[string]string{"codex": codexPath, "opencode": pluginPath}}}
	if err := Sync(context.Background(), c, filepath.Join(root, "bridge.yaml")); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil || len(state.Installed) != 0 {
		t.Fatalf("后置来源仍被探测或安装：%+v，错误=%v", state, err)
	}
	for _, path := range []string{codexPath, pluginPath} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatal("后置来源用户配置被改写")
		}
	}
}
