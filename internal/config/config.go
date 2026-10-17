package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

type Config struct {
	HTTPAddr     string
	DBConnection string
	LogLevel     slog.Level
}

// Bot — настройки, которые нужны только Telegram-боту.
type Bot struct {
	Config

	TelegramToken string
	// Location — часовой пояс семьи: от него считаются «сегодня», «вчера», «этот месяц».
	Location *time.Location

	LLM LLM
}

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
)

// LLM — доступ к языковой модели.
type LLM struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Effort   string
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

func LoadBot() (Bot, error) {
	base, err := Load()
	if err != nil {
		return Bot{}, err
	}

	cfg := Bot{
		Config:        base,
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
	}
	if cfg.TelegramToken == "" {
		return Bot{}, errors.New("TELEGRAM_BOT_TOKEN is required")
	}

	if cfg.Location, err = time.LoadLocation(getenv("TZ", "Europe/Moscow")); err != nil {
		return Bot{}, fmt.Errorf("parse TZ: %w", err)
	}

	if cfg.LLM, err = loadLLM(); err != nil {
		return Bot{}, err
	}

	return cfg, nil
}

func loadLLM() (LLM, error) {
	cfg := LLM{
		Provider: getenv("LLM_PROVIDER", ProviderOpenAI),
		BaseURL:  getenv("LLM_BASE_URL", "https://openrouter.ai/api/v1"),
		APIKey:   os.Getenv("LLM_API_KEY"),
		Model:    os.Getenv("LLM_MODEL"),
		Effort:   os.Getenv("LLM_EFFORT"),
	}

	switch cfg.Provider {
	case ProviderOpenAI:
		if cfg.Model == "" {
			return LLM{}, errors.New("LLM_MODEL is required")
		}
	case ProviderAnthropic:
		if cfg.Model == "" {
			cfg.Model = "claude-opus-5-5"
		}
		if cfg.Effort == "" {
			cfg.Effort = "medium"
		}
	default:
		return LLM{}, fmt.Errorf("unknown LLM_PROVIDER %q, expected %s or %s", cfg.Provider, ProviderOpenAI, ProviderAnthropic)
	}

	if cfg.APIKey == "" {
		return LLM{}, errors.New("LLM_API_KEY is required")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}
