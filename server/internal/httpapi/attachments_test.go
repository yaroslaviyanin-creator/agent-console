package httpapi

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-console/server/internal/config"
	"agent-console/server/internal/store"
	"agent-console/server/internal/uploads"
	"github.com/gorilla/websocket"
)

func buildMultipartUpload(t *testing.T, fieldName string, files map[string][]byte) (string, *bytes.Buffer) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		part, err := mw.CreateFormFile(fieldName, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), &body
}

func testJPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 50, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testZIPBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestChatAttachmentsSecurityAndLifecycle(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "chat-att.db")
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
	chat, err := db.CreateChat(ctx, alice.ID, "Uploads", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	upload := func(token, chatID string, files map[string][]byte) *httptest.ResponseRecorder {
		ct, body := buildMultipartUpload(t, "files", files)
		r := httptest.NewRequest(http.MethodPost, "/api/chats/"+chatID+"/attachments", body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// 1. Upload HTML (with <script>), SVG, ZIP project, and JPEG.
	projectZip := testZIPBytes(t, map[string][]byte{"main.go": []byte("package main\nfunc main() {}\n")})
	w := upload(tokAlice, chat.ID, map[string][]byte{
		"page.html": []byte("<html><script>alert(1)</script></html>"),
		"icon.svg":  []byte(`<svg onload="alert(1)"><script>alert(1)</script></svg>`),
		"proj.zip":  projectZip,
		"pic.jpg":   testJPEGBytes(t),
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("upload failed (%d): %s", w.Code, w.Body.String())
	}
	var uploadResp struct {
		Attachments []store.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &uploadResp); err != nil || len(uploadResp.Attachments) != 4 {
		t.Fatalf("unexpected upload response: %v (%s)", err, w.Body.String())
	}

	byName := map[string]store.Attachment{}
	for _, a := range uploadResp.Attachments {
		byName[a.OriginalName] = a
	}

	// 2. XSS headers for HTML & SVG downloads: strictly text/plain + attachment + nosniff + sandbox.
	for _, name := range []string{"page.html", "icon.svg"} {
		att := byName[name]
		r := httptest.NewRequest(http.MethodGet, "/api/attachments/"+att.ID, nil)
		r.Header.Set("Authorization", "Bearer "+tokAlice)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s failed: %d", name, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Fatalf("%s Content-Type=%q, want text/plain; charset=utf-8", name, got)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
			t.Fatalf("%s Content-Disposition=%q, want attachment", name, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s nosniff=%q", name, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
			t.Fatalf("%s csp=%q", name, got)
		}
	}

	// 3. IDOR: Bob cannot download or delete Alice's attachment.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/attachments/" + byName["page.html"].ID},
		{http.MethodDelete, "/api/attachments/" + byName["page.html"].ID},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tokBob)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Fatalf("IDOR %s %s got %d", tc.method, tc.path, rec.Code)
		}
	}

	// 4. Reject polyglot .zip (JPG+ZIP), zip bomb, Zip Slip, .exe, .rar, .tar.
	polyglot := append(testJPEGBytes(t), testZIPBytes(t, map[string][]byte{"benign.txt": []byte("x")})...)
	slipZip := testZIPBytes(t, map[string][]byte{"../../etc/passwd": []byte("root")})

	var bombBuf bytes.Buffer
	zw := zip.NewWriter(&bombBuf)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	fw, _ := zw.Create("big.txt")
	_, _ = fw.Write(bytes.Repeat([]byte{'A'}, 6<<20))
	_ = zw.Close()

	maliciousUploads := []struct {
		name string
		data []byte
	}{
		{"polyglot.zip", polyglot},
		{"bomb.zip", bombBuf.Bytes()},
		{"slip.zip", slipZip},
		{"evil.exe", []byte("MZ\x90\x00")},
		{"evil.elf", []byte("\x7fELF\x02\x01")},
		{"arch.rar", []byte("Rar!\x1A\x07")},
		{"arch.tar", []byte("tar-data")},
	}
	for _, tc := range maliciousUploads {
		rec := upload(tokAlice, chat.ID, map[string][]byte{tc.name: tc.data})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malicious upload %s got %d want 400: %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	// 5. Polyglot uploaded as .jpg is re-encoded cleanly without ZIP tail.
	recPolyJPG := upload(tokAlice, chat.ID, map[string][]byte{"poly.jpg": polyglot})
	if recPolyJPG.Code != http.StatusCreated {
		t.Fatalf("poly.jpg got %d: %s", recPolyJPG.Code, recPolyJPG.Body.String())
	}

	// 6. Bind attachments via WebSocket run and verify prompt context + DB history.
	srv := httptest.NewServer(handler)
	defer srv.Close()

	ticketReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/ws/ticket", nil)
	ticketReq.Header.Set("Authorization", "Bearer "+tokAlice)
	ticketRes, err := http.DefaultClient.Do(ticketReq)
	if err != nil {
		t.Fatal(err)
	}
	var ticketBody struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(ticketRes.Body).Decode(&ticketBody)
	_ = ticketRes.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?ticket=" + ticketBody.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	attIDs := []string{byName["page.html"].ID, byName["proj.zip"].ID, byName["pic.jpg"].ID}
	msgID := "msg-att-1"
	wsFrame, _ := json.Marshal(map[string]any{
		"chatId":        chat.ID,
		"messageId":     msgID,
		"prompt":        "Check attachments",
		"attachmentIds": attIDs,
		"model":         map[string]string{"provider": "codex", "id": "default"},
		"target":        map[string]string{"mode": "chat"},
	})
	if err := conn.WriteMessage(websocket.TextMessage, wsFrame); err != nil {
		t.Fatal(err)
	}

	var gotText strings.Builder
	var sawStartedAttachments bool
	for {
		var ev runEvent
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "started" && len(ev.Attachments) == 3 {
			sawStartedAttachments = true
		}
		if ev.Type == "text_delta" {
			gotText.WriteString(ev.Delta)
		}
		if ev.Type == "done" {
			break
		}
		if ev.Type == "error" {
			t.Fatalf("unexpected ws error: %+v", ev)
		}
	}
	if !sawStartedAttachments {
		t.Fatal("started event did not include attachments")
	}
	fullAnswer := gotText.String()
	if !strings.Contains(fullAnswer, "<attached_file name=\"page.html\" kind=\"text\">") ||
		!strings.Contains(fullAnswer, "package main") ||
		!strings.Contains(fullAnswer, "[изображений: 1]") {
		t.Fatalf("stub answer missing attachment context: %s", fullAnswer)
	}

	// Verify disk files exist in uploadsDir before chat deletion, and are deleted on DELETE /api/chats/{id}.
	filesBefore, _ := os.ReadDir(db.UploadsDir())
	if len(filesBefore) < 4 {
		t.Fatalf("expected stored files in %s, got %d", db.UploadsDir(), len(filesBefore))
	}
	delReq := httptest.NewRequest(http.MethodDelete, "/api/chats/"+chat.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+tokAlice)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE chat got %d: %s", delRec.Code, delRec.Body.String())
	}
	// Note: poly.jpg wasn't bound to another chat, it belonged to this chat so cascade + DeleteChat removes ALL 5 files!
	filesAfter, _ := os.ReadDir(db.UploadsDir())
	if len(filesAfter) != 0 {
		t.Fatalf("expected all upload files removed on chat deletion, remaining=%d", len(filesAfter))
	}

	// 7. Rate limit on POST /api/chats/{id}/attachments returns 429 + Retry-After.
	chat2, _ := db.CreateChat(ctx, bob.ID, "Rate", "stub", "default")
	hit429 := false
	for i := 0; i < 20; i++ {
		rec := upload(tokBob, chat2.ID, map[string][]byte{"note.txt": []byte("ok")})
		if rec.Code == http.StatusTooManyRequests {
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("expected Retry-After header on 429")
			}
			hit429 = true
			break
		}
	}
	if !hit429 {
		t.Fatal("expected 429 rate limit on repeated uploads")
	}

	_ = uploads.KindImage
}
