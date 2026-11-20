package protocol

// SyncCursor确认已落盘的状态位置；与事件历史position分离，覆盖已读和删除等变化。
type SyncCursor struct {
	RunID   string `json:"run_id"`
	Version uint64 `json:"version"`
}

type SyncHello struct {
	Device Device     `json:"device"`
	Cursor SyncCursor `json:"cursor"`
	Admin  bool       `json:"admin,omitempty"`
}

// SyncDelta只传改变的对象；Reset用于首次连接或恢复窗口外的状态重建。
type SyncDelta struct {
	SyncCursor
	From            uint64                 `json:"from"`
	Reset           *Snapshot              `json:"reset,omitempty"`
	Sessions        []Session              `json:"sessions,omitempty"`
	SessionsRemoved []SessionKey           `json:"sessions_removed,omitempty"`
	Unread          []EventView            `json:"unread,omitempty"`
	UnreadRemoved   []string               `json:"unread_removed,omitempty"`
	Sources         map[string]SourceLabel `json:"sources,omitempty"`
	SourcesRemoved  []string               `json:"sources_removed,omitempty"`
	Devices         []Device               `json:"devices,omitempty"`
	DevicesRemoved  []string               `json:"devices_removed,omitempty"`
}

type SyncCheckpoint struct {
	SyncCursor
	Probe        string `json:"probe"`
	AdminVersion uint64 `json:"admin_version,omitempty"`
}

type EventBody struct {
	EventID  string `json:"event_id"`
	BodyHash string `json:"body_hash"`
	Body     string `json:"body"`
}

type EventBatch struct {
	Events []Event `json:"events"`
}
type EventResult struct {
	EventID   string `json:"event_id"`
	Status    int    `json:"status"`
	Accepted  string `json:"accepted,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}
type BatchReceipt struct {
	Results []EventResult `json:"results"`
}
