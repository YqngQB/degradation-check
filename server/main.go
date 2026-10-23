// Package main 启动降智检测服务：内嵌前端 + REST API + SQLite。
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed all:web
var webFS embed.FS

type config struct {
	Port     string
	DataDir  string
	Instance string
}

func loadConfig() config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8088"
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	instance := os.Getenv("INSTANCE_NAME")
	if instance == "" {
		instance = "降智检测"
	}
	return config{Port: port, DataDir: dataDir, Instance: instance}
}

func main() {
	cfg := loadConfig()

	store, err := openStore(filepath.Join(cfg.DataDir, "degradation.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = store.Close() }()

	app := &App{cfg: cfg, store: store, upstream: newUpstreamClient()}
	mux := http.NewServeMux()
	app.routes(mux)

	// 前端静态资源（Vite 构建产物）
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("读取前端资源失败: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", spaHandler(sub, fileServer, cfg.Instance))

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           withCommonHeaders(mux),
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
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// 允许被任意站点 iframe 嵌入：部署者的中转站域名不固定。
		h.Set("Content-Security-Policy", "frame-ancestors *")
		next.ServeHTTP(w, r)
	})
}
