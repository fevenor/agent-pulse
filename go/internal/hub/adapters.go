package hub

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"agent-pulse/internal/adapters"
	"agent-pulse/internal/transport"
)

// resolvePublisherKey 只接纳显式公钥或本地发布仓库附带公钥，不在运行时生成信任。
func resolvePublisherKey(directory, configured string) (string, error) {
	key := strings.TrimSpace(configured)
	if key == "" && directory != "" {
		raw, err := os.ReadFile(filepath.Join(directory, "publisher.pub"))
		if err != nil {
			if os.IsNotExist(err) {
				// 基础镜像允许尚未附加发布仓库；有仓库却缺公钥则必须修复。
				if _, directoryErr := os.Stat(directory); os.IsNotExist(directoryErr) {
					return "", nil
				}
			}
			return "", fmt.Errorf("无法读取适配包仓库的 publisher.pub；请提供已验证的发布公钥或显式配置 adapter_public_key：%w", err)
		}
		key = strings.TrimSpace(string(raw))
		if key == "" {
			return "", fmt.Errorf("适配包仓库的 publisher.pub 为空")
		}
	}
	if key == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return "", fmt.Errorf("适配包发布公钥必须是 32 字节 Ed25519 公钥的 Base64 内容")
	}
	return key, nil
}

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
		if !validHostID(report.SourceID) || len(report.Items) > 128 {
			transport.Error(w, 400, "报告大小或来源无效")
			return
		}
		report.UpdatedAt = time.Now().UTC()
		data, _ := json.Marshal(report)
		h.mu.Lock()
		// 删除与状态登记共用锁，并由 SQL 检查删除标记，旧报告不能重建主机记录。
		result, err := h.db.ExecContext(r.Context(), `INSERT INTO adapter_hosts(source_id,report) SELECT ?,? WHERE NOT EXISTS (SELECT 1 FROM deleted_hosts WHERE source_id=?) ON CONFLICT(source_id) DO UPDATE SET report=excluded.report`, report.SourceID, data, report.SourceID)
		var affected int64
		if err == nil {
			affected, err = result.RowsAffected()
		}
		if err == nil && affected > 0 {
			h.changedLocked()
		}
		h.mu.Unlock()
		if err != nil {
			transport.Error(w, 503, "状态未保存")
			return
		}
		if affected == 0 {
			transport.Error(w, http.StatusGone, ErrHostDeleted.Error())
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
