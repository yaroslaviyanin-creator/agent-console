package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"agent-console/server/internal/uploads"
	"github.com/gorilla/websocket"
)

func TestChatPinForkMessageOpsAndIDOR(t *testing.T) {
	t.Setenv("PRO_LIMIT_5H_CENTS", "100")
	t.Setenv("PRO_LIMIT_7D_CENTS", "500")
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mkUser := func(name string) (string, string) {
		t.Helper()
		u, err := db.CreateUserWithRole(ctx, name, []byte("hash"), "admin")
		if err != nil {
			t.Fatal(err)
		}
		_, tok, err := db.IssueDevice(ctx, u.ID, "dev-"+name)
		if err != nil {
			t.Fatal(err)
		}
		return u.ID, tok
	}
	aliceID, aliceTok := mkUser("alice")
	readerID, readerTok := mkUser("reader")
	forkerID, forkerTok := mkUser("forker")
	writerID, writerTok := mkUser("writer")
	_, strangerTok := mkUser("stranger")

	captured := make(chan capturedProviderCall, 16)
	var codexSeq, claudeSeq atomic.Int64
	cfg := config.Config{
		Provider:             "codex",
		EnabledProviders:     []string{"codex", "claude"},
		DefaultProvider:      "codex",
		MemoryProvider:       "codex",
		CORSOrigin:           "*",
		MaxConcurrentStreams: 2,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newRegistryHandler(ctx, db, cfg, logger, map[string]provider.Provider{
		"codex":  switchCaptureProvider{name: "codex", seq: &codexSeq, requests: captured},
		"claude": switchCaptureProvider{name: "claude", seq: &claudeSeq, requests: captured},
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	doJSON := func(method, path, tok string, body any) (int, []byte) {
		t.Helper()
		var r io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			r = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, srv.URL+path, r)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, out
	}

	dialWS := func(tok string) *websocket.Conn {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/ws/ticket", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("ticket failed: err=%v res=%v", err, res)
		}
		var b struct{ Ticket string }
		_ = json.NewDecoder(res.Body).Decode(&b)
		res.Body.Close()
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?ticket=" + url.QueryEscape(b.Ticket)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	runWS := func(ws *websocket.Conn, payload map[string]any) runEvent {
		t.Helper()
		if err := ws.WriteJSON(payload); err != nil {
			t.Fatal(err)
		}
		msgID := payload["messageId"].(string)
		for {
			_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
			var ev runEvent
			if err := ws.ReadJSON(&ev); err != nil {
				t.Fatal(err)
			}
			if ev.MessageID != msgID {
				continue
			}
			if ev.Type == "error" {
				t.Fatalf("ws error: %+v", ev)
			}
			if ev.Type == "done" {
				return ev
			}
		}
	}

	// Create 2 chats and check pin ordering
	chat1, err := db.CreateChat(ctx, aliceID, "First", "codex", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	chat2, err := db.CreateChat(ctx, aliceID, "Second", "codex", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := doJSON("PUT", "/api/chats/"+chat1.ID+"/shares/"+readerID, aliceTok, map[string]string{"access": "read"}); code != 204 {
		t.Fatalf("share read: %d", code)
	}
	if code, _ := doJSON("PUT", "/api/chats/"+chat1.ID+"/shares/"+forkerID, aliceTok, map[string]string{"access": "fork"}); code != 204 {
		t.Fatalf("share fork: %d", code)
	}
	if code, _ := doJSON("PUT", "/api/chats/"+chat1.ID+"/shares/"+writerID, aliceTok, map[string]string{"access": "write"}); code != 204 {
		t.Fatalf("share write: %d", code)
	}

	// PATCH /api/chats/{id} IDOR checks: owner ok, shared users 403, stranger 404
	for _, tc := range []struct {
		tok  string
		want int
	}{
		{readerTok, 403},
		{forkerTok, 403},
		{writerTok, 403},
		{strangerTok, 404},
	} {
		if code, _ := doJSON("PATCH", "/api/chats/"+chat1.ID, tc.tok, map[string]any{"pinned": true}); code != tc.want {
			t.Fatalf("PATCH chat as %s: got %d, want %d", tc.tok, code, tc.want)
		}
	}
	if code, _ := doJSON("PATCH", "/api/chats/"+chat1.ID, aliceTok, map[string]any{"title": "Pinned First", "pinned": true}); code != 200 {
		t.Fatalf("owner PATCH chat failed: %d", code)
	}
	code, listRaw := doJSON("GET", "/api/chats", aliceTok, nil)
	if code != 200 {
		t.Fatalf("GET chats: %d", code)
	}
	var chats []store.Chat
	_ = json.Unmarshal(listRaw, &chats)
	if len(chats) != 2 || chats[0].ID != chat1.ID || !chats[0].Pinned || chats[0].Title != "Pinned First" || chats[1].ID != chat2.ID {
		t.Fatalf("expected pinned chat first, got %+v", chats)
	}

	// Upload an attachment and run two turns on chat1
	att, err := db.CreateAttachment(ctx, chat1.ID, aliceID, uploads.ValidatedAttachment{
		Filename:      "spec.txt",
		MIMEType:      "text/plain",
		Kind:          uploads.KindText,
		Size:          12,
		Data:          []byte("hello attach"),
		ExtractedText: "hello attach",
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := dialWS(aliceTok)
	ev1 := runWS(ws, map[string]any{
		"chatId":        chat1.ID,
		"messageId":     "turn1",
		"prompt":        "First prompt",
		"attachmentIds": []string{att.ID},
		"model":         map[string]string{"provider": "codex", "id": "gpt-5"},
		"target":        map[string]string{"mode": "chat"},
	})
	<-captured
	if ev1.Provider != "codex" {
		t.Fatalf("expected provider=codex on done event, got %+v", ev1)
	}
	_ = runWS(ws, map[string]any{
		"chatId":    chat1.ID,
		"messageId": "turn2",
		"prompt":    "Second prompt",
		"model":     map[string]string{"provider": "codex", "id": "gpt-5"},
		"target":    map[string]string{"mode": "chat"},
	})
	<-captured

	msgs, err := db.Messages(ctx, aliceID, chat1.ID)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("expected 4 messages in chat1, got %d err=%v", len(msgs), err)
	}
	u1ID, a1ID, u2ID, a2ID := msgs[0].ID, msgs[1].ID, msgs[2].ID, msgs[3].ID
	if len(msgs[0].Attachments) != 1 {
		t.Fatalf("expected attachment on turn1 user message, got %+v", msgs[0])
	}

	// Fork IDOR: read and stranger get 404; forker gets 201
	if code, _ := doJSON("POST", "/api/chats/"+chat1.ID+"/fork", readerTok, map[string]string{}); code != 404 {
		t.Fatalf("fork as read user: got %d want 404", code)
	}
	if code, _ := doJSON("POST", "/api/chats/"+chat1.ID+"/fork", strangerTok, map[string]string{}); code != 404 {
		t.Fatalf("fork as stranger: got %d want 404", code)
	}
	if code, _ := doJSON("POST", "/api/chats/"+chat1.ID+"/fork", forkerTok, map[string]string{"upToMessageId": a1ID}); code != 201 {
		t.Fatalf("fork upTo as forker: got %d want 201", code)
	}

	// Owner fork upToMessageId=a1ID should keep only turn1 messages + duplicate attachment on disk
	code, forkRaw := doJSON("POST", "/api/chats/"+chat1.ID+"/fork", aliceTok, map[string]string{"upToMessageId": a1ID})
	if code != 201 {
		t.Fatalf("owner fork upTo failed: %d %s", code, string(forkRaw))
	}
	var forked store.Chat
	_ = json.Unmarshal(forkRaw, &forked)
	forkMsgs, err := db.Messages(ctx, aliceID, forked.ID)
	if err != nil || len(forkMsgs) != 2 || len(forkMsgs[0].Attachments) != 1 {
		t.Fatalf("expected 2 msgs + 1 attachment in forked chat, got %+v err=%v", forkMsgs, err)
	}
	forkAttID := forkMsgs[0].Attachments[0].ID
	if forkAttID == att.ID {
		t.Fatalf("forked attachment must have distinct ID")
	}

	// Regenerate IDOR & unavailable provider rejection
	if code, _ := doJSON("POST", "/api/chats/"+chat1.ID+"/messages/"+a2ID+"/regenerate", readerTok, map[string]string{}); code != 403 {
		t.Fatalf("regenerate as read: got %d want 403", code)
	}
	if code, _ := doJSON("POST", "/api/chats/"+chat1.ID+"/messages/"+a2ID+"/regenerate", strangerTok, map[string]string{}); code != 404 {
		t.Fatalf("regenerate as stranger: got %d want 404", code)
	}
	if code, _ := doJSON("POST", "/api/chats/"+chat1.ID+"/messages/"+a2ID+"/regenerate", aliceTok, map[string]string{"provider": "antigravity"}); code != 422 {
		t.Fatalf("regenerate unavailable provider: got %d want 422", code)
	}

	// Regenerate turn2 in another available provider (claude) + WS reuseUserMessageId
	code, regenRaw := doJSON("POST", "/api/chats/"+chat1.ID+"/messages/"+a2ID+"/regenerate", aliceTok, map[string]string{"provider": "claude", "model": "sonnet"})
	if code != 200 {
		t.Fatalf("regenerate claude: %d %s", code, string(regenRaw))
	}
	var regenResp struct {
		UserMessageID string                    `json:"userMessageId"`
		Prompt        string                    `json:"prompt"`
		Attachments   []store.MessageAttachment `json:"attachments"`
	}
	_ = json.Unmarshal(regenRaw, &regenResp)
	if regenResp.UserMessageID != u2ID || regenResp.Prompt != "Second prompt" {
		t.Fatalf("unexpected regenerate response: %+v", regenResp)
	}
	_ = runWS(ws, map[string]any{
		"chatId":             chat1.ID,
		"messageId":          "turn2-claude",
		"reuseUserMessageId": regenResp.UserMessageID,
		"provider":           "claude",
		"prompt":             regenResp.Prompt,
		"model":              map[string]string{"provider": "claude", "id": "sonnet"},
		"target":             map[string]string{"mode": "chat"},
	})
	call := <-captured
	if call.provider != "claude" || call.req.SessionID != "" || !strings.Contains(call.req.Prompt, "First prompt") {
		t.Fatalf("regenerate claude must use SQLite handoff with turn1 context, got %+v", call)
	}
	msgs, _ = db.Messages(ctx, aliceID, chat1.ID)
	if len(msgs) != 4 || msgs[2].ID != u2ID || msgs[3].Provider != "claude" {
		t.Fatalf("expected u2 reused and assistant regenerated by claude, got %+v", msgs)
	}

	// Edit-and-resend turn1 (with attachment preserved, truncating turn2)
	if code, _ := doJSON("PATCH", "/api/chats/"+chat1.ID+"/messages/"+u1ID, strangerTok, map[string]string{"content": "hack"}); code != 404 {
		t.Fatalf("edit as stranger: got %d want 404", code)
	}
	code, editRaw := doJSON("PATCH", "/api/chats/"+chat1.ID+"/messages/"+u1ID, aliceTok, map[string]string{"content": "Edited first prompt"})
	if code != 200 {
		t.Fatalf("edit message failed: %d %s", code, string(editRaw))
	}
	var editResp struct {
		UserMessageID string                    `json:"userMessageId"`
		Attachments   []store.MessageAttachment `json:"attachments"`
	}
	_ = json.Unmarshal(editRaw, &editResp)
	if editResp.UserMessageID != u1ID || len(editResp.Attachments) != 1 || editResp.Attachments[0].ID != att.ID {
		t.Fatalf("expected reused u1 with attachment, got %+v", editResp)
	}
	_ = runWS(ws, map[string]any{
		"chatId":             chat1.ID,
		"messageId":          "turn1-edited",
		"reuseUserMessageId": u1ID,
		"provider":           "codex",
		"prompt":             "Edited first prompt",
		"model":              map[string]string{"provider": "codex", "id": "gpt-5"},
		"target":             map[string]string{"mode": "chat"},
	})
	editCall := <-captured
	if !strings.Contains(editCall.req.Prompt, "hello attach") || !strings.Contains(editCall.req.Prompt, "Edited first prompt") {
		t.Fatalf("expected edited prompt + preserved attachment text passed to provider, got %+v", editCall.req)
	}
	msgs, _ = db.Messages(ctx, aliceID, chat1.ID)
	if len(msgs) != 2 || msgs[0].Content != "Edited first prompt" {
		t.Fatalf("expected branch truncated to edited turn1 (2 msgs), got %+v", msgs)
	}

	// Delete message IDOR & attachment removal on user delete, while forked chat still serves its duplicated attachment
	if code, _ := doJSON("DELETE", "/api/chats/"+chat1.ID+"/messages/"+u1ID, readerTok, nil); code != 403 {
		t.Fatalf("delete msg as read: got %d want 403", code)
	}
	if code, _ := doJSON("DELETE", "/api/chats/"+chat1.ID+"/messages/"+u1ID, aliceTok, nil); code != 204 {
		t.Fatalf("delete user msg: got %d want 204", code)
	}
	if _, err := db.GetAttachment(ctx, aliceID, att.ID); err == nil {
		t.Fatalf("expected deleted user message attachment to be removed")
	}
	if sessID, _, _, _, err := db.ChatRunSessionState(ctx, chat1.ID); err != nil || sessID != "" {
		t.Fatalf("expected CLI session reset after message delete, got session=%q err=%v", sessID, err)
	}
	// Verify forked attachment file still exists on disk and can be read via API even after deleting source chat!
	if code, _ := doJSON("DELETE", "/api/chats/"+chat1.ID, aliceTok, nil); code != 204 {
		t.Fatalf("delete source chat: got %d want 204", code)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/attachments/"+forkAttID, nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	attRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(attRes.Body)
	attRes.Body.Close()
	if attRes.StatusCode != 200 || string(body) != "hello attach" {
		t.Fatalf("expected forked attachment intact after source deletion: status=%d body=%q", attRes.StatusCode, string(body))
	}
	_ = os.ErrNotExist
}
