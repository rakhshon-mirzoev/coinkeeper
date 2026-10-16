package expense

import (
	"context"
	"strings"
)

type Repository interface {
	Create(ctx context.Context, e Expense) (Expense, error)
	Get(ctx context.Context, id, ownerID int64) (Expense, error)
	List(ctx context.Context, f Filter) ([]Expense, error)
	Summary(ctx context.Context, f Filter) ([]CategoryTotal, error)
	Last(ctx context.Context, userID int64) (Expense, error)
	Update(ctx context.Context, e Expense, ownerID int64) (Expense, error)
	Delete(ctx context.Context, id, ownerID int64) error
}

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, e Expense) (Expense, error) {
	if e.UserID == 0 {
		return Expense{}, ErrInvalidUser
	}
	if err := validate(&e); err != nil {
		return Expense{}, err
	}
	return s.repo.Create(ctx, e)
}

func (s *Service) Get(ctx context.Context, id, ownerID int64) (Expense, error) {
	return s.repo.Get(ctx, id, ownerID)
}

func (s *Service) List(ctx context.Context, f Filter) ([]Expense, error) {
	if f.Category != "" && !f.Category.Valid() {
		return nil, ErrInvalidCategory
	}
	return s.repo.List(ctx, f)
}

func (s *Service) Summary(ctx context.Context, f Filter) ([]CategoryTotal, error) {
	if f.Category != "" && !f.Category.Valid() {
		return nil, ErrInvalidCategory
	}
	return s.repo.Summary(ctx, f)
}

func (s *Service) Last(ctx context.Context, userID int64) (Expense, error) {
	if userID == 0 {
		return Expense{}, ErrInvalidUser
	}
	return s.repo.Last(ctx, userID)
}

func (s *Service) Update(ctx context.Context, e Expense, ownerID int64) (Expense, error) {
	if err := validate(&e); err != nil {
		return Expense{}, err
	}
	return s.repo.Update(ctx, e, ownerID)
}

func (s *Service) Delete(ctx context.Context, id, ownerID int64) error {
	return s.repo.Delete(ctx, id, ownerID)
}

// пустая строка хранится как null
func validate(e *Expense) error {
	// Колонка numeric(12,2): больше двух знаков после запятой Postgres молча
	// округлит (0.001 → 0.00), а больше 10 цифр до запятой не поместятся.
	if !e.Amount.IsPositive() || !e.Amount.Equal(e.Amount.Round(2)) || e.Amount.GreaterThan(maxAmount) {
		return ErrInvalidAmount
	}
	if !e.Category.Valid() {
		return ErrInvalidCategory
	}
	if e.SpentAt.IsZero() {
		return ErrInvalidDate
	}
	if e.Comment != nil {
		trimmed := strings.TrimSpace(*e.Comment)
		if trimmed == "" {
			e.Comment = nil
		} else {
			e.Comment = &trimmed
		}
	}
	return nil
}
