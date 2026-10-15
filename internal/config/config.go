package config

import (
	"errors"
	"os"
)

type Config struct {
	HTTPAddr     string
	DBConnection string
}

func Load() (*Config, error) {

	var cfg Config

	cfg.HTTPAddr = os.Getenv("HTTP_ADDR")
	if cfg.HTTPAddr == "" {
		return nil, errors.New("HTTP_ADDR is required")
	}

	cfg.DBConnection = os.Getenv("DB_URL")
	if cfg.DBConnection == "" {
		return nil, errors.New("DB_URL is required")
	}

	return &cfg, nil
}
