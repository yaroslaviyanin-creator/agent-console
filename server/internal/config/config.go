package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Addr                 string
	DBPath               string
	Provider             string
	CodexBin             string
	CORSOrigin           string
	CodexHistoryUsername string
}

func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	cfg := Config{
		Addr:                 value("SERVER_ADDR", ":8080"),
		DBPath:               value("DB_PATH", "./data/app.db"),
		Provider:             value("PROVIDER", "stub"),
		CodexBin:             value("CODEX_BIN", "codex"),
		CORSOrigin:           value("CORS_ORIGIN", "*"),
		CodexHistoryUsername: strings.TrimSpace(os.Getenv("CODEX_HISTORY_USERNAME")),
	}
	if cfg.Provider != "stub" && cfg.Provider != "codex" {
		return Config{}, errors.New("PROVIDER must be stub or codex")
	}
	if cfg.CORSOrigin != "*" {
		u, err := url.Parse(cfg.CORSOrigin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
			u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return Config{}, errors.New("CORS_ORIGIN must be * or a single HTTP(S) origin without a path")
		}
	}
	return cfg, nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
