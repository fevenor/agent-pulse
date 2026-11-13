package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-pulse/internal/config"
	"agent-pulse/internal/transport"
)

type Installed struct {
	Target       string   `json:"target"`
	Package      Package  `json:"package"`
	Previous     *Package `json:"previous,omitempty"`
	AgentVersion string   `json:"agent_version"`
	Error        string   `json:"error,omitempty"`
	ConfigBefore []byte   `json:"config_before,omitempty"`
	ConfigAfter  []byte   `json:"config_after,omitempty"`
}
type State struct {
	Sequence  uint64               `json:"sequence"`
	IndexHash string               `json:"index_hash"`
	Installed map[string]Installed `json:"installed"`
	Rejected  map[string]string    `json:"rejected"`
}
type Runtime struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}
type Status struct {
	Warning      string `json:"warning,omitempty"`
	ID           string `json:"id"`
	Target       string `json:"target"`
	Installed    string `json:"installed"`
	Running      string `json:"running"`
	AgentVersion string `json:"agent_version"`
	Error        string `json:"error,omitempty"`
}
type Report struct {
	SourceID  string    `json:"source_id"`
	Items     []Status  `json:"items"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func loadState(dir string) (State, error) {
	s := State{Installed: map[string]Installed{}, Rejected: map[string]string{}}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if json.Unmarshal(raw, &s) != nil || s.Installed == nil || s.Rejected == nil {
		return s, errors.New("适配器本地状态损坏")
	}
	return s, nil
}
func packageDir(root string, p Package) string {
	return filepath.Join(root, "packages", p.ID, p.Version, p.SHA256)
}
func version(raw string) ([3]int, bool) {
	var v [3]int
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts := strings.Split(strings.Split(raw, "-")[0], ".")
	if len(parts) < 2 || len(parts) > 3 {
		return v, false
	}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
func compare(a, b string) int {
	av, _ := version(a)
	bv, _ := version(b)
	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

// 宿主版本承诺按主、次、补丁区分；清单端点保留为已验证版本线，不再硬锁补丁。
func hostCompatibility(raw string, m Manifest) (string, error) {
	actual, ok := version(raw)
	if !ok {
		return "", errors.New("无法识别宿主版本")
	}
	lower, upper := [3]int{}, [3]int{}
	hasLower, hasUpper := m.AgentMin != "", m.AgentMax != ""
	if hasLower {
		lower, ok = version(m.AgentMin)
		if !ok {
			return "", errors.New("宿主兼容版本下限无效")
		}
	}
	if hasUpper {
		upper, ok = version(m.AgentMax)
		if !ok {
			return "", errors.New("宿主兼容版本上限无效")
		}
	}
	// 仅有一个端点时，该端点的主版本就是已验证主版本，不能无界接受下一主版本。
	if hasLower && !hasUpper {
		upper = lower
	}
	if hasUpper && !hasLower {
		lower = upper
	}
	if (hasLower || hasUpper) && (actual[0] < lower[0] || actual[0] > upper[0]) {
		return "", errors.New("宿主主版本未验证，需要重新测试后纳入支持")
	}
	lineCompare := func(a, b [3]int) int {
		for i := 0; i < 2; i++ {
			if a[i] < b[i] {
				return -1
			}
			if a[i] > b[i] {
				return 1
			}
		}
		return 0
	}
	warning := ""
	if (hasLower || hasUpper) && (lineCompare(actual, lower) < 0 || lineCompare(actual, upper) > 0) {
		warning = "宿主次版本未验证：允许继续使用，但不承诺兼容性"
	}
	// 预发布后缀不是补丁版本承诺，仍可使用但明确提示。
	if strings.Contains(raw, "-") {
		warning = "宿主为预发布版本：允许继续使用，但不承诺兼容性"
	}
	return warning, nil
}

func Detect(ctx context.Context, m Manifest) (string, error) {
	if m.ID == "zcode" {
		return zcodeDesktopVersion()
	}
	path, err := exec.LookPath(m.Command)
	if err != nil {
		return "", errors.New("未安装宿主命令")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := hostCommand(ctx, path, "--version")
	// 宿主退出或取消后，其后代可能仍持有输出管道；限时收尾并保留失败。
	cmd.WaitDelay = time.Second
	var out limitedBuffer
	out.limit = 4096
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("宿主版本探测失败：%w", err)
	}
	for _, field := range strings.Fields(out.String()) {
		if _, ok := version(field); ok {
			return strings.TrimPrefix(field, "v"), nil
		}
	}
	return "", errors.New("无法识别宿主版本")
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("输出超过上限")
	}
	return b.Buffer.Write(p)
}

func get(ctx context.Context, client *http.Client, base, token, path string, headers map[string]string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("更新源不可达")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, errors.New("更新源返回失败")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, resp.StatusCode, errors.New("响应大小无效")
	}
	return data, resp.StatusCode, nil
}
func download(ctx context.Context, c config.Bridge, p Package) ([]byte, error) {
	var data []byte
	client := transport.Client(15 * time.Second)
	for offset := int64(0); offset < p.Size; offset += 256 << 10 {
		end := offset + (256 << 10) - 1
		if end >= p.Size {
			end = p.Size - 1
		}
		headers := map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", offset, end)}
		chunk, status, err := get(ctx, client, c.HubURL, c.Token, "/api/v1/adapters/packages/"+p.SHA256, headers, 256<<10)
		if err != nil && c.RelayURL != "" {
			chunk, status, err = get(ctx, client, c.RelayURL, c.RelayToken, "/api/v1/adapters/packages/"+p.SHA256, headers, 256<<10)
		}
		if err != nil || status != 206 || int64(len(chunk)) != end-offset+1 {
			return nil, errors.New("分块下载失败")
		}
		data = append(data, chunk...)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != p.SHA256 {
		return nil, errors.New("适配包摘要不匹配")
	}
	return data, nil
}

// Sync 只应由单个 Bridge 进程调用；dispatch 读取原子替换后的状态，不持有安装锁。
func Sync(ctx context.Context, c config.Bridge, configPath string) error {
	dir := c.Adapters.Directory
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	unlock, err := lockInstall(dir)
	if err != nil {
		return errors.New("适配器安装正在进行")
	}
	defer unlock()
	if err = recoverInstall(dir); err != nil {
		return err
	}
	state, err := loadState(dir)
	if err != nil {
		return err
	}
	raw, _, err := get(ctx, transport.Client(10*time.Second), c.HubURL, c.Token, "/api/v1/adapters/index", nil, 1<<20)
	if err != nil && c.RelayURL != "" {
		raw, _, err = get(ctx, transport.Client(10*time.Second), c.RelayURL, c.RelayToken, "/api/v1/adapters/index", nil, 1<<20)
	}
	if err != nil {
		return err
	}
	publisherKey := strings.TrimSpace(c.Adapters.PublicKey)
	if publisherKey == "" {
		keyBytes, keyErr := os.ReadFile(filepath.Join(dir, "publisher.pub"))
		if keyErr != nil {
			return fmt.Errorf("发布公钥未配置：设置 adapters.public_key 或将公钥文件放入 %s", filepath.Join(dir, "publisher.pub"))
		}
		publisherKey = strings.TrimSpace(string(keyBytes))
	}
	index, err := Verify(raw, publisherKey)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	if index.Sequence < state.Sequence || index.Sequence == state.Sequence && state.IndexHash != "" && state.IndexHash != hash {
		return errors.New("发布索引回退或同序号内容发生变化")
	}
	if index.Sequence > state.Sequence {
		state.Rejected = map[string]string{}
	}
	state.Sequence = index.Sequence
	state.IndexHash = hash
	candidates := map[string][]Package{}
	excluded := map[string]bool{}
	for _, id := range c.Adapters.Exclude {
		excluded[id] = true
	}
	for _, p := range index.Packages {
		// Windows RC 只安装原生进程钩子，保留旧登记但不重新探测或更新后置的命令/插件来源。
		if runtime.GOOS == "windows" && (p.Kind != "executable" || p.ConfigStyle != "process-hooks") {
			continue
		}
		if c.Adapters.Managed[p.ID] == "" {
			continue
		}
		if excluded[p.ID] {
			continue
		}
		pin := c.Adapters.Pins[p.ID]
		if pin != "" && pin != p.Version {
			continue
		}
		candidates[p.ID] = append(candidates[p.ID], p)
	}
	ids := []string{}
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		packages := candidates[id]
		sort.Slice(packages, func(i, j int) bool { return compare(packages[i].Version, packages[j].Version) > 0 })
		p := packages[0]
		agentVersion := ""
		var compatibilityErr error
		compatible := false
		// 每条发布线可支持不同宿主版本；不能让不兼容的新包挡住仍可用的旧包。
		for _, candidate := range packages {
			if candidate.Platform != runtime.GOOS+"/"+runtime.GOARCH && candidate.Platform != "any" {
				compatibilityErr = errors.New("没有适用于当前平台的包")
				continue
			}
			if compare(BridgeVersion, candidate.MinBridge) < 0 {
				compatibilityErr = errors.New("目标包需要更新的 Bridge 版本")
				continue
			}
			v, detectErr := Detect(ctx, candidate.Manifest)
			if detectErr != nil {
				compatibilityErr = detectErr
				continue
			}
			agentVersion = v
			if _, versionErr := hostCompatibility(v, candidate.Manifest); versionErr != nil {
				compatibilityErr = versionErr
				continue
			}
			p = candidate
			compatible = true
			break
		}
		old := state.Installed[id]
		old.Target = p.Version
		old.AgentVersion = agentVersion
		if !compatible {
			old.Error = compatibilityErr.Error()
			state.Installed[id] = old
			continue
		}
		state.Installed[id] = old
		if old.Package.Version == p.Version && old.Package.SHA256 == p.SHA256 {
			if state.Rejected[p.SHA256] == "" {
				old.Error = ""
				state.Installed[id] = old
			}
			continue
		}
		if old.Package.Version == p.Version && old.Package.SHA256 != "" {
			old.Error = "同一版本包内容不可修改，请发布新版本"
			state.Installed[id] = old
			continue
		}
		if reason := state.Rejected[p.SHA256]; reason != "" {
			old.Error = reason
			state.Installed[id] = old
			continue
		}
		data, err := download(ctx, c, p)
		if err != nil {
			old.Error = err.Error()
			state.Installed[id] = old
			continue
		}
		tmp, err := os.MkdirTemp(dir, ".install-*")
		if err != nil {
			return err
		}
		err = Extract(data, tmp, p.Manifest)
		if err == nil {
			target := packageDir(dir, p)
			if err = os.MkdirAll(filepath.Dir(target), 0700); err == nil {
				if _, exists := os.Stat(target); exists == nil {
					err = os.RemoveAll(tmp)
				} else {
					err = os.Rename(tmp, target)
				}
			}
		}
		_ = os.RemoveAll(tmp)
		if err != nil {
			old.Error = err.Error()
			state.Rejected[p.SHA256] = err.Error()
			state.Installed[id] = old
			continue
		}
		next := Installed{Target: p.Version, Package: p, AgentVersion: agentVersion}
		if old.Package.SHA256 != "" {
			previous := old.Package
			next.Previous = &previous
		}
		before := cloneState(state)
		state.Installed[id] = next
		if err = install(c, configPath, p, before, state); err != nil {
			state = before
			old.Error = err.Error()
			state.Installed[id] = old
			state.Rejected[p.SHA256] = err.Error()
		}
	}
	return AtomicJSON(filepath.Join(dir, "state.json"), state)
}

func ReportState(c config.Bridge, lastErr error) Report {
	r := Report{SourceID: c.SourceID, Items: []Status{}, UpdatedAt: time.Now().UTC()}
	if lastErr != nil {
		r.Error = lastErr.Error()
	}
	s, err := loadState(c.Adapters.Directory)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	for id, p := range s.Installed {
		status := Status{ID: id, Installed: p.Package.Version, Target: p.Target, AgentVersion: p.AgentVersion, Error: p.Error}
		if p.Package.Version != "" && p.AgentVersion != "" {
			status.Warning, _ = hostCompatibility(p.AgentVersion, p.Package.Manifest)
		}
		var running Runtime
		if raw, err := os.ReadFile(filepath.Join(c.Adapters.Directory, "runtime", id+".json")); err == nil {
			_ = json.Unmarshal(raw, &running)
			status.Running = running.Version
		}
		r.Items = append(r.Items, status)
	}
	sort.Slice(r.Items, func(i, j int) bool { return r.Items[i].ID < r.Items[j].ID })
	return r
}
func Run(ctx context.Context, c config.Bridge, path string) {
	if c.Adapters.Directory == "" {
		return
	}
	var lastErr error
	next := time.Time{}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if time.Now().After(next) {
			lastErr = Sync(ctx, c, path)
			interval := c.Adapters.PollSeconds
			if interval == 0 {
				interval = 300
			}
			next = time.Now().Add(time.Duration(interval)*time.Second + time.Duration(rand.IntN(max(1, interval/5)))*time.Second)
		}
		report := ReportState(c, lastErr)
		_, err := transport.Request(ctx, transport.Client(5*time.Second), c.HubURL, c.Token, "POST", "/api/v1/adapters/status", report, nil)
		if err != nil {
			next = time.Time{}
		}
		if err != nil && c.RelayURL != "" {
			_, _ = transport.Request(ctx, transport.Client(5*time.Second), c.RelayURL, c.RelayToken, "POST", "/api/v1/adapters/status", report, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func Dispatch(ctx context.Context, c config.Bridge, id, hook string, input []byte) ([]byte, error) {
	if !identifier.MatchString(id) {
		return nil, errors.New("适配器 ID 无效")
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		return nil, err
	}
	installed, ok := state.Installed[id]
	if !ok || installed.Package.Kind != "executable" {
		return nil, errors.New("适配器未安装或不是命令 hook")
	}
	p := installed.Package
	entry := filepath.Join(packageDir(c.Adapters.Directory, p), filepath.FromSlash(p.Entry))
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	cmd := hostCommand(ctx, entry, "--hook", hook, "--source", c.SourceID)
	cmd.Stdin = bytes.NewReader(input)
	var output limitedBuffer
	output.limit = 65536
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		_ = rollback(c, id, p.SHA256, "适配包入口无法启动")
		return nil, errors.New("适配包入口无法启动，已尝试回滚")
	}
	if err = cmd.Wait(); err != nil {
		return nil, errors.New("适配器转换失败或超时")
	}
	_ = AtomicJSON(filepath.Join(c.Adapters.Directory, "runtime", id+".json"), Runtime{Version: p.Version, At: time.Now().UTC()})
	return output.Bytes(), nil
}

func Launch(ctx context.Context, c config.Bridge, id string, args []string) error {
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		return err
	}
	installed, ok := state.Installed[id]
	if !ok || installed.Package.Kind != "executable" {
		return errors.New("没有可启动的适配包")
	}
	p := installed.Package
	cmd := hostCommand(ctx, filepath.Join(packageDir(c.Adapters.Directory, p), filepath.FromSlash(p.Entry)), append([]string{"run"}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cloneState(s State) State {
	var copy State
	raw, _ := json.Marshal(s)
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func rollback(c config.Bridge, id, failedHash, reason string) error {
	unlock, err := lockInstall(c.Adapters.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	if err = recoverInstall(c.Adapters.Directory); err != nil {
		return err
	}
	s, err := loadState(c.Adapters.Directory)
	if err != nil {
		return err
	}
	v := s.Installed[id]
	if v.Package.SHA256 != failedHash {
		return nil // 失败的是已被后续更新替换的包，不能回滚新版本。
	}
	before := cloneState(s)
	s.Rejected[v.Package.SHA256] = reason
	path := ""
	var current, target []byte
	if v.Previous != nil {
		path = c.Adapters.Managed[id]
		raw, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(raw, v.ConfigAfter) {
			return errors.New("配置已变化，未自动回滚；请保留当前配置并手动处理")
		}
		current = raw
		target = v.ConfigBefore
		v.Package = *v.Previous
		v.Previous = nil
	}
	v.Error = reason
	s.Installed[id] = v
	if path != "" {
		journal := filepath.Join(c.Adapters.Directory, "pending.json")
		if err = AtomicJSON(journal, installJournal{Path: path, Before: current, After: target, Existed: true, State: before}); err != nil {
			return err
		}
		if err = AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), s); err == nil {
			err = AtomicFile(path, target, 0600)
		}
		if err != nil {
			_ = recoverInstall(c.Adapters.Directory)
			return err
		}
		return os.Remove(journal)
	}
	return AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), s)
}

// Disable 从当前有效配置中移除本工具所属钩子，保留用户后续增加的其他配置。
func Disable(c config.Bridge, id, configPath string) error {
	path := c.Adapters.Managed[id]
	if c.Adapters.Directory == "" || path == "" {
		return errors.New("该 Agent 未在 adapters.managed 中启用")
	}
	unlock, err := lockInstall(c.Adapters.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	if err = recoverInstall(c.Adapters.Directory); err != nil {
		return err
	}
	state, err := loadState(c.Adapters.Directory)
	if err != nil {
		return err
	}
	installed, ok := state.Installed[id]
	if !ok || len(installed.ConfigAfter) == 0 {
		return errors.New("找不到安装记录，未修改 Agent 配置")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, err := updateHookConfig(c, configPath, installed.Package, current, false)
	if err != nil {
		return fmt.Errorf("无法安全移除 AgentPulse 钩子，未修改 Agent 配置：%w", err)
	}
	before := cloneState(state)
	delete(state.Installed, id)
	journal := filepath.Join(c.Adapters.Directory, "pending.json")
	if err = AtomicJSON(journal, installJournal{Path: path, Before: current, After: next, Existed: true, State: before}); err != nil {
		return err
	}
	if err = AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), state); err == nil {
		err = AtomicFile(path, next, 0600)
	}
	if err != nil {
		if recovered := recoverInstall(c.Adapters.Directory); recovered != nil {
			return recovered
		}
		return err
	}
	return os.Remove(journal)
}
