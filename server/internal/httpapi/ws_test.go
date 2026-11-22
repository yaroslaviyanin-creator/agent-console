package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"github.com/gorilla/websocket"
)

func TestWebSocketChatLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	userToken := func(name string) (string, string) {
		t.Helper()
		if err := db.CreateUser(t.Context(), name, []byte("test")); err != nil {
			t.Fatal(err)
		}
		user, _, err := db.Credentials(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := db.IssueDevice(t.Context(), user.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		return user.ID, token
	}
	alice, token := userToken("alice")
	_, bobToken := userToken("bob")
	_, secondToken, err := db.IssueDevice(t.Context(), alice, "second device")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(t.Context(), alice, "Title", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateChat(t.Context(), alice, "Independent", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Provider: "stub", CORSOrigin: "http://localhost:1420"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gate := make(chan struct{})
	handler := newHandler(ctx, db, cfg, logger, gatedStub{first: gate})
	server := httptest.NewServer(handler)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token="
	dial := func(token, origin string, status int) *websocket.Conn {
		t.Helper()
		headers := http.Header{"Origin": []string{origin}}
		c, res, err := websocket.DefaultDialer.Dial(wsURL+url.QueryEscape(token), headers)
		if c != nil {
			t.Cleanup(func() { c.Close() })
		}
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		if res == nil || res.StatusCode != status || (status == 101 && err != nil) {
			t.Fatalf("dial response %v, error %v", res, err)
		}
		return c
	}
	dial("", cfg.CORSOrigin, 401)
	dial(token, "https://other.example", 403)
	writer := dial(token, cfg.CORSOrigin, 101)
	watcher := dial(secondToken, cfg.CORSOrigin, 101)
	bob := dial(bobToken, cfg.CORSOrigin, 101)
	write := func(c *websocket.Conn, chatID, messageID, mode string) {
		t.Helper()
		if err := c.WriteJSON(map[string]any{
			"chatId": chatID, "messageId": messageID, "prompt": "Привет",
			"model":  map[string]string{"id": "codex", "provider": "codex"},
			"target": map[string]string{"mode": mode}, "history": []any{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *websocket.Conn) runEvent {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var event runEvent
		if err := c.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	expectError := func(event runEvent, id, code string, retryable bool) {
		t.Helper()
		if event.Type != "error" || event.ChatID != chat.ID || event.MessageID != id || event.Code != code ||
			event.Seq != 1 || event.Message == "" || event.Retryable == nil || *event.Retryable != retryable || event.Delta != "" || event.Reason != "" {
			t.Fatalf("incorrect error envelope: %#v", event)
		}
	}
	write(writer, chat.ID, "mcp", "mcp")
	expectError(read(writer), "mcp", "invalid_request", false)
	write(writer, chat.ID, "answer-1", "chat")
	first := read(writer)
	if first.Type != "thinking_delta" || first.Seq != 1 || first.Delta == "" {
		t.Fatalf("first event: %#v", first)
	}
	if got := read(watcher); !reflect.DeepEqual(first, got) {
		t.Fatalf("different first frame on second device: %#v", got)
	}
	write(bob, chat.ID, "foreign", "chat")
	expectError(read(bob), "foreign", "chat_not_found", false)
	write(watcher, chat.ID, "too-soon", "chat")
	expectError(read(watcher), "too-soon", "chat_busy", true)

	// The prompt is present even while the assistant is still streaming.
	messages, err := db.Messages(t.Context(), alice, chat.ID)
	if err != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("missing in-flight prompt: %v %v", messages, err)
	}
	close(gate)
	for seq := 2; seq <= 5; seq++ {
		got := read(writer)
		if got.MessageID != "answer-1" || got.ChatID != chat.ID || got.Seq != seq || !reflect.DeepEqual(got, read(watcher)) {
			t.Fatalf("stream mismatch: %#v", got)
		}
		if seq < 5 && (got.Type != "text_delta" || got.Delta == "" || got.Retryable != nil) {
			t.Fatalf("invalid text delta: %#v", got)
		}
		if seq == 5 && (got.Type != "done" || got.Reason != "completed" || got.Delta != "") {
			t.Fatalf("invalid done: %#v", got)
		}
	}

	_ = writer.Close()
	writer = dial(token, cfg.CORSOrigin, 101)
	write(writer, chat.ID, "answer-1", "chat")
	expectError(read(writer), "answer-1", "duplicate_run", false)
	write(writer, other.ID, "answer-1", "chat")
	_ = writer.Close() // The run continues when the sending device disconnects.
	for seq := 1; seq <= 5; seq++ {
		got := read(watcher)
		if got.ChatID != other.ID || got.MessageID != "answer-1" || got.Seq != seq {
			t.Fatalf("unexpected independent run frame: %#v", got)
		}
	}
	_ = bob.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	if _, _, err := bob.ReadMessage(); err == nil {
		t.Fatal("another user's socket received a broadcast")
	}

	reopened, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	req, err := http.NewRequest("GET", server.URL+"/api/chats/"+chat.ID+"/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var history []store.Message
	if err := json.NewDecoder(res.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Messages(t.Context(), alice, chat.ID)
	if err != nil || len(history) != 2 || !reflect.DeepEqual(history, persisted) ||
		history[0].Content != "Привет" || history[1].ID != "answer-1" || history[1].Role != "assistant" ||
		history[1].Content != "Получен запрос: Привет\n\nОтвет от StubProvider." || history[1].Thinking != first.Delta {
		t.Fatalf("incorrect durable history, HTTP: %v SQLite: %v (%v)", history, persisted, err)
	}
}

type gatedStub struct {
	first <-chan struct{}
}

func (s gatedStub) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	source, err := (provider.StubProvider{Delay: time.Millisecond}).Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		for event := range source {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
			if event.Type == provider.Thinking {
				select {
				case <-s.first:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
