package expense

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// fakeRepo — репозиторий в памяти. Запоминает, что ему передали,
// и возвращает заранее заданную ошибку.
type fakeRepo struct {
	err error

	called  bool
	got     Expense
	ownerID int64
	filter  Filter
}

func (f *fakeRepo) Create(_ context.Context, e Expense) (Expense, error) {
	f.called, f.got = true, e
	e.ID = 1
	return e, f.err
}

func (f *fakeRepo) Get(_ context.Context, id, ownerID int64) (Expense, error) {
	f.called, f.ownerID = true, ownerID
	return Expense{ID: id}, f.err
}

func (f *fakeRepo) List(_ context.Context, flt Filter) ([]Expense, error) {
	f.called, f.filter = true, flt
	return nil, f.err
}

func (f *fakeRepo) Summary(_ context.Context, flt Filter) ([]CategoryTotal, error) {
	f.called, f.filter = true, flt
	return nil, f.err
}

func (f *fakeRepo) Last(_ context.Context, userID int64) (Expense, error) {
	f.called = true
	return Expense{UserID: userID}, f.err
}

func (f *fakeRepo) Update(_ context.Context, e Expense, ownerID int64) (Expense, error) {
	f.called, f.got, f.ownerID = true, e, ownerID
	return e, f.err
}

func (f *fakeRepo) Delete(_ context.Context, _, ownerID int64) error {
	f.called, f.ownerID = true, ownerID
	return f.err
}

func validExpense() Expense {
	return Expense{
		UserID:   7000000001,
		Amount:   decimal.RequireFromString("350.50"),
		Category: CategoryFood,
		SpentAt:  time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
	}
}

func ptr(s string) *string { return &s }

func TestServiceCreate_Validation(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(e *Expense)
		wantErr error
	}{
		{name: "valid", modify: func(*Expense) {}},
		{name: "no user", modify: func(e *Expense) { e.UserID = 0 }, wantErr: ErrInvalidUser},
		{name: "zero amount", modify: func(e *Expense) { e.Amount = decimal.Zero }, wantErr: ErrInvalidAmount},
		{name: "negative amount", modify: func(e *Expense) { e.Amount = decimal.NewFromInt(-5) }, wantErr: ErrInvalidAmount},
		{name: "fraction of kopeck", modify: func(e *Expense) { e.Amount = decimal.RequireFromString("0.001") }, wantErr: ErrInvalidAmount},
		{name: "three decimals", modify: func(e *Expense) { e.Amount = decimal.RequireFromString("350.555") }, wantErr: ErrInvalidAmount},
		{name: "trailing zeros ok", modify: func(e *Expense) { e.Amount = decimal.RequireFromString("350.500") }},
		{name: "max amount ok", modify: func(e *Expense) { e.Amount = decimal.RequireFromString("9999999999.99") }},
		{name: "too large", modify: func(e *Expense) { e.Amount = decimal.RequireFromString("10000000000") }, wantErr: ErrInvalidAmount},
		{name: "unknown category", modify: func(e *Expense) { e.Category = "abc" }, wantErr: ErrInvalidCategory},
		{name: "empty category", modify: func(e *Expense) { e.Category = "" }, wantErr: ErrInvalidCategory},
		{name: "no date", modify: func(e *Expense) { e.SpentAt = time.Time{} }, wantErr: ErrInvalidDate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			e := validExpense()
			tt.modify(&e)

			_, err := NewService(repo).Create(context.Background(), e)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			// Невалидная трата не должна доходить до базы.
			if wantCalled := tt.wantErr == nil; repo.called != wantCalled {
				t.Errorf("repo called = %v, want %v", repo.called, wantCalled)
			}
		})
	}
}

func TestServiceCreate_NormalizesComment(t *testing.T) {
	tests := []struct {
		name    string
		comment *string
		want    *string
	}{
		{name: "nil stays nil", comment: nil, want: nil},
		{name: "trimmed", comment: ptr("  кофе с собой  "), want: ptr("кофе с собой")},
		{name: "blank becomes nil", comment: ptr("   "), want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			e := validExpense()
			e.Comment = tt.comment

			if _, err := NewService(repo).Create(context.Background(), e); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := repo.got.Comment
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("comment = %q, want nil", *got)
			case tt.want != nil && (got == nil || *got != *tt.want):
				t.Errorf("comment = %v, want %q", got, *tt.want)
			}
		})
	}
}

func TestServiceUpdate(t *testing.T) {
	t.Run("invalid expense is rejected before repo", func(t *testing.T) {
		repo := &fakeRepo{}
		e := validExpense()
		e.Amount = decimal.Zero

		_, err := NewService(repo).Update(context.Background(), e, 42)

		if !errors.Is(err, ErrInvalidAmount) {
			t.Fatalf("err = %v, want %v", err, ErrInvalidAmount)
		}
		if repo.called {
			t.Error("repo must not be called for invalid expense")
		}
	})

	t.Run("owner is passed to repo", func(t *testing.T) {
		repo := &fakeRepo{}

		if _, err := NewService(repo).Update(context.Background(), validExpense(), 42); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.ownerID != 42 {
			t.Errorf("ownerID = %d, want 42", repo.ownerID)
		}
	})

	t.Run("not found from repo is returned as is", func(t *testing.T) {
		repo := &fakeRepo{err: ErrNotFound}

		_, err := NewService(repo).Update(context.Background(), validExpense(), 42)

		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want %v", err, ErrNotFound)
		}
	})
}

func TestServiceList_Category(t *testing.T) {
	tests := []struct {
		name     string
		category Category
		wantErr  error
	}{
		{name: "any category", category: ""},
		{name: "known category", category: CategoryTransport},
		{name: "unknown category", category: "abc", wantErr: ErrInvalidCategory},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}

			_, err := NewService(repo).List(context.Background(), Filter{Category: tt.category})

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if wantCalled := tt.wantErr == nil; repo.called != wantCalled {
				t.Errorf("repo called = %v, want %v", repo.called, wantCalled)
			}
		})
	}
}

func TestServiceLast_RequiresUser(t *testing.T) {
	repo := &fakeRepo{}

	_, err := NewService(repo).Last(context.Background(), 0)

	if !errors.Is(err, ErrInvalidUser) {
		t.Fatalf("err = %v, want %v", err, ErrInvalidUser)
	}
	if repo.called {
		t.Error("repo must not be called without user")
	}
}

func TestServiceDelete_PassesOwnerAndError(t *testing.T) {
	repo := &fakeRepo{err: ErrNotFound}

	err := NewService(repo).Delete(context.Background(), 1, 42)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want %v", err, ErrNotFound)
	}
	if repo.ownerID != 42 {
		t.Errorf("ownerID = %d, want 42", repo.ownerID)
	}
}

func TestCategoryValid(t *testing.T) {
	for _, c := range []Category{
		CategoryFood, CategoryTransport, CategoryHousing, CategoryEntertainment,
		CategoryHealth, CategoryShopping, CategoryOther,
	} {
		if !c.Valid() {
			t.Errorf("%q must be valid", c)
		}
	}
	for _, c := range []Category{"", "Food", "еда", "abc"} {
		if c.Valid() {
			t.Errorf("%q must be invalid", c)
		}
	}
}
