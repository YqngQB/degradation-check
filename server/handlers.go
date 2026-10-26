package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// App 持有服务依赖。
type App struct {
	cfg      config
	store    *Store
	upstream *Upstream
	sessions *sessionStore

	mu      sync.Mutex
	running map[string]time.Time
	sem     chan struct{}
}

const maxConcurrent = 10

func (a *App) initRuntime() {
	if a.running == nil {
		a.running = make(map[string]time.Time)
	}
	if a.sem == nil {
		a.sem = make(chan struct{}, maxConcurrent)
	}
}

func (a *App) routes(mux *http.ServeMux) {
	a.initRuntime()
	mux.HandleFunc("POST /api/session", a.handleSession)
	mux.HandleFunc("POST /api/step-up", a.handleStepUp)
	mux.HandleFunc("GET /api/health", a.handleHealth)
	mux.HandleFunc("GET /api/config", a.handleConfig)
	mux.HandleFunc("GET /api/variants", a.handleVariants)
	mux.HandleFunc("GET /api/accounts", a.handleAccounts)
	mux.HandleFunc("GET /api/results", a.handleResults)
	mux.HandleFunc("POST /api/run", a.handleRun)
	mux.HandleFunc("DELETE /api/results", a.handleClear)
	mux.HandleFunc("GET /api/preview/{accountID}", a.handlePreview)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// ---------------- handlers ----------------

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"instance": a.cfg.Instance,
		"version":  Version,
	})
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"mode": a.cfg.Mode, "instance": a.cfg.Instance, "sub2api_url": a.cfg.PublicBase})
}

func (a *App) handleVariants(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"variants": PelicanVariants,
		"tests":    []string{TestCandy, TestFingerprint, TestPelican},
	})
}

func (a *App) handleAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := a.requestAccounts(r)
	if err != nil {
		writeErr(w, http.StatusBadGateway, a.upstream.redact(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": list})
}

func (a *App) handleResults(w http.ResponseWriter, r *http.Request) {
	results, err := a.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.mu.Lock()
	running := make([]map[string]any, 0, len(a.running))
	for key, startedAt := range a.running {
		parts := strings.SplitN(key, ":", 2)
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		testType := ""
		if len(parts) > 1 {
			testType = parts[1]
		}
		running = append(running, map[string]any{
			"account_id": id, "test_type": testType, "started_at": startedAt,
		})
	}
	a.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"results":  results,
		"running":  running,
		"variants": PelicanVariants,
	})
}

type runRequest struct {
	AccountIDs []int64 `json:"account_ids"`
	TestType   string  `json:"test_type"`
	Model      string  `json:"model"`
	Effort     string  `json:"effort"`
	Variant    string  `json:"variant"`
	Runs       int     `json:"runs"`
	Accounts   []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Platform string `json:"platform"`
		Type     string `json:"type"`
	} `json:"accounts"`
}

func (a *App) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	if len(req.AccountIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "请至少选择一个账号")
		return
	}
	if len(req.AccountIDs) > 200 {
		writeErr(w, http.StatusBadRequest, "单次最多 200 个账号")
		return
	}
	if req.TestType != TestCandy && req.TestType != TestFingerprint && req.TestType != TestPelican {
		writeErr(w, http.StatusBadRequest, "未知的测试类型")
		return
	}
	if strings.TrimSpace(req.Model) == "" || len(req.Model) > 256 {
		writeErr(w, http.StatusBadRequest, "请填写有效模型名")
		return
	}
	switch req.Effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		writeErr(w, http.StatusBadRequest, "不支持的推理强度")
		return
	}

	// Resolve IDs and metadata from server configuration/admin API. Ignore any
	// destination, credentials or account metadata supplied by the browser.
	accounts, err := a.requestAccounts(r)
	if err != nil {
		writeErr(w, http.StatusBadGateway, a.upstream.redact(err.Error()))
		return
	}
	meta := make(map[int64]Account, len(accounts))
	for _, acc := range accounts {
		meta[acc.ID] = acc
	}
	for _, id := range req.AccountIDs {
		if _, ok := meta[id]; !ok {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("未知的账号 ID: %d", id))
			return
		}
	}

	selected := make([]Account, 0, len(req.AccountIDs))
	unique := make(map[int64]bool)
	for _, id := range req.AccountIDs {
		if !unique[id] {
			selected = append(selected, meta[id])
			unique[id] = true
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	upstream, err := a.requestUpstream(r).prepared(ctx, selected)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	started, skipped := 0, 0
	var lastErr string
	for _, acc := range selected {
		if err := a.startTest(acc, req, upstream); err != nil {
			skipped++
			lastErr = err.Error()
			continue
		}
		started++
	}

	payload := map[string]any{"started": started, "skipped": skipped}
	if lastErr != "" {
		payload["message"] = lastErr
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) startTest(acc Account, req runRequest, upstream *Upstream) error {
	key := fmt.Sprintf("%d:%s", acc.ID, req.TestType)

	a.mu.Lock()
	if _, busy := a.running[key]; busy {
		a.mu.Unlock()
		return fmt.Errorf("账号 %d 正在测试", acc.ID)
	}
	a.running[key] = time.Now()
	a.mu.Unlock()

	go func() {
		a.sem <- struct{}{}
		defer func() { <-a.sem }()
		defer func() {
			a.mu.Lock()
			delete(a.running, key)
			a.mu.Unlock()
			if rec := recover(); rec != nil {
				fmt.Printf("[runner] panic account=%d test=%s\n", acc.ID, req.TestType)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		job := &App{cfg: a.cfg, upstream: upstream}
		var result *Result
		switch req.TestType {
		case TestCandy:
			result = job.runCandy(ctx, acc, req.Model, req.Effort, req.Runs)
		case TestPelican:
			result = job.runPelican(ctx, acc, req.Model, req.Effort, req.Variant)
		case TestFingerprint:
			result = job.runFingerprint(ctx, acc, req.Model)
		}
		if result == nil {
			return
		}
		result.TestedAt = time.Now()
		result.Error = upstream.redact(result.Error)
		if err := a.store.Upsert(result); err != nil {
			fmt.Printf("[store] upsert account=%d: %v\n", acc.ID, err)
		}
	}()
	return nil
}

func (a *App) handleClear(w http.ResponseWriter, r *http.Request) {
	testType := r.URL.Query().Get("test_type")
	if testType != "" && testType != TestCandy && testType != TestFingerprint && testType != TestPelican {
		writeErr(w, http.StatusBadRequest, "未知的测试类型")
		return
	}
	removed, err := a.store.Clear(testType)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// handlePreview 输出模型生成的画面（独立 CSP，方便 iframe 直接渲染）。
func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("accountID")
	accountID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "账号 ID 无效", http.StatusBadRequest)
		return
	}
	result, err := a.store.Latest(accountID, TestPelican)
	if err != nil {
		http.Error(w, "没有找到画面", http.StatusNotFound)
		return
	}
	var payload struct {
		HTML string `json:"html"`
	}
	if len(result.Payload) > 0 {
		_ = json.Unmarshal(result.Payload, &payload)
	}
	if strings.TrimSpace(payload.HTML) == "" {
		http.Error(w, "这条记录还没有画面", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
			"img-src data: blob:; font-src data:; media-src data: blob:; sandbox allow-scripts; frame-ancestors 'self'")
	_, _ = w.Write([]byte(payload.HTML))
}

// ---------------- 结果辅助 ----------------

func (a *App) newResult(acc Account, testType, model, effort, variant string, _ any) *Result {
	return &Result{
		AccountID:       acc.ID,
		TestType:        testType,
		AccountName:     acc.Name,
		AccountEmail:    acc.Email,
		AccountPlatform: acc.Platform,
		AccountType:     acc.Type,
		Model:           model,
		Effort:          effort,
		Variant:         variant,
		Status:          StatusFailed,
		TestedAt:        time.Now(),
	}
}

func applyUsage(r *Result, out *TestOutcome) {
	if out == nil {
		return
	}
	r.DurationMS = out.Duration.Milliseconds()
	r.InputTokens = out.InputTokens
	r.OutputTokens = out.OutputTokens
	r.ReasoningTokens = out.ReasoningTokens
}
