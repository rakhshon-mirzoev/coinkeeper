package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/config"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
	"github.com/rakhshon-mirzoev/coinkeeper/internal/httpapi"
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

	cfg, err := config.Load()
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
	users := user.NewPostgresRepo(pool)
	router := httpapi.NewRouter(logger, expenses, users)

	srv := http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server started", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
