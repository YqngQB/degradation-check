// Package main 启动降智检测服务：内嵌前端 + REST API + SQLite。
package main

import (
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed all:web
var webFS embed.FS

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	// Direct channel IDs have a different namespace from Sub2API account IDs.
	// Keep the original database intact when switching detection modes.
	database := "degradation.db"
	if cfg.Mode == "direct" {
		database = "direct-degradation.db"
	}
	store, err := openStore(filepath.Join(cfg.DataDir, database))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = store.Close() }()

	app := &App{cfg: cfg, store: store, upstream: newUpstreamClient(cfg)}
	mux := http.NewServeMux()
	app.routes(mux)

	// 前端静态资源（Vite 构建产物）
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("读取前端资源失败: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", spaHandler(sub, fileServer, cfg.Instance))

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           withCommonHeaders(withAuth(cfg, mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("[%s] listening on %s (data: %s)", cfg.Instance, addr, cfg.DataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// spaHandler 让前端路由回退到 index.html。
func spaHandler(fsys fs.FS, next http.Handler, instance string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err == nil {
			next.ServeHTTP(w, r)
			return
		}
		// 不是静态资源 → 交给 SPA
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "前端资源缺失，请先构建 web/", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}

// withCommonHeaders 允许被 Sub2API 的自定义菜单 iframe 嵌入，并做最小安全隔离。
func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "frame-ancestors 'self'; object-src 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}
