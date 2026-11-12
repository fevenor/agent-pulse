package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"agent-pulse/internal/adapters"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("用法：agent-pulse-adapters keygen|pack|publish|verify；私钥只保存在发布者本机")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	repository := fs.String("repository", "repository", "发布目录")
	switch os.Args[1] {
	case "keygen":
		private := fs.String("private", "publisher.key", "私钥路径")
		public := fs.String("public", "publisher.pub", "公钥路径")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		return adapters.GenerateKey(*private, *public)
	case "pack":
		source := fs.String("source", "", "待打包目录")
		manifest := fs.String("manifest", "", "包清单 JSON")
		out := fs.String("out", "package.json", "输出描述文件")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		var m adapters.Manifest
		if err := read(*manifest, &m); err != nil {
			return err
		}
		p, err := adapters.BuildPackage(*source, *repository, m)
		if err != nil {
			return err
		}
		return adapters.AtomicJSON(*out, p)
	case "verify":
		public := fs.String("public", "publisher.pub", "发布公钥文件")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		key, err := os.ReadFile(*public)
		if err != nil {
			return err
		}
		index, err := adapters.VerifyRepository(*repository, string(key))
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(index)
	case "publish":
		private := fs.String("private", "publisher.key", "发布私钥")
		index := fs.String("index", "index-input.json", "含递增 sequence 与 packages 的目标索引")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		var i adapters.Index
		if err := read(*index, &i); err != nil {
			return err
		}
		return adapters.SignIndex(*repository, *private, i)
	default:
		return fmt.Errorf("不支持的操作")
	}
}
func read(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
