// Package config 只支持简单 YAML，并在解析结构之后展开显式环境变量。
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type SourceLabel struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
}

type Hub struct {
	Sources            map[string]SourceLabel `json:"sources"`
	Listen             string                 `json:"listen"`
	Database           string                 `json:"database"`
	Token              string                 `json:"token"`
	ReminderTTLSeconds int                    `json:"reminder_ttl_seconds"`
	Push               []Channel              `json:"push"`
	RelayURL           string                 `json:"relay_url"`
	RelayToken         string                 `json:"relay_token"`
	WebDirectory       string                 `json:"web_directory"`
	AdapterDirectory   string                 `json:"adapter_directory"`
}

type Relay struct {
	Listen      string `json:"listen"`
	Database    string `json:"database"`
	Token       string `json:"token"`
	QueueEvents int    `json:"queue_events"`
	QueueBytes  int64  `json:"queue_bytes"`
}

type Channel struct {
	Name           string            `json:"name"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Format         string            `json:"format"`
	Headers        map[string]string `json:"headers"`
	Query          map[string]string `json:"query"`
	Fields         map[string]string `json:"fields"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxTitleChars  int               `json:"max_title_chars"`
	MaxBodyChars   int               `json:"max_body_chars"`
}

type Bridge struct {
	Listen      string         `json:"listen"`
	SourceID    string         `json:"source_id"`
	HubURL      string         `json:"hub_url"`
	Token       string         `json:"token"`
	QueueEvents int            `json:"queue_events"`
	QueueBytes  int            `json:"queue_bytes"`
	RelayURL    string         `json:"relay_url"`
	RelayToken  string         `json:"relay_token"`
	Adapters    AdapterUpdates `json:"adapters"`
}

type AdapterUpdates struct {
	Directory   string            `json:"directory"`
	PublicKey   string            `json:"public_key"`
	PollSeconds int               `json:"poll_seconds"`
	Pins        map[string]string `json:"pins"`
	Exclude     []string          `json:"exclude"`
	Managed     map[string]string `json:"managed"`
}

func LoadRelay(path string) (Relay, error) {
	c := Relay{Listen: "127.0.0.1:18701", Database: "relay.db", QueueEvents: 100000, QueueBytes: 512 << 20}
	if err := load(path, &c); err != nil {
		return c, err
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("listen 必须为 host:port")
	}
	if c.Token == "" || c.Database == "" || c.QueueEvents < 1 || c.QueueBytes < 65536 {
		return c, fmt.Errorf("Relay 必须配置 token、database 和有效队列上限")
	}
	return c, nil
}

func relaySettings(raw, token string) error {
	if raw == "" {
		return nil
	}
	if err := HTTPURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
		return fmt.Errorf("relay_url 只填写服务根地址")
	}
	if token == "" {
		return fmt.Errorf("relay_token 必填")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("非回环 Relay 必须使用 HTTPS")
	}
	return nil
}

func LoadHub(path string) (Hub, error) {
	c := Hub{Listen: "127.0.0.1:18700", Database: "agent-pulse.db", ReminderTTLSeconds: 86400}
	if err := load(path, &c); err != nil {
		return c, err
	}
	if err := relaySettings(c.RelayURL, c.RelayToken); err != nil {
		return c, err
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("listen 必须为 host:port")
	}
	if c.Database == "" || c.ReminderTTLSeconds <= 0 {
		return c, fmt.Errorf("database 必填，reminder_ttl_seconds 必须为正数")
	}
	names := map[string]bool{}
	for i := range c.Push {
		p := &c.Push[i]
		if p.TimeoutSeconds == 0 {
			p.TimeoutSeconds = 5
		}
		if p.Name == "" || names[p.Name] {
			return c, fmt.Errorf("push[%d].name 必填且不能重复", i)
		}
		names[p.Name] = true
		if err := HTTPURL(p.URL); err != nil {
			return c, fmt.Errorf("push[%d].url: %w", i, err)
		}
		if p.Method != "GET" && p.Method != "POST" {
			return c, fmt.Errorf("push[%d].method 仅支持 GET/POST", i)
		}
		if p.Method == "GET" && (len(p.Fields) != 0 || p.Format != "") {
			return c, fmt.Errorf("push[%d]: GET 请使用 query", i)
		}
		if p.Method == "POST" && p.Format != "json" && p.Format != "form" {
			return c, fmt.Errorf("push[%d].format 仅支持 json/form", i)
		}
		if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 60 {
			return c, fmt.Errorf("push[%d].timeout_seconds 范围为 1 至 60", i)
		}
		if p.MaxTitleChars < 0 || p.MaxBodyChars < 0 {
			return c, fmt.Errorf("push[%d].max_title_chars 和 max_body_chars 不能为负数", i)
		}
	}
	return c, nil
}

func LoadBridge(path string) (Bridge, error) {
	c := Bridge{Listen: "127.0.0.1:18702", HubURL: "http://127.0.0.1:18700", QueueEvents: 10000, QueueBytes: 64 << 20}
	if err := load(path, &c); err != nil {
		return c, err
	}
	if err := relaySettings(c.RelayURL, c.RelayToken); err != nil {
		return c, err
	}
	if c.Adapters.PollSeconds == 0 {
		c.Adapters.PollSeconds = 300
	}
	if c.Adapters.PollSeconds < 5 {
		return c, fmt.Errorf("adapters.poll_seconds 至少为 5")
	}
	if (c.Adapters.Directory == "") != (c.Adapters.PublicKey == "") {
		return c, fmt.Errorf("adapters.directory 与 public_key 必须同时配置")
	}
	if c.Adapters.Directory != "" && !filepath.IsAbs(c.Adapters.Directory) {
		return c, fmt.Errorf("adapters.directory 必须为绝对路径")
	}
	for _, path := range c.Adapters.Managed {
		if !filepath.IsAbs(path) {
			return c, fmt.Errorf("adapters.managed 中的宿主配置必须为绝对路径")
		}
	}
	if err := Loopback(c.Listen); err != nil {
		return c, err
	}
	if strings.TrimSpace(c.SourceID) == "" || len(c.SourceID) > 256 || strings.ContainsAny(c.SourceID, "\x00\r\n") {
		return c, fmt.Errorf("source_id 必填，最长 256 字节")
	}
	if err := HTTPURL(c.HubURL); err != nil {
		return c, fmt.Errorf("hub_url: %w", err)
	}
	u, _ := url.Parse(c.HubURL)
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
		return c, fmt.Errorf("hub_url 只填写服务根地址")
	}
	if c.QueueEvents < 1 || c.QueueBytes < 65536 {
		return c, fmt.Errorf("queue_events 至少为 1，queue_bytes 至少为 65536")
	}
	return c, nil
}

func HTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("必须是无内嵌凭据的 HTTP/HTTPS 地址")
	}
	return nil
}

func Loopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen 必须使用回环 IP 和端口，例如 127.0.0.1:18702")
	}
	return nil
}

var envRef = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

func load(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("无法读取配置文件 %s", path)
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("配置文件超过 1 MiB")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var root, extra yaml.Node
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("%s: YAML 语法错误或空文件", path)
	}
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s: 仅允许一个 YAML 文档", path)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("配置根节点必须为映射")
	}
	value, err := simple(root.Content[0], "配置")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("配置含不支持的值")
	}
	jd := json.NewDecoder(bytes.NewReader(encoded))
	jd.DisallowUnknownFields()
	if err := jd.Decode(target); err != nil {
		if te, ok := err.(*json.UnmarshalTypeError); ok {
			return fmt.Errorf("配置字段 %s 类型错误，应为 %s", te.Field, te.Type)
		}
		return fmt.Errorf("配置包含未知字段或结构错误：%s", err)
	}
	return nil
}

func simple(n *yaml.Node, path string) (any, error) {
	bad := func(reason string) (any, error) { return nil, fmt.Errorf("第 %d 行 %s: %s", n.Line, path, reason) }
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return bad("不支持锚点和别名")
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" {
			return bad("不支持自定义标签")
		}
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag != "!!str" || k.Kind != yaml.ScalarNode || k.Anchor != "" || k.Value == "<<" {
				return bad("键必须为字符串，且不支持合并键")
			}
			if _, exists := m[k.Value]; exists {
				return bad("存在重复键")
			}
			v, err := simple(n.Content[i+1], path+"."+k.Value)
			if err != nil {
				return nil, err
			}
			m[k.Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		if n.Tag != "!!seq" {
			return bad("不支持自定义标签")
		}
		values := []any{}
		for i, item := range n.Content {
			v, err := simple(item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		return values, nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str":
			missing := false
			v := envRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
				val, ok := os.LookupEnv(envRef.FindStringSubmatch(ref)[1])
				if !ok {
					missing = true
				}
				return val
			})
			if missing {
				return bad("引用的环境变量未设置")
			}
			if strings.Contains(envRef.ReplaceAllString(n.Value, ""), "${env:") {
				return bad("环境变量引用格式错误")
			}
			return v, nil
		case "!!bool", "!!int", "!!float":
			var v any
			if err := n.Decode(&v); err != nil {
				return bad("标量格式错误")
			}
			return v, nil
		default:
			return bad("仅支持字符串、数字和布尔值；时间、版本号请加引号")
		}
	}
	return bad("不支持的 YAML 节点")
}
