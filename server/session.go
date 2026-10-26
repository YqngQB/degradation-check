package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type adminError struct {
	Status        int
	Code, Message string
}

func (e *adminError) Error() string { return e.Message }

// Never echo an admin response body: export errors may contain credentials.
func decodeAdmin(resp *http.Response, target any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var payload struct {
			Reason string `json:"reason"`
			Code   any    `json:"code"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&payload)
		code := firstNonEmpty(payload.Reason, toString(payload.Code))
		messages := map[string]string{
			"STEP_UP_REQUIRED":                "读取账号凭据需要二次验证，请输入当前管理员的动态验证码",
			"STEP_UP_TOTP_NOT_ENABLED":        "请先在 Sub2API 为当前管理员启用二次验证，再重新进入检测页面",
			"STEP_UP_ADMIN_API_KEY_FORBIDDEN": "此接口需要管理员登录会话，请从 Sub2API 后台菜单重新进入，不能使用管理员 API Key",
			"STEP_UP_UNAVAILABLE":             "Sub2API 二次验证服务暂不可用，请稍后重试",
		}
		message, known := messages[code]
		if !known {
			code = "SUB2API_REQUEST_FAILED"
			message = "Sub2API 拒绝请求，请检查管理员登录状态、导出权限或动态验证码"
		}
		return &adminError{Status: resp.StatusCode, Code: code, Message: message}
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 32<<20)).Decode(target); err != nil {
		return &adminError{Status: 502, Code: "SUB2API_INVALID_RESPONSE", Message: "Sub2API 响应格式不正确"}
	}
	return nil
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var ae *adminError
	if errors.As(err, &ae) {
		status := http.StatusBadGateway
		if strings.HasPrefix(ae.Code, "STEP_UP_") {
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]string{"error": ae.Message, "code": ae.Code})
		return
	}
	// selectedEndpoint returns only controlled configuration messages (no raw
	// response body, URL or credential). Keep these actionable in the UI.
	writeErr(w, http.StatusBadGateway, err.Error())
}

type adminSession struct {
	token   string
	expires time.Time
}
type sessionStore struct {
	mu    sync.Mutex
	items map[[32]byte]adminSession
}
type requestIdentity struct {
	upstream *Upstream
	accounts []Account
}
type identityKey struct{}

func (a *App) requestUpstream(r *http.Request) *Upstream {
	if identity, ok := r.Context().Value(identityKey{}).(*requestIdentity); ok {
		return identity.upstream
	}
	return a.upstream
}
func (a *App) requestAccounts(r *http.Request) ([]Account, error) {
	if identity, ok := r.Context().Value(identityKey{}).(*requestIdentity); ok {
		return identity.accounts, nil
	}
	return a.upstream.Accounts(r.Context())
}

func (a *App) authentication(next http.Handler) http.Handler {
	if a.cfg.Mode != "auto" {
		return withAuth(a.cfg, next)
	}
	if a.sessions == nil {
		a.sessions = &sessionStore{items: make(map[[32]byte]adminSession)}
	}
	auto := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-DC-Request") != "1" {
			writeErr(w, 403, "缺少请求校验标记")
			return
		}
		public := !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/config" || r.URL.Path == "/api/health" || r.URL.Path == "/api/variants" || (r.URL.Path == "/api/session" && r.Method == "POST")
		if public {
			next.ServeHTTP(w, r)
			return
		}
		key := sha256.Sum256([]byte(r.Header.Get("X-DC-Session")))
		a.sessions.mu.Lock()
		session, exists := a.sessions.items[key]
		if exists && !session.expires.After(time.Now()) {
			delete(a.sessions.items, key)
			exists = false
		}
		a.sessions.mu.Unlock()
		if !exists {
			writeErr(w, 401, "连接已失效，请从 Sub2API 后台重新进入")
			return
		}
		cfg := a.cfg
		cfg.AdminToken = session.token
		u := newUpstreamClient(cfg)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		accounts, err := u.Accounts(ctx)
		cancel()
		if err != nil {
			var ae *adminError
			if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403) {
				a.sessions.mu.Lock()
				delete(a.sessions.items, key)
				a.sessions.mu.Unlock()
				writeErr(w, 401, "Sub2API 管理员会话已失效，请从后台重新进入")
			} else {
				writeErr(w, 502, "无法验证 Sub2API 管理员权限，请稍后重试")
			}
			return
		}
		identity := &requestIdentity{upstream: u, accounts: accounts}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	})
	if a.cfg.AuthUser != "" {
		return withAuth(a.cfg, auto)
	}
	return auto
}

func (a *App) handleSession(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "auto" {
		writeErr(w, 400, "当前模式不需要 Sub2API 会话")
		return
	}
	var body struct {
		Token  string `json:"token"`
		Source string `json:"src_host"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&body) != nil || body.Token == "" || len(body.Token) > 16384 || strings.ContainsAny(body.Token, "\r\n") {
		writeErr(w, 400, "请提供有效的 Sub2API 管理员登录令牌")
		return
	}
	// src_host is only an instance check. It can NEVER choose a network target.
	if body.Source != "" && NormalizeBase(body.Source) != NormalizeBase(a.cfg.PublicBase) && NormalizeBase(body.Source) != NormalizeBase(a.cfg.AdminBase) {
		writeErr(w, 400, "菜单来源与服务端配置的 Sub2API 地址不一致，请检查 SUB2API_PUBLIC_URL")
		return
	}
	cfg := a.cfg
	cfg.AdminToken = body.Token
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := newUpstreamClient(cfg).Accounts(ctx); err != nil {
		writeErr(w, 401, "无法验证管理员权限，请从配置的 Sub2API 后台重新进入")
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeErr(w, 500, "创建会话失败")
		return
	}
	id := hex.EncodeToString(random[:])
	now := time.Now()
	a.sessions.mu.Lock()
	defer a.sessions.mu.Unlock()
	for key, item := range a.sessions.items {
		if !item.expires.After(now) {
			delete(a.sessions.items, key)
		}
	}
	if len(a.sessions.items) >= 512 {
		writeErr(w, 503, "会话数量已达上限，请稍后重试")
		return
	}
	a.sessions.items[sha256.Sum256([]byte(id))] = adminSession{token: body.Token, expires: now.Add(8 * time.Hour)}
	writeJSON(w, 200, map[string]any{"session": id, "expires_in": 28800})
}

func (a *App) handleStepUp(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Mode != "auto" {
		writeErr(w, 400, "当前模式不需要二次验证")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || len(body.Code) != 6 || strings.Trim(body.Code, "0123456789") != "" {
		writeErr(w, 400, "请输入六位动态验证码")
		return
	}
	u := a.requestUpstream(r)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := u.do(ctx, u.cfg.AdminBase, u.cfg.AdminToken, "POST", "/api/v1/user/totp/step-up", body)
	if err != nil {
		writeErr(w, 502, "二次验证连接失败")
		return
	}
	var result struct {
		Data struct {
			Verified bool `json:"verified"`
		} `json:"data"`
	}
	if err := decodeAdmin(resp, &result); err != nil {
		writeUpstreamError(w, err)
		return
	}
	if !result.Data.Verified {
		writeErr(w, 403, "动态验证码未通过验证")
		return
	}
	writeJSON(w, 200, map[string]bool{"verified": true})
}
