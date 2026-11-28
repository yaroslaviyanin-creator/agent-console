package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agent-console/server/internal/config"
	"agent-console/server/internal/store"
)

func TestFoldersArchiveAndGlobalSearchIDOR(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "org-v2.db")
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

	aliceChat, err := db.CreateChat(ctx, alice.ID, "Проект Альфа", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	bobPrivate, err := db.CreateChat(ctx, bob.ID, "Секрет Боба", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	// Seed messages for search
	if _, err := db.BeginRun(ctx, alice.ID, aliceChat.ID, "r1", "Подготовь финансовый квартальный отчёт", "stub", "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, alice.ID, aliceChat.ID, "r1", "Квартальный отчёт готов к проверке.", "", "", 12, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginRun(ctx, bob.ID, bobPrivate.ID, "r2", "Секретный финансовый план Боба", "stub", "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, bob.ID, bobPrivate.ID, "r2", "Только для Боба: секретный отчёт.", "", "", 10, nil); err != nil {
		t.Fatal(err)
	}

	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	doReq := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// 1. Create folder for Alice
	res := doReq(http.MethodPost, "/api/folders", tokAlice, `{"name":"Работа","color":"#10B981"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create folder status=%d body=%s", res.Code, res.Body.String())
	}
	var folder store.Folder
	_ = json.NewDecoder(res.Body).Decode(&folder)
	if folder.Name != "Работа" || folder.Color != "#10b981" {
		t.Fatalf("unexpected folder: %+v", folder)
	}

	// 2. IDOR: Bob cannot rename or delete Alice's folder, and cannot move Bob's chat into Alice's folder
	if w := doReq(http.MethodPatch, "/api/folders/"+folder.ID, tokBob, `{"name":"Hack"}`); w.Code != http.StatusNotFound {
		t.Fatalf("IDOR patch folder expected 404, got %d", w.Code)
	}
	if w := doReq(http.MethodDelete, "/api/folders/"+folder.ID, tokBob, ""); w.Code != http.StatusNotFound {
		t.Fatalf("IDOR delete folder expected 404, got %d", w.Code)
	}
	if w := doReq(http.MethodPatch, "/api/chats/"+bobPrivate.ID, tokBob, `{"folderId":"`+folder.ID+`"}`); w.Code != http.StatusForbidden {
		t.Fatalf("IDOR move chat to foreign folder expected 403, got %d", w.Code)
	}

	// 3. Alice moves chat to folder & archives it, then unarchives
	if w := doReq(http.MethodPatch, "/api/chats/"+aliceChat.ID, tokAlice, `{"folderId":"`+folder.ID+`","archived":true}`); w.Code != http.StatusOK {
		t.Fatalf("patch chat status=%d body=%s", w.Code, w.Body.String())
	} else {
		var updated store.Chat
		_ = json.NewDecoder(w.Body).Decode(&updated)
		if updated.FolderID == nil || *updated.FolderID != folder.ID || updated.ArchivedAt == nil {
			t.Fatalf("expected folderId and archivedAt set, got %+v", updated)
		}
	}

	// 4. Delete folder preserves Alice's chat with folderId cleared
	if w := doReq(http.MethodDelete, "/api/folders/"+folder.ID, tokAlice, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete folder status=%d", w.Code)
	}
	listRes := doReq(http.MethodGet, "/api/chats", tokAlice, "")
	var chats []store.Chat
	_ = json.NewDecoder(listRes.Body).Decode(&chats)
	if len(chats) != 1 || chats[0].ID != aliceChat.ID || chats[0].FolderID != nil || chats[0].ArchivedAt == nil {
		t.Fatalf("expected chat preserved without folder after folder delete: %+v", chats)
	}

	// 5. Global search: Cyrillic case-insensitive, isolated from Bob's private chat
	searchRes := doReq(http.MethodGet, "/api/search?q=ОТЧЁТ&limit=30", tokAlice, "")
	if searchRes.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", searchRes.Code, searchRes.Body.String())
	}
	var hits []store.SearchResult
	_ = json.NewDecoder(searchRes.Body).Decode(&hits)
	if len(hits) != 2 {
		t.Fatalf("expected 2 message hits for Alice, got %d: %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.ChatID != aliceChat.ID {
			t.Fatalf("search leaked foreign chat hit: %+v", h)
		}
	}
}
