package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
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
	// AllowedUserIDs — Telegram ID членов семьи. Пустой список — бот открыт для всех.
	AllowedUserIDs []int64
	// Location — часовой пояс семьи: от него считаются «сегодня», «вчера», «этот месяц».
	Location *time.Location

	AnthropicAPIKey string
	LLMModel        string
	// LLMEffort — насколько глубоко модель думает: low, medium, high. Пусто — по умолчанию модели.
	LLMEffort string
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

	if cfg.AllowedUserIDs, err = parseIDs(os.Getenv("ALLOWED_USER_IDS")); err != nil {
		return Bot{}, fmt.Errorf("parse ALLOWED_USER_IDS: %w", err)
	}

	if cfg.Location, err = time.LoadLocation(getenv("TZ", "Europe/Moscow")); err != nil {
		return Bot{}, fmt.Errorf("parse TZ: %w", err)
	}

	cfg.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	if cfg.AnthropicAPIKey == "" {
		return Bot{}, errors.New("ANTHROPIC_API_KEY is required")
	}
	cfg.LLMModel = getenv("LLM_MODEL", "claude-opus-5-5")
	cfg.LLMEffort = getenv("LLM_EFFORT", "medium")

	return cfg, nil
}

// parseIDs разбирает список вида "123, 456".
func parseIDs(s string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid id %q", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}
