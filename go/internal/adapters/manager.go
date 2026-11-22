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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	Warning      string   `json:"warning,omitempty"`
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
	if m.ID == "antigravity" {
		if v, err := antigravityDesktopVersion(); err == nil {
			return v, nil
		}
	}
	if m.ID == "codex" {
		if _, pathErr := exec.LookPath(m.Command); pathErr != nil {
			if path := codexDesktopCommand(); path != "" {
				m.Command = path
			}
		}
	}
	if m.ID == "zcode" {
		return zcodeHostVersion()
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
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.StatusCode, nil
	}
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
	cache := filepath.Join(c.Adapters.Directory, "downloads", p.SHA256+".zip")
	if data, err := readBoundedFile(cache, p.Size); err == nil && validatePackageData(data, p) == nil {
		return data, nil
	}
	partial := filepath.Join(c.Adapters.Directory, "downloads", p.SHA256+".part")
	var data []byte
	if p.Size <= 0 || p.Size > MaxPackageBytes || !hashPattern.MatchString(p.SHA256) {
		return nil, errors.New("适配包大小或摘要无效")
	}
	if info, err := os.Stat(partial); err == nil && info.Size() <= p.Size && (info.Size()%(256<<10) == 0 || info.Size() == p.Size) {
		data, _ = os.ReadFile(partial)
	}
	client := transport.Client(15 * time.Second)
	for offset := int64(len(data)); offset < p.Size; offset += 256 << 10 {
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
		if err := AtomicFile(partial, data, 0600); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != p.SHA256 {
		_ = os.Remove(partial)
		return nil, errors.New("适配包摘要不匹配")
	}
	if err := AtomicFile(cache, data, 0600); err != nil {
		return nil, err
	}
	_ = os.Remove(partial)
	return data, nil
}

// Sync 每次启动检查一次；失败仍尝试应用本地可信副本，并返回检查警告。
func Sync(ctx context.Context, c config.Bridge, configPath string) error {
	return SyncWithOptions(ctx, c, configPath, true)
}
func SyncWithOptions(ctx context.Context, c config.Bridge, configPath string, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if c.Adapters.Directory == "" {
		return nil
	}
	checkErr := RefreshCatalog(ctx, c, force)
	if err := ApplyCached(ctx, c, configPath); err != nil {
		return err
	}
	return checkErr
}

// ApplyCached 不检查远端索引；setup仅传入本次明确选择的 managed。
func ApplyCached(ctx context.Context, c config.Bridge, configPath string) error {
	dir := c.Adapters.Directory
	if dir == "" {
		return nil
	}
	unlock, err := lockSync(dir)
	if err != nil {
		return errors.New("适配器同步正在进行")
	}
	defer unlock()
	commit, err := lockInstall(dir)
	if err != nil {
		return errors.New("适配器提交正在进行")
	}
	err = recoverInstall(dir)
	commit()
	if err != nil {
		return err
	}
	state, err := loadState(dir)
	if err != nil {
		return err
	}
	index, err := cachedIndex(c)
	if err != nil {
		return err
	}
	candidates := map[string][]Package{}
	excluded := map[string]bool{}
	for _, id := range c.Adapters.Exclude {
		excluded[id] = true
	}
	for _, p := range index.Packages {
		// 无控制入口的既有Windows包保留原安装范围；新控制包按平台筛选，不使用产品名单。
		if runtime.GOOS == "windows" && p.Control == "" && (p.Kind != "executable" || (p.ConfigStyle != "process-hooks" && p.ID != "codex" && p.ID != "antigravity")) {
			continue
		}
		if p.Control == "" && p.ID == "codex" && windowsDefersCodex(c.Adapters.Managed[p.ID], runtime.GOOS) {
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
		deferredNotice := ""
		// 每条发布线可支持不同宿主版本；不能让不兼容的新包挡住仍可用的旧包。
		for _, candidate := range packages {
			// 已保存的旧JSONC解析误判可在当前配置合法时解除，真正坏入口不解除。
			if candidate.Kind == "opencode-plugin" && state.Rejected[candidate.SHA256] == "宿主配置必须为 JSON 对象；不覆盖无效配置" {
				if raw, e := os.ReadFile(c.Adapters.Managed[id]); e == nil {
					if _, e = hookConfig(c, configPath, candidate, raw); e == nil {
						delete(state.Rejected, candidate.SHA256)
					}
				}
			}
			if reason := state.Rejected[candidate.SHA256]; reason != "" {
				compatibilityErr = errors.New(reason)
				continue
			}
			if candidate.Platform != runtime.GOOS+"/"+runtime.GOARCH && candidate.Platform != "any" {
				compatibilityErr = errors.New("没有适用于当前平台的包")
				continue
			}
			if candidate.Control != "" {
				if e := preparePackage(ctx, c, candidate); e != nil {
					compatibilityErr = e
					continue
				}
			}
			v, detectErr := detectPackage(ctx, c, candidate)
			if detectErr != nil {
				var deferred *deferredPackage
				if errors.As(detectErr, &deferred) {
					deferredNotice = deferred.notice
					if _, ok := version(v); ok {
						if _, e := hostCompatibility(v, candidate.Manifest); e == nil {
							p = candidate
							agentVersion = v
							compatible = true
						}
					}
					break
				}
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
		if deferredNotice != "" {
			old.Error = ""
			old.Warning = deferredNotice
			state.Installed[id] = old
			if compatible && (old.Package.Version != p.Version || old.Package.SHA256 != p.SHA256) {
				if old.Package.Version == p.Version && old.Package.SHA256 != "" {
					old.Error = "同一版本包内容不可修改"
					state.Installed[id] = old
					continue
				}
				next := Installed{Target: p.Version, Package: p, AgentVersion: agentVersion, Warning: deferredNotice}
				if old.Package.SHA256 != "" {
					previous := old.Package
					next.Previous = &previous
				}
				before := cloneState(state)
				state.Installed[id] = next
				if err = installPackageOnly(c, p, before, state); err != nil {
					state = before
					old.Error = err.Error()
					state.Installed[id] = old
				}
			}
			continue
		}
		old.Warning = ""
		if !compatible {
			old.Error = compatibilityErr.Error()
			state.Installed[id] = old
			continue
		}
		state.Installed[id] = old
		if old.Package.Version == p.Version && old.Package.SHA256 == p.SHA256 {
			if !reflect.DeepEqual(old.Package, p) {
				old.Error = "同一版本包元数据不可修改，请发布新版本"
				state.Installed[id] = old
				continue
			}
			if state.Rejected[p.SHA256] == "" {
				old.Error = ""
				state.Installed[id] = old
				// 包版本相同不代表宿主配置仍有效：重新启用、路径变更或用户删除钩子时修复配置。
				if err := ensureInstalledConfig(c, configPath, p, &state); err != nil {
					old.Error = err.Error()
					state.Installed[id] = old
				}
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
		if err := preparePackage(ctx, c, p); err != nil {
			old.Error = err.Error()
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
			// 此时包已验签、校验并解包；宿主配置或文件写入失败不是坏包，允许后续重试。
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
		status := Status{ID: id, Installed: p.Package.Version, Target: p.Target, AgentVersion: p.AgentVersion, Error: p.Error, Warning: p.Warning}
		if p.Package.Control == "" && id == "codex" && windowsDefersCodex(c.Adapters.Managed[id], runtime.GOOS) {
			status.Warning = "由WSL管理共享接入，Windows保留现有钩子"
			status.Error = ""
		}
		if status.Warning == "" && p.Package.Version != "" && p.AgentVersion != "" {
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
func Run(ctx context.Context, c config.Bridge, path string, reconnect <-chan struct{}) {
	if c.Adapters.Directory == "" {
		return
	}
	var lastErr error
	selected, selectionErr := currentAdapterSelection(c, path)
	lastErr = selectionErr
	if selectionErr == nil {
		lastErr = Sync(ctx, selected, path)
	}
	lastSent := time.Time{}
	lastReport := ""
	nextReport := time.Time{}
	retry := 5 * time.Second
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		report := ReportState(c, lastErr)
		report.UpdatedAt = time.Time{}
		raw, _ := json.Marshal(report)
		if time.Now().After(nextReport) && (string(raw) != lastReport || time.Since(lastSent) >= 5*time.Minute) {
			report.UpdatedAt = time.Now().UTC()
			_, err := transport.Request(ctx, transport.Client(5*time.Second), c.HubURL, c.Token, "POST", "/api/v1/adapters/status", report, nil)
			if err != nil && c.RelayURL != "" {
				_, err = transport.Request(ctx, transport.Client(5*time.Second), c.RelayURL, c.RelayToken, "POST", "/api/v1/adapters/status", report, nil)
			}
			if err == nil {
				lastReport = string(raw)
				lastSent = time.Now()
				retry = 5 * time.Second
				nextReport = time.Time{}
			} else {
				nextReport = time.Now().Add(retry)
				retry *= 2
				if retry > time.Minute {
					retry = time.Minute
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-reconnect:
			lastSent = time.Time{}
			nextReport = time.Time{}
		case <-ticker.C:
		}
	}
}

func Dispatch(ctx context.Context, c config.Bridge, id, hook string, input []byte) ([]byte, error) {
	// 选版等待与转换共享原有两秒预算，不叠加延长宿主hook等待。
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !identifier.MatchString(id) {
		return nil, errors.New("适配器 ID 无效")
	}
	state, err := dispatchState(ctx, c.Adapters.Directory)
	if err != nil {
		return nil, err
	}
	installed, ok := state.Installed[id]
	if !ok || installed.Package.Kind != "executable" {
		return nil, errors.New("适配器未安装或不是命令 hook")
	}
	p := installed.Package
	entry := filepath.Join(packageDir(c.Adapters.Directory, p), filepath.FromSlash(p.Entry))
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

func cloneState(s State) State {
	var copy State
	raw, _ := json.Marshal(s)
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func rollback(c config.Bridge, id, failedHash, reason string) error {
	syncUnlock, syncErr := lockSync(c.Adapters.Directory)
	if syncErr != nil {
		return syncErr
	}
	defer syncUnlock()
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
			if !bytes.Equal(current, target) {
				err = AtomicFile(path, target, 0600)
			}
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
	syncUnlock, syncErr := lockSync(c.Adapters.Directory)
	if syncErr != nil {
		return syncErr
	}
	defer syncUnlock()
	path := c.Adapters.Managed[id]
	if c.Adapters.Directory == "" || path == "" {
		return errors.New("该 Agent 未在 adapters.managed 中启用")
	}
	unlock, err := lockInstall(c.Adapters.Directory)
	if err != nil {
		return err
	}
	err = recoverInstall(c.Adapters.Directory)
	unlock()
	if err != nil {
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
	commit, err := lockInstall(c.Adapters.Directory)
	if err != nil {
		return err
	}
	defer commit()
	latest, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if !bytes.Equal(latest, current) {
		return errors.New("宿主配置已外部修改，未停用")
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

// 向导可能仍在展示最终报告；后台同步须沿用磁盘上的最新选择，不能重新装回刚停用的钩子。
// 运行身份、目录或监听变化交由 Tray 重载，不在旧接入进程中跨配置接管服务。
func currentAdapterSelection(running config.Bridge, path string) (config.Bridge, error) {
	latest, err := config.LoadBridge(path)
	if err != nil {
		return running, errors.New("本机配置不可读取，已暂停适配包同步")
	}
	if latest.SourceID != running.SourceID || latest.Listen != running.Listen || latest.Adapters.Directory != running.Adapters.Directory {
		return running, errors.New("本机运行配置已变化，等待接入重新加载")
	}
	running.Adapters.Managed = latest.Adapters.Managed
	running.Adapters.Pins = latest.Adapters.Pins
	running.Adapters.Exclude = latest.Adapters.Exclude
	return running, nil
}
