package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// Endpoint IDs are stable local identifiers, not Sub2API account IDs. Secrets are
// resolved once at startup and never serialized into the browser or database.
type Endpoint struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"base_url"`
	Protocol        string   `json:"protocol"`
	APIKeyEnv       string   `json:"api_key_env"`
	Models          []string `json:"models,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	apiKey          string
}

type config struct {
	Host, Port, DataDir, Instance string
	AuthUser, AuthPassword        string
	Mode, AdminBase, AdminToken   string
	Endpoints                     []Endpoint
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func loadConfig() (config, error) {
	cfg := config{
		Host: envDefault("HOST", "127.0.0.1"), Port: envDefault("PORT", "8088"),
		DataDir: envDefault("DATA_DIR", "./data"), Instance: envDefault("INSTANCE_NAME", "降智检测"),
		AuthUser: os.Getenv("DC_AUTH_USER"), AuthPassword: os.Getenv("DC_AUTH_PASSWORD"),
		Mode:      envDefault("DETECTION_MODE", "direct"),
		AdminBase: os.Getenv("SUB2API_BASE_URL"), AdminToken: os.Getenv("SUB2API_ADMIN_TOKEN"),
	}
	if cfg.AuthUser == "" || strings.Contains(cfg.AuthUser, ":") || len(cfg.AuthPassword) < 16 {
		return cfg, fmt.Errorf("设置 DC_AUTH_USER（不含冒号）及至少 16 字符的 DC_AUTH_PASSWORD；所有页面和结果均需要认证")
	}
	switch cfg.Mode {
	case "direct":
		path := envDefault("UPSTREAMS_FILE", "./upstreams.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("读取上游配置失败，请设置 UPSTREAMS_FILE: %w", err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg.Endpoints); err != nil {
			return cfg, fmt.Errorf("上游配置不是有效的 Endpoint 数组: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return cfg, fmt.Errorf("上游配置只能包含一个 JSON 数组")
		}
		if len(cfg.Endpoints) == 0 {
			return cfg, fmt.Errorf("至少配置一个上游")
		}
		ids := make(map[int64]bool)
		for i := range cfg.Endpoints {
			ep := &cfg.Endpoints[i]
			if ep.ID <= 0 || ids[ep.ID] || strings.TrimSpace(ep.Name) == "" {
				return cfg, fmt.Errorf("每个上游必须有唯一的正整数 id 和非空 name")
			}
			ids[ep.ID] = true
			if ep.Protocol != "responses" && ep.Protocol != "chat_completions" {
				return cfg, fmt.Errorf("上游 #%d protocol 必须是 responses 或 chat_completions", ep.ID)
			}
			if err := validateBase(ep.BaseURL); err != nil {
				return cfg, fmt.Errorf("上游 #%d: %w", ep.ID, err)
			}
			if ep.MaxOutputTokens < 0 {
				return cfg, fmt.Errorf("上游 #%d max_output_tokens 不能为负数", ep.ID)
			}
			ep.apiKey = os.Getenv(ep.APIKeyEnv)
			if ep.apiKey == "" || strings.ContainsAny(ep.apiKey, "\r\n") {
				return cfg, fmt.Errorf("上游 #%d 的 api_key_env 未设置或包含换行", ep.ID)
			}
		}
	case "sub2api":
		if err := validateBase(cfg.AdminBase); err != nil || cfg.AdminToken == "" {
			return cfg, fmt.Errorf("兼容模式需要有效的 SUB2API_BASE_URL 和 SUB2API_ADMIN_TOKEN")
		}
	default:
		return cfg, fmt.Errorf("DETECTION_MODE 必须是 direct 或 sub2api")
	}
	return cfg, nil
}

func validateBase(base string) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url 必须是带 http(s) 的地址，不能包含凭据、查询参数或片段")
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/chat/completions") {
		return fmt.Errorf("base_url 填 API 根地址（例如 https://relay.example/v1），不要填具体接口")
	}
	return nil
}
