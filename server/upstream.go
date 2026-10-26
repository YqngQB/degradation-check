package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Account 是 Sub2API 账号的最小子集（不同版本字段可能不同，只取稳定字段）。
type Account struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Platform    string   `json:"platform"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	PlanType    string   `json:"plan_type"`
	Priority    int      `json:"priority"`
	Schedulable bool     `json:"schedulable"`
	Protocol    string   `json:"protocol,omitempty"`
	Models      []string `json:"models,omitempty"`
}

// Upstream 负责调用 Sub2API 的管理 API。
type Upstream struct {
	client *http.Client
	cfg    config
}

func newUpstreamClient(cfg config) *Upstream {
	return &Upstream{cfg: cfg, client: &http.Client{
		Timeout: 15 * time.Minute,
		// Never forward credentials to a redirect target, including subdomains.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// NormalizeBase 把用户给的地址补成 https://host 形式。
func NormalizeBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	return strings.TrimSuffix(raw, "/")
}

func (u *Upstream) do(ctx context.Context, base, token, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, NormalizeBase(base)+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream, application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return u.client.Do(req)
}

// Accounts 拉账号列表。不同 Sub2API 版本的字段略有差异，这里做兼容取值。
func (u *Upstream) adminAccounts(ctx context.Context, base, token string) ([]Account, error) {
	var items []map[string]any
	seen := make(map[int64]bool)
	for page := 1; ; page++ {
		if page > 1000 {
			return nil, fmt.Errorf("账号列表分页超过上限")
		}
		resp, err := u.do(ctx, base, token, http.MethodGet, fmt.Sprintf("/api/v1/admin/accounts?page=%d&page_size=100", page), nil)
		if err != nil {
			return nil, fmt.Errorf("请求 Sub2API 失败")
		}
		var envelope struct {
			Data struct {
				Items []map[string]any `json:"items"`
				Total int              `json:"total"`
			} `json:"data"`
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		}
		if err := decodeAdmin(resp, &envelope); err != nil {
			return nil, err
		}
		batch, total := envelope.Data.Items, envelope.Data.Total
		if batch == nil {
			batch, total = envelope.Items, envelope.Total
		}
		for _, item := range batch {
			id := toInt64(item["id"])
			if id <= 0 || seen[id] {
				return nil, fmt.Errorf("账号分页重复或 ID 无效，请刷新重试")
			}
			seen[id] = true
			items = append(items, item)
		}
		if (total > 0 && len(items) >= total) || (total == 0 && len(batch) < 100) {
			break
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("账号分页提前结束")
		}
	}

	out := make([]Account, 0, len(items))
	for _, m := range items {
		a := Account{
			ID:       toInt64(m["id"]),
			Name:     toString(m["name"]),
			Platform: toString(m["platform"]),
			Type:     toString(m["type"]),
			Status:   toString(m["status"]),
			Priority: int(toInt64(m["priority"])),
		}
		if v, ok := m["schedulable"].(bool); ok {
			a.Schedulable = v
		} else {
			a.Schedulable = true
		}
		// email / plan_type 可能直接在顶层，也可能埋在 credentials / extra 里。
		a.Email = firstNonEmpty(toString(m["email"]), nested(m, "credentials", "email"), nested(m, "extra", "email_address"))
		a.PlanType = firstNonEmpty(toString(m["plan_type"]), nested(m, "credentials", "plan_type"))
		if a.Platform == "openai" && a.Type == "apikey" {
			extra, _ := m["extra"].(map[string]any)
			a.Protocol = accountProtocol(extra)
		}
		out = append(out, a)
	}
	return out, nil
}

// TestOutcome 是一次账号测试的解析结果。
type TestOutcome struct {
	Text            string
	Error           string
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	Duration        time.Duration
}

// TestAccount 调 Sub2API 的账号测试接口（SSE），把正文拼起来。
// prompt 为空时用 Sub2API 自己的默认提示词。
func (u *Upstream) testAdminAccount(ctx context.Context, base, token string, accountID int64, model, prompt string) (*TestOutcome, error) {
	started := time.Now()
	body := map[string]any{}
	if strings.TrimSpace(model) != "" {
		body["model_id"] = model
	}
	if strings.TrimSpace(prompt) != "" {
		body["prompt"] = prompt
	}

	resp, err := u.do(ctx, base, token, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/accounts/%d/test", accountID), body)
	if err != nil {
		return nil, fmt.Errorf("请求测试接口失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("测试接口返回 %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	out := &TestOutcome{}
	var text strings.Builder
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data: ") {
				var event struct {
					Type            string `json:"type"`
					Text            string `json:"text"`
					Error           string `json:"error"`
					InputTokens     int64  `json:"input_tokens"`
					OutputTokens    int64  `json:"output_tokens"`
					ReasoningTokens int64  `json:"reasoning_tokens"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(trimmed, "data: ")), &event) == nil {
					switch event.Type {
					case "content":
						text.WriteString(event.Text)
					case "error":
						out.Error = event.Error
					case "test_complete":
						out.InputTokens = event.InputTokens
						out.OutputTokens = event.OutputTokens
						out.ReasoningTokens = event.ReasoningTokens
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("读取测试流失败: %w", err)
		}
	}
	out.Text = text.String()
	out.Duration = time.Since(started)
	if out.Error != "" {
		return out, nil
	}
	if strings.TrimSpace(out.Text) == "" {
		return out, errors.New("上游没有返回任何内容")
	}
	return out, nil
}

// ---------------- 小工具 ----------------

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func nested(m map[string]any, outer, inner string) string {
	if obj, ok := m[outer].(map[string]any); ok {
		return toString(obj[inner])
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
