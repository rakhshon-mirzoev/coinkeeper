package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
)

const dateLayout = time.DateOnly

type ExpenseService interface {
	Create(ctx context.Context, e expense.Expense) (expense.Expense, error)
	Get(ctx context.Context, id, ownerID int64) (expense.Expense, error)
	List(ctx context.Context, f expense.Filter) ([]expense.Expense, error)
	Update(ctx context.Context, e expense.Expense, ownerID int64) (expense.Expense, error)
	Delete(ctx context.Context, id, ownerID int64) error
}

const allOwners = 0

type expenseResponse struct {
	ID        int64            `json:"id"`
	UserID    int64            `json:"user_id"`
	Amount    string           `json:"amount"`
	Category  expense.Category `json:"category"`
	SpentAt   string           `json:"spent_at"`
	Comment   *string          `json:"comment"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func toExpenseResponse(e expense.Expense) expenseResponse {
	return expenseResponse{
		ID:        e.ID,
		UserID:    e.UserID,
		Amount:    e.Amount.StringFixed(2),
		Category:  e.Category,
		SpentAt:   e.SpentAt.Format(dateLayout),
		Comment:   e.Comment,
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
}

type createExpenseRequest struct {
	UserID   int64            `json:"user_id"`
	Amount   decimal.Decimal  `json:"amount"`
	Category expense.Category `json:"category"`
	SpentAt  string           `json:"spent_at"`
	Comment  *string          `json:"comment"`
}

type updateExpenseRequest struct {
	Amount   *decimal.Decimal  `json:"amount"`
	Category *expense.Category `json:"category"`
	SpentAt  *string           `json:"spent_at"`
	Comment  *string           `json:"comment"`
}

type expenseHandler struct {
	svc ExpenseService
	log *slog.Logger
}

func (h *expenseHandler) list(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	expenses, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeServiceError(w, h.log, err)
		return
	}

	resp := make([]expenseResponse, 0, len(expenses))
	for _, e := range expenses {
		resp = append(resp, toExpenseResponse(e))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *expenseHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	e, err := h.svc.Get(r.Context(), id, allOwners)
	if err != nil {
		writeServiceError(w, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toExpenseResponse(e))
}

func (h *expenseHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createExpenseRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var spentAt time.Time
	if req.SpentAt != "" {
		var err error
		if spentAt, err = time.Parse(dateLayout, req.SpentAt); err != nil {
			writeError(w, http.StatusBadRequest, "spent_at must be YYYY-MM-DD")
			return
		}
	}

	e, err := h.svc.Create(r.Context(), expense.Expense{
		UserID:   req.UserID,
		Amount:   req.Amount,
		Category: req.Category,
		SpentAt:  spentAt,
		Comment:  req.Comment,
	})
	if err != nil {
		writeServiceError(w, h.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, toExpenseResponse(e))
}

func (h *expenseHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req updateExpenseRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	e, err := h.svc.Get(r.Context(), id, allOwners)
	if err != nil {
		writeServiceError(w, h.log, err)
		return
	}

	if req.Amount != nil {
		e.Amount = *req.Amount
	}
	if req.Category != nil {
		e.Category = *req.Category
	}
	if req.SpentAt != nil {
		if e.SpentAt, err = time.Parse(dateLayout, *req.SpentAt); err != nil {
			writeError(w, http.StatusBadRequest, "spent_at must be YYYY-MM-DD")
			return
		}
	}
	if req.Comment != nil {
		e.Comment = req.Comment
	}

	updated, err := h.svc.Update(r.Context(), e, allOwners)
	if err != nil {
		writeServiceError(w, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toExpenseResponse(updated))
}

func (h *expenseHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := h.svc.Delete(r.Context(), id, allOwners); err != nil {
		writeServiceError(w, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid expense id")
		return 0, false
	}
	return id, true
}

func parseFilter(r *http.Request) (expense.Filter, error) {
	q := r.URL.Query()
	var f expense.Filter

	if v := q.Get("user_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, errInvalidParam("user_id")
		}
		f.UserID = id
	}
	f.Category = expense.Category(q.Get("category"))

	if v := q.Get("from"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return f, errInvalidParam("from")
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return f, errInvalidParam("to")
		}
		f.To = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, errInvalidParam("limit")
		}
		f.Limit = n
	}
	return f, nil
}

type errInvalidParam string

func (e errInvalidParam) Error() string { return "invalid query parameter: " + string(e) }
