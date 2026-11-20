package transport

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestCompressedMessagesAndRawRelayFrames(t *testing.T) {
	value := map[string]string{"body": strings.Repeat("完整中文正文", 2000)}
	kind, raw, err := EncodeMessage(value)
	if err != nil || kind != websocket.BinaryMessage {
		t.Fatal("长消息未压缩", err)
	}
	var out map[string]string
	if DecodeMessage(kind, raw, &out) != nil || out["body"] != value["body"] {
		t.Fatal("压缩未保持内容")
	}
	f := Frame{Type: "response", ID: "one", Body: raw, Binary: true, Headers: map[string]string{"Content-Encoding": "gzip"}}
	encoded, err := EncodeFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFrame(encoded)
	if err != nil || !bytes.Equal(got.Body, raw) || !got.Binary {
		t.Fatal("中继原始载荷损坏")
	}
	if len(encoded) > len(raw)+200 {
		t.Fatal("中继载荷出现额外编码放大")
	}
	if _, err := DecodeFrame([]byte{0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Fatal("中继头部超限必须拒绝")
	}
	var bomb bytes.Buffer
	z := gzip.NewWriter(&bomb)
	_, _ = z.Write(bytes.Repeat([]byte{'x'}, MaxWireJSON+1))
	_ = z.Close()
	if DecodeMessage(websocket.BinaryMessage, bomb.Bytes(), &out) == nil {
		t.Fatal("压缩上限不可绕过")
	}
}
func TestHTTPJSONCompressionNegotiation(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"text": strings.Repeat("正文", 1000)})
	for _, tc := range []struct {
		accept     string
		compressed bool
	}{{"gzip", true}, {"gzip;q=0", false}, {"identity", false}} {
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set("Accept-Encoding", tc.accept)
		response := httptest.NewRecorder()
		WriteJSONBytes(response, request, 200, raw)
		if (response.Header().Get("Content-Encoding") == "gzip") != tc.compressed {
			t.Fatal("压缩协商错误")
		}
		kind := websocket.TextMessage
		if tc.compressed {
			kind = websocket.BinaryMessage
		}
		var v map[string]string
		if DecodeMessage(kind, response.Body.Bytes(), &v) != nil || v["text"] != strings.Repeat("正文", 1000) {
			t.Fatal("HTTP压缩内容变化")
		}
	}
}
