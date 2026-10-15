-- +goose Up
CREATE TABLE users (

    id bigint primary key,
    first_name text not null,
    username text,
    created_at timestamptz not null default now()

);

CREATE TABLE expenses (

    id bigint generated always as identity primary key,
    user_id bigint not null references users(id) on delete cascade,
    amount numeric(12,2) not null check (amount > 0),
    category text not null check (category IN ('food', 'transport', 'housing', 'entertainment', 'health', 'shopping', 'other')),
    spent_at date not null,
    comment text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()

);

create index idx_expenses_user_id_spent_at on expenses (user_id, spent_at);

-- +goose Down
DROP TABLE IF EXISTS expenses;
DROP TABLE IF EXISTS users;