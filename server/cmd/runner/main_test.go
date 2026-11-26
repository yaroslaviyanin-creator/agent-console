package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/runnerwire"
)

const testSecret = "0123456789abcdef0123456789abcdef"

type mockStreamProvider struct {
	streamFn func(ctx context.Context, req provider.Request) (<-chan provider.Event, error)
	calls    atomic.Int32
}

func (m *mockStreamProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	m.calls.Add(1)
	return m.streamFn(ctx, req)
}

type mockModelsProvider struct {
	mockStreamProvider
	modelsFn func(ctx context.Context) ([]provider.Model, error)
}

func (m *mockModelsProvider) ListModels(ctx context.Context) ([]provider.Model, error) {
	return m.modelsFn(ctx)
}

type mockFullProvider struct {
	mockModelsProvider
	listChatsFn func(ctx context.Context) ([]provider.IndexedChat, error)
	readTurnsFn func(ctx context.Context, id string) ([]provider.IndexedTurn, error)
}

func (m *mockFullProvider) ListIndexedChats(ctx context.Context) ([]provider.IndexedChat, error) {
	return m.listChatsFn(ctx)
}

func (m *mockFullProvider) ReadIndexedTurns(ctx context.Context, id string) ([]provider.IndexedTurn, error) {
	return m.readTurnsFn(ctx, id)
}

func testConfig() config {
	return config{
		Addr:             ":0",
		SharedSecret:     testSecret,
		Provider:         "stub",
		CodexBin:         defaultCodexBin,
		AntigravityBin:   defaultAntigravityBin,
		AntigravityModel: defaultAntigravityModel,
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	envMap := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := m[k]
			return v, ok
		}
	}

	cfg, err := loadConfig(envMap(map[string]string{
		"RUNNER_SHARED_SECRET":   testSecret,
		"PROVIDER":               "antigravity",
		"MAX_CONCURRENT_STREAMS": "4",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != defaultRunnerAddr {
		t.Fatalf("expected default addr %q, got %q", defaultRunnerAddr, cfg.Addr)
	}
	if cfg.AntigravityBin != defaultAntigravityBin || cfg.AntigravityModel != defaultAntigravityModel {
		t.Fatalf("unexpected antigravity defaults: %+v", cfg)
	}
	if cfg.CodexBin != defaultCodexBin {
		t.Fatalf("unexpected codex default: %q", cfg.CodexBin)
	}
	if cfg.MaxConcurrentStreams != 4 {
		t.Fatalf("expected MaxConcurrentStreams=4, got %d", cfg.MaxConcurrentStreams)
	}

	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{
			name: "short secret",
			env: map[string]string{
				"RUNNER_SHARED_SECRET": strings.Repeat("a", runnerwire.MinSecretBytes-1),
				"PROVIDER":             "stub",
			},
		},
		{
			name: "unsupported paid_api provider",
			env: map[string]string{
				"RUNNER_SHARED_SECRET": testSecret,
				"PROVIDER":             "paid_api",
			},
		},
		{
			name: "arbitrary provider",
			env: map[string]string{
				"RUNNER_SHARED_SECRET": testSecret,
				"PROVIDER":             "custom",
			},
		},
		{
			name: "duplicate enabled providers",
			env: map[string]string{
				"RUNNER_SHARED_SECRET": testSecret,
				"ENABLED_PROVIDERS":    "codex,codex",
			},
		},
		{
			name: "default provider not enabled",
			env: map[string]string{
				"RUNNER_SHARED_SECRET": testSecret,
				"ENABLED_PROVIDERS":    "codex,claude",
				"DEFAULT_PROVIDER":     "antigravity",
			},
		},
		{
			name: "invalid concurrency",
			env: map[string]string{
				"RUNNER_SHARED_SECRET":   testSecret,
				"PROVIDER":               "stub",
				"MAX_CONCURRENT_STREAMS": "0",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadConfig(envMap(tc.env)); err == nil {
				t.Fatalf("expected config error for %s", tc.name)
			}
		})
	}

	multiCfg, err := loadConfig(envMap(map[string]string{
		"RUNNER_SHARED_SECRET": testSecret,
		"ENABLED_PROVIDERS":    "antigravity,claude,codex",
		"DEFAULT_PROVIDER":     "claude",
		"CLAUDE_BIN":           "/usr/local/bin/claude-custom",
	}))
	if err != nil {
		t.Fatalf("multi loadConfig failed: %v", err)
	}
	if multiCfg.DefaultProvider != "claude" || multiCfg.Provider != "claude" || multiCfg.ClaudeBin != "/usr/local/bin/claude-custom" {
		t.Fatalf("unexpected multi config: %+v", multiCfg)
	}
}

func TestHealthzUnauthenticatedAndNoDetails(t *testing.T) {
	t.Parallel()

	handler, err := newRunnerHandler(testConfig(), provider.StubProvider{})
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, runnerwire.HealthPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty healthz body with no details, got %q", rec.Body.String())
	}
}

func TestAuthAllV1Routes(t *testing.T) {
	t.Parallel()

	if _, err := newRunnerHandler(config{SharedSecret: "too-short"}, provider.StubProvider{}); err == nil {
		t.Fatal("expected error for secret shorter than 32 bytes")
	}

	mock := &mockStreamProvider{
		streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
			t.Fatal("backend should not be called when unauthenticated")
			return nil, nil
		},
	}
	handler, err := newRunnerHandler(testConfig(), mock)
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, runnerwire.ProvidersPath, ""},
		{http.MethodPost, runnerwire.StreamPath, `{"user_id":"u1","prompt":"hi","mcp_endpoint":"http://bad"}`},
		{http.MethodGet, runnerwire.ModelsPath, ""},
		{http.MethodGet, runnerwire.HistoryPath, ""},
		{http.MethodGet, "/v1/history/thread-1", ""},
	}

	headers := []string{
		"",
		"Basic dXNlcjpwYXNz",
		"Bearer ",
		"Bearer " + strings.Repeat("x", len(testSecret)),
		"Bearer " + testSecret + "extra",
	}

	for _, rt := range routes {
		for _, auth := range headers {
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with auth %q: expected 401, got %d", rt.method, rt.path, auth, rec.Code)
			}
		}
	}

	if mock.calls.Load() != 0 {
		t.Fatalf("expected 0 backend calls, got %d", mock.calls.Load())
	}
}

func TestStreamValidationAndMCPRejection(t *testing.T) {
	t.Parallel()

	mock := &mockStreamProvider{
		streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
			t.Fatal("backend must never be invoked on invalid request")
			return nil, nil
		},
	}
	handler, err := newRunnerHandler(testConfig(), mock)
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "nonempty mcp_endpoint",
			body:       `{"user_id":"u1","prompt":"hello","mcp_endpoint":"http://127.0.0.1:9000"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "whitespace mcp_endpoint",
			body:       `{"user_id":"u1","prompt":"hello","mcp_endpoint":" "}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty prompt",
			body:       `{"user_id":"u1","prompt":"   "}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty user_id",
			body:       `{"user_id":"","prompt":"hello"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown json field",
			body:       `{"user_id":"u1","prompt":"hello","unknown_field":"claude"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "disabled provider",
			body:       `{"user_id":"u1","prompt":"hello","provider":"claude"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "paid_api provider rejected",
			body:       `{"user_id":"u1","prompt":"hello","provider":"paid_api"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "mcp_endpoint rejected before provider check",
			body:       `{"user_id":"u1","prompt":"hello","provider":"paid_api","mcp_endpoint":"http://127.0.0.1:9000"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multiple json objects",
			body:       `{"user_id":"u1","prompt":"hello"}{"user_id":"u1","prompt":"two"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "exceeds max request bytes",
			body:       `{"user_id":"u1","prompt":"` + strings.Repeat("a", runnerwire.MaxRequestBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, runnerwire.StreamPath, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testSecret)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d (body=%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}

	if mock.calls.Load() != 0 {
		t.Fatalf("expected backend never invoked, got %d calls", mock.calls.Load())
	}
}

func TestStreamEarlyFlushAndIncrementalDeltas(t *testing.T) {
	t.Parallel()

	headersReceived := make(chan struct{})
	firstDeltaReceived := make(chan struct{})

	mock := &mockStreamProvider{
		streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
			out := make(chan provider.Event)
			go func() {
				defer close(out)
				// Wait until client confirms response headers arrived before any event is emitted.
				select {
				case <-headersReceived:
				case <-ctx.Done():
					return
				}
				select {
				case out <- provider.Event{Type: provider.Thinking, Delta: "plan"}:
				case <-ctx.Done():
					return
				}
				select {
				case <-firstDeltaReceived:
				case <-ctx.Done():
					return
				}
				select {
				case out <- provider.Event{Type: provider.Text, Delta: "answer"}:
				case <-ctx.Done():
					return
				}
				select {
				case out <- provider.Event{Type: provider.Done, SessionID: "chat-v1:s1"}:
				case <-ctx.Done():
					return
				}
			}()
			return out, nil
		},
	}

	handler, err := newRunnerHandler(testConfig(), mock)
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testSecret)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != runnerwire.StreamType {
		t.Fatalf("expected content-type %q, got %q", runnerwire.StreamType, ct)
	}
	close(headersReceived)

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatalf("expected first delta line, err=%v", scanner.Err())
	}
	var ev1 runnerwire.Event
	if err := json.Unmarshal(scanner.Bytes(), &ev1); err != nil {
		t.Fatalf("unmarshal ev1: %v", err)
	}
	if ev1.Type != provider.Thinking || ev1.Delta != "plan" {
		t.Fatalf("unexpected ev1: %+v", ev1)
	}
	close(firstDeltaReceived)

	var rest []runnerwire.Event
	for scanner.Scan() {
		var ev runnerwire.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		rest = append(rest, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	if len(rest) != 2 || rest[0].Type != provider.Text || rest[0].Delta != "answer" || rest[1].Type != provider.Done || rest[1].SessionID != "chat-v1:s1" {
		t.Fatalf("unexpected trailing events: %+v", rest)
	}
}

func TestStreamUsageSessionRoundtrip(t *testing.T) {
	t.Parallel()

	var gotReq provider.Request
	mock := &mockStreamProvider{
		streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
			gotReq = req
			out := make(chan provider.Event, 2)
			out <- provider.Event{Type: provider.Text, Delta: "ok"}
			out <- provider.Event{
				Type:      provider.Done,
				SessionID: "chat-v1:next-session",
				Usage: &provider.Usage{
					InputTokens:       120,
					CachedInputTokens: 45,
					OutputTokens:      33,
				},
			}
			close(out)
			return out, nil
		},
	}

	handler, err := newRunnerHandler(testConfig(), mock)
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}

	wireReq := runnerwire.Request{
		UserID:             "user-42",
		Model:              "gpt-5",
		Reasoning:          "high",
		Prompt:             "explain",
		SessionID:          "chat-v1:prev-session",
		SystemInstructions: "be concise",
		Ephemeral:          true,
	}
	raw, _ := json.Marshal(wireReq)
	req := httptest.NewRequest(http.MethodPost, runnerwire.StreamPath, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+testSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	wantProvReq := provider.Request{
		UserID:             wireReq.UserID,
		Model:              wireReq.Model,
		Reasoning:          wireReq.Reasoning,
		Prompt:             wireReq.Prompt,
		SessionID:          wireReq.SessionID,
		SystemInstructions: wireReq.SystemInstructions,
		Ephemeral:          wireReq.Ephemeral,
	}
	if gotReq != wantProvReq {
		t.Fatalf("mapped provider.Request mismatch:\ngot:  %+v\nwant: %+v", gotReq, wantProvReq)
	}

	events := decodeNDJSON(t, rec.Body.Bytes())
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	done := events[1]
	if done.Type != provider.Done || done.SessionID != "chat-v1:next-session" {
		t.Fatalf("unexpected done event: %+v", done)
	}
	if done.Usage == nil || done.Usage.InputTokens != 120 || done.Usage.CachedInputTokens != 45 || done.Usage.OutputTokens != 33 {
		t.Fatalf("unexpected usage: %+v", done.Usage)
	}
}

func TestStreamCancellationOnClientDisconnect(t *testing.T) {
	t.Parallel()

	backendCanceled := make(chan struct{})
	var once sync.Once

	mock := &mockStreamProvider{
		streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
			out := make(chan provider.Event)
			go func() {
				defer close(out)
				select {
				case out <- provider.Event{Type: provider.Text, Delta: "first"}:
				case <-ctx.Done():
					once.Do(func() { close(backendCanceled) })
					return
				}
				<-ctx.Done()
				once.Do(func() { close(backendCanceled) })
			}()
			return out, nil
		},
	}

	handler, err := newRunnerHandler(testConfig(), mock)
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, srv.URL+runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testSecret)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatalf("expected first event before cancel, err=%v", scanner.Err())
	}

	cancelReq()
	_ = resp.Body.Close()

	select {
	case <-backendCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("expected backend context cancellation on client disconnect")
	}
}

func TestStreamGenericErrorsAndNoDroppedTerminal(t *testing.T) {
	t.Parallel()

	t.Run("provider error event sanitizes internal error", func(t *testing.T) {
		t.Parallel()
		secretErr := "internal-db-password-or-stderr-path"
		mock := &mockStreamProvider{
			streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
				out := make(chan provider.Event, 2)
				out <- provider.Event{Type: provider.Text, Delta: "partial"}
				out <- provider.Event{Type: provider.Error, Err: errors.New(secretErr)}
				close(out)
				return out, nil
			},
		}
		handler, err := newRunnerHandler(testConfig(), mock)
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"hi"}`))
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 stream, got %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), secretErr) {
			t.Fatalf("response leaked internal error detail: %s", rec.Body.String())
		}
		events := decodeNDJSON(t, rec.Body.Bytes())
		if len(events) != 2 || events[1].Type != provider.Error || events[1].Error == "" {
			t.Fatalf("expected generic terminal error event, got %+v", events)
		}
	})

	t.Run("closed channel without terminal synthesizes error terminal", func(t *testing.T) {
		t.Parallel()
		mock := &mockStreamProvider{
			streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
				out := make(chan provider.Event, 1)
				out <- provider.Event{Type: provider.Text, Delta: "partial"}
				close(out)
				return out, nil
			},
		}
		handler, err := newRunnerHandler(testConfig(), mock)
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"hi"}`))
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		events := decodeNDJSON(t, rec.Body.Bytes())
		if len(events) != 2 || events[1].Type != provider.Error || events[1].Error == "" {
			t.Fatalf("expected synthesized terminal error event, got %+v", events)
		}
	})

	t.Run("provider Stream start error hides details", func(t *testing.T) {
		t.Parallel()
		secretErr := "exec: /usr/local/bin/secret-bin: permission denied"
		mock := &mockStreamProvider{
			streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
				return nil, errors.New(secretErr)
			},
		}
		handler, err := newRunnerHandler(testConfig(), mock)
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"hi"}`))
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK {
			t.Fatalf("expected non-200 status on start failure, got 200")
		}
		if strings.Contains(rec.Body.String(), secretErr) {
			t.Fatalf("response leaked internal start error: %s", rec.Body.String())
		}
	})
}

func TestModelsAndHistoryCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("stub models empty array and history 404", func(t *testing.T) {
		t.Parallel()
		handler, err := newRunnerHandler(testConfig(), provider.StubProvider{})
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, runnerwire.ModelsPath, nil)
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for stub models, got %d", rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("expected [] for stub models, got %q", rec.Body.String())
		}

		for _, path := range []string{runnerwire.HistoryPath, "/v1/history/chat-1"} {
			hReq := httptest.NewRequest(http.MethodGet, path, nil)
			hReq.Header.Set("Authorization", "Bearer "+testSecret)
			hRec := httptest.NewRecorder()
			handler.ServeHTTP(hRec, hReq)
			if hRec.Code != http.StatusNotFound {
				t.Fatalf("%s: expected 404 for unsupported history, got %d", path, hRec.Code)
			}
		}
	})

	t.Run("model lister without history", func(t *testing.T) {
		t.Parallel()
		prov := &mockModelsProvider{
			modelsFn: func(ctx context.Context) ([]provider.Model, error) {
				return []provider.Model{{ID: "m1", Name: "Model 1", IsDefault: true}}, nil
			},
		}
		handler, err := newRunnerHandler(testConfig(), prov)
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, runnerwire.ModelsPath, nil)
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var models []provider.Model
		if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
			t.Fatalf("unmarshal models: %v", err)
		}
		if len(models) != 1 || models[0].ID != "m1" || !models[0].IsDefault {
			t.Fatalf("unexpected models: %+v", models)
		}

		hReq := httptest.NewRequest(http.MethodGet, runnerwire.HistoryPath, nil)
		hReq.Header.Set("Authorization", "Bearer "+testSecret)
		hRec := httptest.NewRecorder()
		handler.ServeHTTP(hRec, hReq)
		if hRec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for unsupported history, got %d", hRec.Code)
		}
	})

	t.Run("full capabilities models and history", func(t *testing.T) {
		t.Parallel()
		var gotChatID string
		prov := &mockFullProvider{
			mockModelsProvider: mockModelsProvider{
				modelsFn: func(ctx context.Context) ([]provider.Model, error) {
					return []provider.Model{{ID: "codex-1", Name: "Codex 1", IsDefault: true}}, nil
				},
			},
			listChatsFn: func(ctx context.Context) ([]provider.IndexedChat, error) {
				return []provider.IndexedChat{{ID: "thread-9", Title: "Title", Model: "codex-1", CreatedAt: 1000, UpdatedAt: 2000}}, nil
			},
			readTurnsFn: func(ctx context.Context, id string) ([]provider.IndexedTurn, error) {
				gotChatID = id
				dur := int64(500)
				return []provider.IndexedTurn{{ID: "turn-1", Prompt: "hi", Answer: "hello", DurationMs: &dur, CreatedAt: 1500}}, nil
			},
		}
		handler, err := newRunnerHandler(testConfig(), prov)
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		cReq := httptest.NewRequest(http.MethodGet, runnerwire.HistoryPath, nil)
		cReq.Header.Set("Authorization", "Bearer "+testSecret)
		cRec := httptest.NewRecorder()
		handler.ServeHTTP(cRec, cReq)
		if cRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", cRec.Code)
		}
		var chats []provider.IndexedChat
		if err := json.Unmarshal(cRec.Body.Bytes(), &chats); err != nil {
			t.Fatalf("unmarshal chats: %v", err)
		}
		if len(chats) != 1 || chats[0].ID != "thread-9" {
			t.Fatalf("unexpected chats: %+v", chats)
		}

		tReq := httptest.NewRequest(http.MethodGet, "/v1/history/thread-9", nil)
		tReq.Header.Set("Authorization", "Bearer "+testSecret)
		tRec := httptest.NewRecorder()
		handler.ServeHTTP(tRec, tReq)
		if tRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", tRec.Code)
		}
		if gotChatID != "thread-9" {
			t.Fatalf("expected chat id thread-9, got %q", gotChatID)
		}
		var turns []provider.IndexedTurn
		if err := json.Unmarshal(tRec.Body.Bytes(), &turns); err != nil {
			t.Fatalf("unmarshal turns: %v", err)
		}
		if len(turns) != 1 || turns[0].ID != "turn-1" || turns[0].Answer != "hello" {
			t.Fatalf("unexpected turns: %+v", turns)
		}
	})

	t.Run("models error hides internal details", func(t *testing.T) {
		t.Parallel()
		secretErr := "app-server stderr: secret token leak"
		prov := &mockModelsProvider{
			modelsFn: func(ctx context.Context) ([]provider.Model, error) {
				return nil, errors.New(secretErr)
			},
		}
		handler, err := newRunnerHandler(testConfig(), prov)
		if err != nil {
			t.Fatalf("newRunnerHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, runnerwire.ModelsPath, nil)
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("expected non-200 status on models failure")
		}
		if strings.Contains(rec.Body.String(), secretErr) {
			t.Fatalf("models error leaked internal details: %s", rec.Body.String())
		}
	})
}

func TestConcurrencyAdmission(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})

	mock := &mockStreamProvider{
		streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
			out := make(chan provider.Event)
			go func() {
				defer close(out)
				select {
				case <-started:
				default:
					close(started)
				}
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
				select {
				case out <- provider.Event{Type: provider.Done}:
				case <-ctx.Done():
				}
			}()
			return out, nil
		},
	}

	cfg := testConfig()
	cfg.MaxConcurrentStreams = 1
	handler, err := newRunnerHandler(cfg, mock)
	if err != nil {
		t.Fatalf("newRunnerHandler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	firstDone := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"one"}`))
		req.Header.Set("Authorization", "Bearer "+testSecret)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			firstDone <- 0
			return
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		firstDone <- resp.StatusCode
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first stream did not start")
	}

	secondReq, _ := http.NewRequest(http.MethodPost, srv.URL+runnerwire.StreamPath, strings.NewReader(`{"user_id":"u1","prompt":"two"}`))
	secondReq.Header.Set("Authorization", "Bearer "+testSecret)
	secondResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(secondReq)
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when concurrent stream limit reached, got %d", secondResp.StatusCode)
	}

	close(release)
	if code := <-firstDone; code != http.StatusOK {
		t.Fatalf("expected first stream 200, got %d", code)
	}
}

func decodeNDJSON(t *testing.T, raw []byte) []runnerwire.Event {
	t.Helper()
	var events []runnerwire.Event
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev runnerwire.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal NDJSON line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan NDJSON: %v", err)
	}
	return events
}

func TestMultiProviderRegistryRoutingModelsProvidersAndCodexHistory(t *testing.T) {
	t.Parallel()

	claudeMock := &mockModelsProvider{
		mockStreamProvider: mockStreamProvider{
			streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
				out := make(chan provider.Event, 2)
				out <- provider.Event{Type: provider.Text, Delta: "from-claude"}
				out <- provider.Event{Type: provider.Done}
				close(out)
				return out, nil
			},
		},
		modelsFn: func(ctx context.Context) ([]provider.Model, error) {
			return []provider.Model{{ID: "claude-sonnet", Name: "Claude Sonnet", IsDefault: true}}, nil
		},
	}

	codexMock := &mockFullProvider{
		mockModelsProvider: mockModelsProvider{
			mockStreamProvider: mockStreamProvider{
				streamFn: func(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
					out := make(chan provider.Event, 2)
					out <- provider.Event{Type: provider.Text, Delta: "from-codex"}
					out <- provider.Event{Type: provider.Done}
					close(out)
					return out, nil
				},
			},
			modelsFn: func(ctx context.Context) ([]provider.Model, error) {
				return []provider.Model{{ID: "gpt-5-codex", Name: "GPT-5 Codex", IsDefault: true}}, nil
			},
		},
		listChatsFn: func(ctx context.Context) ([]provider.IndexedChat, error) {
			return []provider.IndexedChat{{ID: "codex-1", Title: "Codex Chat"}}, nil
		},
		readTurnsFn: func(ctx context.Context, id string) ([]provider.IndexedTurn, error) {
			return []provider.IndexedTurn{{ID: "t1", Prompt: "hi", Answer: "codex answer"}}, nil
		},
	}

	cfg := config{
		Addr:             ":0",
		SharedSecret:     testSecret,
		Provider:         "claude",
		DefaultProvider:  "claude",
		EnabledProviders: []string{"claude", "codex"},
	}
	handler, err := newRunnerRegistryHandler(cfg, map[string]provider.Provider{
		"claude": claudeMock,
		"codex":  codexMock,
	})
	if err != nil {
		t.Fatalf("newRunnerRegistryHandler: %v", err)
	}

	// /v1/providers returns enabled providers and default
	pReq := httptest.NewRequest(http.MethodGet, runnerwire.ProvidersPath, nil)
	pReq.Header.Set("Authorization", "Bearer "+testSecret)
	pRec := httptest.NewRecorder()
	handler.ServeHTTP(pRec, pReq)
	if pRec.Code != http.StatusOK {
		t.Fatalf("providers status = %d: %s", pRec.Code, pRec.Body.String())
	}
	var pResp runnerwire.ProvidersResponse
	if err := json.Unmarshal(pRec.Body.Bytes(), &pResp); err != nil {
		t.Fatal(err)
	}
	if pResp.DefaultProvider != "claude" || len(pResp.Providers) != 2 || pResp.Providers[0] != "claude" || pResp.Providers[1] != "codex" {
		t.Fatalf("unexpected /v1/providers response: %+v", pResp)
	}

	// /v1/models defaults to claude, ?provider=codex scopes to codex, unknown/disabled returns 400
	for _, tc := range []struct {
		query      string
		wantStatus int
		wantID     string
	}{
		{"", http.StatusOK, "claude-sonnet"},
		{"?provider=codex", http.StatusOK, "gpt-5-codex"},
		{"?provider=antigravity", http.StatusBadRequest, ""},
		{"?provider=paid_api", http.StatusBadRequest, ""},
	} {
		req := httptest.NewRequest(http.MethodGet, runnerwire.ModelsPath+tc.query, nil)
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus {
			t.Fatalf("models%s status = %d, want %d (%s)", tc.query, rec.Code, tc.wantStatus, rec.Body.String())
		}
		if tc.wantStatus == http.StatusOK {
			var models []provider.Model
			if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
				t.Fatal(err)
			}
			if len(models) != 1 || models[0].ID != tc.wantID {
				t.Fatalf("models%s = %+v, want %s", tc.query, models, tc.wantID)
			}
		}
	}

	// /v1/stream defaults to claude when provider omitted, routes to codex when specified
	for _, tc := range []struct {
		body      string
		wantDelta string
	}{
		{`{"user_id":"u1","prompt":"hi"}`, "from-claude"},
		{`{"user_id":"u1","prompt":"hi","provider":"codex"}`, "from-codex"},
	} {
		req := httptest.NewRequest(http.MethodPost, runnerwire.StreamPath, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("stream %s status = %d (%s)", tc.body, rec.Code, rec.Body.String())
		}
		events := decodeNDJSON(t, rec.Body.Bytes())
		if len(events) != 2 || events[0].Delta != tc.wantDelta {
			t.Fatalf("stream %s events = %+v, want delta %q", tc.body, events, tc.wantDelta)
		}
	}

	// /v1/history still resolves codex even though default is claude
	hReq := httptest.NewRequest(http.MethodGet, runnerwire.HistoryPath, nil)
	hReq.Header.Set("Authorization", "Bearer "+testSecret)
	hRec := httptest.NewRecorder()
	handler.ServeHTTP(hRec, hReq)
	if hRec.Code != http.StatusOK {
		t.Fatalf("history status = %d: %s", hRec.Code, hRec.Body.String())
	}
	var chats []provider.IndexedChat
	if err := json.Unmarshal(hRec.Body.Bytes(), &chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].ID != "codex-1" {
		t.Fatalf("unexpected history chats: %+v", chats)
	}
}
