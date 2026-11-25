package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-console/server/internal/config"
	"agent-console/server/internal/httpapi"
	"agent-console/server/internal/store"
)

func TestRESTAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	t.Setenv("DB_PATH", path)
	t.Setenv("PROVIDER", "stub")
	t.Setenv("CORS_ORIGIN", "http://127.0.0.1:1420")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, name := range []string{"alice", "bob"} {
		args := []string{"create-user", "--username", name, "--password", "fixture-password"}
		out := new(bytes.Buffer)
		if err := run(ctx, args, out, logger); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "fixture-password") {
			t.Fatal("password in CLI output")
		}
		if err := run(ctx, args, io.Discard, logger); !errors.Is(err, store.ErrUsernameTaken) {
			t.Fatalf("duplicate CLI user: %v", err)
		}
	}
	if err := run(ctx, []string{"create-user", "--username", "charlie", "--password", strings.Repeat("a", 73)}, io.Discard, logger); err == nil {
		t.Fatal("create-user accepted password beyond bcrypt limit")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	open := func() (*store.Store, http.Handler) {
		db, err := store.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		handler, err := httpapi.New(ctx, db, cfg, logger)
		if err != nil {
			t.Fatal(err)
		}
		return db, handler
	}
	db, handler := open()
	request := func(method, path, token, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w
	}

	if health := jsonBody[map[string]string](t, request("GET", "/api/health", "", "", 200)); health["provider"] != "stub" || health["status"] != "ok" {
		t.Fatalf("health response: %v", health)
	}
	request("GET", "/api/auth/me", "", "", 401)
	request("GET", "/api/models", "", "", 401)
	request("GET", "/api/chats", strings.Repeat("a", 64), "", 401)
	request("POST", "/api/auth/login", "", `{"username":"alice","password":"wrong","deviceName":"Laptop"}`, 401)
	request("POST", "/api/auth/login", "", `{"username":"charlie","password":"fixture-password","deviceName":"Laptop"}`, 401)
	request("POST", "/api/auth/login", "", `{"username":"alice","password":"fixture-password","deviceName":"Laptop"}{}`, 400)
	var login = func(name string) struct {
		User   store.User
		Device store.Device
		Token  string
	} {
		t.Helper()
		w := request("POST", "/api/auth/login", "", `{"username":"`+name+`","password":"fixture-password","deviceName":"Laptop"}`, 200)
		raw := jsonBody[map[string]json.RawMessage](t, w)
		if len(raw) != 3 || raw["user"] == nil || raw["device"] == nil || raw["token"] == nil || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "tokenHash") {
			t.Fatal("unexpected login response fields")
		}
		return jsonBody[struct {
			User   store.User
			Device store.Device
			Token  string
		}](t, w)
	}
	alice, bob := login("alice"), login("bob")
	if got := request("GET", "/api/models", alice.Token, "", 200); strings.TrimSpace(got.Body.String()) != "[]" {
		t.Fatal("stub returned models")
	}
	if alice.User.Username != "alice" || alice.Device.UserID != alice.User.ID || alice.Device.Name != "Laptop" || len(alice.Token) != 64 || alice.Token == bob.Token {
		t.Fatal("invalid login session")
	}
	request("POST", "/api/chats", alice.Token, `{"userId":"`+bob.User.ID+`"}`, 400)
	request("POST", "/api/chats", alice.Token, "null", 400)
	request("POST", "/api/chats", alice.Token, "["+strings.Repeat(" ", 9000), 413)
	chat := jsonBody[store.Chat](t, request("POST", "/api/chats", alice.Token, `{"title":"Первый чат","model":"sample"}`, 201))
	other := jsonBody[store.Chat](t, request("POST", "/api/chats", alice.Token, `{}`, 201))
	if chat.Title != "Первый чат" || chat.Model != "sample" || chat.Provider != "stub" || other.Title != "Новый чат" || other.Model != "default" {
		t.Fatalf("unexpected created chats: %v, %v", chat, other)
	}
	empty := request("GET", "/api/chats/"+chat.ID+"/messages", alice.Token, "", 200)
	if strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatal("expected empty array for new chat")
	}
	request("GET", "/api/chats/"+chat.ID+"/messages", bob.Token, "", 404)
	request("GET", "/api/chats/unknown/messages", alice.Token, "", 404)
	if got := request("GET", "/api/chats", bob.Token, "", 200); strings.TrimSpace(got.Body.String()) != "[]" {
		t.Fatal("chat list leaked another user's chat")
	}

	// Seed messages at the database boundary. Step 2 will produce these rows through the stream.
	seeder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()
	want := []store.Message{
		{ID: "turn-user", ChatID: chat.ID, Role: "user", Content: "Первый запрос", CreatedAt: 123},
		{ID: "turn-assistant", ChatID: chat.ID, Role: "assistant", Content: "Готово", Thinking: "Думаю", CreatedAt: 123},
	}
	for _, m := range want {
		if _, err := seeder.Exec(`INSERT INTO chat_messages (id,chat_id,role,content,thinking,created_at) VALUES (?,?,?,?,?,?)`,
			m.ID, m.ChatID, m.Role, m.Content, m.Thinking, m.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seeder.Exec("UPDATE chats SET updated_at=? WHERE id=?", other.UpdatedAt+1000, chat.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, handler = open()

	me := jsonBody[struct {
		User   store.User
		Device store.Device
	}](t, request("GET", "/api/auth/me", alice.Token, "", 200))
	if me.User != alice.User || me.Device.ID != alice.Device.ID || me.Device.LastSeenAt < alice.Device.LastSeenAt {
		t.Fatal("session did not survive restart")
	}
	chats := jsonBody[[]store.Chat](t, request("GET", "/api/chats", alice.Token, "", 200))
	if len(chats) != 2 || chats[0].ID != chat.ID || chats[1].ID != other.ID {
		t.Fatalf("incorrect chat ordering: %v", chats)
	}
	history := jsonBody[[]store.Message](t, request("GET", "/api/chats/"+chat.ID+"/messages", alice.Token, "", 200))
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history after restart: %v, want %v", history, want)
	}
}

func TestCORS(t *testing.T) {
	const origin = "http://127.0.0.1:1420"
	for _, tc := range []struct {
		allowed string
		origin  string
		status  int
	}{
		{origin, origin, 204}, {"*", "https://test.example", 204}, {origin, "https://other.example", 403},
	} {
		api, err := httpapi.New(t.Context(), nil, config.Config{Provider: "stub", CORSOrigin: tc.allowed}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("OPTIONS", "/api/chats", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status: got %d, want %d", w.Code, tc.status)
		}
		if w.Code == 204 && (w.Header().Get("Access-Control-Allow-Origin") != tc.allowed ||
			!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization")) {
			t.Fatalf("invalid CORS headers: %v", w.Header())
		}
	}
}

func jsonBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
