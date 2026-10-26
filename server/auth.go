package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// Protect the whole origin, including previews/static HTML, not just mutations.
// Basic auth uses the browser's credential cache; no admin/key is stored in JS.
func withAuth(cfg config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user, password, ok := r.BasicAuth()
		u, p := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(password))
		wantU, wantP := sha256.Sum256([]byte(cfg.AuthUser)), sha256.Sum256([]byte(cfg.AuthPassword))
		if !ok || cfg.AuthUser == "" || cfg.AuthPassword == "" ||
			(subtle.ConstantTimeCompare(u[:], wantU[:])&subtle.ConstantTimeCompare(p[:], wantP[:])) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="degradation-check", charset="UTF-8"`)
			http.Error(w, "需要登录检测服务", http.StatusUnauthorized)
			return
		}
		// A hostile site must not use cached Basic credentials for mutations.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-DC-Request") != "1" {
			http.Error(w, "缺少请求校验标记", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
