package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Addr                 string
	DBPath               string
	Provider             string
	CodexBin             string
	CORSOrigin           string
	MaxConcurrentStreams int
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
		CORSOrigin:           strings.TrimSpace(value("CORS_ORIGIN", "*")),
		CodexHistoryUsername: strings.TrimSpace(os.Getenv("CODEX_HISTORY_USERNAME")),
	}
	if cfg.Provider != "stub" && cfg.Provider != "codex" {
		return Config{}, errors.New("PROVIDER must be stub or codex")
	}
	if cfg.CORSOrigin != "*" {
		for _, origin := range strings.Split(cfg.CORSOrigin, ",") {
			origin = strings.TrimSpace(origin)
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "tauri") ||
				u.Hostname() == "" || u.User != nil || u.Path != "" || strings.ContainsAny(origin, "?#") {
				return Config{}, errors.New("CORS_ORIGIN must be * or a comma-separated list of HTTP, HTTPS or Tauri origins without paths")
			}
		}
	}
	limit, err := strconv.Atoi(value("MAX_CONCURRENT_STREAMS", "1"))
	if err != nil || limit < 1 {
		return Config{}, errors.New("MAX_CONCURRENT_STREAMS must be an integer >= 1")
	}
	cfg.MaxConcurrentStreams = limit
	return cfg, nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
