package main

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestVersionReportsInjectedCandidateAndSelfDigest(t *testing.T) {
	oldVersion, oldCommit := buildVersion, buildCommit
	t.Cleanup(func() { buildVersion, buildCommit = oldVersion, oldCommit })
	buildVersion = "v0.5.0-rc.4"
	buildCommit = strings.Repeat("a", 40)
	var output bytes.Buffer
	if err := printVersion(&output, true); err != nil {
		t.Fatal(err)
	}
	var info bridgeVersion
	if err := json.Unmarshal(output.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != buildVersion || info.Commit != buildCommit || info.GoVersion != runtime.Version() || info.Platform != runtime.GOOS+"/"+runtime.GOARCH || len(info.SHA256) != 64 {
		t.Fatal("发布标签、提交、平台或当前程序摘要缺失")
	}
	output.Reset()
	if err := printVersion(&output, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), buildVersion) || !strings.Contains(output.String(), buildCommit) || !strings.Contains(output.String(), info.SHA256) {
		t.Fatal("中文版本查询缺少必要取证信息")
	}
}

func TestUnmarkedBuildIsNotReportedAsPublishedRelease(t *testing.T) {
	oldVersion, oldCommit := buildVersion, buildCommit
	t.Cleanup(func() { buildVersion, buildCommit = oldVersion, oldCommit })
	buildVersion = "dev"
	buildCommit = ""
	var output bytes.Buffer
	if err := printVersion(&output, true); err != nil {
		t.Fatal(err)
	}
	var info bridgeVersion
	if err := json.Unmarshal(output.Bytes(), &info); err != nil || info.Version != "dev" {
		t.Fatal("未标注构建不能冒充正式候选")
	}
}
