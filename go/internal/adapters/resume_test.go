package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"agent-pulse/internal/config"
)

func TestDownloadResumesVerifiedRangeWithoutRestartingWholePackage(t *testing.T) {
	data := make([]byte, 600000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	hash := sha256.Sum256(data)
	p := Package{SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}
	dir := t.TempDir()
	var failed atomic.Bool
	var zeroRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var lo, hi int
		if _, e := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &lo, &hi); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if lo == 0 {
			zeroRequests.Add(1)
		}
		if lo > 0 && failed.CompareAndSwap(false, true) {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, hi, len(data)))
		w.WriteHeader(206)
		_, _ = w.Write(data[lo : hi+1])
	}))
	defer server.Close()
	c := config.Bridge{HubURL: server.URL, Adapters: config.AdapterUpdates{Directory: dir}}
	if _, err := download(context.Background(), c, p); err == nil {
		t.Fatal("应观察到中途下载失败")
	}
	partial := filepath.Join(dir, "downloads", p.SHA256+".part")
	if info, e := os.Stat(partial); e != nil || info.Size() != 256<<10 {
		t.Fatal("首块未持久保存", e)
	}
	out, err := download(context.Background(), c, p)
	if err != nil || len(out) != len(data) || zeroRequests.Load() != 1 {
		t.Fatal("续传不应重新请求首块", err, zeroRequests.Load())
	}
	if _, err = os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("完整校验后临时分块应清除")
	}
}
