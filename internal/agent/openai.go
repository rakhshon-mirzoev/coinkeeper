package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	openAITimeout = 90 * time.Second
	// Бесплатные модели часто отвечают 429 (лимит запросов) — пробуем ещё пару раз.
	openAIRetries   = 2
	openAIRetryWait = 2 * time.Second
)

// OpenAI — реализация LLM для OpenAI-совместимого Chat Completions API:
// OpenRouter, Cloud.ru, OpenAI, локальные Ollama/vLLM. Отличаются только адрес и ключ.
type OpenAI struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func NewOpenAI(baseURL, apiKey, model string) *OpenAI {
	return &OpenAI{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: openAITimeout},
	}
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model    string      `json:"model"`
	Messages []oaMessage `json:"messages"`
	Tools    []oaTool    `json:"tools,omitempty"`
}

type oaResponse struct {
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	// OpenRouter может вернуть ошибку провайдера со статусом 200.
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(oaRequest{
		Model:    o.model,
		Messages: toOpenAIMessages(req.System, req.Messages),
		Tools:    toOpenAITools(req.Tools),
	})
	if err != nil {
		return Response{}, fmt.Errorf("encode request: %w", err)
	}

	resp, err := o.send(ctx, body)
	if err != nil {
		return Response{}, err
	}
	if resp.Error != nil {
		return Response{}, fmt.Errorf("openai: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("openai: empty response")
	}

	choice := resp.Choices[0]
	switch choice.FinishReason {
	case "content_filter":
		return Response{Refused: true}, nil
	case "length":
		return Response{}, errTruncated
	}

	m := choice.Message
	msg := Message{Role: RoleAssistant, Raw: m}
	if m.Content != nil {
		msg.Text = *m.Content
	}
	for _, c := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:    c.ID,
			Name:  c.Function.Name,
			Input: json.RawMessage(c.Function.Arguments),
		})
	}
	return Response{Message: msg}, nil
}

func (o *OpenAI) send(ctx context.Context, body []byte) (oaResponse, error) {
	var lastErr error
	for attempt := range openAIRetries + 1 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return oaResponse{}, ctx.Err()
			case <-time.After(openAIRetryWait * time.Duration(attempt)):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return oaResponse{}, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

		httpResp, err := o.http.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("openai: %w", err)
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 10<<20))
		httpResp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("openai: read response: %w", err)
			continue
		}

		if httpResp.StatusCode == http.StatusTooManyRequests || httpResp.StatusCode >= 500 {
			lastErr = fmt.Errorf("openai: status %d: %s", httpResp.StatusCode, snippet(raw))
			continue
		}
		if httpResp.StatusCode != http.StatusOK {
			return oaResponse{}, fmt.Errorf("openai: status %d: %s", httpResp.StatusCode, snippet(raw))
		}

		var out oaResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return oaResponse{}, fmt.Errorf("openai: decode response: %w", err)
		}
		return out, nil
	}
	return oaResponse{}, lastErr
}

func toOpenAIMessages(system string, msgs []Message) []oaMessage {
	text := func(s string) *string { return &s }

	out := make([]oaMessage, 0, len(msgs)+1)
	out = append(out, oaMessage{Role: "system", Content: text(system)})
	for _, m := range msgs {
		switch {
		case m.Role == RoleAssistant:
			if raw, ok := m.Raw.(oaMessage); ok {
				out = append(out, raw)
			} else {
				out = append(out, oaMessage{Role: "assistant", Content: text(m.Text)})
			}
		case len(m.ToolResults) > 0:
			// В этом формате у каждого результата своё сообщение с ролью tool.
			for _, r := range m.ToolResults {
				content := r.Content
				if r.IsError {
					// Отдельного флага ошибки здесь нет — пишем прямо в текст.
					content = "error: " + content
				}
				out = append(out, oaMessage{Role: "tool", ToolCallID: r.CallID, Content: text(content)})
			}
		default:
			out = append(out, oaMessage{Role: "user", Content: text(m.Text)})
		}
	}
	return out
}

func toOpenAITools(tools []Tool) []oaTool {
	out := make([]oaTool, 0, len(tools))
	for _, t := range tools {
		var tool oaTool
		tool.Type = "function"
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		params := map[string]any{"type": "object", "properties": t.Properties}
		if len(t.Required) > 0 {
			params["required"] = t.Required
		}
		tool.Function.Parameters = params
		out = append(out, tool)
	}
	return out
}

// snippet обрезает тело ошибки для лога.
func snippet(b []byte) string {
	const limit = 300
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
