package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"

	"agent-pulse/internal/config"
	"github.com/tailscale/hujson"
)

// OpenCode允许注释和尾逗号；保留非本软件条目的原始语法，不经浮点数重写用户配置。
func updateOpenCodeJSONC(c config.Bridge, p Package, raw []byte, enable bool) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}\n")
	}
	value, err := hujson.Parse(raw)
	if err != nil {
		return nil, errors.New("OpenCode JSON/JSONC无效；未覆盖原配置")
	}
	obj, ok := value.Value.(*hujson.Object)
	if !ok {
		return nil, errors.New("宿主配置必须为JSON对象；不覆盖无效配置")
	}
	if err := rejectDuplicateJSONKeys(&value); err != nil {
		return nil, err
	}
	index := -1
	for i := range obj.Members {
		if jsonName(obj.Members[i].Name) == "plugins" {
			index = i
			break
		}
	}
	if index < 0 && !enable {
		return raw, nil
	}
	if index < 0 {
		obj.Members = append(obj.Members, hujson.ObjectMember{Name: hujson.Value{Value: hujson.String("plugins"), BeforeExtra: []byte("\n  ")}, Value: hujson.Value{Value: &hujson.Array{}, BeforeExtra: []byte(" ")}})
		index = len(obj.Members) - 1
	}
	array, ok := obj.Members[index].Value.Value.(*hujson.Array)
	if !ok {
		return nil, errors.New("plugins必须为数组；未覆盖原配置")
	}
	loader := filepath.Join(c.Adapters.Directory, "loaders", p.ID)
	found := false
	kept := make([]hujson.Value, 0, len(array.Elements)+1)
	for _, entry := range array.Elements {
		plugin, isObject := entry.Value.(*hujson.Object)
		owned := false
		if isObject {
			for _, member := range plugin.Members {
				if jsonName(member.Name) == "package" && jsonName(member.Value) == loader {
					owned = true
				}
			}
		}
		if !owned {
			kept = append(kept, entry)
			continue
		}
		if !enable || found {
			continue
		}
		found = true
		data, _ := json.Marshal(map[string]any{"source_id": c.SourceID, "bridge_url": "http://" + c.Listen})
		options, _ := hujson.Parse(data)
		optionsIndex := -1
		for i := range plugin.Members {
			if jsonName(plugin.Members[i].Name) == "options" {
				optionsIndex = i
				break
			}
		}
		if optionsIndex >= 0 {
			plugin.Members[optionsIndex].Value.Value = options.Value
		} else {
			plugin.Members = append(plugin.Members, hujson.ObjectMember{Name: hujson.Value{Value: hujson.String("options")}, Value: options})
		}
		kept = append(kept, entry)
	}
	if enable && !found {
		data, _ := json.Marshal(map[string]any{"package": loader, "options": map[string]any{"source_id": c.SourceID, "bridge_url": "http://" + c.Listen}})
		entry, _ := hujson.Parse(data)
		entry.BeforeExtra = []byte("\n    ")
		kept = append(kept, entry)
	}
	array.Elements = kept
	return value.Pack(), nil
}

func jsonName(v hujson.Value) string {
	var name string
	if literal, ok := v.Value.(hujson.Literal); ok {
		_ = json.Unmarshal(literal, &name)
	}
	return name
}

// 重复键会让宿主与安装器看到不同设置，必须拒绝而不是悄悄选择最后一项。
func rejectDuplicateJSONKeys(v *hujson.Value) error {
	if obj, ok := v.Value.(*hujson.Object); ok {
		seen := map[string]bool{}
		for i := range obj.Members {
			name := jsonName(obj.Members[i].Name)
			if seen[name] {
				return errors.New("配置含重复JSON键；未覆盖原配置")
			}
			seen[name] = true
			if err := rejectDuplicateJSONKeys(&obj.Members[i].Value); err != nil {
				return err
			}
		}
	}
	if array, ok := v.Value.(*hujson.Array); ok {
		for i := range array.Elements {
			if err := rejectDuplicateJSONKeys(&array.Elements[i]); err != nil {
				return err
			}
		}
	}
	return nil
}
