package adapters

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 桌面宿主的 --version 会启动界面，改读 PE 版本资源；不依赖开发机安装目录。
func zcodeDesktopVersion() (string, error) {
	var candidates []string
	if path, err := exec.LookPath("ZCode.exe"); err == nil {
		candidates = append(candidates, path)
	}
	for _, root := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles")} {
		if root == "" {
			continue
		}
		if root == os.Getenv("LOCALAPPDATA") {
			root = filepath.Join(root, "Programs")
		}
		candidates = append(candidates, filepath.Join(root, "ZCode", "ZCode.exe"))
	}
	for _, path := range candidates {
		if version, err := executableVersion(path); err == nil {
			return version, nil
		}
	}
	return "", errors.New("未找到 ZCode 桌面程序；自定义安装目录需将 ZCode.exe 所在目录加入 PATH")
}

func executableVersion(path string) (string, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		return "", err
	}
	if size == 0 || size > 1<<20 {
		return "", errors.New("程序版本资源大小无效")
	}
	buffer := make([]byte, size)
	if err = windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buffer[0])); err != nil {
		return "", err
	}
	var info *windows.VS_FIXEDFILEINFO
	var length uint32
	if err = windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&info), &length); err != nil {
		return "", err
	}
	if info == nil || length < uint32(unsafe.Sizeof(*info)) || info.Signature != 0xfeef04bd {
		return "", errors.New("程序版本资源无效")
	}
	return fmt.Sprintf("%d.%d.%d", info.FileVersionMS>>16, info.FileVersionMS&0xffff, info.FileVersionLS>>16), nil
}
