package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// maxTokens — потолок на размышления и ответ вместе. Ответы бота короткие,
// но при адаптивном мышлении нужен запас, иначе ответ обрежется.
const maxTokens = 16000

var errTruncated = errors.New("llm response truncated by max_tokens")

// Anthropic — реализация LLM через Claude API.
type Anthropic struct {
	client anthropic.Client
	model  string
	effort anthropic.OutputConfigEffort
}

// NewAnthropic создаёт клиента. effort пустой — используется значение модели по умолчанию.
// opts — дополнительные настройки SDK (в тестах — адрес поддельного сервера).
func NewAnthropic(apiKey, model, effort string, opts ...option.RequestOption) *Anthropic {
	return &Anthropic{
		client: anthropic.NewClient(append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)...),
		model:  model,
		effort: anthropic.OutputConfigEffort(effort),
	}
}

func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: req.System}},
		Messages:  toAnthropicMessages(req.Messages),
		Tools:     toAnthropicTools(req.Tools),
	}
	if a.effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: a.effort}
	}

	resp, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic: %w", err)
	}

	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		return Response{Refused: true}, nil
	case anthropic.StopReasonMaxTokens:
		return Response{}, errTruncated
	}

	msg := Message{Role: RoleAssistant, Raw: resp.ToParam()}
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			msg.Text += b.Text
		case anthropic.ToolUseBlock:
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: json.RawMessage(b.JSON.Input.Raw()),
			})
		}
	}
	return Response{Message: msg}, nil
}

func toAnthropicMessages(msgs []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.Role == RoleAssistant:
			// Ответ из текущего цикла — отправляем как есть, вместе с блоками
			// размышлений. Из истории прошлых сообщений — только текст.
			if raw, ok := m.Raw.(anthropic.MessageParam); ok {
				out = append(out, raw)
			} else {
				out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Text)))
			}
		case len(m.ToolResults) > 0:
			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolResults))
			for _, r := range m.ToolResults {
				blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			// Все результаты — одним сообщением: так модель продолжает вызывать
			// инструменты параллельно, а не по одному.
			out = append(out, anthropic.NewUserMessage(blocks...))
		default:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Text)))
		}
	}
	return out
}

func toAnthropicTools(tools []Tool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tool := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: t.Properties,
				Required:   t.Required,
			},
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out
}
