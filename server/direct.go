package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (u *Upstream) Accounts(ctx context.Context) ([]Account, error) {
	if u.cfg.Mode == "sub2api" || u.cfg.Mode == "auto" {
		return u.adminAccounts(ctx, u.cfg.AdminBase, u.cfg.AdminToken)
	}
	accounts := make([]Account, 0, len(u.cfg.Endpoints))
	for _, ep := range u.cfg.Endpoints {
		accounts = append(accounts, Account{ID: ep.ID, Name: ep.Name, Platform: "openai",
			Type: "apikey", Status: "active", Schedulable: true, Protocol: ep.Protocol, Models: ep.Models})
	}
	return accounts, nil
}

func (u *Upstream) TestAccount(ctx context.Context, accountID int64, model, prompt, effort string) (*TestOutcome, error) {
	if u.cfg.Mode == "sub2api" {
		out, err := u.testAdminAccount(ctx, u.cfg.AdminBase, u.cfg.AdminToken, accountID, model, prompt)
		if out != nil {
			out.Text = u.redact(out.Text)
			out.Error = u.redact(out.Error)
		}
		return out, u.safeError(err)
	}
	for _, ep := range u.cfg.Endpoints {
		if ep.ID == accountID {
			return u.testDirect(ctx, ep, model, prompt, effort)
		}
	}
	return nil, fmt.Errorf("未知的上游 ID: %d", accountID)
}

func (u *Upstream) redact(text string) string {
	secrets := []string{u.cfg.AdminToken, u.cfg.AuthPassword}
	for _, ep := range u.cfg.Endpoints {
		secrets = append(secrets, ep.apiKey)
		secrets = append(secrets, ep.secrets...)
	}
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}

func (u *Upstream) safeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", u.redact(err.Error()))
}

// API roots may be https://relay.example or https://relay.example/v1.
// Other explicit paths (e.g. /openai/v1) are preserved without adding /v1.
func endpointURL(ep Endpoint) string {
	base := strings.TrimRight(ep.BaseURL, "/")
	// validateBase already verified the scheme and host.
	afterScheme := strings.SplitN(base, "://", 2)[1]
	if !strings.Contains(afterScheme, "/") {
		base += "/v1"
	}
	if ep.Protocol == "responses" {
		return base + "/responses"
	}
	return base + "/chat/completions"
}

func (u *Upstream) testDirect(ctx context.Context, ep Endpoint, model, prompt, effort string) (*TestOutcome, error) {
	started := time.Now()
	payload := map[string]any{"model": mappedModel(ep, model), "stream": true}
	if ep.Protocol == "responses" {
		payload["input"] = []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": prompt}}}}
		payload["store"] = false
		if effort != "" {
			payload["reasoning"] = map[string]string{"effort": effort}
		}
		if ep.MaxOutputTokens > 0 {
			payload["max_output_tokens"] = ep.MaxOutputTokens
		}
	} else {
		payload["messages"] = []map[string]string{{"role": "user", "content": prompt}}
		payload["stream_options"] = map[string]bool{"include_usage": true}
		if effort != "" {
			payload["reasoning_effort"] = effort
		}
		if ep.MaxOutputTokens > 0 {
			payload["max_completion_tokens"] = ep.MaxOutputTokens
		}
	}
	// Use the complete, server-resolved API endpoint; the browser cannot
	// supply a destination or a credential. No fallback changes protocol.
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("生成检测请求失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL(ep), strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("上游地址格式不正确")
	}
	for key, value := range ep.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", "Bearer "+ep.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	client := *u.client
	if u.cfg.Mode == "auto" || ep.proxyURL != "" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		if ep.proxyURL != "" {
			proxy, parseErr := url.Parse(ep.proxyURL)
			if parseErr != nil {
				return nil, fmt.Errorf("代理地址格式不正确")
			}
			transport.Proxy = http.ProxyURL(proxy)
		}
		defer transport.CloseIdleConnections()
		client.Transport = transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, u.safeError(fmt.Errorf("上游连接失败: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, u.safeError(fmt.Errorf("%s 上游 HTTP %d: %s", ep.Protocol, resp.StatusCode, truncateRunes(string(body), 1500)))
	}
	out, err := parseDirectResponse(resp.Body, resp.Header.Get("Content-Type"), ep.Protocol)
	if err != nil {
		return nil, u.safeError(err)
	}
	out.Text = u.redact(out.Text)
	out.Duration = time.Since(started)
	return out, nil
}

type wireError struct {
	Message string          `json:"message"`
	Code    json.RawMessage `json:"code"`
}
type usage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	Prompt        int64 `json:"prompt_tokens"`
	Completion    int64 `json:"completion_tokens"`
	OutputDetails struct {
		Reasoning int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	CompletionDetails struct {
		Reasoning int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}
type wireResponse struct {
	Status     string     `json:"status"`
	Error      *wireError `json:"error"`
	Usage      usage      `json:"usage"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
}
type wireEvent struct {
	Type     string        `json:"type"`
	Delta    string        `json:"delta"`
	Text     string        `json:"text"`
	Refusal  string        `json:"refusal"`
	Message  string        `json:"message"`
	Error    *wireError    `json:"error"`
	Response *wireResponse `json:"response"`
	Choices  []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usage `json:"usage"`
}

func applyDirectUsage(out *TestOutcome, value usage) {
	if value.Input != 0 {
		out.InputTokens = value.Input
	}
	if value.Output != 0 {
		out.OutputTokens = value.Output
	}
	if value.Prompt != 0 {
		out.InputTokens = value.Prompt
	}
	if value.Completion != 0 {
		out.OutputTokens = value.Completion
	}
	if value.OutputDetails.Reasoning != 0 {
		out.ReasoningTokens = value.OutputDetails.Reasoning
	}
	if value.CompletionDetails.Reasoning != 0 {
		out.ReasoningTokens = value.CompletionDetails.Reasoning
	}
}

func responseText(response *wireResponse) string {
	var text strings.Builder
	for _, item := range response.Output {
		for _, content := range item.Content {
			text.WriteString(content.Text)
			text.WriteString(content.Refusal)
		}
	}
	return text.String()
}

func responseError(response *wireResponse) error {
	if response.Error != nil {
		return fmt.Errorf("Responses 请求失败: %s", response.Error.Message)
	}
	if response.Status != "completed" {
		return fmt.Errorf("Responses 未完成（status=%s, reason=%s）；不对部分回答判分", response.Status, response.Incomplete.Reason)
	}
	return nil
}

func parseDirectResponse(body io.Reader, contentType, protocol string) (*TestOutcome, error) {
	out := &TestOutcome{}
	const maxBody = 32 << 20
	limited := &io.LimitedReader{R: body, N: maxBody + 1}
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		data, err := io.ReadAll(limited)
		if err != nil || len(data) > maxBody {
			return nil, fmt.Errorf("读取上游 JSON 失败或超过 32 MiB")
		}
		if protocol == "responses" {
			var response wireResponse
			if err := json.Unmarshal(data, &response); err != nil {
				return nil, fmt.Errorf("无效的 Responses JSON: %w", err)
			}
			if err := responseError(&response); err != nil {
				return nil, err
			}
			out.Text = responseText(&response)
			applyDirectUsage(out, response.Usage)
		} else {
			var event wireEvent
			if err := json.Unmarshal(data, &event); err != nil {
				return nil, fmt.Errorf("无效的 Chat Completions JSON: %w", err)
			}
			if event.Error != nil {
				return nil, fmt.Errorf("上游错误: %s", event.Error.Message)
			}
			if len(event.Choices) > 0 {
				choice := event.Choices[0]
				if choice.FinishReason != "stop" {
					return nil, fmt.Errorf("Chat Completions 未正常完成（%s）", choice.FinishReason)
				}
				out.Text = choice.Message.Content
			}
			applyDirectUsage(out, event.Usage)
		}
	} else {
		var text strings.Builder
		finished := false
		consume := func(data string) error {
			if data == "[DONE]" {
				if protocol == "chat_completions" {
					finished = true
				}
				return nil
			}
			var event wireEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				return fmt.Errorf("上游返回无效的 SSE JSON: %w", err)
			}
			if event.Error != nil {
				return fmt.Errorf("上游错误: %s", event.Error.Message)
			}
			if protocol == "responses" {
				switch event.Type {
				case "response.output_text.delta":
					text.WriteString(event.Delta)
				case "response.refusal.delta":
					text.WriteString(event.Delta)
				case "response.completed":
					if event.Response == nil {
						return fmt.Errorf("Responses 完成事件缺少 response")
					}
					if err := responseError(event.Response); err != nil {
						return err
					}
					if text.Len() == 0 {
						text.WriteString(responseText(event.Response))
					}
					applyDirectUsage(out, event.Response.Usage)
					finished = true
				case "response.failed", "response.incomplete":
					if event.Response != nil {
						if err := responseError(event.Response); err != nil {
							return err
						}
					}
					return fmt.Errorf("上游返回 %s；不对失败或截断回答判分", event.Type)
				case "error":
					return fmt.Errorf("上游错误: %s", event.Message)
				}
			} else {
				if len(event.Choices) > 0 {
					choice := event.Choices[0]
					text.WriteString(choice.Delta.Content)
					if choice.FinishReason != "" {
						if choice.FinishReason != "stop" {
							return fmt.Errorf("Chat Completions 未正常完成（%s）；不对部分回答判分", choice.FinishReason)
						}
						finished = true
					}
				}
				applyDirectUsage(out, event.Usage)
			}
			return nil
		}
		// Dispatch complete SSE frames, including multi-line data and CRLF.
		scanner := bufio.NewScanner(limited)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		var frame []string
		dispatch := func() error {
			if len(frame) == 0 {
				return nil
			}
			data := strings.Join(frame, "\n")
			frame = nil
			return consume(data)
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err := dispatch(); err != nil {
					return nil, err
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				frame = append(frame, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("读取上游 SSE 失败: %w", err)
		}
		if limited.N <= 0 {
			return nil, fmt.Errorf("上游响应超过 32 MiB")
		}
		if err := dispatch(); err != nil {
			return nil, err
		}
		if !finished {
			return nil, fmt.Errorf("上游流提前结束，没有完成事件；不对部分回答判分")
		}
		out.Text = text.String()
	}
	if strings.TrimSpace(out.Text) == "" {
		return nil, fmt.Errorf("上游已结束但没有文本输出，请检查接口格式、模型权限及输出预算")
	}
	return out, nil
}
