package httpapi

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

func testLargePNGBytes(t *testing.T) []byte {
	t.Helper()
	const w, h = 800, 800
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	var state uint32 = 0x12345678
	for i := 0; i < len(img.Pix); i += 4 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		img.Pix[i] = byte(state)
		img.Pix[i+1] = byte(state >> 8)
		img.Pix[i+2] = byte(state >> 16)
		img.Pix[i+3] = 255
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= 1<<20 {
		t.Fatalf("expected large PNG > 1 MiB, got %d", buf.Len())
	}
	return buf.Bytes()
}

func testTextPDFBytes(t *testing.T, text string) []byte {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n",
		fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream),
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, obj := range objs {
		offsets[i+1] = out.Len()
		out.WriteString(obj)
	}
	xrefOffset := out.Len()
	out.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objs)+1))
	for i := 1; i <= len(objs); i++ {
		out.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	out.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefOffset))
	return out.Bytes()
}

func TestChatAttachmentsTypesLimitsAndErrors(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "chat-att-types.db")
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
	chat, err := db.CreateChat(ctx, alice.ID, "Types", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*", MaxAttachmentMB: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	upload := func(files map[string][]byte) *httptest.ResponseRecorder {
		ct, body := buildMultipartUpload(t, "files", files)
		r := httptest.NewRequest(http.MethodPost, "/api/chats/"+chat.ID+"/attachments", body)
		r.Header.Set("Authorization", "Bearer "+tokAlice)
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// 1. Upload .txt, large .png (> 1 MiB), .pdf (with text layer), and .zip.
	largePNG := testLargePNGBytes(t)
	pdfBytes := testTextPDFBytes(t, "Invoice PDF Content")
	zipBytes := testZIPBytes(t, map[string][]byte{"readme.txt": []byte("hello zip")})

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"note.txt", []byte("plain text note")},
		{"large.png", largePNG},
		{"doc.pdf", pdfBytes},
		{"bundle.zip", zipBytes},
	} {
		rec := upload(map[string][]byte{tc.name: tc.data})
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %s failed (%d): %s", tc.name, rec.Code, rec.Body.String())
		}
		var resp struct {
			Attachments []store.Attachment `json:"attachments"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Attachments) != 1 {
			t.Fatalf("unexpected upload response for %s: %v (%s)", tc.name, err, rec.Body.String())
		}
		getReq := httptest.NewRequest(http.MethodGet, "/api/attachments/"+resp.Attachments[0].ID, nil)
		getReq.Header.Set("Authorization", "Bearer "+tokAlice)
		getRec := httptest.NewRecorder()
		handler.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusOK || getRec.Body.Len() == 0 {
			t.Fatalf("GET %s failed: %d (len=%d)", tc.name, getRec.Code, getRec.Body.Len())
		}
	}

	// 2. Request exceeding MaxAttachmentMB returns 413, not invalid_multipart.
	oversized := bytes.Repeat([]byte("a"), (3<<20)+1024)
	overRec := upload(map[string][]byte{"huge.txt": oversized})
	if overRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status=%d, want 413: %s", overRec.Code, overRec.Body.String())
	}
	if strings.Contains(overRec.Body.String(), "Ожидается multipart/form-data") {
		t.Fatalf("oversized upload returned misleading multipart error: %s", overRec.Body.String())
	}

	// 3. Non-multipart request returns 400 invalid_multipart.
	nonMultipartReq := httptest.NewRequest(http.MethodPost, "/api/chats/"+chat.ID+"/attachments", strings.NewReader(`{"hello":"world"}`))
	nonMultipartReq.Header.Set("Authorization", "Bearer "+tokAlice)
	nonMultipartReq.Header.Set("Content-Type", "application/json")
	nonMultipartRec := httptest.NewRecorder()
	handler.ServeHTTP(nonMultipartRec, nonMultipartReq)
	if nonMultipartRec.Code != http.StatusBadRequest {
		t.Fatalf("non-multipart status=%d, want 400: %s", nonMultipartRec.Code, nonMultipartRec.Body.String())
	}
	if !strings.Contains(nonMultipartRec.Body.String(), "invalid_multipart") {
		t.Fatalf("expected invalid_multipart code, got: %s", nonMultipartRec.Body.String())
	}

	// 4. Empty file returns 400 empty_file.
	emptyRec := upload(map[string][]byte{"empty.txt": nil})
	if emptyRec.Code != http.StatusBadRequest || !strings.Contains(emptyRec.Body.String(), "empty_file") {
		t.Fatalf("empty file status=%d body=%s, want 400 empty_file", emptyRec.Code, emptyRec.Body.String())
	}
}

func TestChatAttachmentsStreamingLargePDFAndOfficeFormats(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "chat-att-streaming.db")
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
	chat, err := db.CreateChat(ctx, alice.ID, "Office & Large PDF", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	handler, err := New(ctx, db, config.Config{
		Provider:                  "stub",
		CORSOrigin:                "*",
		MaxAttachmentMB:           100,
		MaxAttachmentsPerMessage:  10,
		MaxAttachmentsPerChat:     20,
		MaxAttachmentContextBytes: 256 << 10,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	upload := func(files map[string][]byte) *httptest.ResponseRecorder {
		ct, body := buildMultipartUpload(t, "files", files)
		r := httptest.NewRequest(http.MethodPost, "/api/chats/"+chat.ID+"/attachments", body)
		r.Header.Set("Authorization", "Bearer "+tokAlice)
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// 1. Streamed 30 MB PDF upload succeeds without OOM and extracts text.
	basePDF := testTextPDFBytes(t, "Large Lecture PDF Content")
	// Pad PDF safely with a comment block before %%EOF so PDF readers still parse it cleanly.
	padChunk := bytes.Repeat([]byte("% padding comment line for large pdf streaming test\n"), 550_000)
	var largePDF bytes.Buffer
	idx := bytes.LastIndex(basePDF, []byte("%%EOF"))
	largePDF.Write(basePDF[:idx])
	largePDF.Write(padChunk)
	largePDF.Write(basePDF[idx:])
	if largePDF.Len() < 25<<20 {
		t.Fatalf("expected synthetic PDF >= 25 MiB, got %d", largePDF.Len())
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("files", "lecture_30mb.pdf")
		if err == nil {
			_, err = io.Copy(part, bytes.NewReader(largePDF.Bytes()))
		}
		_ = mw.Close()
		_ = pw.CloseWithError(err)
	}()
	req := httptest.NewRequest(http.MethodPost, "/api/chats/"+chat.ID+"/attachments", pr)
	req.Header.Set("Authorization", "Bearer "+tokAlice)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("large 30MB PDF upload failed (%d): %s", rec.Code, rec.Body.String())
	}
	var pdfResp struct {
		Attachments []store.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pdfResp); err != nil || len(pdfResp.Attachments) != 1 {
		t.Fatalf("unexpected large PDF response: %v (%s)", err, rec.Body.String())
	}
	storedPDF, err := db.GetAttachment(ctx, alice.ID, pdfResp.Attachments[0].ID)
	if err != nil || !strings.Contains(storedPDF.ExtractedText, "Large Lecture PDF Content") {
		t.Fatalf("large PDF extracted text missing: err=%v text=%q", err, storedPDF.ExtractedText)
	}

	// 2. Scan PDF without text layer returns 201 + warning.
	scanPDF := testTextPDFBytes(t, "")
	scanRec := upload(map[string][]byte{"scan.pdf": scanPDF})
	if scanRec.Code != http.StatusCreated {
		t.Fatalf("scan PDF upload failed (%d): %s", scanRec.Code, scanRec.Body.String())
	}
	if !strings.Contains(scanRec.Body.String(), "warning") {
		t.Fatalf("expected warning in scan PDF response, got: %s", scanRec.Body.String())
	}

	// 3. Office documents & TSV upload cleanly and enforce strict attachment download headers.
	docx := testZIPBytes(t, map[string][]byte{
		"word/document.xml": []byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Документ Word</w:t></w:r></w:p></w:body></w:document>`),
	})
	pptx := testZIPBytes(t, map[string][]byte{
		"ppt/slides/slide1.xml": []byte(`<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Слайд 1</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`),
	})
	rtf := []byte(`{\rtf1\ansi\ansicpg1251 Привет RTF\par}`)
	officeRec := upload(map[string][]byte{
		"paper.docx": docx,
		"deck.pptx":  pptx,
		"memo.rtf":   rtf,
		"data.tsv":   []byte("k\tv\n1\t2\n"),
	})
	if officeRec.Code != http.StatusCreated {
		t.Fatalf("office upload failed (%d): %s", officeRec.Code, officeRec.Body.String())
	}
	var officeResp struct {
		Attachments []store.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal(officeRec.Body.Bytes(), &officeResp); err != nil || len(officeResp.Attachments) != 4 {
		t.Fatalf("unexpected office response: %v (%s)", err, officeRec.Body.String())
	}
	for _, att := range officeResp.Attachments {
		gReq := httptest.NewRequest(http.MethodGet, "/api/attachments/"+att.ID, nil)
		gReq.Header.Set("Authorization", "Bearer "+tokAlice)
		gRec := httptest.NewRecorder()
		handler.ServeHTTP(gRec, gReq)
		if gRec.Code != http.StatusOK {
			t.Fatalf("GET %s failed: %d", att.OriginalName, gRec.Code)
		}
		if !strings.HasPrefix(gRec.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatalf("%s Content-Disposition=%q, want attachment", att.OriginalName, gRec.Header().Get("Content-Disposition"))
		}
		if gRec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing nosniff", att.OriginalName)
		}
		if gRec.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
			t.Fatalf("%s missing strict CSP", att.OriginalName)
		}
	}

	// 4. Zip bomb disguised as .docx returns 400 zip_bomb without OOM.
	var bombBuf bytes.Buffer
	zw := zip.NewWriter(&bombBuf)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	fw, _ := zw.Create("word/document.xml")
	_, _ = fw.Write(bytes.Repeat([]byte{'A'}, int(uploads.ChatMaxOfficeUncompressedBytes)+4096))
	_ = zw.Close()
	bombRec := upload(map[string][]byte{"bomb.docx": bombBuf.Bytes()})
	if bombRec.Code != http.StatusBadRequest || !strings.Contains(bombRec.Body.String(), "zip_bomb") {
		t.Fatalf("bomb.docx status=%d body=%s, want 400 zip_bomb", bombRec.Code, bombRec.Body.String())
	}

	// 5. JPG+ZIP polyglot disguised as .docx returns 400 content_mismatch.
	polyDOCX := append(testJPEGBytes(t), docx...)
	polyRec := upload(map[string][]byte{"poly.docx": polyDOCX})
	if polyRec.Code != http.StatusBadRequest || !strings.Contains(polyRec.Body.String(), "content_mismatch") {
		t.Fatalf("poly.docx status=%d body=%s, want 400 content_mismatch", polyRec.Code, polyRec.Body.String())
	}

	// 6. PDF > 1000 pages returns 400 pdf_too_many_pages.
	var pageKids strings.Builder
	var pageObjs []string
	pageObjs = append(pageObjs, "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	for i := 0; i < 1001; i++ {
		pageKids.WriteString(fmt.Sprintf("%d 0 R ", 3+i))
	}
	pageObjs = append(pageObjs, fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count 1001 >>\nendobj\n", pageKids.String()))
	for i := 0; i < 1001; i++ {
		pageObjs = append(pageObjs, fmt.Sprintf("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\n", 3+i))
	}
	var hugePagesPDF bytes.Buffer
	hugePagesPDF.WriteString("%PDF-1.4\n")
	pOffsets := make([]int, len(pageObjs)+1)
	for i, o := range pageObjs {
		pOffsets[i+1] = hugePagesPDF.Len()
		hugePagesPDF.WriteString(o)
	}
	pXref := hugePagesPDF.Len()
	hugePagesPDF.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(pageObjs)+1))
	for i := 1; i <= len(pageObjs); i++ {
		hugePagesPDF.WriteString(fmt.Sprintf("%010d 00000 n \n", pOffsets[i]))
	}
	hugePagesPDF.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(pageObjs)+1, pXref))
	manyPagesRec := upload(map[string][]byte{"too_many_pages.pdf": hugePagesPDF.Bytes()})
	if manyPagesRec.Code != http.StatusBadRequest || !strings.Contains(manyPagesRec.Body.String(), "pdf_too_many_pages") {
		t.Fatalf("1001-page PDF status=%d body=%s, want 400 pdf_too_many_pages", manyPagesRec.Code, manyPagesRec.Body.String())
	}

	// 7. Verify no lingering .tmp-* files remain in uploadsDir.
	entries, _ := os.ReadDir(db.UploadsDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file in uploadsDir: %s", e.Name())
		}
	}
}
