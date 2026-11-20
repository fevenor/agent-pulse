package transport

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxWireJSON = 8 << 20

// 较大消息使用独立gzip成员，小消息直接JSON；解压同样限制大小，避免压缩载荷绕过边界。
func EncodeMessage(value any) (int, []byte, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxWireJSON {
		return 0, nil, errors.New("消息编码失败或超过上限")
	}
	if len(raw) < 1024 {
		return websocket.TextMessage, raw, nil
	}
	var out bytes.Buffer
	w, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	_, err = w.Write(raw)
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, nil, err
	}
	if out.Len()+32 >= len(raw) {
		return websocket.TextMessage, raw, nil
	}
	return websocket.BinaryMessage, out.Bytes(), nil
}
func DecodeMessage(kind int, raw []byte, target any) error {
	if len(raw) > MaxWireJSON {
		return errors.New("消息超过上限")
	}
	if kind == websocket.BinaryMessage {
		r, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		raw, err = io.ReadAll(io.LimitReader(r, MaxWireJSON+1))
		closeErr := r.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if kind != websocket.TextMessage {
		return errors.New("不支持的消息类型")
	}
	if len(raw) > MaxWireJSON {
		return errors.New("解压消息超过上限")
	}
	return json.Unmarshal(raw, target)
}

type JSONSocket struct {
	*websocket.Conn
	Category string
	writeMu  sync.Mutex
}

func (s *JSONSocket) ReadJSON(v any) error {
	k, b, e := s.ReadMessage()
	if e != nil {
		return e
	}
	RecordTraffic(s.Category, 0, uint64(len(b)), false)
	return DecodeMessage(k, b, v)
}
func (s *JSONSocket) WriteJSON(v any) error {
	k, b, e := EncodeMessage(v)
	if e != nil {
		return e
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	e = s.WriteMessage(k, b)
	if e == nil {
		RecordTraffic(s.Category, uint64(len(b)), 0, false)
	}
	return e
}

// HTTP大响应单独压缩；文件和分块包保持原字节，已压缩响应不二次处理。
func WriteJSONBytes(w http.ResponseWriter, r *http.Request, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept-Encoding")
	if len(raw) >= 1024 && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		var out bytes.Buffer
		z, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
		_, _ = z.Write(raw)
		_ = z.Close()
		if out.Len() < len(raw) {
			raw = out.Bytes()
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func acceptsGzip(value string) bool {
	for _, entry := range strings.Split(value, ",") {
		fields := strings.Split(strings.TrimSpace(entry), ";")
		if fields[0] != "gzip" {
			continue
		}
		enabled := true
		for _, p := range fields[1:] {
			p = strings.TrimSpace(p)
			if p == "q=0" || p == "q=0.0" || p == "q=0.00" || p == "q=0.000" {
				enabled = false
			}
		}
		return enabled
	}
	return false
}

type TrafficCount struct {
	Sent     uint64 `json:"sent"`
	Received uint64 `json:"received"`
	Requests uint64 `json:"requests"`
}

var trafficState = struct {
	sync.Mutex
	Started time.Time
	Counts  map[string]TrafficCount
}{Started: time.Now().UTC(), Counts: map[string]TrafficCount{}}

func RecordTraffic(category string, sent, received uint64, request bool) {
	trafficState.Lock()
	defer trafficState.Unlock()
	n := trafficState.Counts[category]
	n.Sent += sent
	n.Received += received
	if request {
		n.Requests++
	}
	trafficState.Counts[category] = n
}
func TrafficSnapshot() any {
	trafficState.Lock()
	defer trafficState.Unlock()
	counts := map[string]TrafficCount{}
	for k, v := range trafficState.Counts {
		counts[k] = v
	}
	return map[string]any{"since": trafficState.Started, "unit": "application_bytes", "categories": counts}
}
