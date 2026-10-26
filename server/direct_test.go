package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectPelicanSendsRealPrompt(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				wantPath := "/v1/responses"
				if protocol == "chat_completions" {
					wantPath = "/v1/chat/completions"
				}
				if r.URL.Path != wantPath {
					t.Errorf("path = %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-upstream-secret" {
					t.Error("missing upstream auth")
				}
				body, _ := io.ReadAll(r.Body)
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Error(err)
				}
				if !strings.Contains(string(body), "鹈鹕") || strings.Contains(string(body), `"text":"hi"`) {
					t.Errorf("real prompt not sent: %s", body)
				}
				if payload["model"] != "relay-model" || payload["stream"] != true {
					t.Errorf("payload: %s", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == "responses" {
					if payload["input"] == nil || payload["messages"] != nil || payload["max_output_tokens"] != float64(4096) {
						t.Errorf("bad responses payload: %s", body)
					}
					if payload["reasoning"].(map[string]any)["effort"] != "high" {
						t.Error("effort not forwarded")
					}
					fmt.Fprint(w, "event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"<svg></svg>\"}\r\n\r\n")
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n")
				} else {
					if payload["messages"] == nil || payload["input"] != nil || payload["max_completion_tokens"] != float64(4096) {
						t.Errorf("bad chat payload: %s", body)
					}
					if payload["reasoning_effort"] != "high" {
						t.Error("effort not forwarded")
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<svg></svg>\"},\"finish_reason\":null}]}\n\n")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"completion_tokens_details\":{\"reasoning_tokens\":3}}}\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			cfg := config{Mode: "direct", Endpoints: []Endpoint{{ID: 6, Name: "relay", BaseURL: server.URL + "/v1", Protocol: protocol, apiKey: "test-upstream-secret", MaxOutputTokens: 4096}}}
			app := &App{cfg: cfg, upstream: newUpstreamClient(cfg)}
			result := app.runPelican(context.Background(), Account{ID: 6}, "relay-model", "high", "original")
			if calls != 1 {
				t.Errorf("want exactly one upstream request, got %d", calls)
			}
			if result.Status != StatusCompleted || !strings.Contains(string(result.Payload), "svg") {
				t.Fatalf("result: %+v", result)
			}
			if result.InputTokens != 10 || result.OutputTokens != 20 || result.ReasoningTokens != 3 {
				t.Errorf("usage not preserved: %+v", result)
			}
		})
	}
}

func TestParseDirectResponses(t *testing.T) {
	for _, tc := range []struct{ name, protocol, contentType, body, want, err string }{
		{"responses JSON", "responses", "application/json", `{"status":"completed","output":[{"content":[{"type":"output_text","text":"answer"}]}]}`, "answer", ""},
		{"chat JSON", "chat_completions", "application/json", `{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`, "answer", ""},
		{"terminal snapshot", "responses", "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"text\":\"answer\"}]}]}}\n\n", "answer", ""},
		{"multi line", "responses", "text/event-stream", "data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"text\":\"answer\"}]}]}}\n\n", "answer", ""},
		{"failure", "responses", "text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"Upstream request failed\"}}}\n\n", "", "Upstream request failed"},
		{"incomplete", "responses", "text/event-stream", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", "", "max_output_tokens"},
		{"transport cut", "responses", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\n", "", "提前结束"},
		{"wrong wire", "responses", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"21\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "", "提前结束"},
		{"chat length", "chat_completions", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"21\"},\"finish_reason\":\"length\"}]}\n\n", "", "length"},
		{"error event", "responses", "text/event-stream", "data: {\"type\":\"error\",\"message\":\"permission denied\"}\n\n", "", "permission denied"},
		{"empty", "responses", "application/json", `{"status":"completed","output":[]}`, "", "没有文本输出"},
		{"bad JSON", "responses", "application/json", `not json`, "", "无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := parseDirectResponse(strings.NewReader(tc.body), tc.contentType, tc.protocol)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil || out.Text != tc.want {
				t.Fatalf("got %+v %v", out, err)
			}
		})
	}
}

func TestRedirectAndErrorRedaction(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("error") != "" {
			return
		}
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		fmt.Fprint(w, "echo test-secret-key")
	}))
	defer relay.Close()
	cfg := config{Mode: "direct", Endpoints: []Endpoint{{ID: 1, BaseURL: relay.URL, Protocol: "responses", apiKey: "test-secret-key"}}}
	u := newUpstreamClient(cfg)
	_, err := u.TestAccount(context.Background(), 1, "model", "prompt", "")
	if err == nil || !strings.Contains(err.Error(), "307") || strings.Contains(err.Error(), "test-secret-key") {
		t.Fatalf("unsafe error: %v", err)
	}
	if calls != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestEndpointURLs(t *testing.T) {
	for _, tc := range []struct{ base, protocol, want string }{
		{"https://relay.example/", "responses", "https://relay.example/v1/responses"},
		{"https://relay.example/v1/", "chat_completions", "https://relay.example/v1/chat/completions"},
		{"https://relay.example/openai/v1", "responses", "https://relay.example/openai/v1/responses"},
	} {
		if got := endpointURL(Endpoint{BaseURL: tc.base, Protocol: tc.protocol}); got != tc.want {
			t.Errorf("%s -> %s", tc.base, got)
		}
	}
}

func TestConfigRequiresServerSecretsAndRejectsArbitraryDestinations(t *testing.T) {
	t.Setenv("DC_AUTH_USER", "operator")
	t.Setenv("DC_AUTH_PASSWORD", "a-long-test-password")
	t.Setenv("DETECTION_MODE", "direct")
	t.Setenv("RELAY_KEY", "secret-only-on-server")
	path := filepath.Join(t.TempDir(), "upstreams.json")
	t.Setenv("UPSTREAMS_FILE", path)
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `[{"id":1,"name":"relay","base_url":"https://relay.example/v1","protocol":"responses","api_key_env":"RELAY_KEY"}]`, true},
		{"unknown protocol", `[{"id":1,"name":"relay","base_url":"https://relay.example","protocol":"auto","api_key_env":"RELAY_KEY"}]`, false},
		{"embedded key", `[{"id":1,"name":"relay","base_url":"https://relay.example","protocol":"responses","api_key":"secret"}]`, false},
		{"missing key", `[{"id":1,"name":"relay","base_url":"https://relay.example","protocol":"responses","api_key_env":"UNSET_TEST_RELAY_KEY"}]`, false},
		{"query", `[{"id":1,"name":"relay","base_url":"https://relay.example?token=secret","protocol":"responses","api_key_env":"RELAY_KEY"}]`, false},
		{"endpoint as root", `[{"id":1,"name":"relay","base_url":"https://relay.example/v1/responses","protocol":"responses","api_key_env":"RELAY_KEY"}]`, false},
		{"duplicate IDs", `[{"id":1,"name":"a","base_url":"https://relay.example","protocol":"responses","api_key_env":"RELAY_KEY"},{"id":1,"name":"b","base_url":"https://relay.example","protocol":"responses","api_key_env":"RELAY_KEY"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid {
				if cfg.Endpoints[0].apiKey != "secret-only-on-server" {
					t.Fatal("key not resolved")
				}
				if cfg.Host != "127.0.0.1" {
					t.Fatal("not loopback by default")
				}
			}
		})
	}
	t.Setenv("DC_AUTH_PASSWORD", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("startup allowed without authentication")
	}
}

func TestAllRoutesRequireAuthentication(t *testing.T) {
	cfg := config{Mode: "direct", AuthUser: "operator", AuthPassword: "long-enough-password", Endpoints: []Endpoint{{ID: 6, Name: "relay", Protocol: "responses", apiKey: "super-secret-upstream"}}}
	store, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &App{cfg: cfg, store: store, upstream: newUpstreamClient(cfg)}
	mux := http.NewServeMux()
	app.routes(mux)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "frontend") })
	handler := withCommonHeaders(withAuth(cfg, mux))
	for _, path := range []string{"/", "/api/config", "/api/results", "/api/accounts", "/api/preview/6", "/api/health"} {
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest("GET", path, nil)
			if authenticated {
				req.SetBasicAuth(cfg.AuthUser, cfg.AuthPassword)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if !authenticated && rec.Code != 401 {
				t.Errorf("public route: %s (%d)", path, rec.Code)
			}
			if authenticated && rec.Code == 401 {
				t.Errorf("login failed: %s", path)
			}
			if strings.Contains(rec.Body.String(), "super-secret-upstream") {
				t.Fatal("key leaked to API")
			}
		}
	}
	for _, method := range []string{"DELETE", "POST"} {
		path := "/api/results"
		if method == "POST" {
			path = "/api/run"
		}
		// Supplying the CSRF marker cannot bypass authentication either.
		publicReq := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		publicReq.Header.Set("X-DC-Request", "1")
		publicRec := httptest.NewRecorder()
		handler.ServeHTTP(publicRec, publicReq)
		if publicRec.Code != 401 {
			t.Errorf("unauthenticated mutation accepted: %s %d", method, publicRec.Code)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.SetBasicAuth(cfg.AuthUser, cfg.AuthPassword)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("cross-site mutation accepted: %s %d", method, rec.Code)
		}
	}
	// Browser-provided host, token, and metadata cannot introduce a new target.
	req := httptest.NewRequest("POST", "/api/run", strings.NewReader(`{"account_ids":[999],"test_type":"pelican","model":"gpt-test","accounts":[{"id":999,"name":"forged"}]}`))
	req.SetBasicAuth(cfg.AuthUser, cfg.AuthPassword)
	req.Header.Set("X-DC-Request", "1")
	req.Header.Set("X-Sub2API-Base", "http://127.0.0.1:1")
	req.Header.Set("X-Sub2API-Token", "attacker")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("unknown account accepted: %d %s", rec.Code, rec.Body.String())
	}
}
