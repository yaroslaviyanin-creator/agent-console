package uploads

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dslipak/pdf"
)

const (
	ChatMaxImageBytes           = 10 << 20
	ChatMaxImageDimension       = 8192
	ChatMaxImagePixels    int64 = 40_000_000
	ChatTargetImageMaxDimension = 2048

	ChatMaxTextBytes            = 2 << 20
	ChatMaxPDFBytes             = 10 << 20
	ChatMaxZIPBytes             = 10 << 20
	ChatMaxZIPUncompressedBytes = 5 << 20
	ChatMaxZIPEntries           = 150
	ChatMaxZIPRatio             = 100
	ChatMaxZIPRatioMinBytes     = 1 << 20
	ChatMaxExtractedTextBytes   = 256 << 10
)

var (
	ErrAttachmentTooLarge        = errors.New("attachment exceeds maximum allowed size")
	ErrAttachmentDisallowed      = errors.New("attachment file type is not allowed")
	ErrArchiveDisallowed         = errors.New("unsupported archive format")
	ErrAttachmentContentMismatch = errors.New("attachment content does not match declared file type")
	ErrZipSlip                   = errors.New("archive contains unsafe file path")
	ErrZipBomb                   = errors.New("archive exceeds decompression safety limits")
)

// AttachmentKind describes the validated class of a chat upload.
type AttachmentKind string

const (
	KindImage   AttachmentKind = "image"
	KindText    AttachmentKind = "text"
	KindPDF     AttachmentKind = "pdf"
	KindArchive AttachmentKind = "archive"
)

// AttachmentTypeSpec defines an allowlisted file extension and content validator.
type AttachmentTypeSpec struct {
	MIMEType string
	Kind     AttachmentKind
	MaxBytes int64
}

// AttachmentPolicy controls validation and sanitization for chat file uploads.
// All upload endpoints must validate through this policy and never unpack archives to disk.
type AttachmentPolicy struct {
	MaxBytes                int64
	MaxImageDimension       int
	MaxImagePixels          int64
	ReencodeImages          bool
	TargetImageMaxDimension int
	AllowZipArchives        bool
	AllowedExtensions       map[string]AttachmentTypeSpec
}

// ValidatedAttachment holds sanitized metadata, extracted text context, and safe bytes.
type ValidatedAttachment struct {
	Filename      string
	MIMEType      string
	Kind          AttachmentKind
	Size          int64
	Width         int
	Height        int
	Data          []byte
	ExtractedText string
}

// DefaultChatAttachmentPolicy returns the strict upload policy for chat attachments.
func DefaultChatAttachmentPolicy() AttachmentPolicy {
	textSpec := AttachmentTypeSpec{MIMEType: "text/plain; charset=utf-8", Kind: KindText, MaxBytes: ChatMaxTextBytes}
	allowed := map[string]AttachmentTypeSpec{
		".jpg":  {MIMEType: "image/jpeg", Kind: KindImage, MaxBytes: ChatMaxImageBytes},
		".jpeg": {MIMEType: "image/jpeg", Kind: KindImage, MaxBytes: ChatMaxImageBytes},
		".png":  {MIMEType: "image/png", Kind: KindImage, MaxBytes: ChatMaxImageBytes},
		".webp": {MIMEType: "image/webp", Kind: KindImage, MaxBytes: ChatMaxImageBytes},
		".gif":  {MIMEType: "image/gif", Kind: KindImage, MaxBytes: ChatMaxImageBytes},
		".pdf":  {MIMEType: "application/pdf", Kind: KindPDF, MaxBytes: ChatMaxPDFBytes},
		".zip":  {MIMEType: "application/zip", Kind: KindArchive, MaxBytes: ChatMaxZIPBytes},
	}
	for _, ext := range []string{
		".txt", ".md", ".csv", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".log",
		".html", ".htm", ".css", ".xml", ".svg",
		".js", ".jsx", ".ts", ".tsx", ".py", ".go", ".c", ".cpp", ".cc", ".h", ".hpp",
		".rs", ".java", ".sql", ".sh", ".cmake", ".dockerfile",
	} {
		allowed[ext] = textSpec
	}

	return AttachmentPolicy{
		MaxBytes:                ChatMaxImageBytes,
		MaxImageDimension:       ChatMaxImageDimension,
		MaxImagePixels:          ChatMaxImagePixels,
		ReencodeImages:          true,
		TargetImageMaxDimension: ChatTargetImageMaxDimension,
		AllowZipArchives:        true,
		AllowedExtensions:       allowed,
	}
}

var blockedExtensions = map[string]bool{
	".svgz": true, ".xhtml": true, ".mhtml": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
	".apk": true, ".dmg": true, ".msi": true, ".jar": true, ".war": true, ".wasm": true,
	".com": true, ".scr": true, ".cpl": true, ".sys": true,
}

var disallowedArchiveExtensions = map[string]bool{
	".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true,
}

// ValidateAttachment checks filename extension, magic bytes, archive/executable signatures,
// image dimensions/re-encoding, PDF text extraction, in-memory ZIP safety, and size limits.
func ValidateAttachment(policy AttachmentPolicy, filename string, data []byte) (ValidatedAttachment, error) {
	if policy.MaxBytes > 0 && int64(len(data)) > policy.MaxBytes {
		return ValidatedAttachment{}, ErrAttachmentTooLarge
	}
	if len(data) == 0 {
		return ValidatedAttachment{}, ErrAttachmentContentMismatch
	}

	safeName := SanitizeFilename(filename)
	ext := strings.ToLower(filepath.Ext(safeName))
	if ext == "" && strings.EqualFold(safeName, "dockerfile") {
		ext = ".dockerfile"
	}

	if disallowedArchiveExtensions[ext] {
		return ValidatedAttachment{}, ErrArchiveDisallowed
	}
	if blockedExtensions[ext] || looksLikeExecutable(data) {
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
	spec, ok := policy.AllowedExtensions[ext]
	if !ok {
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
	if spec.MaxBytes > 0 && int64(len(data)) > spec.MaxBytes {
		return ValidatedAttachment{}, ErrAttachmentTooLarge
	}

	switch spec.Kind {
	case KindImage:
		expected := strings.TrimPrefix(spec.MIMEType, "image/")
		if policy.ReencodeImages {
			clean, outMIME, w, h, err := SanitizeRasterImage(
				data, expected, policy.MaxImageDimension, policy.MaxImagePixels, policy.TargetImageMaxDimension,
			)
			if err != nil {
				return ValidatedAttachment{}, err
			}
			return ValidatedAttachment{
				Filename: replaceExtForMIME(safeName, outMIME),
				MIMEType: outMIME,
				Kind:     KindImage,
				Size:     int64(len(clean)),
				Width:    w,
				Height:   h,
				Data:     clean,
			}, nil
		}
		if DetectImageFormat(data) != expected {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		return ValidatedAttachment{
			Filename: safeName,
			MIMEType: spec.MIMEType,
			Kind:     KindImage,
			Size:     int64(len(data)),
			Data:     data,
		}, nil

	case KindText:
		if looksLikeArchive(data) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		if ext == ".json" && !json.Valid(data) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		extracted := clipValidUTF8(string(data), ChatMaxExtractedTextBytes)
		// Force text/plain for all text/source/HTML/SVG/XML attachments so browsers never execute them.
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      "text/plain; charset=utf-8",
			Kind:          KindText,
			Size:          int64(len(data)),
			Data:          data,
			ExtractedText: extracted,
		}, nil

	case KindPDF:
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		extracted := extractPDFTextSafe(data, ChatMaxExtractedTextBytes)
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      "application/pdf",
			Kind:          KindPDF,
			Size:          int64(len(data)),
			Data:          data,
			ExtractedText: extracted,
		}, nil

	case KindArchive:
		if !policy.AllowZipArchives {
			return ValidatedAttachment{}, ErrArchiveDisallowed
		}
		// Require ZIP magic bytes strictly at offset 0 so JPG+ZIP polyglots are rejected as .zip.
		if !isZipMagicAtZero(data) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		extracted, err := inspectZipInMemory(data)
		if err != nil {
			return ValidatedAttachment{}, err
		}
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      "application/zip",
			Kind:          KindArchive,
			Size:          int64(len(data)),
			Data:          data,
			ExtractedText: extracted,
		}, nil

	default:
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
}

func isZipMagicAtZero(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x03, 0x04}) ||
		bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x05, 0x06})
}

func extractPDFTextSafe(data []byte, maxBytes int) (out string) {
	defer func() {
		if recover() != nil {
			out = "[Не удалось извлечь текстовый слой PDF.]"
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "[Не удалось разобрать структуру PDF.]"
	}
	plainReader, err := reader.GetPlainText()
	if err != nil {
		return "[PDF не содержит читаемого текстового слоя.]"
	}
	raw, err := io.ReadAll(io.LimitReader(plainReader, int64(maxBytes)+1))
	if err != nil {
		return "[Ошибка чтения текстового слоя PDF.]"
	}
	truncated := len(raw) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
	if text == "" {
		return "[PDF не содержит текстового слоя (вероятно, скан или изображения).]"
	}
	if truncated {
		return clipValidUTF8(text, maxBytes-64) + "\n\n[Текст PDF усечён по лимиту размера.]"
	}
	return text
}

var ignoredZipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".venv": true, "dist": true, "build": true, "__macosx": true,
}

func validateZipEntryPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || strings.ContainsRune(raw, '\\') {
		return "", ErrZipSlip
	}
	if strings.HasPrefix(raw, "/") {
		return "", ErrZipSlip
	}
	if len(raw) >= 2 && ((raw[0] >= 'a' && raw[0] <= 'z') || (raw[0] >= 'A' && raw[0] <= 'Z')) && raw[1] == ':' {
		return "", ErrZipSlip
	}
	parts := strings.Split(strings.TrimSuffix(raw, "/"), "/")
	for _, seg := range parts {
		if seg == ".." {
			return "", ErrZipSlip
		}
	}
	cleaned := path.Clean(strings.TrimSuffix(raw, "/"))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", ErrZipSlip
	}
	return cleaned, nil
}

func hasIgnoredZipDir(cleanPath string) bool {
	for _, seg := range strings.Split(cleanPath, "/") {
		if ignoredZipDirs[strings.ToLower(seg)] {
			return true
		}
	}
	return false
}

func isNestedArchiveName(cleanPath string) bool {
	ext := strings.ToLower(path.Ext(cleanPath))
	return ext == ".zip" || disallowedArchiveExtensions[ext]
}

func inspectZipInMemory(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", ErrAttachmentContentMismatch
	}
	if len(zr.File) > ChatMaxZIPEntries {
		return "", ErrZipBomb
	}

	var totalRead int64
	var tree []string
	var contents strings.Builder
	contentBudget := ChatMaxExtractedTextBytes - 8192
	if contentBudget < 16<<10 {
		contentBudget = 16 << 10
	}
	contentTruncated := false

	for _, f := range zr.File {
		cleanPath, err := validateZipEntryPath(f.Name)
		if err != nil {
			return "", err
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if cleanPath == "." {
			continue
		}
		// Reject symlinks as unsafe archive entries.
		if f.Mode()&0o170000 == 0o120000 {
			return "", ErrZipSlip
		}
		if hasIgnoredZipDir(cleanPath) {
			tree = append(tree, fmt.Sprintf("- %s [пропущен: служебный каталог]", cleanPath))
			continue
		}
		if isNestedArchiveName(cleanPath) {
			tree = append(tree, fmt.Sprintf("- %s [пропущен: вложенный архив]", cleanPath))
			continue
		}

		remaining := ChatMaxZIPUncompressedBytes - totalRead
		if remaining <= 0 {
			return "", ErrZipBomb
		}
		rc, err := f.Open()
		if err != nil {
			return "", ErrAttachmentContentMismatch
		}
		entryBytes, readErr := io.ReadAll(io.LimitReader(rc, remaining+1))
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil {
			return "", ErrAttachmentContentMismatch
		}

		totalRead += int64(len(entryBytes))
		if totalRead > ChatMaxZIPUncompressedBytes {
			return "", ErrZipBomb
		}

		// Check actual compression ratio on large entries without trusting UncompressedSize64.
		if int64(len(entryBytes)) > ChatMaxZIPRatioMinBytes {
			comp := int64(f.CompressedSize64)
			if comp == 0 || int64(len(entryBytes))/comp > ChatMaxZIPRatio {
				return "", ErrZipBomb
			}
		}

		if looksLikeExecutable(entryBytes) || looksLikeArchive(entryBytes) ||
			!utf8.Valid(entryBytes) || bytes.IndexByte(entryBytes, 0) >= 0 {
			tree = append(tree, fmt.Sprintf("- %s (%d байт) [пропущен: бинарный файл]", cleanPath, len(entryBytes)))
			continue
		}

		tree = append(tree, fmt.Sprintf("- %s (%d байт)", cleanPath, len(entryBytes)))
		if contentTruncated {
			continue
		}
		header := "\n=== FILE: " + cleanPath + " ===\n"
		text := string(entryBytes)
		if contents.Len()+len(header)+len(text) > contentBudget {
			avail := contentBudget - contents.Len() - len(header)
			if avail > 64 {
				contents.WriteString(header)
				contents.WriteString(clipValidUTF8(text, avail))
				contents.WriteString("\n[Содержимое обрезано по лимиту размера.]\n")
			}
			contentTruncated = true
			continue
		}
		contents.WriteString(header)
		contents.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			contents.WriteByte('\n')
		}
	}

	var result strings.Builder
	result.WriteString("Дерево файлов архива:\n")
	if len(tree) == 0 {
		result.WriteString("(пустой архив)\n")
	} else {
		for _, line := range tree {
			result.WriteString(line)
			result.WriteByte('\n')
		}
	}
	if contents.Len() > 0 {
		result.WriteString("\nСодержимое текстовых файлов:\n")
		result.WriteString(contents.String())
	}
	if contentTruncated {
		result.WriteString("\n[Часть текстовых файлов архива опущена из-за лимита размера контекста.]\n")
	}
	return clipValidUTF8(result.String(), ChatMaxExtractedTextBytes), nil
}

func clipValidUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// SanitizeFilename strips directory paths and control characters for safe Content-Disposition use.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == ".." || base == "/" {
		return "attachment"
	}
	var b strings.Builder
	b.Grow(len(base))
	for _, r := range base {
		if unicode.IsControl(r) || r == '"' || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "attachment"
	}
	return cleaned
}

// DownloadHeaders returns strict security headers for serving user-uploaded attachments.
// Text attachments (including .html/.svg/.xml) are always served as text/plain attachment.
func DownloadHeaders(filename, mimeType string, kind AttachmentKind, inline bool) http.Header {
	h := make(http.Header)
	safeName := SanitizeFilename(filename)
	dispType := "attachment"
	if kind == KindImage && inline {
		dispType = "inline"
	}
	if kind == KindText {
		mimeType = "text/plain; charset=utf-8"
		dispType = "attachment"
	}
	disposition := mime.FormatMediaType(dispType, map[string]string{"filename": safeName})
	if disposition == "" {
		disposition = dispType + `; filename="attachment"`
	}
	h.Set("Content-Type", mimeType)
	h.Set("Content-Disposition", disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	return h
}

// ApplyDownloadHeaders writes strict download headers to w before streaming attachment bytes.
func ApplyDownloadHeaders(w http.ResponseWriter, filename, mimeType string, kind AttachmentKind, inline bool) {
	for k, vals := range DownloadHeaders(filename, mimeType, kind, inline) {
		for _, v := range vals {
			w.Header().Set(k, v)
		}
	}
}

func replaceExtForMIME(filename, mimeType string) string {
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	if stem == "" {
		stem = "image"
	}
	switch mimeType {
	case "image/png":
		return stem + ".png"
	case "image/jpeg":
		return stem + ".jpg"
	default:
		return filename
	}
}

func looksLikeArchive(data []byte) bool {
	return isZipMagicAtZero(data) ||
		bytes.HasPrefix(data, []byte("PK\x07\x08")) ||
		bytes.HasPrefix(data, []byte{0x1F, 0x8B}) ||
		bytes.HasPrefix(data, []byte("Rar!\x1A\x07")) ||
		bytes.HasPrefix(data, []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}) ||
		(len(data) > 262 && bytes.Equal(data[257:262], []byte("ustar")))
}

func looksLikeExecutable(data []byte) bool {
	return bytes.HasPrefix(data, []byte("MZ")) ||
		bytes.HasPrefix(data, []byte{0x7F, 'E', 'L', 'F'}) ||
		bytes.HasPrefix(data, []byte{0xFE, 0xED, 0xFA, 0xCE}) ||
		bytes.HasPrefix(data, []byte{0xFE, 0xED, 0xFA, 0xCF}) ||
		bytes.HasPrefix(data, []byte{0xCF, 0xFA, 0xED, 0xFE}) ||
		bytes.HasPrefix(data, []byte{0xCA, 0xFE, 0xBA, 0xBE}) ||
		bytes.HasPrefix(data, []byte{0x00, 'a', 's', 'm'})
}
