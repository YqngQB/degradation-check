package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func accountProtocol(extra map[string]any) string {
	switch toString(extra["openai_responses_mode"]) {
	case "force_responses":
		return "responses"
	case "force_chat_completions":
		return "chat_completions"
	}
	if supported, ok := extra["openai_responses_supported"].(bool); ok && !supported {
		return "chat_completions"
	}
	return "responses"
}

type exportedAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
	ProxyKey    string         `json:"proxy_key"`
}
type exportedProxy struct {
	Key       string `json:"proxy_key"`
	Protocol  string `json:"protocol"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Status    string `json:"status"`
	ExpiresAt *int64 `json:"expires_at"`
}

// Export is the official privileged backup API; list/detail deliberately redact
// credentials. Export ONE ID at a time because the export schema contains no ID.
// The raw response is never returned to a browser or persisted.
func (u *Upstream) selectedEndpoint(ctx context.Context, account Account) (Endpoint, error) {
	ep := Endpoint{ID: account.ID, Name: account.Name}
	if account.Platform != "openai" || account.Type != "apikey" {
		return ep, fmt.Errorf("账号 #%d：自动直连目前仅支持 OpenAI API Key 账号", account.ID)
	}
	if account.Status != "active" || !account.Schedulable {
		return ep, fmt.Errorf("账号 #%d 不处于可用状态", account.ID)
	}
	resp, err := u.do(ctx, u.cfg.AdminBase, u.cfg.AdminToken, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/accounts/data?ids=%d&include_proxies=true", account.ID), nil)
	if err != nil {
		return ep, fmt.Errorf("账号导出请求失败")
	}
	var payload struct {
		Data struct {
			Accounts []exportedAccount `json:"accounts"`
			Proxies  []exportedProxy   `json:"proxies"`
		} `json:"data"`
	}
	if err := decodeAdmin(resp, &payload); err != nil {
		return ep, err
	}
	if len(payload.Data.Accounts) != 1 {
		return ep, fmt.Errorf("账号 #%d 导出为空或不唯一（影子账号不持有独立凭据）", account.ID)
	}
	data := payload.Data.Accounts[0]
	if data.Name != account.Name || data.Platform != account.Platform || data.Type != account.Type {
		return ep, fmt.Errorf("账号 #%d 信息发生变化，请刷新账号列表后重试", account.ID)
	}
	ep.BaseURL = firstNonEmpty(toString(data.Credentials["base_url"]), "https://api.openai.com")
	if err := validateBase(ep.BaseURL); err != nil {
		return ep, fmt.Errorf("账号 #%d 上游地址格式不正确", account.ID)
	}
	ep.apiKey = toString(data.Credentials["api_key"])
	if ep.apiKey == "" || strings.ContainsAny(ep.apiKey, "\r\n*") || strings.Contains(ep.apiKey, "…") {
		return ep, fmt.Errorf("账号 #%d 未导出有效的原始 API Key", account.ID)
	}
	ep.Protocol = accountProtocol(data.Extra)
	ep.modelMapping = make(map[string]string)
	if mapping, ok := data.Credentials["model_mapping"].(map[string]any); ok {
		for source, target := range mapping {
			if s, ok := target.(string); ok {
				ep.modelMapping[source] = s
			}
		}
	}
	// Unusual authentication/session header overrides must never replace the
	// account's own authorization. Fail explicitly rather than send a wrong test.
	ep.headers = make(map[string]string)
	if agent := toString(data.Credentials["user_agent"]); agent != "" {
		ep.headers["User-Agent"] = agent
	}
	if enabled, _ := data.Credentials["header_override_enabled"].(bool); enabled {
		if headers, ok := data.Credentials["header_overrides"].(map[string]any); ok {
			for key, value := range headers {
				s := toString(value)
				if s == "" {
					continue
				}
				if !safeOverride(key, s) {
					return ep, fmt.Errorf("账号 #%d 有不支持的请求头覆写，请先检查账号配置", account.ID)
				}
				ep.headers[http.CanonicalHeaderKey(key)] = s
				ep.secrets = append(ep.secrets, s)
			}
		}
	}
	for key, value := range ep.headers {
		if !safeOverride(key, value) {
			return ep, fmt.Errorf("账号 #%d 请求头格式不正确", account.ID)
		}
	}
	if data.ProxyKey != "" {
		found := false
		for _, proxy := range payload.Data.Proxies {
			if proxy.Key != data.ProxyKey {
				continue
			}
			found = true
			if proxy.Status != "active" || (proxy.ExpiresAt != nil && *proxy.ExpiresAt <= time.Now().Unix()) {
				return ep, fmt.Errorf("账号 #%d 代理已停用或过期；检测不会自动绕过代理", account.ID)
			}
			switch proxy.Protocol {
			case "http", "https", "socks5", "socks5h":
			default:
				return ep, fmt.Errorf("账号 #%d 代理协议不支持", account.ID)
			}
			if proxy.Host == "" || strings.ContainsAny(proxy.Host, "/@?#\r\n") || proxy.Port < 1 || proxy.Port > 65535 {
				return ep, fmt.Errorf("账号 #%d 代理地址不正确", account.ID)
			}
			proxyURL := &url.URL{Scheme: proxy.Protocol, Host: net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port))}
			if proxy.Username != "" {
				proxyURL.User = url.UserPassword(proxy.Username, proxy.Password)
			}
			ep.proxyURL = proxyURL.String()
			ep.secrets = append(ep.secrets, proxy.Password, ep.proxyURL)
			break
		}
		if !found {
			return ep, fmt.Errorf("账号 #%d 代理未导出，无法保证检测使用相同出口", account.ID)
		}
	}
	return ep, nil
}

func safeOverride(key, value string) bool {
	lower := strings.ToLower(key)
	blocked := " host content-length content-type transfer-encoding connection keep-alive proxy-authenticate proxy-authorization proxy-connection te trailer upgrade authorization x-api-key x-goog-api-key cookie accept-encoding session_id conversation_id x-codex-turn-state x-codex-turn-metadata chatgpt-account-id x-claude-code-session-id x-client-request-id x-grok-conv-id "
	if key == "" || len(key) > 200 || len(value) > 8192 || strings.Contains(blocked, " "+lower+" ") || strings.HasPrefix(lower, "sec-") {
		return false
	}
	for _, ch := range key {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", ch)) {
			return false
		}
	}
	for _, ch := range value {
		if ch == 127 || (ch < 32 && ch != '\t') {
			return false
		}
	}
	return true
}

func mappedModel(ep Endpoint, model string) string {
	if target, ok := ep.modelMapping[model]; ok {
		return target
	}
	var patterns []string
	for pattern := range ep.modelMapping {
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
			patterns = append(patterns, pattern)
		}
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) != len(patterns[j]) {
			return len(patterns[i]) > len(patterns[j])
		}
		return patterns[i] < patterns[j]
	})
	if len(patterns) > 0 {
		return ep.modelMapping[patterns[0]]
	}
	return model
}

func (u *Upstream) prepared(ctx context.Context, accounts []Account) (*Upstream, error) {
	if u.cfg.Mode != "auto" {
		return u, nil
	}
	cfg := u.cfg
	cfg.Endpoints = nil
	for _, account := range accounts {
		ep, err := u.selectedEndpoint(ctx, account)
		if err != nil {
			return nil, err
		}
		cfg.Endpoints = append(cfg.Endpoints, ep)
	}
	return newUpstreamClient(cfg), nil
}
