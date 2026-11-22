package adapters

import (
	"agent-pulse/internal/config"
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
)

// setupCatalog 的来源是可信签名清单；控制入口可定义新Agent的名称、探测及默认路径。
// 只预备目录和包缓存，绝不在用户选择之前改动任何宿主配置。
func setupCatalog(ctx context.Context, c config.Bridge, configPath string) ([]setupAgent, error) {
	unlock, err := lockSync(c.Adapters.Directory)
	if err != nil {
		return nil, err
	}
	defer unlock()
	index, err := cachedIndex(c)
	if err != nil {
		return scanSetupAgents(ctx), err
	}
	byID := map[string][]Package{}
	for _, p := range index.Packages {
		if p.Platform == runtime.GOOS+"/"+runtime.GOARCH || p.Platform == "any" {
			if runtime.GOOS == "windows" && p.Control == "" && p.Kind != "executable" {
				continue
			}
			byID[p.ID] = append(byID[p.ID], p)
		}
	}
	ids := []string{}
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	home, _ := os.UserHomeDir()
	defaults := map[string]setupAgent{}
	for _, item := range setupAgentCatalog(home, runtime.GOOS) {
		defaults[item.ID] = item
	}
	items := []setupAgent{}
	var problems []string
	for _, id := range ids {
		packages := byID[id]
		sort.Slice(packages, func(i, j int) bool { return compare(packages[i].Version, packages[j].Version) > 0 })
		p := packages[0]
		item := defaults[id]
		item.ID = id
		item.Manifest = p.Manifest
		item.Command = p.Command
		if p.Name != "" {
			item.Name = p.Name
		}
		if item.Name == "" {
			item.Name = id
		}
		if c.Adapters.Managed[id] != "" {
			item.ConfigPath = c.Adapters.Managed[id]
		}
		for _, candidate := range packages {
			if candidate.Control != "" {
				if e := preparePackage(ctx, c, candidate); e != nil {
					problems = append(problems, id+"："+e.Error())
					continue
				}
				reply, e := control(ctx, c, configPath, candidate, "probe", nil, false)
				if e != nil {
					problems = append(problems, id+"："+e.Error())
					continue
				}
				if reply.Name != "" {
					item.Name = reply.Name
				}
				if reply.ConfigPath != "" {
					item.ConfigPath = reply.ConfigPath
				}
				item.Deferred = reply.Deferred
				item.Notice = reply.Notice
				if reply.Version != "" {
					if _, e = hostCompatibility(reply.Version, candidate.Manifest); e == nil {
						item.Manifest = candidate.Manifest
						item.Version = reply.Version
						item.Detected = true
						break
					}
				}
			} else {
				v, e := Detect(ctx, candidate.Manifest)
				if e == nil {
					item.Version = v
					item.Detected = true
					break
				}
			}
		}
		items = append(items, item)
	}
	// 清单暂时撤下的已授权来源仍可停用；不凭空丢失用户的维护入口。
	for id, path := range c.Adapters.Managed {
		if _, ok := byID[id]; ok {
			continue
		}
		state, e := loadState(c.Adapters.Directory)
		if e != nil {
			continue
		}
		installed := state.Installed[id]
		name := installed.Package.Name
		if name == "" {
			name = id
		}
		items = append(items, setupAgent{ID: id, Name: name, ConfigPath: path, Manifest: installed.Package.Manifest})
	}
	if len(problems) > 0 {
		return items, fmt.Errorf("部分适配控制入口不可用：%v", problems)
	}
	return items, nil
}
