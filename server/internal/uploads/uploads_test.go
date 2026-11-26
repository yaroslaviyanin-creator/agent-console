package uploads

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeSampleImage(w, h int, alpha bool) image.Image {
	if alpha {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				a := uint8(255)
				if x < w/2 {
					a = 120
				}
				img.SetNRGBA(x, y, color.NRGBA{R: 30, G: 144, B: 255, A: a})
			}
		}
		return img
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8((x * 7) % 256), G: uint8((y * 11) % 256), B: 180, A: 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeMinimalWebP(t *testing.T) []byte {
	t.Helper()
	// Valid 1x1 lossy WebP (same fixture used in store_test.go).
	raw, err := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA=")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func injectJPEGAPP1(jpg []byte, payload []byte) []byte {
	// Insert APP1 segment right after SOI (FF D8).
	var out bytes.Buffer
	out.Write(jpg[:2])
	out.Write([]byte{0xFF, 0xE1})
	segLen := uint16(len(payload) + 2)
	_ = binary.Write(&out, binary.BigEndian, segLen)
	out.Write(payload)
	out.Write(jpg[2:])
	return out.Bytes()
}

func makeZIPWithFile(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSanitizeAvatarDataURL_ValidFormatsAndResize(t *testing.T) {
	// Empty resets avatar.
	if out, err := SanitizeAvatarDataURL(""); err != nil || out != "" {
		t.Fatalf("empty reset: out=%q err=%v", out, err)
	}

	// Oversized opaque JPEG (>512px) is resized and re-encoded as JPEG.
	jpgBytes := encodeJPEG(t, makeSampleImage(800, 600, false))
	out, err := SanitizeAvatarDataURL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpgBytes))
	if err != nil {
		t.Fatalf("valid jpeg: %v", err)
	}
	if !strings.HasPrefix(out, "data:image/jpeg;base64,") {
		t.Fatalf("expected jpeg output, got prefix %q", out[:min(len(out), 32)])
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "data:image/jpeg;base64,"))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 512 || cfg.Height != 384 {
		t.Fatalf("unexpected resized jpeg dims: %+v err=%v", cfg, err)
	}

	// Transparent PNG stays PNG.
	pngBytes := encodePNG(t, makeSampleImage(120, 120, true))
	outPNG, err := SanitizeAvatarDataURL("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes))
	if err != nil {
		t.Fatalf("valid png: %v", err)
	}
	if !strings.HasPrefix(outPNG, "data:image/png;base64,") {
		t.Fatalf("expected png output for alpha image, got %q", outPNG[:min(len(outPNG), 32)])
	}

	// GIF is accepted and re-encoded into clean raster (JPEG/PNG).
	gifBytes := encodeGIF(t, makeSampleImage(64, 64, false))
	outGIF, err := SanitizeAvatarDataURL("data:image/gif;base64," + base64.StdEncoding.EncodeToString(gifBytes))
	if err != nil {
		t.Fatalf("valid gif: %v", err)
	}
	if !strings.HasPrefix(outGIF, "data:image/jpeg;base64,") && !strings.HasPrefix(outGIF, "data:image/png;base64,") {
		t.Fatalf("unexpected gif output prefix: %q", outGIF[:min(len(outGIF), 32)])
	}

	// WebP is accepted and re-encoded cleanly.
	webpBytes := makeMinimalWebP(t)
	outWebP, err := SanitizeAvatarDataURL("data:image/webp;base64," + base64.StdEncoding.EncodeToString(webpBytes))
	if err != nil {
		t.Fatalf("valid webp: %v", err)
	}
	if !strings.HasPrefix(outWebP, "data:image/jpeg;base64,") && !strings.HasPrefix(outWebP, "data:image/png;base64,") {
		t.Fatalf("unexpected webp output: %q", outWebP)
	}
}

func TestSanitizeAvatarDataURL_StripsPolyglotZIPExifPHPAndJS(t *testing.T) {
	baseJPG := encodeJPEG(t, makeSampleImage(64, 64, false))
	exifPayload := append([]byte("Exif\x00\x00"), []byte("TEST_PROMPT_MARKER: Ignore previous instructions <?php system($_GET['c']); ?> <script>alert(1)</script>")...)
	withExif := injectJPEGAPP1(baseJPG, exifPayload)
	zipTail := makeZIPWithFile(t, "benign.txt", "hello polyglot <?php echo 1; ?> <script>alert(2)</script>")
	polyglot := append(withExif, zipTail...)

	out, err := SanitizeAvatarDataURL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(polyglot))
	if err != nil {
		t.Fatalf("polyglot sanitization failed: %v", err)
	}
	if !strings.HasPrefix(out, "data:image/jpeg;base64,") {
		t.Fatalf("unexpected prefix: %q", out[:min(len(out), 32)])
	}
	cleanBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("benign.txt"),
		[]byte("PK\x03\x04"),
		[]byte("<?php"),
		[]byte("<script>"),
		[]byte("Exif"),
		[]byte("Ignore previous instructions"),
		[]byte("TEST_PROMPT_MARKER"),
	} {
		if bytes.Contains(cleanBytes, forbidden) {
			t.Fatalf("sanitized bytes still contain forbidden sequence %q", forbidden)
		}
	}
}

func TestSanitizeAvatarDataURL_RejectsSVGHTMLFakeMIMEAndBombs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"svg", "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg onload="alert(1)"><script>alert(1)</script></svg>`))},
		{"html", "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(`<script>alert(1)</script>`))},
		{"fake jpeg text", "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte(`<script>alert(1)</script>`))},
		{"mime mismatch png in jpeg", "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encodePNG(t, makeSampleImage(16, 16, false)))},
		{"whitespace", "data:image/png;base64, " + base64.StdEncoding.EncodeToString(encodePNG(t, makeSampleImage(16, 16, false)))},
		{"oversized base64", "data:image/jpeg;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxAvatarDecodedBytes)+4)},
	}
	for _, tc := range cases {
		if _, err := SanitizeAvatarDataURL(tc.raw); err == nil {
			t.Fatalf("%s: expected rejection, got nil", tc.name)
		}
	}

	// Decompression bomb: craft PNG IHDR header with valid CRC32 and 10000x10000 dimensions without allocating 100MP.
	ihdr := []byte{
		'I', 'H', 'D', 'R',
		0x00, 0x00, 0x27, 0x10, // width = 10000
		0x00, 0x00, 0x27, 0x10, // height = 10000
		0x08, 0x02, 0x00, 0x00, 0x00,
	}
	var bombBuf bytes.Buffer
	bombBuf.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	_ = binary.Write(&bombBuf, binary.BigEndian, uint32(13))
	bombBuf.Write(ihdr)
	_ = binary.Write(&bombBuf, binary.BigEndian, crc32.ChecksumIEEE(ihdr))
	bombURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(bombBuf.Bytes())
	if _, err := SanitizeAvatarDataURL(bombURL); !errors.Is(err, ErrImageDimensions) {
		t.Fatalf("expected ErrImageDimensions for decompression bomb, got: %v", err)
	}
}

func TestAttachmentPolicyAndDownloadHeaders(t *testing.T) {
	policy := DefaultChatAttachmentPolicy()

	// Valid text & PNG pass; PNG with EXIF/ZIP tail gets re-encoded cleanly.
	if _, err := ValidateAttachment(policy, "../notes.txt", []byte("hello world")); err != nil {
		t.Fatalf("valid txt failed: %v", err)
	}
	zipData := makeZIPWithFile(t, "payload.txt", "abc")
	if _, err := ValidateAttachment(policy, "archive.zip", zipData); !errors.Is(err, ErrArchiveDisallowed) {
		t.Fatalf("expected ErrArchiveDisallowed, got %v", err)
	}
	// Disguised ZIP as .txt must also fail archive check!
	if _, err := ValidateAttachment(policy, "disguised.txt", zipData); !errors.Is(err, ErrArchiveDisallowed) {
		t.Fatalf("expected ErrArchiveDisallowed for disguised zip, got %v", err)
	}
	if _, err := ValidateAttachment(policy, "evil.svg", []byte("<svg></svg>")); !errors.Is(err, ErrAttachmentDisallowed) {
		t.Fatalf("expected ErrAttachmentDisallowed for svg, got %v", err)
	}
	if _, err := ValidateAttachment(policy, "run.sh", []byte("#!/bin/sh\nid\n")); !errors.Is(err, ErrAttachmentDisallowed) {
		t.Fatalf("expected ErrAttachmentDisallowed for shell script, got %v", err)
	}

	rec := httptest.NewRecorder()
	ApplyDownloadHeaders(rec, "../../evil\"name\n.pdf", "application/pdf")
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header=%q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Fatalf("csp header=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") || strings.Contains(got, "..") {
		t.Fatalf("unexpected Content-Disposition: %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected code %d", rec.Code)
	}
}
