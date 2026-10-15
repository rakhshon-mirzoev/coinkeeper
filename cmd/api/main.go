package main

import (
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("server starting", "addr", cfg.HTTPAddr)

	return nil
}
