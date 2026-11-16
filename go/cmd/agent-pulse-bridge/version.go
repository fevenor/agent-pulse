package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

// 发布构建显式注入候选标签和提交；普通源码构建不能冒充某个已发布版本。
var buildVersion = "dev"
var buildCommit = ""

type bridgeVersion struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
	SHA256    string `json:"sha256"`
}

func versionInformation() (bridgeVersion, error) {
	info := bridgeVersion{Version: buildVersion, Commit: buildCommit, GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if info.Commit == "" {
		if metadata, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range metadata.Settings {
				if setting.Key == "vcs.revision" {
					info.Commit = setting.Value
				}
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return info, fmt.Errorf("无法定位当前程序：%w", err)
	}
	file, err := os.Open(exe)
	if err != nil {
		return info, fmt.Errorf("无法读取当前程序摘要：%w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return info, fmt.Errorf("无法计算当前程序摘要：%w", err)
	}
	info.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return info, nil
}

func printVersion(w io.Writer, asJSON bool) error {
	info, err := versionInformation()
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(w).Encode(info)
	}
	commit := info.Commit
	if commit == "" {
		commit = "未注入提交信息"
	}
	_, err = fmt.Fprintf(w, "AgentPulse Bridge %s\n构建提交：%s\n编译器：%s\n平台：%s\nSHA-256：%s\n", info.Version, commit, info.GoVersion, info.Platform, info.SHA256)
	return err
}
