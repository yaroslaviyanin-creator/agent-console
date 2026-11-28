package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"github.com/gorilla/websocket"
)

type artifactMockProvider struct{}

func (artifactMockProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		replyText := strings.Join([]string{
			"Вот подготовленные файлы:",
			`<console_file name="table.xlsx">`,
			"Имя,Сумма",
			"Анна,500",
			`</console_file>`,
			`<console_file name="doc.docx">`,
			"# Приветствие\n\nТекст на русском языке.",
			`</console_file>`,
			`<console_file name="report.pdf">`,
			"# Отчёт\n\nКириллица в PDF.",
			`</console_file>`,
			`<console_file name="app.html">`,
			"<h1>Hello</h1>",
			`</console_file>`,
			"Все файлы готовы.",
		}, "\n")
		out <- provider.Event{Type: provider.Text, Delta: replyText}
		out <- provider.Event{Type: provider.Done, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 50}}
	}()
	return out, nil
}

func TestAssistantGeneratedArtifactsLifecycleAndSecurity(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "artifacts-e2e.db")
	db, err := store.Open(ctx, dbPath)
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
	_, tokAlice, err := db.IssueDevice(ctx, alice.ID, "devA")
	if err != nil {
		t.Fatal(err)
	}
	_, tokBob, err := db.IssueDevice(ctx, bob.ID, "devB")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, alice.ID, "Artifacts", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	handler := newHandler(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)), artifactMockProvider{})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	reqTicket, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/ws/ticket", nil)
	reqTicket.Header.Set("Authorization", "Bearer "+tokAlice)
	resTicket, err := http.DefaultClient.Do(reqTicket)
	if err != nil {
		t.Fatal(err)
	}
	var ticketPayload struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(resTicket.Body).Decode(&ticketPayload)
	_ = resTicket.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?ticket=" + ticketPayload.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"chatId":    chat.ID,
		"messageId": "art-msg-1",
		"provider":  "stub",
		"model":     map[string]string{"provider": "codex", "id": "default"},
		"target":    map[string]string{"mode": "chat"},
		"prompt":    "Сделай файлы",
	}); err != nil {
		t.Fatal(err)
	}

	var doneEvt runEvent
	for {
		var ev runEvent
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "error" {
			t.Fatalf("unexpected error event: %+v", ev)
		}
		if ev.Type == "done" {
			doneEvt = ev
			break
		}
	}

	if len(doneEvt.Attachments) != 4 {
		t.Fatalf("expected 4 assistant attachments in done event, got %d: %+v", len(doneEvt.Attachments), doneEvt.Attachments)
	}
	if doneEvt.CleanText == nil || strings.Contains(*doneEvt.CleanText, "<console_file") {
		t.Fatalf("expected cleanText without <console_file>, got %v", doneEvt.CleanText)
	}
	for _, att := range doneEvt.Attachments {
		if att.Source != "assistant" {
			t.Fatalf("expected source=assistant, got %+v", att)
		}
	}

	// Verify REST messages endpoint returns attachments on assistant message
	msgReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/chats/"+chat.ID+"/messages", nil)
	msgReq.Header.Set("Authorization", "Bearer "+tokAlice)
	msgRes, err := http.DefaultClient.Do(msgReq)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []store.Message
	_ = json.NewDecoder(msgRes.Body).Decode(&msgs)
	_ = msgRes.Body.Close()
	if len(msgs) != 2 || msgs[1].Role != "assistant" || len(msgs[1].Attachments) != 4 {
		t.Fatalf("expected assistant message with 4 attachments, got %+v", msgs)
	}
	if strings.Contains(msgs[1].Content, "<console_file") {
		t.Fatalf("stored assistant content still contains <console_file>: %q", msgs[1].Content)
	}

	// Verify IDOR on GET /api/attachments/{id} and strict headers for .html
	htmlAtt := doneEvt.Attachments[3]
	bobReq, _ := http.NewRequest(http.MethodGet, srv.URL+htmlAtt.URL, nil)
	bobReq.Header.Set("Authorization", "Bearer "+tokBob)
	bobRes, err := http.DefaultClient.Do(bobReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = bobRes.Body.Close()
	if bobRes.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign user on attachment, got %d", bobRes.StatusCode)
	}

	aliceReq, _ := http.NewRequest(http.MethodGet, srv.URL+htmlAtt.URL, nil)
	aliceReq.Header.Set("Authorization", "Bearer "+tokAlice)
	aliceRes, err := http.DefaultClient.Do(aliceReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = aliceRes.Body.Close()
	if aliceRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for owner, got %d", aliceRes.StatusCode)
	}
	if !strings.HasPrefix(aliceRes.Header.Get("Content-Type"), "text/plain") ||
		!strings.Contains(aliceRes.Header.Get("Content-Disposition"), "attachment") ||
		aliceRes.Header.Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatalf("unexpected security headers on .html artifact: %+v", aliceRes.Header)
	}

	// Verify disk cleanup on DeleteChat
	entriesBefore, err := os.ReadDir(db.UploadsDir())
	if err != nil || len(entriesBefore) != 4 {
		t.Fatalf("expected 4 files in uploadsDir before delete, got %d (err=%v)", len(entriesBefore), err)
	}
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/chats/"+chat.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+tokAlice)
	delRes, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = delRes.Body.Close()
	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 deleting chat, got %d", delRes.StatusCode)
	}
	entriesAfter, err := os.ReadDir(db.UploadsDir())
	if err != nil || len(entriesAfter) != 0 {
		t.Fatalf("expected 0 files in uploadsDir after chat delete, got %d (err=%v)", len(entriesAfter), err)
	}
}
