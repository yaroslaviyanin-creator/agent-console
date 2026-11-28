package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
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

func TestFolderProjectsCRUDIDORAndContextIsolation(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "projects.db")
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

	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{db: db, config: config.Config{Provider: "stub"}}

	doJSON := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// 1. Create folder project with systemPrompt
	res := doJSON(http.MethodPost, "/api/folders", tokAlice, `{"name":"Go Backend","color":"#10b981","systemPrompt":"Always use Go 1.26 idioms."}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create project folder status=%d body=%s", res.Code, res.Body.String())
	}
	var folder store.Folder
	_ = json.NewDecoder(res.Body).Decode(&folder)
	if folder.SystemPrompt != "Always use Go 1.26 idioms." {
		t.Fatalf("unexpected systemPrompt: %q", folder.SystemPrompt)
	}

	// Reject >8KB systemPrompt
	tooLongPrompt, _ := json.Marshal(map[string]string{"systemPrompt": strings.Repeat("x", 8193)})
	if w := doJSON(http.MethodPatch, "/api/folders/"+folder.ID, tokAlice, string(tooLongPrompt)); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for >8KB systemPrompt, got %d", w.Code)
	}

	// 2. Upload project file (HTML must be stored as kind=text and served with strict attachment/CSP headers)
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	fw, err := mw.CreateFormFile("files", "spec.html")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("<h1>Project Secret Spec</h1>"))
	_ = mw.Close()

	upReq := httptest.NewRequest(http.MethodPost, "/api/folders/"+folder.ID+"/attachments", &bodyBuf)
	upReq.Header.Set("Authorization", "Bearer "+tokAlice)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRes := httptest.NewRecorder()
	handler.ServeHTTP(upRes, upReq)
	if upRes.Code != http.StatusCreated {
		t.Fatalf("upload folder attachment status=%d body=%s", upRes.Code, upRes.Body.String())
	}
	var upPayload struct {
		Attachments []store.Attachment `json:"attachments"`
	}
	_ = json.NewDecoder(upRes.Body).Decode(&upPayload)
	if len(upPayload.Attachments) != 1 {
		t.Fatalf("expected 1 uploaded folder attachment, got %d", len(upPayload.Attachments))
	}
	att := upPayload.Attachments[0]

	// 3. IDOR checks: Bob cannot list, upload, download, or delete Alice's folder files
	if w := doJSON(http.MethodGet, "/api/folders/"+folder.ID+"/attachments", tokBob, ""); w.Code != http.StatusNotFound {
		t.Fatalf("IDOR list folder attachments status=%d", w.Code)
	}
	if w := doJSON(http.MethodGet, "/api/folders/"+folder.ID+"/attachments/"+att.ID, tokBob, ""); w.Code != http.StatusNotFound {
		t.Fatalf("IDOR download folder attachment status=%d", w.Code)
	}
	if w := doJSON(http.MethodDelete, "/api/folders/"+folder.ID+"/attachments/"+att.ID, tokBob, ""); w.Code != http.StatusNotFound {
		t.Fatalf("IDOR delete folder attachment status=%d", w.Code)
	}

	// Alice downloads attachment -> must have attachment disposition & strict CSP
	dlRes := doJSON(http.MethodGet, "/api/folders/"+folder.ID+"/attachments/"+att.ID, tokAlice, "")
	if dlRes.Code != http.StatusOK {
		t.Fatalf("alice download folder attachment status=%d", dlRes.Code)
	}
	if !strings.Contains(dlRes.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("expected Content-Disposition: attachment, got %q", dlRes.Header().Get("Content-Disposition"))
	}
	if dlRes.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatalf("expected strict CSP, got %q", dlRes.Header().Get("Content-Security-Policy"))
	}

	// 4. Context injection in personal chat vs shared chat isolation
	aliceChat, err := db.CreateChat(ctx, alice.ID, "Project chat", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	if w := doJSON(http.MethodPatch, "/api/chats/"+aliceChat.ID, tokAlice, `{"folderId":"`+folder.ID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("assign chat to folder status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.SetChatShare(ctx, alice.ID, aliceChat.ID, bob.ID, "write"); err != nil {
		t.Fatal(err)
	}

	// Personal run (alice): gets project instructions and project file context
	projFolder, projAtts, err := db.GetChatProjectContext(ctx, alice.ID, aliceChat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projFolder.SystemPrompt != "Always use Go 1.26 idioms." || len(projAtts) != 1 {
		t.Fatalf("unexpected project context for owner: folder=%+v atts=%d", projFolder, len(projAtts))
	}
	aliceInstr, err := api.userInstructions(ctx, alice.ID, false, projFolder.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(aliceInstr, "<project_instructions>") || !strings.Contains(aliceInstr, "Always use Go 1.26 idioms.") {
		t.Fatalf("expected <project_instructions> in owner instructions: %s", aliceInstr)
	}
	alicePrompt := buildProjectPromptWithAttachments("Вопрос", projAtts, 64<<10)
	if !strings.Contains(alicePrompt, "<project_file name=\"spec.html\"") || !strings.Contains(alicePrompt, "Project Secret Spec") {
		t.Fatalf("expected project file in owner prompt: %s", alicePrompt)
	}

	// Shared collaborator (bob): GetChatProjectContext returns nothing & shared userInstructions excludes project prompt
	bobFolder, bobAtts, err := db.GetChatProjectContext(ctx, bob.ID, aliceChat.ID)
	if err != nil && err.Error() != "sql: no rows in result set" {
		t.Fatalf("unexpected error for collaborator project context: %v", err)
	}
	if bobFolder.SystemPrompt != "" || len(bobAtts) != 0 {
		t.Fatalf("project context leaked to collaborator: folder=%+v atts=%d", bobFolder, len(bobAtts))
	}
	bobInstr, err := api.userInstructions(ctx, bob.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bobInstr, "<project_instructions>") || strings.Contains(bobInstr, "Go 1.26") {
		t.Fatalf("project instructions leaked to shared chat: %s", bobInstr)
	}

	// 5. Delete folder attachment
	if w := doJSON(http.MethodDelete, "/api/folders/"+folder.ID+"/attachments/"+att.ID, tokAlice, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete folder attachment status=%d", w.Code)
	}
}
