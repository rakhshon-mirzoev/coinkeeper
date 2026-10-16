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

// queryArgs собирает значения для плейсхолдеров $1, $2, ...
type queryArgs []any

// add добавляет значение и возвращает его плейсхолдер.
func (a *queryArgs) add(v any) string {
	*a = append(*a, v)
	return fmt.Sprintf("$%d", len(*a))
}

// filterWhere строит WHERE только из заполненных полей фильтра (Limit не учитывает).
func filterWhere(f Filter, args *queryArgs) string {
	var conds []string
	if f.UserID != 0 {
		conds = append(conds, "user_id = "+args.add(f.UserID))
	}
	if f.Category != "" {
		conds = append(conds, "category = "+args.add(f.Category))
	}
	if !f.From.IsZero() {
		conds = append(conds, "spent_at >= "+args.add(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "spent_at <= "+args.add(f.To))
	}
	if len(conds) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(conds, " AND ")
}

func (r *PostgresRepo) List(ctx context.Context, f Filter) ([]Expense, error) {
	var args queryArgs
	query := `SELECT ` + columns + ` FROM expenses` + filterWhere(f, &args) + ` ORDER BY spent_at DESC, id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ` + args.add(f.Limit)
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

// Summary считает суммы по категориям в базе, а не в Go и не в LLM:
// модели плохо складывают числа, а выгружать в контекст сотни трат дорого.
func (r *PostgresRepo) Summary(ctx context.Context, f Filter) ([]CategoryTotal, error) {
	var args queryArgs
	query := `SELECT category, SUM(amount), COUNT(*) FROM expenses` + filterWhere(f, &args) +
		` GROUP BY category ORDER BY SUM(amount) DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("summarize expenses: %w", err)
	}

	totals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CategoryTotal, error) {
		var t CategoryTotal
		err := row.Scan(&t.Category, &t.Total, &t.Count)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan summary: %w", err)
	}
	return totals, nil
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
