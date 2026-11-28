package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
)

func TestAdminAPIProvidersMaskingAndNonAdmin404(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "paid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	adminUser, err := db.CreateUserWithRole(ctx, "admin", []byte("h"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	regUser, err := db.CreateUserWithRole(ctx, "alice", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	_, tokAdmin, err := db.IssueDevice(ctx, adminUser.ID, "adm")
	if err != nil {
		t.Fatal(err)
	}
	_, tokUser, err := db.IssueDevice(ctx, regUser.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}

	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*", APIKeysSecret: "test-secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	req := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// Non-admin must get 404 on /api/admin/api-providers
	if w := req(http.MethodGet, "/api/admin/api-providers", tokUser, ""); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-admin, got %d", w.Code)
	}

	// Admin creates provider -> response must mask API key and never contain plaintext key
	secretKey := "sk-live-supersecret9876"
	createRes := req(http.MethodPost, "/api/admin/api-providers", tokAdmin,
		`{"name":"DeepSeek","baseUrl":"https://api.deepseek.com/v1","apiKey":"`+secretKey+`","models":[{"id":"deepseek-chat","label":"DeepSeek V3","inputPricePer1M":0.27,"outputPricePer1M":1.1,"supportsVision":false,"supportsReasoning":false}],"enabled":true}`)
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create api provider status=%d body=%s", createRes.Code, createRes.Body.String())
	}
	if strings.Contains(createRes.Body.String(), secretKey) {
		t.Fatalf("plaintext api key leaked in create response: %s", createRes.Body.String())
	}
	var created store.PaidAPIConfig
	_ = json.NewDecoder(createRes.Body).Decode(&created)
	if !created.HasAPIKey || created.APIKeyMask != "sk-••••9876" {
		t.Fatalf("unexpected mask: has=%v mask=%q", created.HasAPIKey, created.APIKeyMask)
	}

	// GET /api/admin/api-providers must also mask key
	listRes := req(http.MethodGet, "/api/admin/api-providers", tokAdmin, "")
	if listRes.Code != http.StatusOK || strings.Contains(listRes.Body.String(), secretKey) {
		t.Fatalf("list api providers failed or leaked key: status=%d body=%s", listRes.Code, listRes.Body.String())
	}

	// Reject internal baseUrl (SSRF)
	if w := req(http.MethodPost, "/api/admin/api-providers", tokAdmin,
		`{"name":"Internal","baseUrl":"https://127.0.0.1:8443/v1","apiKey":"sk-123456","models":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for internal baseUrl, got %d", w.Code)
	}

	// Delete provider
	if w := req(http.MethodDelete, "/api/admin/api-providers/"+created.ID, tokAdmin, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete api provider status=%d", w.Code)
	}
}

func TestTelegramNotificationTriggers(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	alice, err := db.CreateUserWithRole(ctx, "alice", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.CreateUserWithRole(ctx, "bob", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateUserTelegramID(ctx, alice.ID, "123456789"); err != nil {
		t.Fatal(err)
	}
	tgOn := json.RawMessage(`{"telegramNotify":true}`)
	if _, err := db.UpdateUserProfileWithUI(ctx, alice.ID, nil, nil, nil, nil, &tgOn); err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, alice.ID, "Важный чат", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var sentBodies []string
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sentBodies = append(sentBodies, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tgSrv.Close()

	api := &API{
		db:                 db,
		live:               newLiveRunsWithProviders(ctx, "stub", nil, 1),
		config:             config.Config{TelegramBotToken: "123:test-token", PublicURL: "https://example.ts.net"},
		telegramAPIBaseURL: tgSrv.URL,
		telegramHTTPClient: tgSrv.Client(),
	}

	// 1. Foreign caller (bob != alice) must NOT trigger notification
	api.maybeNotifyTelegram(alice.ID, bob.ID, chat.ID, "sonnet", "secret", 0, 15000, false)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(sentBodies) != 0 {
		t.Fatalf("expected 0 telegram notifications for foreign caller, got %d", len(sentBodies))
	}
	mu.Unlock()

	// 2. Owner with closed WS sends notification with preview + web_app button
	api.maybeNotifyTelegram(alice.ID, alice.ID, chat.ID, "sonnet", "Ответ готов!", 1, 1500, false)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sentBodies)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sentBodies) != 1 {
		t.Fatalf("expected 1 telegram notification, got %d", len(sentBodies))
	}
	if !strings.Contains(sentBodies[0], `"chat_id":"123456789"`) ||
		!strings.Contains(sentBodies[0], "Важный чат") ||
		!strings.Contains(sentBodies[0], "https://example.ts.net/?chat="+chat.ID) {
		t.Fatalf("unexpected telegram payload: %s", sentBodies[0])
	}
}

func TestTelegramTestNotificationEndpoint(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tg_test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	alice, err := db.CreateUserWithRole(ctx, "alice", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := db.IssueDevice(ctx, alice.ID, "mac")
	if err != nil {
		t.Fatal(err)
	}

	var sentCount int
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tgSrv.Close()

	h, err := New(ctx, db, config.Config{
		Provider:         "stub",
		CORSOrigin:       "*",
		TelegramBotToken: "123:test-token",
		PublicURL:        "https://example.ts.net",
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// Inject mock Bot API on the underlying *API if needed via direct handler call for missing telegramId first.
	reqMissing := httptest.NewRequest(http.MethodPost, "/api/profile/telegram-test", nil)
	reqMissing.Header.Set("Authorization", "Bearer "+token)
	recMissing := httptest.NewRecorder()
	h.ServeHTTP(recMissing, reqMissing)
	if recMissing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when telegramId missing, got %d (%s)", recMissing.Code, recMissing.Body.String())
	}

	if err := db.UpdateUserTelegramID(ctx, alice.ID, "123456789"); err != nil {
		t.Fatal(err)
	}
	api := &API{
		db:                 db,
		live:               newLiveRunsWithProviders(ctx, "stub", nil, 1),
		config:             config.Config{TelegramBotToken: "123:test-token", PublicURL: "https://example.ts.net"},
		telegramAPIBaseURL: tgSrv.URL,
		telegramHTTPClient: tgSrv.Client(),
	}
	reqOK := httptest.NewRequest(http.MethodPost, "/api/profile/telegram-test", nil)
	recOK := httptest.NewRecorder()
	api.testTelegramNotify(recOK, reqOK, session{User: store.User{ID: alice.ID, Username: alice.Username, Role: alice.Role}})
	if recOK.Code != http.StatusOK || sentCount != 1 {
		t.Fatalf("expected 200 and 1 telegram call, got %d count=%d body=%s", recOK.Code, sentCount, recOK.Body.String())
	}
}

func TestPatchChatToPaidAPIAndGetPaidAPIModels(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "paid_switch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	alice, err := db.CreateUserWithRole(ctx, "alice", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := db.IssueDevice(ctx, alice.ID, "mac")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.CreatePaidAPIConfig(ctx, "Munito", "https://api.munito.ai/inference/v1", "sk-test-1234", []store.PaidAPIModel{
		{ID: "deepseek-chat"},
		{ID: "gpt-4o-mini"},
	}, true, "test-secret"); err != nil {
		t.Fatalf("CreatePaidAPIConfig: %v", err)
	}

	h, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. GET /api/models?provider=paid_api returns enabled models
	reqModels := httptest.NewRequest(http.MethodGet, "/api/models?provider=paid_api", nil)
	reqModels.Header.Set("Authorization", "Bearer "+token)
	recModels := httptest.NewRecorder()
	h.ServeHTTP(recModels, reqModels)
	if recModels.Code != http.StatusOK {
		t.Fatalf("GET /api/models?provider=paid_api status = %d, body = %s", recModels.Code, recModels.Body.String())
	}
	if !strings.Contains(recModels.Body.String(), `"deepseek-chat"`) || !strings.Contains(recModels.Body.String(), `"gpt-4o-mini"`) {
		t.Fatalf("unexpected paid_api models response: %s", recModels.Body.String())
	}

	// 2. POST /api/chats with provider=paid_api succeeds when enabled Paid API config exists
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/chats", strings.NewReader(`{"title":"Paid","provider":"paid_api","model":"deepseek-chat"}`))
	reqCreate.Header.Set("Authorization", "Bearer "+token)
	reqCreate.Header.Set("Content-Type", "application/json")
	recCreate := httptest.NewRecorder()
	h.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated && recCreate.Code != http.StatusOK {
		t.Fatalf("POST /api/chats status = %d, body = %s", recCreate.Code, recCreate.Body.String())
	}
	if !strings.Contains(recCreate.Body.String(), `"provider":"paid_api"`) {
		t.Fatalf("expected chat provider=paid_api, got %s", recCreate.Body.String())
	}
}

func TestPaidAPIStreamDirectInCore(t *testing.T) {
	ctx := t.Context()
	var gotAuth string
	var gotModel string

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "paid_ws.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.CreatePaidAPIConfig(ctx, "Munito", "https://api.example.com/v1", "sk-secret-key", []store.PaidAPIModel{
		{ID: "deepseek-chat"},
	}, true, "test-secret"); err != nil {
		t.Fatal(err)
	}

	backend := provider.NewOpenAICompatProvider(&paidAPIResolver{db: db, secret: "test-secret"})
	backend.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &req)
			gotModel = req.Model
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"Привет\"}}]}\n\ndata: [DONE]\n\n")),
			}, nil
		}),
	}
	events, err := backend.Stream(ctx, provider.Request{
		Model:  "deepseek-chat",
		Prompt: "Привет",
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	var text strings.Builder
	var sawDone bool
	for ev := range events {
		switch ev.Type {
		case provider.Text:
			text.WriteString(ev.Delta)
		case provider.Done:
			sawDone = true
		case provider.Error:
			t.Fatalf("unexpected provider error: %v", ev.Err)
		}
	}
	if !sawDone || text.String() != "Привет" {
		t.Fatalf("expected done=true text=Привет, got done=%v text=%q", sawDone, text.String())
	}
	if gotAuth != "Bearer sk-secret-key" || gotModel != "deepseek-chat" {
		t.Fatalf("unexpected upstream request auth=%q model=%q", gotAuth, gotModel)
	}
}

func TestResolvePaidAPIModelSlashAndStaleID(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "paid_resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.CreatePaidAPIConfig(ctx, "Munito", "https://api.example.com/v1", "sk-secret-key", []store.PaidAPIModel{
		{ID: "deepseek-ai/deepseek-v4.1-flash-1017"},
	}, true, "test-secret"); err != nil {
		t.Fatal(err)
	}

	_, m, key, err := db.ResolvePaidAPIModel(ctx, "deepseek-ai/deepseek-v4.1-flash-1017", "test-secret")
	if err != nil {
		t.Fatalf("ResolvePaidAPIModel with slash failed: %v", err)
	}
	if m.ID != "deepseek-ai/deepseek-v4.1-flash-1017" || key != "sk-secret-key" {
		t.Fatalf("unexpected resolved model=%+v key=%q", m, key)
	}

	_, _, _, err = db.ResolvePaidAPIModel(ctx, "stale-deleted-model", "test-secret")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for stale model ID, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
