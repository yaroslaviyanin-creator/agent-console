package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-console/server/internal/runnerwire"
)

const testRunnerSecret = "0123456789abcdef0123456789abcdef"

func TestNewRemoteRunnerProviderValidation(t *testing.T) {
	validURLs := []string{
		"http://127.0.0.1:8080",
		"https://runner.example.com",
		"https://runner.example.com/base/path/",
	}
	for _, rawURL := range validURLs {
		p, err := NewRemoteRunnerProvider(rawURL, testRunnerSecret)
		if err != nil || p == nil {
			t.Fatalf("NewRemoteRunnerProvider(%q) failed: %v", rawURL, err)
		}
	}

	invalidURLs := []string{
		"",
		"://bad",
		"ftp://runner.example.com",
		"http://",
		"http://:8080",
		"/relative/path",
		"http://user:pass@runner.example.com",
		"http://user@runner.example.com",
		"http://runner.example.com?a=1",
		"http://runner.example.com?",
		"http://runner.example.com#frag",
		"http://runner.example.com#",
	}
	for _, rawURL := range invalidURLs {
		if _, err := NewRemoteRunnerProvider(rawURL, testRunnerSecret); err == nil {
			t.Fatalf("expected invalid URL error for %q", rawURL)
		}
	}

	invalidSecrets := []string{
		"",
		strings.Repeat("a", runnerwire.MinSecretBytes-1),
		strings.Repeat("a", runnerwire.MinSecretBytes) + "\n",
		strings.Repeat("a", runnerwire.MinSecretBytes) + "\r\nX-Injected: 1",
		strings.Repeat("a", runnerwire.MinSecretBytes) + "\x00",
		strings.Repeat(" ", runnerwire.MinSecretBytes),
		" " + strings.Repeat("a", runnerwire.MinSecretBytes),
		strings.Repeat("a", runnerwire.MinSecretBytes) + "\t",
		strings.Repeat("б", runnerwire.MinSecretBytes),
	}
	for _, secret := range invalidSecrets {
		if _, err := NewRemoteRunnerProvider("http://127.0.0.1:8080", secret); err == nil {
			t.Fatalf("expected invalid secret error for %q", secret)
		}
	}
}

func TestRemoteRunnerStreamRoundtripDeltaUsageSession(t *testing.T) {
	releaseDone := make(chan struct{})
	var gotMethod, gotPath, gotAuth, gotContentType, gotAccept string
	var gotRawBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		var err error
		gotRawBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}

		w.Header().Set("Content-Type", runnerwire.StreamType)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		_, _ = io.WriteString(w, `{"type":"thinking_delta","delta":"Plan"}`+"\n")
		_, _ = io.WriteString(w, `{"type":"text_delta","delta":"Hello"}`+"\n")
		if flusher != nil {
			flusher.Flush()
		}

		<-releaseDone

		_, _ = io.WriteString(w, `{"type":"done","session_id":"`+testSessionID+`","usage":{"input_tokens":12,"cached_input_tokens":4,"output_tokens":7}}`+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p, err := NewRemoteRunnerProvider(srv.URL+"/runner", testRunnerSecret)
	if err != nil {
		t.Fatal(err)
	}

	req := Request{
		UserID:             "user-123",
		Model:              "gpt-5",
		Reasoning:          "high",
		Prompt:             "Say hello",
		SessionID:          testSessionID,
		SystemInstructions: "Only chat",
		Ephemeral:          true,
	}
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	first := <-ch
	second := <-ch
	if first.Type != Thinking || first.Delta != "Plan" {
		t.Fatalf("unexpected first event before Done released: %+v", first)
	}
	if second.Type != Text || second.Delta != "Hello" {
		t.Fatalf("unexpected second event before Done released: %+v", second)
	}

	// Ensure no third event is emitted until the server releases Done.
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event before Done released: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseDone)

	doneEv, ok := <-ch
	if !ok {
		t.Fatal("channel closed before Done event")
	}
	wantUsage := Usage{InputTokens: 12, CachedInputTokens: 4, OutputTokens: 7}
	if doneEv.Type != Done || doneEv.SessionID != testSessionID || doneEv.Usage == nil || *doneEv.Usage != wantUsage {
		t.Fatalf("unexpected Done event: %+v", doneEv)
	}
	if extra, ok := <-ch; ok {
		t.Fatalf("expected closed channel after Done, got %+v", extra)
	}

	if gotMethod != http.MethodPost || gotPath != "/runner"+runnerwire.StreamPath {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+testRunnerSecret {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" || gotAccept != runnerwire.StreamType {
		t.Fatalf("headers content-type=%q accept=%q", gotContentType, gotAccept)
	}
	var wireReq map[string]any
	if err := json.Unmarshal(gotRawBody, &wireReq); err != nil {
		t.Fatalf("unmarshal wire request: %v", err)
	}
	if _, hasMCP := wireReq["mcp_endpoint"]; hasMCP {
		t.Fatalf("mcp_endpoint must not be sent: %s", gotRawBody)
	}
	var decoded runnerwire.Request
	if err := json.Unmarshal(gotRawBody, &decoded); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	wantWire := runnerwire.Request{
		UserID:             req.UserID,
		Model:              req.Model,
		Reasoning:          req.Reasoning,
		Prompt:             req.Prompt,
		SessionID:          req.SessionID,
		SystemInstructions: req.SystemInstructions,
		Ephemeral:          req.Ephemeral,
	}
	if decoded != wantWire {
		t.Fatalf("wire request = %+v, want %+v", decoded, wantWire)
	}
}

func TestRemoteRunnerStreamCancellationObservedByServer(t *testing.T) {
	serverCanceled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", runnerwire.StreamType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"text_delta","delta":"partial"}`+"\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(serverCanceled)
	}))
	defer srv.Close()

	p, err := NewRemoteRunnerProvider(srv.URL, testRunnerSecret)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := p.Stream(ctx, Request{UserID: "u1", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	ev, ok := <-ch
	if !ok || ev.Type != Text || ev.Delta != "partial" {
		t.Fatalf("expected partial delta, got (%+v, %v)", ev, ok)
	}

	cancel()

	select {
	case _, stillOpen := <-ch:
		if stillOpen {
			// Drain at most nothing; on cancellation channel should close without trailing errors.
			t.Fatal("expected stream channel to close promptly after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Stream channel to close after cancel")
	}

	select {
	case <-serverCanceled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for httptest server to observe cancellation")
	}
}

func TestRemoteRunnerStreamErrorsAndMalformed(t *testing.T) {
	t.Run("non-200 status omits server body and secret", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "SENSITIVE_UPSTREAM_DIAGNOSTIC "+testRunnerSecret)
		}))
		defer srv.Close()

		p, err := NewRemoteRunnerProvider(srv.URL, testRunnerSecret)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := p.Stream(context.Background(), Request{UserID: "u1", Prompt: "hi"})
		if err == nil || ch != nil {
			t.Fatalf("expected error and nil channel, got ch=%v err=%v", ch, err)
		}
		if strings.Contains(err.Error(), "SENSITIVE_UPSTREAM_DIAGNOSTIC") || strings.Contains(err.Error(), testRunnerSecret) {
			t.Fatalf("error leaked body or secret: %v", err)
		}
	})

	cases := []struct {
		name string
		body string
	}{
		{"empty truncation", ""},
		{"eof after delta", `{"type":"text_delta","delta":"partial"}` + "\n"},
		{"malformed json", "{not-json\n"},
		{"unknown event", `{"type":"tool_call"}` + "\n"},
		{"oversized event", `{"type":"text_delta","delta":"` + strings.Repeat("x", runnerwire.MaxEventBytes) + `"}` + "\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", runnerwire.StreamType)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			p, err := NewRemoteRunnerProvider(srv.URL, testRunnerSecret)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := p.Stream(context.Background(), Request{UserID: "u1", Prompt: "hi"})
			if err != nil {
				t.Fatalf("unexpected Stream setup error: %v", err)
			}
			var events []Event
			for ev := range ch {
				events = append(events, ev)
			}
			if len(events) == 0 {
				t.Fatal("expected terminal error event")
			}
			last := events[len(events)-1]
			if last.Type != Error || last.Err == nil {
				t.Fatalf("expected terminal Error event, got %+v", last)
			}
			if strings.Contains(last.Err.Error(), testRunnerSecret) {
				t.Fatalf("error leaked secret: %v", last.Err)
			}
		})
	}

	t.Run("error event from runner", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", runnerwire.StreamType)
			_, _ = io.WriteString(w, `{"type":"error","error":"provider unavailable"}`+"\n")
		}))
		defer srv.Close()

		p, err := NewRemoteRunnerProvider(srv.URL, testRunnerSecret)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := p.Stream(context.Background(), Request{UserID: "u1", Prompt: "hi"})
		if err != nil {
			t.Fatalf("unexpected Stream error: %v", err)
		}
		ev := <-ch
		if ev.Type != Error || ev.Err == nil || ev.Err.Error() != "provider unavailable" {
			t.Fatalf("unexpected error event: %+v", ev)
		}
		if _, ok := <-ch; ok {
			t.Fatal("expected closed channel after error event")
		}
	})
}

func TestRemoteRunnerRedirectAndNoProxy(t *testing.T) {
	var redirectHits atomic.Int32
	var redirectAuth atomic.Value
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectHits.Add(1)
		redirectAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	var originHits atomic.Int32
	var originAuth atomic.Value
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		originAuth.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, redirectTarget.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer proxySrv.Close()
	t.Setenv("HTTP_PROXY", proxySrv.URL)
	t.Setenv("HTTPS_PROXY", proxySrv.URL)

	p, err := NewRemoteRunnerProvider(origin.URL, testRunnerSecret)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := p.Stream(context.Background(), Request{UserID: "u1", Prompt: "hi"}); err == nil {
		t.Fatal("expected error on redirect from Stream")
	}
	if _, err := p.ListModels(context.Background()); err == nil {
		t.Fatal("expected error on redirect from ListModels")
	}
	if _, err := p.ListIndexedChats(context.Background()); err == nil {
		t.Fatal("expected error on redirect from ListIndexedChats")
	}
	if _, err := p.ReadIndexedTurns(context.Background(), testSessionID); err == nil {
		t.Fatal("expected error on redirect from ReadIndexedTurns")
	}

	if originHits.Load() != 4 {
		t.Fatalf("expected 4 origin hits, got %d", originHits.Load())
	}
	if got, _ := originAuth.Load().(string); got != "Bearer "+testRunnerSecret {
		t.Fatalf("origin auth = %q", got)
	}
	if redirectHits.Load() != 0 {
		t.Fatalf("redirect target should not be requested, got %d hits (auth=%v)", redirectHits.Load(), redirectAuth.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("environment proxy should not be used, got %d hits", proxyHits.Load())
	}
}

func TestRemoteRunnerListModelsAndHistory(t *testing.T) {
	wantModels := []Model{
		{
			ID:               "gpt-5",
			Name:             "GPT-5",
			IsDefault:        true,
			ReasoningEfforts: []string{"low", "medium", "high"},
			DefaultReasoning: "medium",
		},
	}
	wantChats := []IndexedChat{
		{
			ID:        testSessionID,
			Title:     "Test chat",
			Model:     "gpt-5",
			CreatedAt: 1000,
			UpdatedAt: 2000,
			Archived:  false,
		},
	}
	dur := int64(450)
	wantTurns := []IndexedTurn{
		{
			ID:          "turn-1",
			Prompt:      "hello",
			Attachments: []IndexedAttachment{{Kind: "file", Name: "note.txt", Path: "/tmp/note.txt"}},
			Answer:      "hi",
			Progress:    "thinking",
			DurationMs:  &dur,
			CreatedAt:   1500,
		},
	}

	var emptyMode atomic.Bool
	var invalidTurnRequests atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testRunnerSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/base" + runnerwire.ModelsPath:
			if emptyMode.Load() {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode(wantModels)
		case "/base" + runnerwire.HistoryPath:
			if emptyMode.Load() {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode(wantChats)
		case "/base" + runnerwire.HistoryPath + "/" + testSessionID:
			if emptyMode.Load() {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode(wantTurns)
		default:
			invalidTurnRequests.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p, err := NewRemoteRunnerProvider(srv.URL+"/base", testRunnerSecret)
	if err != nil {
		t.Fatal(err)
	}

	models, err := p.ListModels(context.Background())
	if err != nil || !reflect.DeepEqual(models, wantModels) {
		t.Fatalf("ListModels = (%#v, %v), want %#v", models, err, wantModels)
	}
	chats, err := p.ListIndexedChats(context.Background())
	if err != nil || !reflect.DeepEqual(chats, wantChats) {
		t.Fatalf("ListIndexedChats = (%#v, %v), want %#v", chats, err, wantChats)
	}
	turns, err := p.ReadIndexedTurns(context.Background(), testSessionID)
	if err != nil || !reflect.DeepEqual(turns, wantTurns) {
		t.Fatalf("ReadIndexedTurns = (%#v, %v), want %#v", turns, err, wantTurns)
	}

	emptyMode.Store(true)
	emptyModels, err := p.ListModels(context.Background())
	if err != nil || emptyModels == nil || len(emptyModels) != 0 {
		t.Fatalf("expected non-nil empty []Model, got (%#v, %v)", emptyModels, err)
	}
	emptyChats, err := p.ListIndexedChats(context.Background())
	if err != nil || emptyChats == nil || len(emptyChats) != 0 {
		t.Fatalf("expected non-nil empty []IndexedChat, got (%#v, %v)", emptyChats, err)
	}
	emptyTurns, err := p.ReadIndexedTurns(context.Background(), testSessionID)
	if err != nil || emptyTurns == nil || len(emptyTurns) != 0 {
		t.Fatalf("expected non-nil empty []IndexedTurn, got (%#v, %v)", emptyTurns, err)
	}

	for _, badID := range []string{"", "not-a-uuid", "../models", testSessionID + "/extra"} {
		turns, err := p.ReadIndexedTurns(context.Background(), badID)
		if err == nil || turns != nil {
			t.Fatalf("expected error for invalid chat ID %q, got (%#v, %v)", badID, turns, err)
		}
	}
	if invalidTurnRequests.Load() != 0 {
		t.Fatalf("invalid chat IDs should not hit server, got %d requests", invalidTurnRequests.Load())
	}

	t.Run("non-200 and malformed and oversized GET replies", func(t *testing.T) {
		mode := "non200"
		errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch mode {
			case "non200":
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "PRIVATE_ERROR_BODY "+testRunnerSecret)
			case "malformed":
				_, _ = io.WriteString(w, `{"not":"an array"}`)
			case "oversized":
				_, _ = w.Write(bytes.Repeat([]byte(" "), runnerwire.MaxReplyBytes+1))
			}
		}))
		defer errSrv.Close()

		ep, err := NewRemoteRunnerProvider(errSrv.URL, testRunnerSecret)
		if err != nil {
			t.Fatal(err)
		}

		for _, m := range []string{"non200", "malformed", "oversized"} {
			mode = m
			got, err := ep.ListModels(context.Background())
			if err == nil || got != nil {
				t.Fatalf("%s: expected nil and error, got (%#v, %v)", m, got, err)
			}
			if strings.Contains(err.Error(), "PRIVATE_ERROR_BODY") || strings.Contains(err.Error(), testRunnerSecret) {
				t.Fatalf("%s: error leaked body or secret: %v", m, err)
			}
		}
	})
}

func TestRemoteRunnerContextDeadlineCancellationOnGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	p, err := NewRemoteRunnerProvider(srv.URL, testRunnerSecret)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	models, err := p.ListModels(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || models != nil {
		t.Fatalf("expected context.DeadlineExceeded and nil slice, got (%#v, %v)", models, err)
	}
}

func TestRemoteRunnerForProviderAndListProviders(t *testing.T) {
	var gotStreamBody []byte
	var gotModelsPath, gotModelsQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testRunnerSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/base" + runnerwire.StreamPath:
			var err error
			gotStreamBody, err = io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read stream body: %v", err)
				return
			}
			w.Header().Set("Content-Type", runnerwire.StreamType)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"type":"done","session_id":"s1"}`+"\n")
		case "/base" + runnerwire.ModelsPath:
			gotModelsPath = r.URL.Path
			gotModelsQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]Model{{ID: "claude-sonnet", Name: "Claude Sonnet", IsDefault: true}})
		case "/base" + runnerwire.ProvidersPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(runnerwire.ProvidersResponse{
				DefaultProvider: "codex",
				Providers:       []string{"codex", "claude"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	base, err := NewRemoteRunnerProvider(srv.URL+"/base", testRunnerSecret)
	if err != nil {
		t.Fatal(err)
	}
	bound := base.ForProvider("claude")

	ch, err := bound.Stream(context.Background(), Request{UserID: "u1", Prompt: "hi", Model: "claude-sonnet"})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	for range ch {
	}

	var wireReq runnerwire.Request
	if err := json.Unmarshal(gotStreamBody, &wireReq); err != nil {
		t.Fatalf("unmarshal stream request: %v", err)
	}
	if wireReq.Provider != "claude" {
		t.Fatalf("expected wire provider claude, got %q", wireReq.Provider)
	}

	models, err := bound.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "claude-sonnet" {
		t.Fatalf("bound ListModels = (%#v, %v)", models, err)
	}
	if gotModelsPath != "/base"+runnerwire.ModelsPath || gotModelsQuery != "provider=claude" {
		t.Fatalf("models request = %s?%s", gotModelsPath, gotModelsQuery)
	}

	provs, err := base.ListProviders(context.Background())
	if err != nil || provs.DefaultProvider != "codex" || !reflect.DeepEqual(provs.Providers, []string{"codex", "claude"}) {
		t.Fatalf("ListProviders = (%+v, %v)", provs, err)
	}
}
