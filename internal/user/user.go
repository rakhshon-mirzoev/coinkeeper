package user

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID        int64
	FirstName string
	Username  *string
	CreatedAt time.Time
}

type PostgresRepo struct {
	pool *pgxpool.Pool
}

func NewPostgresRepo(pool *pgxpool.Pool) *PostgresRepo {
	return &PostgresRepo{pool: pool}
}

func (r *PostgresRepo) List(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, first_name, username, created_at FROM users ORDER BY first_name, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		var u User
		err := row.Scan(&u.ID, &u.FirstName, &u.Username, &u.CreatedAt)
		return u, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan users: %w", err)
	}
	return users, nil
}
