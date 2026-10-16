package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
)

const (
	listDefaultLimit = 20
	listMaxLimit     = 50
)

// ExpenseService — то, что инструментам нужно от сервиса трат.
type ExpenseService interface {
	Create(ctx context.Context, e expense.Expense) (expense.Expense, error)
	Get(ctx context.Context, id, ownerID int64) (expense.Expense, error)
	List(ctx context.Context, f expense.Filter) ([]expense.Expense, error)
	Summary(ctx context.Context, f expense.Filter) ([]expense.CategoryTotal, error)
	Last(ctx context.Context, userID int64) (expense.Expense, error)
	Update(ctx context.Context, e expense.Expense, ownerID int64) (expense.Expense, error)
	Delete(ctx context.Context, id, ownerID int64) error
}

// toolError — ошибка, которую стоит показать модели: неверные аргументы,
// трата не найдена. Модель исправит вызов или объяснит пользователю.
type toolError struct{ msg string }

func (e toolError) Error() string { return e.msg }

func toolErrorf(format string, args ...any) error {
	return toolError{msg: fmt.Sprintf(format, args...)}
}

// toolbox выполняет инструменты от имени конкретного пользователя.
// userID подставляет код, а не модель: модель не может обратиться к чужим тратам,
// даже если её об этом попросят.
type toolbox struct {
	svc ExpenseService
	loc *time.Location
	now func() time.Time
}

var categoryEnum = []string{
	string(expense.CategoryFood), string(expense.CategoryTransport), string(expense.CategoryHousing),
	string(expense.CategoryEntertainment), string(expense.CategoryHealth), string(expense.CategoryShopping),
	string(expense.CategoryOther),
}

func dateProp(desc string) map[string]any {
	return map[string]any{"type": "string", "format": "date", "description": desc + " Формат YYYY-MM-DD."}
}

var (
	idProp       = map[string]any{"type": "integer", "description": "ID траты."}
	amountProp   = map[string]any{"type": "number", "description": "Сумма в рублях, больше нуля."}
	categoryProp = map[string]any{"type": "string", "enum": categoryEnum, "description": "Категория траты."}
	commentProp  = map[string]any{"type": "string", "description": "На что потрачено, коротко: «кофе», «такси до работы»."}
)

func (t *toolbox) specs() []Tool {
	return []Tool{
		{
			Name:        "add_expense",
			Description: "Сохранить новую трату пользователя.",
			Properties: map[string]any{
				"amount":   amountProp,
				"category": categoryProp,
				"date":     dateProp("Дата траты. Не указывай, если трата сегодняшняя."),
				"comment":  commentProp,
			},
			Required: []string{"amount", "category"},
		},
		{
			Name:        "list_expenses",
			Description: "Показать траты пользователя, новые сверху. Используй, чтобы найти трату для изменения или удаления и чтобы показать список. Не используй для подсчёта сумм — для этого get_summary.",
			Properties: map[string]any{
				"from":     dateProp("Начало периода, включительно."),
				"to":       dateProp("Конец периода, включительно."),
				"category": categoryProp,
				"limit":    map[string]any{"type": "integer", "description": fmt.Sprintf("Сколько трат вернуть, по умолчанию %d, максимум %d.", listDefaultLimit, listMaxLimit)},
			},
		},
		{
			Name:        "get_summary",
			Description: "Посчитать, сколько пользователь потратил за период: итог и разбивка по категориям. Всегда используй его для вопросов «сколько потратил».",
			Properties: map[string]any{
				"from":     dateProp("Начало периода, включительно."),
				"to":       dateProp("Конец периода, включительно."),
				"category": categoryProp,
			},
			Required: []string{"from", "to"},
		},
		{
			Name:        "last_expense",
			Description: "Последняя добавленная трата пользователя. Нужна для просьб вида «последнее было не 350, а 450» или «удали последнюю».",
			Properties:  map[string]any{},
		},
		{
			Name:        "update_expense",
			Description: "Изменить трату по ID. Передай только поля, которые меняются.",
			Properties: map[string]any{
				"id":       idProp,
				"amount":   amountProp,
				"category": categoryProp,
				"date":     dateProp("Новая дата траты."),
				"comment":  commentProp,
			},
			Required: []string{"id"},
		},
		{
			Name:        "delete_expense",
			Description: "Удалить трату по ID.",
			Properties:  map[string]any{"id": idProp},
			Required:    []string{"id"},
		},
	}
}

// run выполняет вызов и возвращает результат в JSON для модели.
func (t *toolbox) run(ctx context.Context, userID int64, call ToolCall) (string, error) {
	var (
		result any
		err    error
	)
	switch call.Name {
	case "add_expense":
		result, err = t.add(ctx, userID, call.Input)
	case "list_expenses":
		result, err = t.list(ctx, userID, call.Input)
	case "get_summary":
		result, err = t.summary(ctx, userID, call.Input)
	case "last_expense":
		result, err = t.last(ctx, userID)
	case "update_expense":
		result, err = t.update(ctx, userID, call.Input)
	case "delete_expense":
		result, err = t.delete(ctx, userID, call.Input)
	default:
		err = toolErrorf("unknown tool %q", call.Name)
	}
	if err != nil {
		return "", asToolError(err)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode %s result: %w", call.Name, err)
	}
	return string(out), nil
}

// asToolError превращает ожидаемые доменные ошибки в toolError,
// остальные (база недоступна и т. п.) оставляет как есть.
func asToolError(err error) error {
	for _, domain := range []error{
		expense.ErrNotFound, expense.ErrInvalidAmount, expense.ErrInvalidCategory,
		expense.ErrInvalidDate, expense.ErrInvalidUser, expense.ErrUnknownUser,
	} {
		if errors.Is(err, domain) {
			return toolError{msg: domain.Error()}
		}
	}
	return err
}

type expenseView struct {
	ID       int64   `json:"id"`
	Amount   string  `json:"amount"`
	Category string  `json:"category"`
	Date     string  `json:"date"`
	Comment  *string `json:"comment,omitempty"`
}

func toView(e expense.Expense) expenseView {
	return expenseView{
		ID:       e.ID,
		Amount:   e.Amount.StringFixed(2),
		Category: string(e.Category),
		Date:     e.SpentAt.Format(time.DateOnly),
		Comment:  e.Comment,
	}
}

func (t *toolbox) add(ctx context.Context, userID int64, raw json.RawMessage) (any, error) {
	var in struct {
		Amount   decimal.Decimal  `json:"amount"`
		Category expense.Category `json:"category"`
		Date     string           `json:"date"`
		Comment  *string          `json:"comment"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return nil, err
	}

	spentAt := t.today()
	if in.Date != "" {
		var err error
		if spentAt, err = t.parseDate(in.Date); err != nil {
			return nil, err
		}
	}

	e, err := t.svc.Create(ctx, expense.Expense{
		UserID:   userID,
		Amount:   in.Amount,
		Category: in.Category,
		SpentAt:  spentAt,
		Comment:  in.Comment,
	})
	if err != nil {
		return nil, err
	}
	return toView(e), nil
}

func (t *toolbox) list(ctx context.Context, userID int64, raw json.RawMessage) (any, error) {
	var in struct {
		From     string           `json:"from"`
		To       string           `json:"to"`
		Category expense.Category `json:"category"`
		Limit    int              `json:"limit"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return nil, err
	}

	f, err := t.filter(userID, in.From, in.To, in.Category)
	if err != nil {
		return nil, err
	}
	f.Limit = in.Limit
	if f.Limit <= 0 || f.Limit > listMaxLimit {
		f.Limit = listDefaultLimit
	}

	expenses, err := t.svc.List(ctx, f)
	if err != nil {
		return nil, err
	}

	views := make([]expenseView, 0, len(expenses))
	for _, e := range expenses {
		views = append(views, toView(e))
	}
	return struct {
		Expenses []expenseView `json:"expenses"`
		// LimitReached подсказывает модели, что трат может быть больше, чем показано.
		LimitReached bool `json:"limit_reached"`
	}{views, len(views) == f.Limit}, nil
}

func (t *toolbox) summary(ctx context.Context, userID int64, raw json.RawMessage) (any, error) {
	var in struct {
		From     string           `json:"from"`
		To       string           `json:"to"`
		Category expense.Category `json:"category"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return nil, err
	}

	f, err := t.filter(userID, in.From, in.To, in.Category)
	if err != nil {
		return nil, err
	}

	totals, err := t.svc.Summary(ctx, f)
	if err != nil {
		return nil, err
	}

	type categoryView struct {
		Category string `json:"category"`
		Total    string `json:"total"`
		Count    int    `json:"count"`
	}
	sum := decimal.Zero
	count := 0
	byCategory := make([]categoryView, 0, len(totals))
	for _, c := range totals {
		sum = sum.Add(c.Total)
		count += c.Count
		byCategory = append(byCategory, categoryView{string(c.Category), c.Total.StringFixed(2), c.Count})
	}
	return struct {
		From       string         `json:"from"`
		To         string         `json:"to"`
		Total      string         `json:"total"`
		Count      int            `json:"count"`
		ByCategory []categoryView `json:"by_category"`
	}{in.From, in.To, sum.StringFixed(2), count, byCategory}, nil
}

func (t *toolbox) last(ctx context.Context, userID int64) (any, error) {
	e, err := t.svc.Last(ctx, userID)
	if err != nil {
		return nil, err
	}
	return toView(e), nil
}

func (t *toolbox) update(ctx context.Context, userID int64, raw json.RawMessage) (any, error) {
	var in struct {
		ID       int64             `json:"id"`
		Amount   *decimal.Decimal  `json:"amount"`
		Category *expense.Category `json:"category"`
		Date     *string           `json:"date"`
		Comment  *string           `json:"comment"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return nil, err
	}

	e, err := t.svc.Get(ctx, in.ID, userID)
	if err != nil {
		return nil, err
	}
	if in.Amount != nil {
		e.Amount = *in.Amount
	}
	if in.Category != nil {
		e.Category = *in.Category
	}
	if in.Date != nil {
		if e.SpentAt, err = t.parseDate(*in.Date); err != nil {
			return nil, err
		}
	}
	if in.Comment != nil {
		e.Comment = in.Comment
	}

	updated, err := t.svc.Update(ctx, e, userID)
	if err != nil {
		return nil, err
	}
	return toView(updated), nil
}

func (t *toolbox) delete(ctx context.Context, userID int64, raw json.RawMessage) (any, error) {
	var in struct {
		ID int64 `json:"id"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return nil, err
	}

	// Сначала читаем трату, чтобы вернуть модели, что именно удалено.
	e, err := t.svc.Get(ctx, in.ID, userID)
	if err != nil {
		return nil, err
	}
	if err := t.svc.Delete(ctx, in.ID, userID); err != nil {
		return nil, err
	}
	return struct {
		Deleted expenseView `json:"deleted"`
	}{toView(e)}, nil
}

func (t *toolbox) filter(userID int64, from, to string, category expense.Category) (expense.Filter, error) {
	f := expense.Filter{UserID: userID, Category: category}
	var err error
	if from != "" {
		if f.From, err = t.parseDate(from); err != nil {
			return f, err
		}
	}
	if to != "" {
		if f.To, err = t.parseDate(to); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (t *toolbox) today() time.Time {
	y, m, d := t.now().In(t.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.loc)
}

func (t *toolbox) parseDate(s string) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, s, t.loc)
	if err != nil {
		return time.Time{}, toolErrorf("invalid date %q, expected YYYY-MM-DD", s)
	}
	return d, nil
}

func decodeInput(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return toolErrorf("invalid arguments: %v", err)
	}
	return nil
}
