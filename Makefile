DB_URL ?= postgres://coinkeeper:coinkeeper@localhost:5433/coinkeeper?sslmode=disable

.PHONY: up down migrate-up migrate-down migrate-status

up:
	docker compose up -d

down:
	docker compose down

migrate-up:
	goose -dir migrations postgres "$(DB_URL)" up

migrate-down:
	goose -dir migrations postgres "$(DB_URL)" down

migrate-status:
	goose -dir migrations postgres "$(DB_URL)" status