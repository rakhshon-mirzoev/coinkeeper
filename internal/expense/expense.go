package expense

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

type Category string

const (
	CategoryFood          Category = "food"
	CategoryTransport     Category = "transport"
	CategoryHousing       Category = "housing"
	CategoryEntertainment Category = "entertainment"
	CategoryHealth        Category = "health"
	CategoryShopping      Category = "shopping"
	CategoryOther         Category = "other"
)

func (c Category) Valid() bool {
	switch c {
	case CategoryFood, CategoryTransport, CategoryHousing, CategoryEntertainment,
		CategoryHealth, CategoryShopping, CategoryOther:
		return true
	}
	return false
}

type Expense struct {
	ID        int64           `json:"id"`
	UserID    int64           `json:"user_id"`
	Amount    decimal.Decimal `json:"amount"`
	Category  Category        `json:"category"`
	SpentAt   time.Time       `json:"spent_at"`
	Comment   *string         `json:"comment"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Filter struct {
	UserID   int64
	Category Category
	From     time.Time // включительно
	To       time.Time // включительно
	Limit    int
}

// CategoryTotal — итог по одной категории за период.
type CategoryTotal struct {
	Category Category
	Total    decimal.Decimal
	Count    int
}

var (
	ErrNotFound        = errors.New("expense not found")
	ErrInvalidUser     = errors.New("user id is required")
	ErrUnknownUser     = errors.New("user not found")
	ErrInvalidAmount   = errors.New("amount must be positive")
	ErrInvalidCategory = errors.New("unknown category")
	ErrInvalidDate     = errors.New("spent_at is required")
)
