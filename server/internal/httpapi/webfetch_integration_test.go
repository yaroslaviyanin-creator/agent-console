package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"github.com/gorilla/websocket"
)

type capturePromptProvider struct {
	prompts chan string
}

func (p capturePromptProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.prompts <- req.Prompt
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		out <- provider.Event{Type: provider.Text, Delta: "Проверено."}
		out <- provider.Event{Type: provider.Done, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 5}}
	}()
	return out, nil
}

func TestWebFetchSSRFAndPersistenceInWS(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "webfetch-ws.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	alice, err := db.CreateUserWithRole(ctx, "alice", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	_, tokAlice, err := db.IssueDevice(ctx, alice.ID, "devA")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, alice.ID, "Web", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	capProv := capturePromptProvider{prompts: make(chan string, 1)}
	handler := newHandler(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)), capProv)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	reqTicket, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/ws/ticket", nil)
	reqTicket.Header.Set("Authorization", "Bearer "+tokAlice)
	resTicket, err := http.DefaultClient.Do(reqTicket)
	if err != nil {
		t.Fatal(err)
	}
	var tp struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(resTicket.Body).Decode(&tp)
	_ = resTicket.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?ticket=" + tp.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"chatId":    chat.ID,
		"messageId": "web-msg-1",
		"provider":  "stub",
		"model":     map[string]string{"provider": "codex", "id": "default"},
		"target":    map[string]string{"mode": "chat"},
		"prompt":    "Проверь http://127.0.0.1/secret и http://169.254.169.254/latest/meta-data/",
	}); err != nil {
		t.Fatal(err)
	}

	var seenWebSources []store.WebSource
	for {
		var ev runEvent
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "web_sources" {
			seenWebSources = ev.WebSources
		}
		if ev.Type == "done" {
			break
		}
	}

	if len(seenWebSources) != 2 {
		t.Fatalf("expected 2 web_sources events, got %+v", seenWebSources)
	}
	for _, ws := range seenWebSources {
		if ws.Status != "blocked" {
			t.Fatalf("expected SSRF target %s to be blocked, got %+v", ws.URL, ws)
		}
	}

	sentPrompt := <-capProv.prompts
	if !strings.Contains(sentPrompt, "UNTRUSTED EXTERNAL WEB CONTENT") || !strings.Contains(sentPrompt, "<attached_webpage") {
		t.Fatalf("expected untrusted webpage wrapper in prompt: %q", sentPrompt)
	}

	msgReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/chats/"+chat.ID+"/messages", nil)
	msgReq.Header.Set("Authorization", "Bearer "+tokAlice)
	msgRes, err := http.DefaultClient.Do(msgReq)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []store.Message
	_ = json.NewDecoder(msgRes.Body).Decode(&msgs)
	_ = msgRes.Body.Close()
	if len(msgs) != 2 || len(msgs[0].WebSources) != 2 || msgs[0].WebSources[0].Status != "blocked" {
		t.Fatalf("expected persisted webSources on user message, got %+v", msgs)
	}
}
