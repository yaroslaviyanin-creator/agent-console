package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Addr                    string
	DBPath                  string
	Provider                string
	CodexBin                string
	AntigravityBin          string
	AntigravityDefaultModel string
	CORSOrigin              string
	TrustProxyHeaders       bool
	MaxConcurrentStreams    int
	CodexHistoryUsername    string
	MemoryAutoUpdate        bool
	MemoryModel             string
	MemoryCheckInterval     time.Duration
	MemoryMinNewMessages    int
}

func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	cfg := Config{
		Addr:                    value("SERVER_ADDR", ":8080"),
		DBPath:                  value("DB_PATH", "./data/app.db"),
		Provider:                value("PROVIDER", "stub"),
		CodexBin:                value("CODEX_BIN", "codex"),
		AntigravityBin:          value("ANTIGRAVITY_BIN", "agy"),
		AntigravityDefaultModel: value("ANTIGRAVITY_MODEL", "gemini-3.8-flash-high"),
		CORSOrigin:              strings.TrimSpace(value("CORS_ORIGIN", "*")),
		CodexHistoryUsername:    strings.TrimSpace(os.Getenv("CODEX_HISTORY_USERNAME")),
	}
	if cfg.Provider != "stub" && cfg.Provider != "codex" && cfg.Provider != "antigravity" && cfg.Provider != "claude" && cfg.Provider != "paid_api" {
		return Config{}, errors.New("PROVIDER must be antigravity, codex, claude, paid_api or stub")
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
	if raw, ok := os.LookupEnv("TRUST_PROXY_HEADERS"); ok {
		cfg.TrustProxyHeaders, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse TRUST_PROXY_HEADERS: %w", err)
		}
	}
	cfg.MemoryAutoUpdate, err = strconv.ParseBool(value("MEMORY_AUTO_UPDATE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("parse MEMORY_AUTO_UPDATE: %w", err)
	}
	cfg.MemoryModel = strings.TrimSpace(os.Getenv("MEMORY_MODEL"))
	cfg.MemoryCheckInterval, err = time.ParseDuration(value("MEMORY_CHECK_INTERVAL", "5m"))
	if err != nil || cfg.MemoryCheckInterval <= 0 {
		return Config{}, errors.New("MEMORY_CHECK_INTERVAL must be a positive duration")
	}
	cfg.MemoryMinNewMessages, err = strconv.Atoi(value("MEMORY_MIN_NEW_MESSAGES", "6"))
	if err != nil || cfg.MemoryMinNewMessages < 2 {
		return Config{}, errors.New("MEMORY_MIN_NEW_MESSAGES must be an integer >= 2")
	}
	return cfg, nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
