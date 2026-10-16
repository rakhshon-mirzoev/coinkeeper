package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// Проверяем адаптер без настоящего API: поддельный сервер принимает запрос
// в формате Messages API и отвечает вызовом инструмента.
func TestAnthropic_Complete(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request is not json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
			"stop_reason": "tool_use",
			"content": [
				{"type": "text", "text": "Сейчас запишу."},
				{"type": "tool_use", "id": "toolu_1", "name": "add_expense", "input": {"amount": 350, "category": "food"}}
			],
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`)
	}))
	defer srv.Close()

	llm := NewAnthropic("test-key", "test-model", "medium", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	resp, err := llm.Complete(context.Background(), Request{
		System: "system prompt",
		Tools:  []Tool{{Name: "add_expense", Description: "d", Properties: map[string]any{"amount": map[string]any{"type": "number"}}, Required: []string{"amount"}}},
		Messages: []Message{
			{Role: RoleUser, Text: "раньше"},
			{Role: RoleAssistant, Text: "ответ из истории"},
			{Role: RoleUser, Text: "кофе 350"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Что ушло в API.
	if body["model"] != "test-model" {
		t.Errorf("model = %v", body["model"])
	}
	if cfg, _ := body["output_config"].(map[string]any); cfg["effort"] != "medium" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	schema := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if schema["type"] != "object" || schema["required"].([]any)[0] != "amount" {
		t.Errorf("input_schema = %v", schema)
	}
	if msgs, _ := body["messages"].([]any); len(msgs) != 3 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Errorf("messages = %v", body["messages"])
	}

	// Как разобран ответ.
	if resp.Message.Text != "Сейчас запишу." {
		t.Errorf("text = %q", resp.Message.Text)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	var in struct {
		Amount   float64 `json:"amount"`
		Category string  `json:"category"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil || call.ID != "toolu_1" || call.Name != "add_expense" || in.Amount != 350 || in.Category != "food" {
		t.Errorf("call = %+v, input = %+v, err = %v", call, in, err)
	}
	if resp.Message.Raw == nil {
		t.Error("raw provider message must be kept for the tool loop")
	}
}

func TestAnthropic_ToolResultsRoundTrip(t *testing.T) {
	msgs := toAnthropicMessages([]Message{
		{Role: RoleUser, ToolResults: []ToolResult{
			{CallID: "toolu_1", Content: `{"id":1}`},
			{CallID: "toolu_2", Content: "expense not found", IsError: true},
		}},
	})

	raw, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// Оба результата — в одном сообщении пользователя.
	if len(got) != 1 || got[0].Role != "user" || len(got[0].Content) != 2 {
		t.Fatalf("messages = %s", raw)
	}
	if c := got[0].Content[1]; c.Type != "tool_result" || c.ToolUseID != "toolu_2" || !c.IsError {
		t.Errorf("second block = %+v", c)
	}
}

func TestAnthropic_Refusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"m","stop_reason":"refusal","content":[],"usage":{"input_tokens":1,"output_tokens":0}}`)
	}))
	defer srv.Close()

	resp, err := NewAnthropic("k", "m", "", option.WithBaseURL(srv.URL)).Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "x"}},
	})
	if err != nil || !resp.Refused {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}
