package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCodexEphemeralMemoryWorkspaceNeverEmits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_SQLITE_HOME", t.TempDir())
	for _, name := range []string{"memories", "memories_v2"} {
		directory := filepath.Join(home, name)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		for _, hook := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd", "Interrupt"} {
			raw, _ := json.Marshal(map[string]string{"session_id": "ephemeral-memory", "cwd": directory, "hook_event_name": hook})
			e, err := Convert("codex", "memory-test", hook, raw, time.Now())
			if err != nil || e != nil {
				t.Fatalf("临时记忆%s/%s被投递：%+v %v", name, hook, e, err)
			}
		}
	}
}
func TestCodexOrdinarySameNameProjectIsNotMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_SQLITE_HOME", t.TempDir())
	for _, cwd := range []string{filepath.Join(t.TempDir(), "memories_v2"), home, filepath.Join(home, "memories_v2-other"), filepath.Join(home, "memories_v2", "ordinary-subdirectory")} {
		if codexMemoryWorkspace(cwd) {
			t.Fatalf("普通目录被误过滤：%s", cwd)
		}
	}
}
func TestCodexMemoryWindowsPathInWSL(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅WSL路径转换")
	}
	t.Setenv("WSL_DISTRO_NAME", "memory-test")
	t.Setenv("CODEX_HOME", "/mnt/c/Users/test/.codex")
	if !codexMemoryWorkspace(`C:\Users\test\.codex\memories_v2`) || !codexMemoryWorkspace(`\\?\C:\Users\test\.codex\memories_v2`) || !codexMemoryWorkspace(`C:\USERS\TEST\.CODEX\MEMORIES_V2`) || codexMemoryWorkspace(`C:\project\memories_v2`) {
		t.Fatal("共享钩子的Windows路径识别错误")
	}
}
