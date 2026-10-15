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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	slog.Info("bot starting", "addr", cfg.HTTPAddr)

	return nil
}
