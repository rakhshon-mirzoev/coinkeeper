package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/config"
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

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	level.Set(cfg.LogLevel)

	slog.Info("bot starting", "addr", cfg.HTTPAddr)

	return nil
}
