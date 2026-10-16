package agent

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// maxSteps — сколько раз подряд модель может вызвать инструменты в ответ
	// на одно сообщение. Защита от зацикливания и лишних трат на API.
	maxSteps = 8
	// historyLimit — сколько последних сообщений диалога помнит агент.
	historyLimit = 10
	// historyTTL — после такой паузы диалог начинается заново.
	historyTTL = time.Hour

	refusedReply = "Не могу помочь с этим запросом. Я записываю и показываю траты."
	emptyReply   = "Не понял, что нужно сделать. Напиши, например: «кофе 350»."
)

var errTooManySteps = errors.New("agent: too many tool steps")

// Agent понимает сообщения пользователя через LLM и работает с тратами через инструменты.
type Agent struct {
	llm   LLM
	tools *toolbox
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	dialogs map[int64]*dialog
}

// dialog — короткая память разговора с одним пользователем.
type dialog struct {
	// mu выстраивает сообщения одного пользователя в очередь: бот обрабатывает
	// их параллельно, и без блокировки «кофе 350» и «последнее было не 350, а 450»
	// могли бы выполниться одновременно.
	mu      sync.Mutex
	history []Message
	updated time.Time
}

func New(llm LLM, svc ExpenseService, loc *time.Location, log *slog.Logger) *Agent {
	return &Agent{
		llm:     llm,
		tools:   &toolbox{svc: svc, loc: loc, now: time.Now},
		log:     log,
		now:     time.Now,
		dialogs: make(map[int64]*dialog),
	}
}

func (a *Agent) Reply(ctx context.Context, userID int64, text string) (string, error) {
	d := a.dialog(userID)
	d.mu.Lock()
	defer d.mu.Unlock()

	now := a.now()
	if now.Sub(d.updated) > historyTTL {
		d.history = nil
	}

	req := Request{
		System: systemPrompt(a.tools.today()),
		Tools:  a.tools.specs(),
	}
	msgs := append(slices.Clone(d.history), Message{Role: RoleUser, Text: text})

	for range maxSteps {
		req.Messages = msgs
		resp, err := a.llm.Complete(ctx, req)
		if err != nil {
			return "", err
		}
		if resp.Refused {
			return refusedReply, nil
		}
		msgs = append(msgs, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			reply := strings.TrimSpace(resp.Message.Text)
			if reply == "" {
				reply = emptyReply
			}
			d.remember(text, reply, now)
			return reply, nil
		}

		results := make([]ToolResult, 0, len(resp.Message.ToolCalls))
		for _, call := range resp.Message.ToolCalls {
			results = append(results, a.runTool(ctx, userID, call))
		}
		msgs = append(msgs, Message{Role: RoleUser, ToolResults: results})
	}
	return "", errTooManySteps
}

func (a *Agent) runTool(ctx context.Context, userID int64, call ToolCall) ToolResult {
	log := a.log.With("user_id", userID, "tool", call.Name)

	out, err := a.tools.run(ctx, userID, call)
	if err == nil {
		log.Debug("tool call")
		return ToolResult{CallID: call.ID, Content: out}
	}

	var te toolError
	if errors.As(err, &te) {
		log.Debug("tool call rejected", "reason", te.msg)
		return ToolResult{CallID: call.ID, Content: te.msg, IsError: true}
	}
	// Внутренняя ошибка: подробности — в лог, модели — общее сообщение,
	// чтобы она извинилась перед пользователем, а не пересказала ошибку базы.
	log.Error("tool call failed", "error", err)
	return ToolResult{CallID: call.ID, Content: "internal error, try again later", IsError: true}
}

func (a *Agent) dialog(userID int64) *dialog {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Заодно забываем давно молчащих пользователей, чтобы карта не росла вечно.
	now := a.now()
	for id, d := range a.dialogs {
		if id != userID && d.mu.TryLock() {
			if now.Sub(d.updated) > historyTTL {
				delete(a.dialogs, id)
			}
			d.mu.Unlock()
		}
	}

	d, ok := a.dialogs[userID]
	if !ok {
		d = &dialog{}
		a.dialogs[userID] = d
	}
	return d
}

// remember сохраняет в историю только текст: сообщение и итоговый ответ.
// Вызовы инструментов и блоки размышлений модели не храним — история остаётся
// короткой (дешевле контекст), а старые сообщения можно отбрасывать без ошибок API.
func (d *dialog) remember(userText, reply string, at time.Time) {
	d.history = append(d.history,
		Message{Role: RoleUser, Text: userText},
		Message{Role: RoleAssistant, Text: reply},
	)
	if extra := len(d.history) - historyLimit; extra > 0 {
		d.history = slices.Clone(d.history[extra:])
	}
	d.updated = at
}
