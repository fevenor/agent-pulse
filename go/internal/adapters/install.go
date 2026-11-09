package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-pulse/internal/config"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// 只修改明确归本安装器所有的命令。未能解析用户配置时拒绝写入。
func hookConfig(c config.Bridge, configPath string, p Package, raw []byte) ([]byte, error) {
	root := map[string]any{}
	if len(raw) > 0 {
		if json.Unmarshal(raw, &root) != nil || root == nil {
			return nil, errors.New("宿主配置必须为 JSON 对象；不覆盖无效配置")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cfg, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	prefix := quote(exe) + " dispatch --config " + quote(cfg) + " --adapter " + quote(p.ID) + " --hook "
	if p.Kind == "opencode-plugin" {
		loader := filepath.Join(c.Adapters.Directory, "loaders", p.ID)
		entries := []any{}
		if value, exists := root["plugins"]; exists {
			var ok bool
			entries, ok = value.([]any)
			if !ok {
				return nil, errors.New("plugins 必须为数组")
			}
		}
		found := false
		for _, entry := range entries {
			if plugin, ok := entry.(map[string]any); ok && plugin["package"] == loader {
				found = true
				plugin["options"] = map[string]any{"source_id": c.SourceID, "bridge_url": "http://" + c.Listen}
			}
		}
		if !found {
			entries = append(entries, map[string]any{"package": loader, "options": map[string]any{"source_id": c.SourceID, "bridge_url": "http://" + c.Listen}})
		}
		root["plugins"] = entries
	} else {
		key := "hooks"
		if p.ConfigStyle == "named-hooks" {
			key = "agent-pulse-" + p.ID
		}
		events := map[string]any{}
		if value, exists := root[key]; exists {
			var ok bool
			events, ok = value.(map[string]any)
			if !ok {
				return nil, errors.New("hooks 配置必须为对象")
			}
		}
		for event, value := range events {
			entries, ok := value.([]any)
			if !ok {
				if event == "enabled" {
					continue
				}
				return nil, errors.New("hook 事件配置必须为数组")
			}
			kept := []any{}
			for _, entry := range entries {
				group, ok := entry.(map[string]any)
				if !ok {
					return nil, errors.New("hook 条目必须为对象")
				}
				if command, ok := group["command"].(string); ok && strings.HasPrefix(command, prefix) {
					continue
				}
				if list, ok := group["hooks"].([]any); ok {
					rest := []any{}
					for _, item := range list {
						h, ok := item.(map[string]any)
						if !ok {
							return nil, errors.New("hook 命令条目无效")
						}
						command, _ := h["command"].(string)
						if !strings.HasPrefix(command, prefix) {
							rest = append(rest, item)
						}
					}
					if len(rest) == 0 {
						continue
					}
					group["hooks"] = rest
				}
				kept = append(kept, group)
			}
			events[event] = kept
		}
		direct := map[string]bool{}
		for _, event := range p.DirectHooks {
			direct[event] = true
		}
		for event, matcher := range p.Hooks {
			handler := map[string]any{"type": "command", "command": prefix + quote(event), "timeout": 1}
			var entry any = map[string]any{"matcher": matcher, "hooks": []any{handler}}
			if direct[event] {
				entry = handler
			}
			entries, _ := events[event].([]any)
			events[event] = append(entries, entry)
		}
		root[key] = events
	}
	data, err := json.MarshalIndent(root, "", "  ")
	return append(data, '\n'), err
}

type installJournal struct {
	Path    string `json:"path"`
	Before  []byte `json:"before"`
	After   []byte `json:"after"`
	Existed bool   `json:"existed"`
	State   State  `json:"state"`
}

func recoverInstall(dir string) error {
	file := filepath.Join(dir, "pending.json")
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j installJournal
	if json.Unmarshal(raw, &j) != nil {
		return errors.New("安装恢复记录损坏")
	}
	current, err := os.ReadFile(j.Path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if string(current) != string(j.Before) && string(current) != string(j.After) {
		return errors.New("安装中断后宿主配置被外部修改，请保留 pending.json 并人工合并")
	}
	if j.Existed {
		err = AtomicFile(j.Path, j.Before, 0600)
	} else {
		err = os.Remove(j.Path)
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if err = AtomicJSON(filepath.Join(dir, "state.json"), j.State); err != nil {
		return err
	}
	return os.Remove(file)
}

func install(c config.Bridge, configPath string, p Package, before, after State) error {
	path := c.Adapters.Managed[p.ID]
	raw, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, err := hookConfig(c, configPath, p, raw)
	if err != nil {
		return err
	}
	installed := after.Installed[p.ID]
	installed.ConfigBefore = raw
	installed.ConfigAfter = next
	after.Installed[p.ID] = installed
	if p.Kind == "opencode-plugin" {
		// 每个宿主进程加载一次当前包。回执由实际 setup 写入，不把安装当成生效。
		statePath, _ := json.Marshal(filepath.Join(c.Adapters.Directory, "state.json"))
		root, _ := json.Marshal(c.Adapters.Directory)
		id, _ := json.Marshal(p.ID)
		loader := fmt.Sprintf(`import { readFileSync, mkdirSync, writeFileSync, renameSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
const root = %s, id = %s;
const state = JSON.parse(readFileSync(%s, "utf8"));
const p = state.installed[id].package;
const entry = join(root, "packages", id, p.version, p.sha256, p.entry);
const plugin = (await import(pathToFileURL(entry).href)).default;
export default { ...plugin, setup(ctx) {
  const result = plugin.setup(ctx);
  try { const dir = join(root, "runtime"); mkdirSync(dir, {recursive:true});
    const target = join(dir, id + ".json"), tmp = target + "." + process.pid;
    writeFileSync(tmp, JSON.stringify({version:p.version, at:new Date().toISOString()}), {mode:0o600}); renameSync(tmp, target);
  } catch {}
  return result;
}};
`, root, id, statePath)
		if err = AtomicFile(filepath.Join(c.Adapters.Directory, "loaders", p.ID, "index.ts"), []byte(loader), 0600); err != nil {
			return err
		}
	}
	journal := filepath.Join(c.Adapters.Directory, "pending.json")
	if err = AtomicJSON(journal, installJournal{Path: path, Before: raw, After: next, Existed: existed, State: before}); err != nil {
		return err
	}
	// 先固定包，再切换配置；重启若发现 journal 则恢复两者。
	if err = AtomicJSON(filepath.Join(c.Adapters.Directory, "state.json"), after); err == nil {
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
