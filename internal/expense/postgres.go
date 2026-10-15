package expense

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const columns = `id, user_id, amount, category, spent_at, comment, created_at, updated_at`

// foreignKeyViolation — SQLSTATE ошибки внешнего ключа: user_id нет в таблице users.
const foreignKeyViolation = "23503"

type PostgresRepo struct {
	pool *pgxpool.Pool
}

func NewPostgresRepo(pool *pgxpool.Pool) *PostgresRepo {
	return &PostgresRepo{pool: pool}
}

func (r *PostgresRepo) Create(ctx context.Context, e Expense) (Expense, error) {
	query := `
		INSERT INTO expenses (user_id, amount, category, spent_at, comment)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING ` + columns

	created, err := scanExpense(r.pool.QueryRow(ctx, query, e.UserID, e.Amount, e.Category, e.SpentAt, e.Comment))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return Expense{}, ErrUnknownUser
	}
	if err != nil {
		return Expense{}, fmt.Errorf("insert expense: %w", err)
	}
	return created, nil
}

func (r *PostgresRepo) Get(ctx context.Context, id, ownerID int64) (Expense, error) {
	query := `SELECT ` + columns + ` FROM expenses WHERE id = $1 AND ($2::bigint = 0 OR user_id = $2)`

	e, err := scanExpense(r.pool.QueryRow(ctx, query, id, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Expense{}, ErrNotFound
	}
	if err != nil {
		return Expense{}, fmt.Errorf("get expense: %w", err)
	}
	return e, nil
}

func (r *PostgresRepo) List(ctx context.Context, f Filter) ([]Expense, error) {
	var (
		conds []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if f.UserID != 0 {
		conds = append(conds, "user_id = "+arg(f.UserID))
	}
	if f.Category != "" {
		conds = append(conds, "category = "+arg(f.Category))
	}
	if !f.From.IsZero() {
		conds = append(conds, "spent_at >= "+arg(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "spent_at <= "+arg(f.To))
	}

	query := `SELECT ` + columns + ` FROM expenses`
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY spent_at DESC, id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ` + arg(f.Limit)
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}

	expenses, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Expense, error) {
		return scanExpense(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan expenses: %w", err)
	}
	return expenses, nil
}

func (r *PostgresRepo) Last(ctx context.Context, userID int64) (Expense, error) {
	query := `SELECT ` + columns + ` FROM expenses WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`

	e, err := scanExpense(r.pool.QueryRow(ctx, query, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Expense{}, ErrNotFound
	}
	if err != nil {
		return Expense{}, fmt.Errorf("get last expense: %w", err)
	}
	return e, nil
}

func (r *PostgresRepo) Update(ctx context.Context, e Expense, ownerID int64) (Expense, error) {
	query := `
		UPDATE expenses
		SET amount = $1, category = $2, spent_at = $3, comment = $4, updated_at = now()
		WHERE id = $5 AND ($6::bigint = 0 OR user_id = $6)
		RETURNING ` + columns

	updated, err := scanExpense(r.pool.QueryRow(ctx, query, e.Amount, e.Category, e.SpentAt, e.Comment, e.ID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Expense{}, ErrNotFound
	}
	if err != nil {
		return Expense{}, fmt.Errorf("update expense: %w", err)
	}
	return updated, nil
}

func (r *PostgresRepo) Delete(ctx context.Context, id, ownerID int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM expenses WHERE id = $1 AND ($2::bigint = 0 OR user_id = $2)`, id, ownerID)
	if err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanExpense(row pgx.Row) (Expense, error) {
	var e Expense
	err := row.Scan(&e.ID, &e.UserID, &e.Amount, &e.Category, &e.SpentAt, &e.Comment, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}
