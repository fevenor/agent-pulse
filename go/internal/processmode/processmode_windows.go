//go:build windows

package processmode

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var queryProcessInformation = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationProcess")

func processInfo(pid int) (int, string, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parent := -1
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == uint32(pid) {
			parent = int(entry.ParentProcessID)
			break
		}
	}
	if parent < 0 {
		return 0, "", fmt.Errorf("进程已结束")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(handle)
	path := make([]uint16, 32768)
	size := uint32(len(path))
	if err = windows.QueryFullProcessImageName(handle, 0, &path[0], &size); err != nil {
		return 0, "", err
	}
	return parent, windows.UTF16ToString(path[:size]), nil
}
func processArguments(pid int) ([]string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	var size uint32
	_, _, _ = queryProcessInformation.Call(uintptr(handle), 60, 0, 0, uintptr(unsafe.Pointer(&size)))
	if size < 16 || size > 64<<10 {
		return nil, fmt.Errorf("启动信息大小无效")
	}
	buffer := make([]byte, size)
	status, _, _ := queryProcessInformation.Call(uintptr(handle), 60, uintptr(unsafe.Pointer(&buffer[0])), uintptr(size), uintptr(unsafe.Pointer(&size)))
	if uint32(status) != 0 {
		return nil, fmt.Errorf("无法查询启动信息")
	}
	type unicodeString struct {
		Length        uint16
		MaximumLength uint16
		Buffer        *uint16
	}
	text := (*unicodeString)(unsafe.Pointer(&buffer[0]))
	start := uintptr(unsafe.Pointer(text.Buffer))
	base := uintptr(unsafe.Pointer(&buffer[0]))
	if text.Length%2 != 0 || start < base || start-base > uintptr(len(buffer)) || uintptr(text.Length) > uintptr(len(buffer))-(start-base) {
		return nil, fmt.Errorf("启动字符串范围无效")
	}
	// 只用于本地模式判断，解析后不记录或上报参数。
	args, err := windows.DecomposeCommandLine(windows.UTF16ToString(unsafe.Slice(text.Buffer, int(text.Length/2))))
	if len(args) > 2 {
		args = args[:2]
	}
	return args, err
}
