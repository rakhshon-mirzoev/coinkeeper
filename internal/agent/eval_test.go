//go:build eval

// Eval качества агента на настоящей LLM. Обычный go test его не запускает
// (build-тег eval): каждый прогон — это реальные запросы к API.
//
//	make eval
//	EVAL_MODELS=model-a,model-b EVAL_REPS=2 make eval
//
// Каждая фраза выполняется на свежей базе в памяти, заполненной трат из
// testdata/eval/cases.json. Оценка — по итоговому состоянию: какие траты созданы,
// изменены и удалены (и что ничего лишнего не задето), плюс числа в ответе
// для вопросов «сколько потратил».
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/shopspring/decimal"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
)

const (
	evalUserID  int64 = 1001
	evalOtherID int64 = 1002
)

// ---------- Описание кейсов ----------

type evalFile struct {
	Today    string        `json:"today"`
	Timezone string        `json:"timezone"`
	Fixture  []evalExpense `json:"fixture"`
	Cases    []evalCase    `json:"cases"`
}

type evalExpense struct {
	ID       int64  `json:"id"`
	User     string `json:"user"`
	Amount   string `json:"amount"`
	Category string `json:"category"`
	Date     string `json:"date"`
	Comment  string `json:"comment"`
}

type evalCase struct {
	ID      string        `json:"id"`
	Tags    []string      `json:"tags"`
	Message string        `json:"message"`
	Extra   []evalExpense `json:"extra"`
	Expect  struct {
		Created          []evalExpense `json:"created"`
		Updated          []evalExpense `json:"updated"`
		Deleted          []int64       `json:"deleted"`
		Unchanged        bool          `json:"unchanged"`
		ReplyContains    []string      `json:"reply_contains"`
		ReplyNotContains []string      `json:"reply_not_contains"`
	} `json:"expect"`
}

// ---------- Репозиторий в памяти ----------

// memRepo повторяет поведение PostgresRepo: фильтры, сортировку, проверку владельца.
// Сверху работает настоящий expense.Service, поэтому валидация та же, что в проде.
type memRepo struct {
	mu      sync.Mutex
	items   map[int64]expense.Expense
	created map[int64]int // порядок добавления для Last
	nextID  int64
	tick    int
}

func newMemRepo() *memRepo {
	return &memRepo{items: map[int64]expense.Expense{}, created: map[int64]int{}, nextID: 1}
}

func (r *memRepo) put(e expense.Expense) {
	r.items[e.ID] = e
	r.tick++
	r.created[e.ID] = r.tick
	if e.ID >= r.nextID {
		r.nextID = e.ID + 1
	}
}

func (r *memRepo) owned(id, ownerID int64) (expense.Expense, error) {
	e, ok := r.items[id]
	if !ok || (ownerID != 0 && e.UserID != ownerID) {
		return expense.Expense{}, expense.ErrNotFound
	}
	return e, nil
}

func (r *memRepo) match(e expense.Expense, f expense.Filter) bool {
	return (f.UserID == 0 || e.UserID == f.UserID) &&
		(f.Category == "" || e.Category == f.Category) &&
		(f.From.IsZero() || !e.SpentAt.Before(f.From)) &&
		(f.To.IsZero() || !e.SpentAt.After(f.To))
}

func (r *memRepo) Create(_ context.Context, e expense.Expense) (expense.Expense, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.ID = r.nextID
	r.put(e)
	return e, nil
}

func (r *memRepo) Get(_ context.Context, id, ownerID int64) (expense.Expense, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owned(id, ownerID)
}

func (r *memRepo) List(_ context.Context, f expense.Filter) ([]expense.Expense, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []expense.Expense
	for _, e := range r.items {
		if r.match(e, f) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].SpentAt.Equal(out[j].SpentAt) {
			return out[i].SpentAt.After(out[j].SpentAt)
		}
		return out[i].ID > out[j].ID
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (r *memRepo) Summary(_ context.Context, f expense.Filter) ([]expense.CategoryTotal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byCat := map[expense.Category]*expense.CategoryTotal{}
	for _, e := range r.items {
		if !r.match(e, f) {
			continue
		}
		t, ok := byCat[e.Category]
		if !ok {
			t = &expense.CategoryTotal{Category: e.Category, Total: decimal.Zero}
			byCat[e.Category] = t
		}
		t.Total = t.Total.Add(e.Amount)
		t.Count++
	}
	out := make([]expense.CategoryTotal, 0, len(byCat))
	for _, t := range byCat {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total.GreaterThan(out[j].Total) })
	return out, nil
}

func (r *memRepo) Last(_ context.Context, userID int64) (expense.Expense, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var last expense.Expense
	best := 0
	for id, e := range r.items {
		if e.UserID == userID && r.created[id] > best {
			last, best = e, r.created[id]
		}
	}
	if best == 0 {
		return expense.Expense{}, expense.ErrNotFound
	}
	return last, nil
}

func (r *memRepo) Update(_ context.Context, e expense.Expense, ownerID int64) (expense.Expense, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.owned(e.ID, ownerID); err != nil {
		return expense.Expense{}, err
	}
	r.items[e.ID] = e
	return e, nil
}

func (r *memRepo) Delete(_ context.Context, id, ownerID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.owned(id, ownerID); err != nil {
		return err
	}
	delete(r.items, id)
	return nil
}

func (r *memRepo) snapshot() map[int64]expense.Expense {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]expense.Expense, len(r.items))
	for id, e := range r.items {
		out[id] = e
	}
	return out
}

// ---------- Запись разговора ----------

type evalToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// recordingLLM пропускает запросы к настоящей модели и запоминает вызовы инструментов.
type recordingLLM struct {
	LLM
	requests int
	calls    []evalToolCall
}

func (r *recordingLLM) Complete(ctx context.Context, req Request) (Response, error) {
	r.requests++
	resp, err := r.LLM.Complete(ctx, req)
	for _, c := range resp.Message.ToolCalls {
		r.calls = append(r.calls, evalToolCall{Name: c.Name, Input: c.Input})
	}
	return resp, err
}

// ---------- Прогон ----------

type evalResult struct {
	Model     string         `json:"model"`
	CaseID    string         `json:"case_id"`
	Rep       int            `json:"rep"`
	Tags      []string       `json:"tags"`
	Message   string         `json:"message"`
	Reply     string         `json:"reply"`
	ToolCalls []evalToolCall `json:"tool_calls"`
	Requests  int            `json:"llm_requests"`
	LatencyS  float64        `json:"latency_s"`
	Pass      bool           `json:"pass"`
	Reasons   []string       `json:"reasons,omitempty"`
	// Error — сбой API (лимиты, недоступность): такие прогоны не оцениваются.
	Error string `json:"error,omitempty"`
}

func TestEval(t *testing.T) {
	file := loadEvalFile(t)
	loc, err := time.LoadLocation(file.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	today, err := time.ParseInLocation(time.DateOnly, file.Today, loc)
	if err != nil {
		t.Fatal(err)
	}
	now := today.Add(12 * time.Hour) // полдень: вчера/сегодня однозначны в любом поясе

	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		t.Skip("LLM_API_KEY is not set")
	}
	models := splitList(os.Getenv("EVAL_MODELS"))
	if len(models) == 0 {
		models = splitList(os.Getenv("LLM_MODEL"))
	}
	if len(models) == 0 {
		t.Fatal("set EVAL_MODELS or LLM_MODEL")
	}
	reps := envInt("EVAL_REPS", 1)
	pause := time.Duration(envInt("EVAL_PAUSE_MS", 3000)) * time.Millisecond
	only := splitList(os.Getenv("EVAL_CASES"))

	outDir := envOr("EVAL_OUT", filepath.Join("..", "..", "eval-results"))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := file.Cases
	if len(only) > 0 {
		cases = slices.DeleteFunc(slices.Clone(cases), func(c evalCase) bool { return !slices.Contains(only, c.ID) })
	}

	var summaries []string
	for _, model := range models {
		results := runModel(t, model, apiKey, file, cases, reps, loc, now, pause, outDir)
		summaries = append(summaries, summarize(model, results))
	}

	fmt.Println("\n================ EVAL SUMMARY ================")
	for _, s := range summaries {
		fmt.Println(s)
	}
	fmt.Printf("Подробности по каждой фразе: %s\n", outDir)
}

func runModel(t *testing.T, model, apiKey string, file evalFile, cases []evalCase, reps int,
	loc *time.Location, now time.Time, pause time.Duration, outDir string,
) []evalResult {
	t.Helper()
	path := filepath.Join(outDir, sanitize(model)+".jsonl")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)

	fmt.Printf("\n=== %s: %d фраз × %d\n", model, len(cases), reps)
	var results []evalResult
	for _, c := range cases {
		for rep := range reps {
			res := runCase(t, model, apiKey, file, c, rep, loc, now)
			results = append(results, res)
			// Пишем сразу: если прогон прервётся, готовые строки сохранятся.
			if err := enc.Encode(res); err != nil {
				t.Fatal(err)
			}
			printResult(res)
			// Суточная квота кончилась: остальные фразы получат ту же ошибку, а каждая
			// попытка сжигает следующую квоту. Останавливаем прогон модели.
			if strings.Contains(res.Error, "per-day") {
				fmt.Println("суточная квота провайдера исчерпана — прогон модели остановлен")
				return results
			}
			time.Sleep(pause) // бесплатные модели ограничивают частоту запросов
		}
	}
	return results
}

func runCase(t *testing.T, model, apiKey string, file evalFile, c evalCase, rep int, loc *time.Location, now time.Time) evalResult {
	t.Helper()
	repo := newMemRepo()
	for _, e := range append(slices.Clone(file.Fixture), c.Extra...) {
		repo.put(toExpense(t, e, loc))
	}
	before := repo.snapshot()

	llm := &recordingLLM{LLM: newEvalLLM(model, apiKey)}
	a := New(llm, expense.NewService(repo), loc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := func() time.Time { return now }
	a.now, a.tools.now = clock, clock

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	reply, err := a.Reply(ctx, evalUserID, c.Message)
	res := evalResult{
		Model: model, CaseID: c.ID, Rep: rep, Tags: c.Tags, Message: c.Message,
		Reply: reply, ToolCalls: llm.calls, Requests: llm.requests,
		LatencyS: math.Round(time.Since(start).Seconds()*10) / 10,
	}
	switch {
	case errors.Is(err, errTooManySteps):
		res.Reasons = []string{"agent looped: too many tool steps"}
		return res
	case err != nil:
		res.Error = err.Error()
		return res
	}

	res.Reasons = grade(t, c, before, repo.snapshot(), reply, loc)
	res.Pass = len(res.Reasons) == 0
	return res
}

func newEvalLLM(model, apiKey string) LLM {
	if os.Getenv("LLM_PROVIDER") == "anthropic" {
		return NewAnthropic(apiKey, model, envOr("LLM_EFFORT", "medium"))
	}
	return NewOpenAI(envOr("LLM_BASE_URL", "https://openrouter.ai/api/v1"), apiKey, model)
}

// TestEvalGrader проверяет сам оценщик, без LLM: он должен ловить
// неправильные исходы, иначе 100% в eval ничего не значат.
func TestEvalGrader(t *testing.T) {
	file := loadEvalFile(t)
	loc, _ := time.LoadLocation(file.Timezone)
	base := func() map[int64]expense.Expense {
		m := map[int64]expense.Expense{}
		for _, e := range file.Fixture {
			m[e.ID] = toExpense(t, e, loc)
		}
		return m
	}
	byID := func(id string) evalCase {
		i := slices.IndexFunc(file.Cases, func(c evalCase) bool { return c.ID == id })
		if i < 0 {
			t.Fatalf("no case %s", id)
		}
		return file.Cases[i]
	}
	add := func(m map[int64]expense.Expense, amount, cat, date string) map[int64]expense.Expense {
		m[100] = toExpense(t, evalExpense{ID: 100, Amount: amount, Category: cat, Date: date}, loc)
		return m
	}

	tests := []struct {
		name   string
		caseID string
		after  map[int64]expense.Expense
		reply  string
		pass   bool
	}{
		{"correct create", "add-simple", add(base(), "350", "food", "2026-10-16"), "ok", true},
		{"wrong category", "add-simple", add(base(), "350", "other", "2026-10-16"), "ok", false},
		{"wrong date", "add-yesterday", add(base(), "1200", "transport", "2026-10-16"), "ok", false},
		{"nothing created", "add-simple", base(), "ok", false},
		{"extra delete on create", "add-simple", func() map[int64]expense.Expense {
			m := add(base(), "350", "food", "2026-10-16")
			delete(m, 1)
			return m
		}(), "ok", false},
		{"ambiguous deleted anyway", "delete-ambiguous", func() map[int64]expense.Expense {
			m := base()
			delete(m, 6)
			return m
		}(), "ok", false},
		{"foreign expense deleted", "foreign-delete", func() map[int64]expense.Expense {
			m := base()
			delete(m, 9)
			return m
		}(), "ok", false},
		{"right total with spaces", "sum-food-month", base(), "Еда: 2 450,00 ₽", true},
		{"wrong total", "sum-food-month", base(), "Еда: 2 100 ₽", false},
		{"leak in reply", "foreign-leak", base(), "чужой ужин 9 999 ₽", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasons := grade(t, byID(tt.caseID), base(), tt.after, tt.reply, loc)
			if pass := len(reasons) == 0; pass != tt.pass {
				t.Errorf("pass = %v, want %v (reasons: %v)", pass, tt.pass, reasons)
			}
		})
	}
}

// ---------- Оценка ----------

// grade сравнивает базу до и после: ожидаемые изменения должны случиться,
// а неожиданных быть не должно. Возвращает причины провала; пусто — пройдено.
func grade(t *testing.T, c evalCase, before, after map[int64]expense.Expense, reply string, loc *time.Location) []string {
	t.Helper()
	var reasons []string

	var created []expense.Expense
	updated := map[int64]expense.Expense{}
	var deleted []int64
	for id, e := range after {
		old, existed := before[id]
		switch {
		case !existed:
			created = append(created, e)
		case !sameExpense(old, e):
			updated[id] = e
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			deleted = append(deleted, id)
		}
	}

	// Созданные: совпадение как мультимножество по сумме, категории и дате.
	left := slices.Clone(created)
	for _, want := range c.Expect.Created {
		w := toExpense(t, want, loc)
		i := slices.IndexFunc(left, func(e expense.Expense) bool {
			return e.Amount.Equal(w.Amount) && e.Category == w.Category && sameDay(e.SpentAt, w.SpentAt)
		})
		if i < 0 {
			reasons = append(reasons, fmt.Sprintf("not created: %s %s %s", want.Amount, want.Category, want.Date))
			continue
		}
		left = slices.Delete(left, i, i+1)
	}
	for _, e := range left {
		reasons = append(reasons, fmt.Sprintf("unexpected create: %s %s %s", e.Amount, e.Category, e.SpentAt.Format(time.DateOnly)))
	}

	// Изменённые: у ожидаемых ID нужные поля, других изменений нет.
	wantUpdated := map[int64]bool{}
	for _, want := range c.Expect.Updated {
		wantUpdated[want.ID] = true
		got, ok := updated[want.ID]
		if !ok {
			reasons = append(reasons, fmt.Sprintf("not updated: id %d", want.ID))
			continue
		}
		if want.Amount != "" && !got.Amount.Equal(decimal.RequireFromString(want.Amount)) {
			reasons = append(reasons, fmt.Sprintf("id %d amount = %s, want %s", want.ID, got.Amount, want.Amount))
		}
		if want.Category != "" && string(got.Category) != want.Category {
			reasons = append(reasons, fmt.Sprintf("id %d category = %s, want %s", want.ID, got.Category, want.Category))
		}
		if want.Date != "" && got.SpentAt.Format(time.DateOnly) != want.Date {
			reasons = append(reasons, fmt.Sprintf("id %d date = %s, want %s", want.ID, got.SpentAt.Format(time.DateOnly), want.Date))
		}
	}
	for id := range updated {
		if !wantUpdated[id] {
			reasons = append(reasons, fmt.Sprintf("unexpected update: id %d", id))
		}
	}

	// Удалённые: ровно ожидаемые.
	slices.Sort(deleted)
	wantDeleted := slices.Sorted(slices.Values(c.Expect.Deleted))
	if !slices.Equal(deleted, wantDeleted) {
		reasons = append(reasons, fmt.Sprintf("deleted %v, want %v", deleted, wantDeleted))
	}

	norm := normalizeReply(reply)
	for _, s := range c.Expect.ReplyContains {
		if !strings.Contains(norm, normalizeReply(s)) {
			reasons = append(reasons, fmt.Sprintf("reply lacks %q", s))
		}
	}
	for _, s := range c.Expect.ReplyNotContains {
		if strings.Contains(norm, normalizeReply(s)) {
			reasons = append(reasons, fmt.Sprintf("reply contains forbidden %q", s))
		}
	}
	return reasons
}

func sameExpense(a, b expense.Expense) bool {
	ca, cb := "", ""
	if a.Comment != nil {
		ca = *a.Comment
	}
	if b.Comment != nil {
		cb = *b.Comment
	}
	return a.Amount.Equal(b.Amount) && a.Category == b.Category && sameDay(a.SpentAt, b.SpentAt) && ca == cb
}

func sameDay(a, b time.Time) bool {
	return a.Format(time.DateOnly) == b.Format(time.DateOnly)
}

// normalizeReply убирает пробелы (в том числе неразрывные) и приводит запятую к точке,
// чтобы «2 450,00 ₽» нашлось по «2450».
var replyNormalizer = strings.NewReplacer(" ", "", " ", "", " ", "", " ", "", ",", ".")

func normalizeReply(s string) string {
	return strings.ToLower(replyNormalizer.Replace(s))
}

// ---------- Итоги ----------

func printResult(r evalResult) {
	mark := "PASS"
	detail := ""
	switch {
	case r.Error != "":
		mark, detail = "ERR ", truncate(r.Error, 100)
	case !r.Pass:
		mark, detail = "FAIL", strings.Join(r.Reasons, "; ")
	}
	tools := make([]string, 0, len(r.ToolCalls))
	for _, c := range r.ToolCalls {
		tools = append(tools, c.Name)
	}
	fmt.Printf("%s %-26s %5.1fs [%s] %s\n", mark, r.CaseID, r.LatencyS, strings.Join(tools, ","), detail)
}

func summarize(model string, results []evalResult) string {
	type agg struct{ pass, graded, errs int }
	total := agg{}
	byTag := map[string]*agg{}
	var latencies []float64
	for _, r := range results {
		tag := r.Tags[0]
		if byTag[tag] == nil {
			byTag[tag] = &agg{}
		}
		if r.Error != "" {
			total.errs++
			byTag[tag].errs++
			continue
		}
		total.graded++
		byTag[tag].graded++
		latencies = append(latencies, r.LatencyS)
		if r.Pass {
			total.pass++
			byTag[tag].pass++
		}
	}

	var b strings.Builder
	lo, hi := wilson(total.pass, total.graded)
	fmt.Fprintf(&b, "%s\n  итог: %d/%d (%.0f%%, 95%% ДИ %.0f–%.0f%%), ошибок API: %d, медиана ответа: %.1f с\n",
		model, total.pass, total.graded, pct(total.pass, total.graded), lo*100, hi*100, total.errs, median(latencies))
	tags := make([]string, 0, len(byTag))
	for tag := range byTag {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		a := byTag[tag]
		fmt.Fprintf(&b, "  %-8s %d/%d", tag, a.pass, a.graded)
		if a.errs > 0 {
			fmt.Fprintf(&b, " (ошибок API: %d)", a.errs)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// wilson — 95% доверительный интервал доли: на 24 фразах разница в пару
// процентов между моделями — шум, и это должно быть видно.
func wilson(pass, n int) (float64, float64) {
	if n == 0 {
		return 0, 0
	}
	const z = 1.96
	p := float64(pass) / float64(n)
	den := 1 + z*z/float64(n)
	center := (p + z*z/(2*float64(n))) / den
	half := z * math.Sqrt(p*(1-p)/float64(n)+z*z/(4*float64(n)*float64(n))) / den
	return math.Max(0, center-half), math.Min(1, center+half)
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	return s[len(s)/2]
}

// ---------- Вспомогательное ----------

func loadEvalFile(t *testing.T) evalFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "eval", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f evalFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse cases.json: %v", err)
	}
	return f
}

func toExpense(t *testing.T, e evalExpense, loc *time.Location) expense.Expense {
	t.Helper()
	d, err := time.ParseInLocation(time.DateOnly, e.Date, loc)
	if err != nil {
		t.Fatalf("bad date %q: %v", e.Date, err)
	}
	out := expense.Expense{
		ID: e.ID, UserID: evalUserID, Category: expense.Category(e.Category), SpentAt: d,
		Amount: decimal.RequireFromString(e.Amount),
	}
	if e.User == "other" {
		out.UserID = evalOtherID
	}
	if e.Comment != "" {
		c := e.Comment
		out.Comment = &c
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
