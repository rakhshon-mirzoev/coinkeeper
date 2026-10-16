# Переменные из .env (ключи LLM для eval). Файла может не быть — тогда пропускаем.
-include .env
export

DB_URL ?= postgres://coinkeeper:coinkeeper@localhost:5433/coinkeeper?sslmode=disable

.PHONY: up down migrate-up migrate-down migrate-status test eval

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

test:
	go test ./...

# Eval агента на настоящей LLM: модели из EVAL_MODELS (по умолчанию LLM_MODEL).
# Пример: make eval EVAL_MODELS=model-a,model-b EVAL_REPS=2
eval:
	go test -tags eval -count=1 -run TestEval -timeout 60m -v ./internal/agent/
