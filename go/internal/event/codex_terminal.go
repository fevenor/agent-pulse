package event

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agent-pulse/internal/sqlitepath"
	"agent-pulse/protocol"
)

// 游标只登记正在处理的一轮和增量位置；不保存用户输入、回答或模型请求错误。
type codexCursor struct {
	Home         string
	DatabaseHome string
	fileInfo     os.FileInfo
	SessionID    string
	TurnID       string
	Path         string
	Offset       int64
	Skipping     bool
}

func codexHome() string {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		user, _ := os.UserHomeDir()
		home = filepath.Join(user, ".codex")
	}
	return home
}

// 共用目录在 WSL 中可登记 Windows 原生路径；只转换标准盘符，不猜自定义挂载。
func codexLocalPath(path string) string {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	if runtime.GOOS == "linux" && os.Getenv("WSL_DISTRO_NAME") != "" && len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		path = "/mnt/" + strings.ToLower(path[:1]) + "/" + strings.ReplaceAll(path[3:], "\\", "/")
	}
	return filepath.Clean(path)
}

func codexRollout(id, transcript string) string {
	return codexRolloutAt(id, transcript, codexHome(), strings.TrimSpace(os.Getenv("CODEX_SQLITE_HOME")))
}

func codexRolloutAt(id, transcript, home, databaseHome string) string {
	if home == "" {
		home = codexHome()
	}
	path := transcript
	if path == "" {
		if databaseHome == "" {
			databaseHome = home
		}
		file := filepath.Join(databaseHome, "state_5.sqlite")
		if _, err := os.Stat(file); err != nil {
			return ""
		}
		db, err := sql.Open("sqlite", sqlitepath.URI(file)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30)")
		if err != nil {
			return ""
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = db.QueryRowContext(ctx, "SELECT rollout_path FROM threads WHERE id=?", id).Scan(&path)
	}
	if path == "" {
		return ""
	}
	path = codexLocalPath(path)
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	for _, name := range []string{"sessions", "archived_sessions"} {
		base := codexLocalPath(filepath.Join(home, name))
		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}
		relative, err := filepath.Rel(base, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return path
		}
	}
	return ""
}

func CodexObservation(hook string, raw []byte) *protocol.Observation {
	if hook != "UserPromptSubmit" && hook != "Stop" {
		return nil
	}
	var h Hook
	if json.Unmarshal(raw, &h) != nil || h.SessionID == "" || h.TurnID == "" {
		return nil
	}
	if h.AgentID != "" || h.AgentType != "" || h.ParentSession != "" || h.ParentID != "" || h.ThreadSource == "memory_consolidation" || h.ThreadSource == "subagent" || codexMemoryWorkspace(h.CWD) || codexInternalSession(h.SessionID, h.Transcript) {
		return nil
	}
	cursor := codexCursor{Home: codexHome(), DatabaseHome: strings.TrimSpace(os.Getenv("CODEX_SQLITE_HOME")), SessionID: h.SessionID, TurnID: h.TurnID, Path: codexRollout(h.SessionID, h.Transcript)}
	if hook == "Stop" {
		if info, err := os.Stat(cursor.Path); err != nil || !info.Mode().IsRegular() {
			return nil
		}
	}
	if info, err := os.Stat(cursor.Path); err == nil && info.Mode().IsRegular() {
		cursor.Offset = info.Size()
	}
	rawCursor, _ := json.Marshal(cursor)
	return &protocol.Observation{SessionID: h.SessionID, TurnID: h.TurnID, Cursor: rawCursor}
}

// 只解码目标轮次的最终事件字段；其他记录不转换，不采集请求错误、思考或历史正文。
func codexTerminal(raw []byte, cursor codexCursor, source string) (*protocol.Event, bool, error) {
	if !bytes.Contains(raw, []byte("\"task_complete\"")) && !bytes.Contains(raw, []byte("\"turn_aborted\"")) {
		return nil, false, nil
	}
	var envelope struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Payload   struct {
			Type          string          `json:"type"`
			TurnID        string          `json:"turn_id"`
			Error         json.RawMessage `json:"error"`
			LastAssistant string          `json:"last_agent_message"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false, errors.New("原生终态记录格式无效")
	}
	if envelope.Type != "event_msg" {
		return nil, false, nil
	}
	if envelope.Payload.Type == "task_complete" && envelope.Payload.TurnID == "" {
		return nil, false, errors.New("原生终态缺少轮次")
	}
	if envelope.Payload.TurnID != cursor.TurnID {
		return nil, false, nil
	}
	if envelope.Payload.Type == "turn_aborted" {
		return nil, true, nil
	}
	if envelope.Payload.Type != "task_complete" {
		return nil, false, nil
	}
	kind := "turn.completed"
	body := visibleBody(envelope.Payload.LastAssistant)
	if len(envelope.Payload.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Payload.Error), []byte("null")) {
		kind = "run.failed"
		body = nativeFailureBody(envelope.Payload.Error, "模型调用最终报错，本轮已结束")
	}
	if envelope.Timestamp.IsZero() {
		return nil, false, errors.New("原生终态缺少发生时间")
	}
	sum := sha256.Sum256([]byte(source + "\x00codex\x00" + cursor.SessionID + "\x00" + cursor.TurnID + "\x00" + kind))
	return &protocol.Event{EventID: hex.EncodeToString(sum[:]), SourceID: source, Agent: "codex", SessionID: cursor.SessionID, Kind: kind, OccurredAt: envelope.Timestamp.UTC(), Scope: "main", Title: "codex · " + title(kind), Body: body, SessionTitle: codexSessionTitleAt(cursor.SessionID, cursor.Home)}, true, nil
}

// 每次只读取有限增量；超大非终态行直接跳过，未写完的行留到下一次读取。
func readCodexTerminal(cursor *codexCursor, source string) (*protocol.Event, bool, error) {
	path := codexRolloutAt(cursor.SessionID, cursor.Path, cursor.Home, cursor.DatabaseHome)
	if path == "" {
		return nil, false, errors.New("原生终态来源不可用")
	}
	cursor.Path = path
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("原生终态来源不是普通文件")
	}
	if cursor.fileInfo != nil && !os.SameFile(cursor.fileInfo, info) {
		return nil, false, errors.New("原生终态来源已替换")
	}
	cursor.fileInfo = info
	if info.Size() < cursor.Offset {
		return nil, false, errors.New("原生终态来源已替换或截断")
	}
	if _, err = file.Seek(cursor.Offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	line := make([]byte, 0, 4096)
	oversized := cursor.Skipping
	position := cursor.Offset
	initial := position
	for {
		part, err := reader.ReadSlice('\n')
		position += int64(len(part))
		if !oversized {
			if len(line)+len(part) > 8<<20 {
				if bytes.Contains(line[:min(len(line), 4096)], []byte("task_complete")) {
					return nil, false, errors.New("原生终态记录超限")
				}
				line = nil
				oversized = true
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			if oversized && position-initial >= 8<<20 {
				cursor.Offset = position
				cursor.Skipping = true
				return nil, false, nil
			}
			continue
		}
		if err == io.EOF {
			if oversized {
				cursor.Offset = position
				cursor.Skipping = true
			}
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		cursor.Skipping = false
		if !oversized {
			event, done, parseErr := codexTerminal(line, *cursor, source)
			if parseErr != nil {
				return nil, false, parseErr
			}
			if done {
				cursor.Offset = position
				return event, true, nil
			}
		}
		cursor.Offset = position
		if position-initial >= 8<<20 {
			return nil, false, nil
		}
		line = line[:0]
		oversized = false
	}

}

func ObserveCodex(ctx context.Context, raw []byte, source string, emit func(protocol.LocalEvent) error) error {
	var cursor codexCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.SessionID == "" || cursor.TurnID == "" || cursor.Offset < 0 {
		return errors.New("本轮终态游标无效")
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	unavailableSince := time.Time{}
	for {
		event, done, err := readCodexTerminal(&cursor, source)
		if err != nil {
			if unavailableSince.IsZero() {
				unavailableSince = time.Now()
			}
			if time.Since(unavailableSince) >= 10*time.Second {
				return errors.New("本轮原生终态持续不可读取")
			}
		} else {
			unavailableSince = time.Time{}
		}
		if event != nil {
			return emit(protocol.LocalEvent{Event: *event, TurnID: cursor.TurnID})
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
