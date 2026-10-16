package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAI_Complete(t *testing.T) {
	var (
		body map[string]any
		auth string
		path string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{
			"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"add_expense","arguments":"{\"amount\":350,\"category\":\"food\"}"}}]
		}}]}`)
	}))
	defer srv.Close()

	llm := NewOpenAI(srv.URL+"/api/v1/", "test-key", "test/model:free")
	resp, err := llm.Complete(context.Background(), Request{
		System: "system prompt",
		Tools:  []Tool{{Name: "last_expense", Description: "d", Properties: map[string]any{}}},
		Messages: []Message{
			{Role: RoleUser, Text: "кофе 350"},
			{Role: RoleAssistant, Raw: oaMessage{Role: "assistant", ToolCalls: []oaToolCall{{ID: "call_0", Type: "function"}}}},
			{Role: RoleUser, ToolResults: []ToolResult{
				{CallID: "call_0", Content: `{"id":1}`},
				{CallID: "call_9", Content: "expense not found", IsError: true},
			}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if auth != "Bearer test-key" || path != "/api/v1/chat/completions" {
		t.Errorf("auth = %q, path = %q", auth, path)
	}
	if body["model"] != "test/model:free" {
		t.Errorf("model = %v", body["model"])
	}

	msgs := body["messages"].([]any)
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	// system + user + assistant + по сообщению tool на каждый результат.
	if got := strings.Join(roles, ","); got != "system,user,assistant,tool,tool" {
		t.Errorf("roles = %s", got)
	}
	errResult := msgs[4].(map[string]any)
	if errResult["tool_call_id"] != "call_9" || errResult["content"] != "error: expense not found" {
		t.Errorf("error tool result = %v", errResult)
	}

	params := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("parameters = %v", params)
	}

	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "add_expense" || string(call.Input) != `{"amount":350,"category":"food"}` {
		t.Errorf("call = %+v", call)
	}
	if _, ok := resp.Message.Raw.(oaMessage); !ok {
		t.Error("raw message must be kept for the tool loop")
	}
}

func TestOpenAI_RetriesRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Привет"}}]}`)
	}))
	defer srv.Close()

	resp, err := NewOpenAI(srv.URL, "k", "m").Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
	if err != nil || resp.Message.Text != "Привет" || calls.Load() != 2 {
		t.Fatalf("resp = %+v, err = %v, calls = %d", resp, err, calls.Load())
	}
}

func TestOpenAI_Errors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
		refused bool
	}{
		{name: "bad key", status: 401, body: `{"error":{"message":"No auth credentials found"}}`, wantErr: "status 401"},
		{name: "error in 200", status: 200, body: `{"error":{"message":"provider is down"}}`, wantErr: "provider is down"},
		{name: "truncated", status: 200, body: `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"..."}}]}`, wantErr: errTruncated.Error()},
		{name: "content filter", status: 200, body: `{"choices":[{"finish_reason":"content_filter","message":{"role":"assistant","content":null}}]}`, refused: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			resp, err := NewOpenAI(srv.URL, "k", "m").Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
			if tt.refused {
				if err != nil || !resp.Refused {
					t.Fatalf("resp = %+v, err = %v, want refused", resp, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
