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

// Upsert регистрирует пользователя при первом сообщении и обновляет имя,
// если он поменял его в Telegram.
func (r *PostgresRepo) Upsert(ctx context.Context, u User) error {
	query := `
		INSERT INTO users (id, first_name, username)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE
		SET first_name = EXCLUDED.first_name, username = EXCLUDED.username`

	if _, err := r.pool.Exec(ctx, query, u.ID, u.FirstName, u.Username); err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
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
