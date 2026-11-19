package event

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// 游标仅在同机流转，不上传宿主目录；只针对已复现的App首次身份落盘窗口。
type antigravityIdentityCursor struct {
	SessionID string `json:"session_id"`
	Root      string `json:"root"`
}

func AntigravityIdentityCursor(sessionID string, metadata *AntigravityMetadata) json.RawMessage {
	if metadata == nil || metadata.Identity != "unknown" || metadata.RootSource != "transcript" || filepath.Base(metadata.Root) != "antigravity" {
		return nil
	}
	raw, _ := json.Marshal(antigravityIdentityCursor{SessionID: sessionID, Root: metadata.Root})
	return raw
}

func CheckAntigravityIdentity(raw []byte) (string, error) {
	var cursor antigravityIdentityCursor
	if len(raw) > 8192 || json.Unmarshal(raw, &cursor) != nil || cursor.SessionID == "" || strings.ContainsAny(cursor.SessionID, "/\\\x00\r\n") || cursor.SessionID == "." || cursor.SessionID == ".." || !filepath.IsAbs(cursor.Root) || filepath.Base(cursor.Root) != "antigravity" {
		return "", errors.New("身份观察游标无效")
	}
	result := inspectAntigravityDatabase(filepath.Join(cursor.Root, "conversation_summaries.db"), cursor.SessionID)
	return result.Identity, nil
}
