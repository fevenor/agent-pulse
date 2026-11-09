package hub

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/transport"
)

func (h *Hub) adapterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/adapters/index", func(w http.ResponseWriter, r *http.Request) {
		if h.config.AdapterDirectory == "" {
			transport.Error(w, 404, "未配置适配包仓库")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(h.config.AdapterDirectory, "index.json"))
	})
	mux.HandleFunc("GET /api/v1/adapters/packages/{hash}", func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		if h.config.AdapterDirectory == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
			transport.Error(w, 404, "包不存在")
			return
		}
		http.ServeFile(w, r, filepath.Join(h.config.AdapterDirectory, "packages", hash+".zip"))
	})
	mux.HandleFunc("POST /api/v1/adapters/status", func(w http.ResponseWriter, r *http.Request) {
		var report adapters.Report
		if !transport.Decode(w, r, &report) {
			return
		}
		if report.SourceID == "" || len(report.SourceID) > 256 || len(report.Items) > 128 {
			transport.Error(w, 400, "报告大小或来源无效")
			return
		}
		report.UpdatedAt = time.Now().UTC()
		data, _ := json.Marshal(report)
		h.mu.Lock()
		_, err := h.db.ExecContext(r.Context(), `INSERT INTO adapter_hosts(source_id,report) VALUES(?,?) ON CONFLICT(source_id) DO UPDATE SET report=excluded.report`, report.SourceID, data)
		if err == nil {
			h.changedLocked()
		}
		h.mu.Unlock()
		if err != nil {
			transport.Error(w, 503, "状态未保存")
			return
		}
		transport.JSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/adapters/status", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		rows, err := h.db.QueryContext(r.Context(), `SELECT report FROM adapter_hosts ORDER BY source_id`)
		if err != nil {
			transport.Error(w, 503, "状态读取失败")
			return
		}
		defer rows.Close()
		reports := []adapters.Report{}
		for rows.Next() {
			var raw []byte
			var report adapters.Report
			if rows.Scan(&raw) != nil || json.Unmarshal(raw, &report) != nil {
				transport.Error(w, 503, "状态读取失败")
				return
			}
			reports = append(reports, report)
		}
		if rows.Err() != nil {
			transport.Error(w, 503, "状态读取失败")
			return
		}
		transport.JSON(w, 200, reports)
	})
	if h.config.WebDirectory != "" {
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if _, err := os.Stat(filepath.Join(h.config.WebDirectory, "index.html")); err != nil {
				transport.Error(w, 503, "Web 资源未构建")
				return
			}
			http.FileServer(http.Dir(h.config.WebDirectory)).ServeHTTP(w, r)
		}))
	}
}
