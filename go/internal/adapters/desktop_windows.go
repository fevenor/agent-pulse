package adapters

import (
	"errors"
	"golang.org/x/sys/windows/registry"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 桌面程序只读版本资源；不以--version启动用户界面。
func antigravityDesktopVersion() (string, error) {
	candidates := []string{}
	if p, err := exec.LookPath("Antigravity.exe"); err == nil {
		candidates = append(candidates, p)
	}
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		candidates = append(candidates, filepath.Join(base, "Programs", "antigravity", "Antigravity.exe"))
	}
	if base := os.Getenv("ProgramFiles"); base != "" {
		candidates = append(candidates, filepath.Join(base, "Antigravity", "Antigravity.exe"))
	}
	for _, p := range candidates {
		if v, err := executableVersion(p); err == nil {
			return v, nil
		}
	}
	return "", errors.New("未找到Antigravity桌面程序")
}

// 从当前用户注册的桌面包定位捆绑执行器，不要求额外安装Codex CLI。
func codexDesktopCommand() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, "Software\\Classes\\Local Settings\\Software\\Microsoft\\Windows\\CurrentVersion\\AppModel\\Repository\\Packages", registry.READ)
	if err != nil {
		return ""
	}
	defer key.Close()
	names, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return ""
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "OpenAI.Codex_") {
			continue
		}
		pkg, err := registry.OpenKey(key, name, registry.READ)
		if err != nil {
			continue
		}
		base, _, err := pkg.GetStringValue("PackageRootFolder")
		pkg.Close()
		if err != nil {
			continue
		}
		allowed := os.Getenv("ProgramFiles")
		relative, relErr := filepath.Rel(allowed, base)
		if allowed == "" || relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		p := filepath.Join(base, "app", "resources", "codex.exe")
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}
