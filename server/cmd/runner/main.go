package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	appconfig "agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/runnerwire"
)

const (
	defaultRunnerAddr       = ":8081"
	defaultCodexBin         = "codex"
	defaultAntigravityBin   = "agy"
	defaultAntigravityModel = "gemini-3.8-flash-high"
	defaultClaudeBin        = "claude"

	maxStreamDuration   = 5 * time.Minute
	maxMetadataDuration = 30 * time.Second
	startupTimeout      = 30 * time.Second
	shutdownTimeout     = 15 * time.Second

	publicStreamError  = "provider stream failed"
	publicTimeoutError = "provider stream timed out"
)

var errEventTooLarge = errors.New("stream event exceeds max size")

type config struct {
	Addr                 string
	SharedSecret         string
	Provider             string
	EnabledProviders     []string
	DefaultProvider      string
	CodexBin             string
	AntigravityBin       string
	AntigravityModel     string
	ClaudeBin            string
	MaxConcurrentStreams int
}

type modelLister interface {
	ListModels(ctx context.Context) ([]provider.Model, error)
}

type historyReader interface {
	ListIndexedChats(ctx context.Context) ([]provider.IndexedChat, error)
	ReadIndexedTurns(ctx context.Context, id string) ([]provider.IndexedTurn, error)
}

type runnerServer struct {
	providers       map[string]provider.Provider
	defaultProvider string
	enabled         []string
	secretDigest    [sha256.Size]byte
	sem             chan struct{}
}

func main() {
	if !isRunningInDocker() {
		slog.Error("runner refuses host launch; requires RUNNING_IN_DOCKER=true or /.dockerenv")
		os.Exit(1)
	}

	if err := restrictProcessInspection(); err != nil {
		slog.Error("cannot protect runner credentials from child processes", "error", err)
		os.Exit(1)
	}

	cfg, err := loadConfig(os.LookupEnv)
	if err != nil {
		slog.Error("invalid runner configuration", "error", err)
		os.Exit(1)
	}

	os.Unsetenv("RUNNER_SHARED_SECRET")

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	initCtx, cancelInit := context.WithTimeout(sigCtx, startupTimeout)
	providers, err := buildProviders(initCtx, cfg)
	cancelInit()
	if err != nil {
		slog.Error("failed to initialize providers", "defaultProvider", cfg.DefaultProvider, "enabled", cfg.EnabledProviders, "error", err)
		os.Exit(1)
	}

	handler, err := newRunnerRegistryHandler(cfg, providers)
	if err != nil {
		slog.Error("failed to initialize runner handler", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return sigCtx },
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      maxStreamDuration + 30*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("runner listening", "addr", cfg.Addr, "defaultProvider", cfg.DefaultProvider, "enabled", cfg.EnabledProviders)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("runner server stopped with error", "error", err)
			os.Exit(1)
		}
	case <-sigCtx.Done():
		slog.Info("runner shutting down")
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancelShutdown()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed; closing server", "error", err)
			_ = srv.Close()
			os.Exit(1)
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("runner server exit error", "error", err)
			os.Exit(1)
		}
	}
}

func isRunningInDocker() bool {
	if os.Getenv("RUNNING_IN_DOCKER") == "true" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func loadConfig(getenv func(string) (string, bool)) (config, error) {
	get := func(key string) string {
		v, _ := getenv(key)
		return v
	}

	addr := strings.TrimSpace(get("RUNNER_ADDR"))
	if addr == "" {
		addr = defaultRunnerAddr
	}

	secret := get("RUNNER_SHARED_SECRET")
	if len(secret) < runnerwire.MinSecretBytes {
		return config{}, fmt.Errorf("RUNNER_SHARED_SECRET must be at least %d bytes", runnerwire.MinSecretBytes)
	}

	provCfg, err := appconfig.ParseProviderConfig(getenv)
	if err != nil {
		return config{}, err
	}

	codexBin := strings.TrimSpace(get("CODEX_BIN"))
	if codexBin == "" {
		codexBin = defaultCodexBin
	}

	agyBin := strings.TrimSpace(get("ANTIGRAVITY_BIN"))
	if agyBin == "" {
		agyBin = defaultAntigravityBin
	}

	agyModel := strings.TrimSpace(get("ANTIGRAVITY_MODEL"))
	if agyModel == "" {
		agyModel = defaultAntigravityModel
	}

	claudeBin := strings.TrimSpace(get("CLAUDE_BIN"))
	if claudeBin == "" {
		claudeBin = defaultClaudeBin
	}

	var maxConcurrent int
	if rawMax, ok := getenv("MAX_CONCURRENT_STREAMS"); ok && strings.TrimSpace(rawMax) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(rawMax))
		if err != nil || n <= 0 {
			return config{}, errors.New("MAX_CONCURRENT_STREAMS must be a positive integer")
		}
		maxConcurrent = n
	}

	return config{
		Addr:                 addr,
		SharedSecret:         secret,
		Provider:             provCfg.DefaultProvider,
		EnabledProviders:     provCfg.EnabledProviders,
		DefaultProvider:      provCfg.DefaultProvider,
		CodexBin:             codexBin,
		AntigravityBin:       agyBin,
		AntigravityModel:     agyModel,
		ClaudeBin:            claudeBin,
		MaxConcurrentStreams: maxConcurrent,
	}, nil
}

func buildProviders(ctx context.Context, cfg config) (map[string]provider.Provider, error) {
	enabled := cfg.EnabledProviders
	if len(enabled) == 0 {
		def := cfg.DefaultProvider
		if def == "" {
			def = cfg.Provider
		}
		if def == "" {
			def = "stub"
		}
		enabled = []string{def}
	}
	providers := make(map[string]provider.Provider, len(enabled))
	for _, name := range enabled {
		prov, err := buildProvider(ctx, name, cfg)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		providers[name] = prov
	}
	return providers, nil
}

func buildProvider(ctx context.Context, name string, cfg config) (provider.Provider, error) {
	switch name {
	case "stub":
		return provider.StubProvider{Delay: 100 * time.Millisecond}, nil
	case "codex":
		return provider.NewCodexCliProvider(ctx, cfg.CodexBin)
	case "antigravity":
		return provider.AntigravityCliProvider{
			Bin:          cfg.AntigravityBin,
			DefaultModel: cfg.AntigravityModel,
		}, nil
	case "claude":
		return provider.ClaudeCliProvider{
			Bin: cfg.ClaudeBin,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", name)
	}
}

func newRunnerHandler(cfg config, prov provider.Provider) (http.Handler, error) {
	if prov == nil {
		return nil, errors.New("provider is required")
	}
	def := strings.ToLower(strings.TrimSpace(cfg.DefaultProvider))
	if def == "" {
		def = strings.ToLower(strings.TrimSpace(cfg.Provider))
	}
	if def == "" {
		def = "stub"
	}
	return newRunnerRegistryHandler(cfg, map[string]provider.Provider{def: prov})
}

func newRunnerRegistryHandler(cfg config, providers map[string]provider.Provider) (http.Handler, error) {
	if len(cfg.SharedSecret) < runnerwire.MinSecretBytes {
		return nil, fmt.Errorf("shared secret must be at least %d bytes", runnerwire.MinSecretBytes)
	}
	if len(providers) == 0 {
		return nil, errors.New("at least one provider is required")
	}
	if cfg.MaxConcurrentStreams < 0 {
		return nil, errors.New("max concurrent streams cannot be negative")
	}

	normalized := make(map[string]provider.Provider, len(providers))
	for k, v := range providers {
		if v == nil {
			return nil, fmt.Errorf("provider %q is nil", k)
		}
		normalized[strings.ToLower(strings.TrimSpace(k))] = v
	}

	def := strings.ToLower(strings.TrimSpace(cfg.DefaultProvider))
	if def == "" {
		def = strings.ToLower(strings.TrimSpace(cfg.Provider))
	}
	if def == "" && len(normalized) == 1 {
		for k := range normalized {
			def = k
		}
	}
	if _, ok := normalized[def]; !ok {
		return nil, fmt.Errorf("default provider %q is not registered", def)
	}

	var enabled []string
	if len(cfg.EnabledProviders) > 0 {
		enabled = make([]string, 0, len(cfg.EnabledProviders))
		for _, p := range cfg.EnabledProviders {
			name := strings.ToLower(strings.TrimSpace(p))
			if _, ok := normalized[name]; !ok {
				return nil, fmt.Errorf("enabled provider %q is not registered", name)
			}
			enabled = append(enabled, name)
		}
	} else {
		enabled = make([]string, 0, len(normalized))
		for k := range normalized {
			enabled = append(enabled, k)
		}
		sort.Strings(enabled)
	}

	var sem chan struct{}
	if cfg.MaxConcurrentStreams > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrentStreams)
	}

	s := &runnerServer{
		providers:       normalized,
		defaultProvider: def,
		enabled:         enabled,
		secretDigest:    sha256.Sum256([]byte(cfg.SharedSecret)),
		sem:             sem,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+runnerwire.HealthPath, s.handleHealthz)
	mux.HandleFunc("GET "+runnerwire.ProvidersPath, s.handleProviders)
	mux.HandleFunc("POST "+runnerwire.StreamPath, s.handleStream)
	mux.HandleFunc("GET "+runnerwire.ModelsPath, s.handleModels)
	mux.HandleFunc("GET "+runnerwire.HistoryPath, s.handleHistoryList)
	mux.HandleFunc("GET "+runnerwire.HistoryTurnPath, s.handleHistoryTurns)

	return s.requireAuth(mux), nil
}

func (s *runnerServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == runnerwire.HealthPath && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}

		const bearerPrefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) < len(bearerPrefix) || !strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		tokenDigest := sha256.Sum256([]byte(auth[len(bearerPrefix):]))
		if subtle.ConstantTimeCompare(tokenDigest[:], s.secretDigest[:]) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *runnerServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *runnerServer) resolveProvider(requested string) (provider.Provider, bool) {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" {
		name = s.defaultProvider
	}
	prov, ok := s.providers[name]
	return prov, ok && prov != nil
}

func (s *runnerServer) historyProvider() (historyReader, bool) {
	if p, ok := s.providers["codex"]; ok && p != nil {
		if hr, ok := p.(historyReader); ok {
			return hr, true
		}
	}
	if p, ok := s.providers[s.defaultProvider]; ok && p != nil {
		if hr, ok := p.(historyReader); ok {
			return hr, true
		}
	}
	return nil, false
}

func (s *runnerServer) handleProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, runnerwire.ProvidersResponse{
		DefaultProvider: s.defaultProvider,
		Providers:       append([]string(nil), s.enabled...),
	})
}

func (s *runnerServer) handleStream(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, runnerwire.MaxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req runnerwire.Request
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "request must contain a single JSON object")
		return
	}

	if strings.TrimSpace(req.UserID) == "" {
		writeJSONError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSONError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if req.MCPEndpoint != "" {
		writeJSONError(w, http.StatusBadRequest, "mcp_endpoint is not supported")
		return
	}

	prov, ok := s.resolveProvider(req.Provider)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		default:
			writeJSONError(w, http.StatusTooManyRequests, "too many concurrent streams")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxStreamDuration)
	defer cancel()

	provReq := provider.Request{
		UserID:             req.UserID,
		Model:              req.Model,
		Reasoning:          req.Reasoning,
		Prompt:             req.Prompt,
		SessionID:          req.SessionID,
		SystemInstructions: req.SystemInstructions,
		Ephemeral:          req.Ephemeral,
	}

	events, err := prov.Stream(ctx, provReq)
	if err != nil {
		slog.Error("provider stream failed to start", "error", err)
		writeJSONError(w, http.StatusBadGateway, publicStreamError)
		return
	}
	if events == nil {
		slog.Error("provider returned nil event channel")
		writeJSONError(w, http.StatusBadGateway, publicStreamError)
		return
	}

	w.Header().Set("Content-Type", runnerwire.StreamType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	if err := flushResponse(rc); err != nil {
		cancel()
		return
	}

	emitTerminalError := func(msg string) {
		if r.Context().Err() != nil {
			return
		}
		_, _ = writeNDJSONEvent(w, rc, runnerwire.Event{
			Type:  provider.Error,
			Error: msg,
		})
	}

	var replyBytes int
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					emitTerminalError(publicTimeoutError)
				} else {
					emitTerminalError(publicStreamError)
				}
			}
			return
		case ev, ok := <-events:
			if !ok {
				emitTerminalError(publicStreamError)
				return
			}
			switch ev.Type {
			case provider.Thinking, provider.Text:
				n, err := writeNDJSONEventBounded(w, rc, runnerwire.Event{
					Type:  ev.Type,
					Delta: ev.Delta,
				}, replyBytes, runnerwire.MaxReplyBytes)
				if err != nil {
					slog.Error("stream delta write failed", "error", err)
					if errors.Is(err, errEventTooLarge) {
						emitTerminalError(publicStreamError)
					}
					cancel()
					return
				}
				replyBytes += n
			case provider.Done:
				var usage *runnerwire.Usage
				if ev.Usage != nil {
					usage = &runnerwire.Usage{
						InputTokens:       ev.Usage.InputTokens,
						CachedInputTokens: ev.Usage.CachedInputTokens,
						OutputTokens:      ev.Usage.OutputTokens,
					}
				}
				if _, err := writeNDJSONEvent(w, rc, runnerwire.Event{
					Type:      provider.Done,
					SessionID: ev.SessionID,
					Usage:     usage,
				}); err != nil {
					slog.Error("stream done write failed", "error", err)
					if errors.Is(err, errEventTooLarge) {
						emitTerminalError(publicStreamError)
					}
				}
				return
			case provider.Error:
				if ev.Err != nil {
					slog.Error("provider stream error", "error", ev.Err)
				}
				emitTerminalError(publicStreamError)
				return
			default:
				slog.Warn("ignoring unknown provider event type", "type", ev.Type)
			}
		}
	}
}

func (s *runnerServer) handleModels(w http.ResponseWriter, r *http.Request) {
	prov, ok := s.resolveProvider(r.URL.Query().Get("provider"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unsupported provider")
		return
	}
	lister, ok := prov.(modelLister)
	if !ok {
		writeJSON(w, http.StatusOK, []provider.Model{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxMetadataDuration)
	defer cancel()

	models, err := lister.ListModels(ctx)
	if err != nil {
		slog.Error("provider list models failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to list models")
		return
	}
	if models == nil {
		models = []provider.Model{}
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *runnerServer) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	hr, ok := s.historyProvider()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "history is not supported")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxMetadataDuration)
	defer cancel()

	chats, err := hr.ListIndexedChats(ctx)
	if err != nil {
		slog.Error("provider list history failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to list history")
		return
	}
	if chats == nil {
		chats = []provider.IndexedChat{}
	}
	writeJSON(w, http.StatusOK, chats)
}

func (s *runnerServer) handleHistoryTurns(w http.ResponseWriter, r *http.Request) {
	hr, ok := s.historyProvider()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "history is not supported")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "history id is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxMetadataDuration)
	defer cancel()

	turns, err := hr.ReadIndexedTurns(ctx, id)
	if err != nil {
		slog.Error("provider read history failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to read history")
		return
	}
	if turns == nil {
		turns = []provider.IndexedTurn{}
	}
	writeJSON(w, http.StatusOK, turns)
}

func writeNDJSONEvent(w http.ResponseWriter, rc *http.ResponseController, ev runnerwire.Event) (int, error) {
	return writeNDJSONEventBounded(w, rc, ev, 0, 0)
}

func writeNDJSONEventBounded(w http.ResponseWriter, rc *http.ResponseController, ev runnerwire.Event, written int, maxTotal int) (int, error) {
	line, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	if len(line) > runnerwire.MaxEventBytes {
		return 0, errEventTooLarge
	}
	line = append(line, '\n')
	if maxTotal > 0 && written+len(line) > maxTotal {
		return 0, errEventTooLarge
	}
	n, err := w.Write(line)
	if err != nil {
		return n, err
	}
	if err := flushResponse(rc); err != nil {
		return n, err
	}
	return n, nil
}

func flushResponse(rc *http.ResponseController) error {
	err := rc.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		slog.Error("encode JSON response failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}` + "\n"))
		return
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
