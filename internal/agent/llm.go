package agent

import (
	"context"
	"encoding/json"
)

// Типы ниже не зависят от провайдера LLM. Агент работает только с ними,
// а адаптер (например, Anthropic) переводит их в формат своего API.
// Чтобы подключить другого провайдера, достаточно ещё одной реализации LLM.

type LLM interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Request struct {
	System   string
	Messages []Message
	Tools    []Tool
}

type Response struct {
	Message Message
	// Refused — модель отказалась отвечать по соображениям безопасности.
	Refused bool
}

type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall   // assistant: какие инструменты модель просит вызвать
	ToolResults []ToolResult // user: результаты вызовов
	// Raw — исходное сообщение ассистента в формате провайдера. Внутри одного
	// цикла инструментов адаптер отправляет его обратно без изменений.
	Raw any
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Tool — описание инструмента для модели: имя, назначение и JSON Schema аргументов.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}
