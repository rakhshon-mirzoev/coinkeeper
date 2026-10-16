package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // база часовых поясов внутри бинарника: в alpine-образе её нет

	"github.com/rakhshon-mirzoev/coinkeeper/internal/agent"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/bot"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/config"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/postgres"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/user"
)

func main() {
	if err := run(); err != nil {
		slog.Error("app setup", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var level slog.LevelVar
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &level}))
	slog.SetDefault(logger)

	cfg, err := config.LoadBot()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	level.Set(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DBConnection)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	expenses := expense.NewService(expense.NewPostgresRepo(pool))
	ag := agent.New(newLLM(cfg.LLM), expenses, cfg.Location, logger)

	b, err := bot.New(cfg.TelegramToken, cfg.AllowedUserIDs, user.NewPostgresRepo(pool), ag, logger)
	if err != nil {
		return err
	}

	if len(cfg.AllowedUserIDs) == 0 {
		logger.Warn("ALLOWED_USER_IDS is empty: bot is open to everyone")
	}
	logger.Info("bot started",
		"timezone", cfg.Location.String(),
		"allowed_users", len(cfg.AllowedUserIDs),
		"llm_provider", cfg.LLM.Provider,
		"llm_model", cfg.LLM.Model,
	)

	b.Run(ctx)

	logger.Info("bot stopped")
	return nil
}

func newLLM(cfg config.LLM) agent.LLM {
	if cfg.Provider == config.ProviderAnthropic {
		return agent.NewAnthropic(cfg.APIKey, cfg.Model, cfg.Effort)
	}
	return agent.NewOpenAI(cfg.BaseURL, cfg.APIKey, cfg.Model)
}
