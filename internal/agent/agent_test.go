package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
)

const (
	me    int64 = 7000000001
	other int64 = 7000000002
)

var moscow = time.FixedZone("MSK", 3*60*60)

// fixedNow — пятница, 16 октября 2026, 01:30 по Москве (в UTC ещё 15-е).
var fixedNow = time.Date(2026, 10, 15, 22, 30, 0, 0, time.UTC)

// scriptedLLM отдаёт заранее заданные ответы и запоминает запросы.
type scriptedLLM struct {
	responses []Response
	requests  []Request
}

func (s *scriptedLLM) Complete(_ context.Context, req Request) (Response, error) {
	s.requests = append(s.requests, req)
	if len(s.responses) == 0 {
		return Response{}, errors.New("no scripted response")
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func toolCall(id, name, input string) Response {
	return Response{Message: Message{
		Role:      RoleAssistant,
		ToolCalls: []ToolCall{{ID: id, Name: name, Input: json.RawMessage(input)}},
	}}
}

func text(s string) Response {
	return Response{Message: Message{Role: RoleAssistant, Text: s}}
}

// memService — сервис трат в памяти с той же проверкой владельца, что и в SQL.
type memService struct {
	items  map[int64]expense.Expense
	nextID int64
}

func newMemService() *memService { return &memService{items: map[int64]expense.Expense{}, nextID: 1} }

func (m *memService) owned(id, ownerID int64) (expense.Expense, error) {
	e, ok := m.items[id]
	if !ok || (ownerID != 0 && e.UserID != ownerID) {
		return expense.Expense{}, expense.ErrNotFound
	}
	return e, nil
}

func (m *memService) Create(_ context.Context, e expense.Expense) (expense.Expense, error) {
	if !e.Amount.IsPositive() {
		return expense.Expense{}, expense.ErrInvalidAmount
	}
	if !e.Category.Valid() {
		return expense.Expense{}, expense.ErrInvalidCategory
	}
	e.ID = m.nextID
	m.nextID++
	m.items[e.ID] = e
	return e, nil
}

func (m *memService) Get(_ context.Context, id, ownerID int64) (expense.Expense, error) {
	return m.owned(id, ownerID)
}

func (m *memService) List(_ context.Context, f expense.Filter) ([]expense.Expense, error) {
	var out []expense.Expense
	for _, e := range m.items {
		if e.UserID == f.UserID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memService) Summary(_ context.Context, f expense.Filter) ([]expense.CategoryTotal, error) {
	return []expense.CategoryTotal{
		{Category: expense.CategoryFood, Total: decimal.RequireFromString("1000.50"), Count: 3},
		{Category: expense.CategoryTransport, Total: decimal.RequireFromString("499.50"), Count: 1},
	}, nil
}

func (m *memService) Last(_ context.Context, userID int64) (expense.Expense, error) {
	var last expense.Expense
	for _, e := range m.items {
		if e.UserID == userID && e.ID > last.ID {
			last = e
		}
	}
	if last.ID == 0 {
		return expense.Expense{}, expense.ErrNotFound
	}
	return last, nil
}

func (m *memService) Update(_ context.Context, e expense.Expense, ownerID int64) (expense.Expense, error) {
	if _, err := m.owned(e.ID, ownerID); err != nil {
		return expense.Expense{}, err
	}
	m.items[e.ID] = e
	return e, nil
}

func (m *memService) Delete(_ context.Context, id, ownerID int64) error {
	if _, err := m.owned(id, ownerID); err != nil {
		return err
	}
	delete(m.items, id)
	return nil
}

func newTestAgent(llm LLM, svc ExpenseService) *Agent {
	a := New(llm, svc, moscow, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := func() time.Time { return fixedNow }
	a.now = clock
	a.tools.now = clock
	return a
}

// lastToolResult — результат инструмента, который агент отправил модели последним.
func lastToolResult(t *testing.T, llm *scriptedLLM) ToolResult {
	t.Helper()
	msgs := llm.requests[len(llm.requests)-1].Messages
	last := msgs[len(msgs)-1]
	if len(last.ToolResults) != 1 {
		t.Fatalf("expected one tool result in last message, got %+v", last)
	}
	return last.ToolResults[0]
}

func TestReply_AddExpense(t *testing.T) {
	llm := &scriptedLLM{responses: []Response{
		toolCall("c1", "add_expense", `{"amount":350,"category":"food","comment":"кофе"}`),
		text("Записал: кофе 350 ₽, еда, сегодня."),
	}}
	svc := newMemService()

	reply, err := newTestAgent(llm, svc).Reply(context.Background(), me, "кофе 350")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != "Записал: кофе 350 ₽, еда, сегодня." {
		t.Errorf("reply = %q", reply)
	}

	saved := svc.items[1]
	if saved.UserID != me {
		t.Errorf("user = %d, want %d: owner must come from the bot, not the model", saved.UserID, me)
	}
	// В UTC ещё 15-е, но у семьи в Москве уже 16-е.
	if got := saved.SpentAt.Format(time.DateOnly); got != "2026-10-16" {
		t.Errorf("spent_at = %s, want today in family timezone 2026-10-16", got)
	}
	if !saved.Amount.Equal(decimal.NewFromInt(350)) {
		t.Errorf("amount = %s", saved.Amount)
	}

	res := lastToolResult(t, llm)
	if res.IsError || res.CallID != "c1" || !strings.Contains(res.Content, `"amount":"350.00"`) {
		t.Errorf("tool result = %+v", res)
	}
}

func TestReply_ExplicitDate(t *testing.T) {
	llm := &scriptedLLM{responses: []Response{
		toolCall("c1", "add_expense", `{"amount":1200,"category":"transport","date":"2026-10-15","comment":"такси"}`),
		text("ok"),
	}}
	svc := newMemService()

	if _, err := newTestAgent(llm, svc).Reply(context.Background(), me, "такси 1200 вчера"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := svc.items[1].SpentAt.Format(time.DateOnly); got != "2026-10-15" {
		t.Errorf("spent_at = %s, want 2026-10-15", got)
	}
}

func TestReply_CannotTouchOtherUsersExpense(t *testing.T) {
	svc := newMemService()
	foreign, _ := svc.Create(context.Background(), expense.Expense{
		UserID: other, Amount: decimal.NewFromInt(100), Category: expense.CategoryFood, SpentAt: fixedNow,
	})

	llm := &scriptedLLM{responses: []Response{
		toolCall("c1", "delete_expense", `{"id":1}`),
		text("Такой траты нет."),
	}}

	if _, err := newTestAgent(llm, svc).Reply(context.Background(), me, "удали трату 1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := svc.items[foreign.ID]; !ok {
		t.Fatal("other user's expense was deleted")
	}
	if res := lastToolResult(t, llm); !res.IsError || res.Content != expense.ErrNotFound.Error() {
		t.Errorf("tool result = %+v, want not found error", res)
	}
}

func TestReply_ToolErrorsGoBackToModel(t *testing.T) {
	tests := []struct {
		name  string
		call  Response
		wants string
	}{
		{"invalid json", toolCall("c1", "add_expense", `{"amount":`), "invalid arguments"},
		{"unknown tool", toolCall("c1", "drop_database", `{}`), "unknown tool"},
		{"bad date", toolCall("c1", "add_expense", `{"amount":1,"category":"food","date":"вчера"}`), "invalid date"},
		{"zero amount", toolCall("c1", "add_expense", `{"amount":0,"category":"food"}`), expense.ErrInvalidAmount.Error()},
		{"bad category", toolCall("c1", "add_expense", `{"amount":1,"category":"еда"}`), expense.ErrInvalidCategory.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &scriptedLLM{responses: []Response{tt.call, text("Не получилось.")}}

			reply, err := newTestAgent(llm, newMemService()).Reply(context.Background(), me, "x")
			if err != nil {
				t.Fatalf("tool error must not abort the reply: %v", err)
			}
			if reply != "Не получилось." {
				t.Errorf("reply = %q", reply)
			}
			if res := lastToolResult(t, llm); !res.IsError || !strings.Contains(res.Content, tt.wants) {
				t.Errorf("tool result = %+v, want error containing %q", res, tt.wants)
			}
		})
	}
}

func TestReply_UpdateLast(t *testing.T) {
	svc := newMemService()
	_, _ = svc.Create(context.Background(), expense.Expense{
		UserID: me, Amount: decimal.NewFromInt(350), Category: expense.CategoryFood, SpentAt: fixedNow,
	})

	llm := &scriptedLLM{responses: []Response{
		toolCall("c1", "last_expense", `{}`),
		toolCall("c2", "update_expense", `{"id":1,"amount":450}`),
		text("Исправил на 450 ₽."),
	}}

	if _, err := newTestAgent(llm, svc).Reply(context.Background(), me, "последнее было не 350, а 450"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := svc.items[1]; !got.Amount.Equal(decimal.NewFromInt(450)) || got.Category != expense.CategoryFood {
		t.Errorf("expense after update = %+v", got)
	}
}

func TestReply_SummaryIsCountedInCode(t *testing.T) {
	llm := &scriptedLLM{responses: []Response{
		toolCall("c1", "get_summary", `{"from":"2026-10-01","to":"2026-10-16"}`),
		text("1500 ₽"),
	}}

	if _, err := newTestAgent(llm, newMemService()).Reply(context.Background(), me, "сколько потратил в этом месяце"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := lastToolResult(t, llm)
	if !strings.Contains(res.Content, `"total":"1500.00"`) || !strings.Contains(res.Content, `"count":4`) {
		t.Errorf("summary = %s", res.Content)
	}
}

func TestReply_TooManySteps(t *testing.T) {
	var responses []Response
	for range maxSteps + 1 {
		responses = append(responses, toolCall("c", "last_expense", `{}`))
	}

	_, err := newTestAgent(&scriptedLLM{responses: responses}, newMemService()).Reply(context.Background(), me, "x")
	if !errors.Is(err, errTooManySteps) {
		t.Fatalf("err = %v, want %v", err, errTooManySteps)
	}
}

func TestReply_Refused(t *testing.T) {
	llm := &scriptedLLM{responses: []Response{{Refused: true}}}

	reply, err := newTestAgent(llm, newMemService()).Reply(context.Background(), me, "x")
	if err != nil || reply != refusedReply {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
}

func TestReply_HistoryKeepsOnlyText(t *testing.T) {
	llm := &scriptedLLM{responses: []Response{
		toolCall("c1", "add_expense", `{"amount":350,"category":"food"}`),
		text("Записал."),
		text("Пожалуйста!"),
	}}
	a := newTestAgent(llm, newMemService())

	_, _ = a.Reply(context.Background(), me, "кофе 350")
	_, _ = a.Reply(context.Background(), me, "спасибо")

	// Второй запрос: прошлый обмен только текстом, без вызовов инструментов.
	got := llm.requests[2].Messages
	want := []Message{
		{Role: RoleUser, Text: "кофе 350"},
		{Role: RoleAssistant, Text: "Записал."},
		{Role: RoleUser, Text: "спасибо"},
	}
	if len(got) != len(want) {
		t.Fatalf("messages = %+v", got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text || len(got[i].ToolCalls) > 0 || got[i].Raw != nil {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReply_HistoryIsPerUserAndExpires(t *testing.T) {
	llm := &scriptedLLM{responses: []Response{text("a"), text("b"), text("c")}}
	a := newTestAgent(llm, newMemService())

	_, _ = a.Reply(context.Background(), me, "первое")
	_, _ = a.Reply(context.Background(), other, "чужое")
	if n := len(llm.requests[1].Messages); n != 1 {
		t.Errorf("other user sees %d messages, want 1", n)
	}

	later := fixedNow.Add(historyTTL + time.Minute)
	a.now = func() time.Time { return later }
	_, _ = a.Reply(context.Background(), me, "через два часа")
	if n := len(llm.requests[2].Messages); n != 1 {
		t.Errorf("expired history has %d messages, want 1", n)
	}
}

func TestDialog_HistoryLimit(t *testing.T) {
	var d dialog
	for i := range historyLimit {
		d.remember("q", string(rune('a'+i)), fixedNow)
	}
	if len(d.history) != historyLimit {
		t.Fatalf("history len = %d, want %d", len(d.history), historyLimit)
	}
	if d.history[0].Role != RoleUser {
		t.Errorf("history must start with a user message, got %s", d.history[0].Role)
	}
	if last := d.history[len(d.history)-1].Text; last != string(rune('a'+historyLimit-1)) {
		t.Errorf("newest reply = %q", last)
	}
}

func TestSystemPrompt_Calendar(t *testing.T) {
	p := systemPrompt(time.Date(2026, 10, 16, 0, 0, 0, 0, moscow)) // пятница

	for _, want := range []string{
		"сегодня: 2026-10-16, пятница",
		"вчера: 2026-10-15",
		"эта неделя: с 2026-10-12 по 2026-10-16",
		"прошлая неделя: с 2026-10-05 по 2026-10-11",
		"этот месяц: с 2026-10-01 по 2026-10-16",
		"прошлый месяц: с 2026-09-01 по 2026-09-30",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt has no %q", want)
		}
	}
}

func TestSystemPrompt_WeekStartsOnMonday(t *testing.T) {
	p := systemPrompt(time.Date(2026, 10, 18, 0, 0, 0, 0, moscow)) // воскресенье
	if !strings.Contains(p, "эта неделя: с 2026-10-12 по 2026-10-18") {
		t.Errorf("sunday must belong to the week started on monday:\n%s", p)
	}
}
