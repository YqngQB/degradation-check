package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func autoRequest(handler http.Handler, method, path, session, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-DC-Request", "1")
	if session != "" {
		r.Header.Set("X-DC-Session", session)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func autoSession(t *testing.T, handler http.Handler, token string) string {
	t.Helper()
	w := autoRequest(handler, "POST", "/api/session", "", fmt.Sprintf(`{"token":%q}`, token))
	var data struct {
		Session string `json:"session"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || data.Session == "" {
		t.Fatalf("session failed: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("admin token returned")
	}
	return data.Session
}

func TestAutoSelectedAccountsAndStepUp(t *testing.T) {
	const adminToken = "mock-admin-jwt-secret"
	var granted, revoked atomic.Bool
	var mu sync.Mutex
	var exports []int
	var upstreamCalls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "actual-model" || body["stream"] != true {
			t.Error("model mapping missing")
		}
		if r.Header.Get("X-Relay-Route") != "custom-route" {
			t.Error("header override missing")
		}
		data, _ := json.Marshal(body)
		if !strings.Contains(string(data), "鹈鹕") || strings.Contains(string(data), `"hi"`) {
			t.Error("real prompt not forwarded")
		}
		key := "relay-key-six"
		if r.URL.Path == "/v1/chat/completions" {
			key = "relay-key-seven"
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("wrong selected credential")
		}
		text := "<svg>" + key + " " + adminToken + "</svg>"
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]string{"text": text}}}}})
		} else if r.URL.Path == "/v1/chat/completions" {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}})
		} else {
			t.Error("wrong protocol path")
			w.WriteHeader(404)
		}
	}))
	defer relay.Close()
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+adminToken || revoked.Load() {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/accounts":
			fmt.Fprint(w, `{"data":{"items":[{"id":6,"name":"same-name","platform":"openai","type":"apikey","status":"active","credentials_status":{"has_api_key":true},"extra":{"openai_responses_mode":"force_responses","openai_responses_supported":false}},{"id":7,"name":"same-name","platform":"openai","type":"apikey","status":"active","extra":{"openai_responses_mode":"force_chat_completions","openai_responses_supported":true}}],"total":2}}`)
		case "/api/v1/admin/accounts/data":
			if !granted.Load() {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"code":"STEP_UP_REQUIRED","message":"This operation requires recent verification"}`)
				return
			}
			id, _ := strconv.Atoi(r.URL.Query().Get("ids"))
			if id != 6 && id != 7 || r.URL.Query().Get("include_proxies") != "true" {
				t.Error("must export exactly selected ID")
			}
			mu.Lock()
			exports = append(exports, id)
			mu.Unlock()
			mode, key := "force_responses", "relay-key-six"
			if id == 7 {
				mode, key = "force_chat_completions", "relay-key-seven"
			}
			account := map[string]any{"name": "same-name", "platform": "openai", "type": "apikey", "credentials": map[string]any{"base_url": relay.URL, "api_key": key, "model_mapping": map[string]string{"alias": "actual-model"}, "header_override_enabled": true, "header_overrides": map[string]string{"X-Relay-Route": "custom-route"}}, "extra": map[string]string{"openai_responses_mode": mode}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accounts": []any{account}, "proxies": []any{}}})
		case "/api/v1/user/totp/step-up":
			var code struct {
				Code string `json:"code"`
			}
			_ = json.NewDecoder(r.Body).Decode(&code)
			if code.Code != "123456" {
				w.WriteHeader(400)
				return
			}
			granted.Store(true)
			fmt.Fprint(w, `{"data":{"verified":true,"expires_in":300}}`)
		default:
			t.Errorf("unexpected admin endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer admin.Close()
	cfg := config{Mode: "auto", AdminBase: admin.URL, PublicBase: admin.URL}
	store, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &App{cfg: cfg, store: store, upstream: newUpstreamClient(cfg)}
	mux := http.NewServeMux()
	a.routes(mux)
	handler := withCommonHeaders(a.authentication(mux), admin.URL)
	for _, route := range []struct{ method, path string }{{"GET", "/api/accounts"}, {"GET", "/api/results"}, {"GET", "/api/preview/6"}, {"DELETE", "/api/results"}, {"POST", "/api/run"}, {"POST", "/api/step-up"}} {
		if got := autoRequest(handler, route.method, route.path, "", "{}").Code; got != 401 {
			t.Fatalf("unprotected %s: %d", route.path, got)
		}
	}
	// Browser host must not pick an arbitrary credential destination.
	w := autoRequest(handler, "POST", "/api/session", "", `{"token":"`+adminToken+`","src_host":"https://attacker.invalid"}`)
	if w.Code != 400 {
		t.Fatal("untrusted source accepted")
	}
	session := autoSession(t, handler, adminToken)
	w = autoRequest(handler, "GET", "/api/accounts", session, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "responses") || strings.Contains(w.Body.String(), "credentials") {
		t.Fatalf("account metadata: %s", w.Body)
	}
	payload := `{"account_ids":[6,7],"test_type":"pelican","model":"alias","variant":"original"}`
	w = autoRequest(handler, "POST", "/api/run", session, payload)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "STEP_UP_REQUIRED") || upstreamCalls.Load() != 0 {
		t.Fatalf("step-up bypassed: %s", w.Body)
	}
	w = autoRequest(handler, "POST", "/api/step-up", session, `{"code":"123456"}`)
	if w.Code != 200 {
		t.Fatalf("step-up: %s", w.Body)
	}
	w = autoRequest(handler, "POST", "/api/run", session, payload)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"started":2`) {
		t.Fatalf("run: %s", w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		active := len(a.running)
		a.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if upstreamCalls.Load() != 2 {
		t.Fatal("wrong upstream call count")
	}
	mu.Lock()
	if fmt.Sprint(exports) != "[6 7]" {
		t.Errorf("exports: %v", exports)
	}
	mu.Unlock()
	w = autoRequest(handler, "GET", "/api/results", session, "")
	if w.Code != 200 || strings.Count(w.Body.String(), `"status":"completed"`) != 2 {
		t.Fatalf("results: %s", w.Body)
	}
	for _, secret := range []string{adminToken, "relay-key-six", "relay-key-seven"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret in results")
		}
	}
	w = autoRequest(handler, "GET", "/api/preview/6", session, "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatal("preview auth/sandbox failed")
	}
	if a.upstream.cfg.AdminToken != "" || len(a.upstream.cfg.Endpoints) != 0 {
		t.Fatal("request credentials mutated global client")
	}
	revoked.Store(true)
	if w = autoRequest(handler, "GET", "/api/results", session, ""); w.Code != 401 {
		t.Fatal("revoked admin session retained access")
	}
}

func TestAutoPagination(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		count, start := 100, 1
		if page == 2 {
			count, start = 1, 101
		}
		items := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, map[string]any{"id": start + i, "name": "account"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": items, "total": 101}})
	}))
	defer server.Close()
	u := newUpstreamClient(config{Mode: "auto", AdminBase: server.URL})
	accounts, err := u.Accounts(context.Background())
	if err != nil || len(accounts) != 101 || pages != 2 {
		t.Fatalf("pagination: %d/%d %v", len(accounts), pages, err)
	}
}

func TestAutoProxyAndMapping(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "unresolvable.invalid" || r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get("Proxy-Authorization") == "" {
			t.Error("proxy route/auth incorrect")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"mapped"`) {
			t.Error("wildcard mapping missing")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[{"content":[{"text":"answer"}]}]}`)
	}))
	defer proxy.Close()
	parsed, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(parsed.Port())
	var expired atomic.Bool
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyData := map[string]any{"proxy_key": "proxy-one", "protocol": "http", "host": parsed.Hostname(), "port": port, "username": "user", "password": "proxy-secret", "status": "active"}
		if expired.Load() {
			proxyData["expires_at"] = time.Now().Add(-time.Minute).Unix()
		}
		account := map[string]any{"name": "proxy-account", "platform": "openai", "type": "apikey", "proxy_key": "proxy-one", "credentials": map[string]any{"base_url": "http://unresolvable.invalid", "api_key": "upstream-key", "model_mapping": map[string]string{"gpt-*": "mapped", "*": "wrong"}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accounts": []any{account}, "proxies": []any{proxyData}}})
	}))
	defer admin.Close()
	u := newUpstreamClient(config{Mode: "auto", AdminBase: admin.URL})
	account := Account{ID: 1, Name: "proxy-account", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true}
	prepared, err := u.prepared(context.Background(), []Account{account})
	if err != nil {
		t.Fatal(err)
	}
	out, err := prepared.TestAccount(context.Background(), 1, "gpt-custom", "prompt", "")
	if err != nil || out.Text != "answer" || calls.Load() != 1 {
		t.Fatalf("proxy test: %+v %v", out, err)
	}
	expired.Store(true)
	if _, err = u.prepared(context.Background(), []Account{account}); err == nil || !strings.Contains(err.Error(), "代理") {
		t.Fatal("expired proxy silently bypassed")
	}
	if safeOverride("Authorization", "bad") || safeOverride("X-Test", "bad\r\nInjected: yes") {
		t.Fatal("unsafe header accepted")
	}
}

func TestAutoConfigAndInstanceBinding(t *testing.T) {
	t.Setenv("DETECTION_MODE", "auto")
	t.Setenv("SUB2API_BASE_URL", "https://sub2api.example")
	t.Setenv("DC_AUTH_USER", "")
	t.Setenv("DC_AUTH_PASSWORD", "")
	cfg, err := loadConfig()
	if err != nil || cfg.Mode != "auto" || len(cfg.Endpoints) != 0 {
		t.Fatalf("auto should need only instance address: %v", err)
	}
	store, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.bindInstance(cfg.AdminBase); err != nil {
		t.Fatal(err)
	}
	if err = store.bindInstance(cfg.AdminBase); err != nil {
		t.Fatal(err)
	}
	if err = store.bindInstance("https://other.example"); err == nil {
		t.Fatal("mixed instance histories")
	}
}

func TestAutoProtocolAndHeaderPolicy(t *testing.T) {
	for _, tc := range []struct {
		extra map[string]any
		want  string
	}{
		{nil, "responses"},
		{map[string]any{"openai_responses_supported": false}, "chat_completions"},
		{map[string]any{"openai_responses_supported": "false"}, "responses"},
		{map[string]any{"openai_responses_mode": "force_responses", "openai_responses_supported": false}, "responses"},
		{map[string]any{"openai_responses_mode": "force_chat_completions", "openai_responses_supported": true}, "chat_completions"},
	} {
		if got := accountProtocol(tc.extra); got != tc.want {
			t.Errorf("protocol: %s != %s", got, tc.want)
		}
	}
	handler := withCommonHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), "https://official.example")
	r := httptest.NewRequest("GET", "http://detector.example/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	policy := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "https://detector.example") || !strings.Contains(policy, "https://official.example") {
		t.Fatal("sandboxed nested preview parents missing")
	}
	r.Host = "bad.example; frame-ancestors *"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "bad.example") {
		t.Fatal("host injected into CSP")
	}
}
