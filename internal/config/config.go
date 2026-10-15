package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
)

type Config struct {
	HTTPAddr     string
	DBConnection string
	LogLevel     slog.Level
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:     getenv("HTTP_ADDR", ":8080"),
		DBConnection: os.Getenv("DATABASE_URL"),
	}

	if cfg.DBConnection == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("parse LOG_LEVEL: %w", err)
	}

	return cfg, nil
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}
