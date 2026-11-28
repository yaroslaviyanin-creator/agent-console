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
	Addr                 string
	DBPath               string
	Provider             string
	EnabledProviders     []string
	DefaultProvider      string
	MemoryProvider       string
	RunnerURL            string
	RunnerSharedSecret   string
	CORSOrigin           string
	TrustProxyHeaders    bool
	MaxConcurrentStreams int
	CodexHistoryUsername string
	MemoryAutoUpdate       bool
	MemoryModel            string
	MemoryCheckInterval    time.Duration
	MemoryMinNewMessages   int
	TelegramBotToken          string
	TelegramInitDataMaxAge    time.Duration
	MaxAttachmentMB           int
	MaxAttachmentsPerMessage  int
	MaxAttachmentsPerChat     int
	MaxAttachmentContextBytes int
	SessionTTLHours           int
	SearXNGURL                string
	BraveAPIKey               string
	TavilyAPIKey              string
	APIKeysSecret             string
	PublicURL                 string
}

const (
	DefaultMaxAttachmentMB           = 100
	DefaultMaxAttachmentsPerMessage  = 10
	DefaultMaxAttachmentsPerChat     = 20
	DefaultMaxAttachmentContextBytes = 256 << 10
	DefaultSessionTTLHours           = 168
)

func (c Config) EffectiveSessionTTL() time.Duration {
	if c.SessionTTLHours <= 0 {
		return time.Duration(DefaultSessionTTLHours) * time.Hour
	}
	return time.Duration(c.SessionTTLHours) * time.Hour
}

func (c Config) EffectiveMaxAttachmentBytes() int64 {
	mb := c.MaxAttachmentMB
	if mb <= 0 {
		mb = DefaultMaxAttachmentMB
	}
	return int64(mb) << 20
}

func (c Config) EffectiveMaxAttachmentsPerMessage() int {
	if c.MaxAttachmentsPerMessage <= 0 {
		return DefaultMaxAttachmentsPerMessage
	}
	return c.MaxAttachmentsPerMessage
}

func (c Config) EffectiveMaxAttachmentsPerChat() int {
	if c.MaxAttachmentsPerChat <= 0 {
		return DefaultMaxAttachmentsPerChat
	}
	return c.MaxAttachmentsPerChat
}

func (c Config) EffectiveMaxAttachmentContextBytes() int {
	if c.MaxAttachmentContextBytes <= 0 {
		return DefaultMaxAttachmentContextBytes
	}
	return c.MaxAttachmentContextBytes
}

type ProviderConfig struct {
	EnabledProviders []string
	DefaultProvider  string
	MemoryProvider   string
}

// IsCanonicalProvider reports whether name is a supported backend provider ID.
func IsCanonicalProvider(name string) bool {
	switch name {
	case "antigravity", "codex", "claude", "paid_api", "stub":
		return true
	default:
		return false
	}
}

func validateProviderName(name, source string) error {
	if !IsCanonicalProvider(name) {
		return fmt.Errorf("%s must use antigravity, codex, claude, paid_api or stub (got %q)", source, name)
	}
	return nil
}

// ParseProviderConfig parses ENABLED_PROVIDERS (with legacy PROVIDER fallback),
// DEFAULT_PROVIDER, and MEMORY_PROVIDER without loading .env files.
func ParseProviderConfig(lookup func(string) (string, bool)) (ProviderConfig, error) {
	get := func(key string) string {
		if lookup == nil {
			return ""
		}
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}

	var enabled []string
	seen := make(map[string]bool)

	if rawEnabled := get("ENABLED_PROVIDERS"); rawEnabled != "" {
		for _, part := range strings.Split(rawEnabled, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				return ProviderConfig{}, errors.New("ENABLED_PROVIDERS must not contain empty entries")
			}
			if err := validateProviderName(name, "ENABLED_PROVIDERS"); err != nil {
				return ProviderConfig{}, err
			}
			if seen[name] {
				return ProviderConfig{}, fmt.Errorf("duplicate provider %q in ENABLED_PROVIDERS", name)
			}
			seen[name] = true
			enabled = append(enabled, name)
		}
	} else {
		legacy := strings.ToLower(get("PROVIDER"))
		if legacy == "" {
			legacy = "stub"
		}
		if err := validateProviderName(legacy, "PROVIDER"); err != nil {
			return ProviderConfig{}, err
		}
		seen[legacy] = true
		enabled = []string{legacy}
	}

	def := strings.ToLower(get("DEFAULT_PROVIDER"))
	if def == "" {
		def = enabled[0]
	} else {
		if err := validateProviderName(def, "DEFAULT_PROVIDER"); err != nil {
			return ProviderConfig{}, err
		}
		if !seen[def] {
			return ProviderConfig{}, fmt.Errorf("DEFAULT_PROVIDER %q must be enabled", def)
		}
	}

	mem := strings.ToLower(get("MEMORY_PROVIDER"))
	if mem == "" {
		mem = def
	} else {
		if err := validateProviderName(mem, "MEMORY_PROVIDER"); err != nil {
			return ProviderConfig{}, err
		}
		if !seen[mem] {
			return ProviderConfig{}, fmt.Errorf("MEMORY_PROVIDER %q must be enabled", mem)
		}
	}

	return ProviderConfig{
		EnabledProviders: enabled,
		DefaultProvider:  def,
		MemoryProvider:   mem,
	}, nil
}

func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	provCfg, err := ParseProviderConfig(os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	hasCLI := false
	for _, p := range provCfg.EnabledProviders {
		if p != "stub" && p != "paid_api" {
			hasCLI = true
			break
		}
	}
	if hasCLI && !isRunningInDocker() && !isEnvTrue("ALLOW_HOST_CLI_EXECUTION") {
		return Config{}, errors.New("refusing to run CLI provider outside Docker; use docker compose")
	}
	cfg := Config{
		Addr:                 value("SERVER_ADDR", ":8080"),
		DBPath:               value("DB_PATH", "./data/app.db"),
		Provider:             provCfg.DefaultProvider,
		EnabledProviders:     provCfg.EnabledProviders,
		DefaultProvider:      provCfg.DefaultProvider,
		MemoryProvider:       provCfg.MemoryProvider,
		RunnerURL:            strings.TrimSpace(os.Getenv("RUNNER_URL")),
		RunnerSharedSecret:   os.Getenv("RUNNER_SHARED_SECRET"),
		CORSOrigin:           strings.TrimSpace(value("CORS_ORIGIN", "*")),
		CodexHistoryUsername: strings.TrimSpace(os.Getenv("CODEX_HISTORY_USERNAME")),
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
	cfg.TelegramBotToken = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	cfg.TelegramInitDataMaxAge, err = time.ParseDuration(value("TELEGRAM_INIT_DATA_MAX_AGE", "1h"))
	if err != nil || cfg.TelegramInitDataMaxAge <= 0 {
		return Config{}, errors.New("TELEGRAM_INIT_DATA_MAX_AGE must be a positive duration")
	}
	cfg.MaxAttachmentMB, err = strconv.Atoi(value("MAX_ATTACHMENT_MB", strconv.Itoa(DefaultMaxAttachmentMB)))
	if err != nil || cfg.MaxAttachmentMB < 1 {
		return Config{}, errors.New("MAX_ATTACHMENT_MB must be an integer >= 1")
	}
	cfg.MaxAttachmentsPerMessage, err = strconv.Atoi(value("MAX_ATTACHMENTS_PER_MESSAGE", strconv.Itoa(DefaultMaxAttachmentsPerMessage)))
	if err != nil || cfg.MaxAttachmentsPerMessage < 1 {
		return Config{}, errors.New("MAX_ATTACHMENTS_PER_MESSAGE must be an integer >= 1")
	}
	cfg.MaxAttachmentsPerChat, err = strconv.Atoi(value("MAX_ATTACHMENTS_PER_CHAT", strconv.Itoa(DefaultMaxAttachmentsPerChat)))
	if err != nil || cfg.MaxAttachmentsPerChat < cfg.MaxAttachmentsPerMessage {
		return Config{}, errors.New("MAX_ATTACHMENTS_PER_CHAT must be an integer >= MAX_ATTACHMENTS_PER_MESSAGE")
	}
	cfg.MaxAttachmentContextBytes, err = strconv.Atoi(value("MAX_ATTACHMENT_CONTEXT_BYTES", strconv.Itoa(DefaultMaxAttachmentContextBytes)))
	if err != nil || cfg.MaxAttachmentContextBytes < 1024 {
		return Config{}, errors.New("MAX_ATTACHMENT_CONTEXT_BYTES must be an integer >= 1024")
	}
	cfg.SessionTTLHours, err = strconv.Atoi(value("SESSION_TTL_HOURS", strconv.Itoa(DefaultSessionTTLHours)))
	if err != nil || cfg.SessionTTLHours < 1 {
		return Config{}, errors.New("SESSION_TTL_HOURS must be an integer >= 1")
	}
	cfg.SearXNGURL = strings.TrimSpace(os.Getenv("SEARXNG_URL"))
	cfg.BraveAPIKey = strings.TrimSpace(os.Getenv("BRAVE_API_KEY"))
	cfg.TavilyAPIKey = strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	cfg.APIKeysSecret = os.Getenv("API_KEYS_SECRET")
	if cfg.APIKeysSecret == "" {
		cfg.APIKeysSecret = cfg.RunnerSharedSecret
	}
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_URL")), "/")
	return cfg, nil
}

var dockerenvPath = "/.dockerenv"

func isRunningInDocker() bool {
	if isEnvTrue("RUNNING_IN_DOCKER") {
		return true
	}
	_, err := os.Stat(dockerenvPath)
	return err == nil
}

func isEnvTrue(key string) bool {
	return os.Getenv(key) == "true"
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
