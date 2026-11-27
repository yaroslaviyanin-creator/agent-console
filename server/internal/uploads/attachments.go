package uploads

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dslipak/pdf"
)

const (
	ChatMaxImageBytes           = 20 << 20
	ChatMaxImageDimension       = 8000
	ChatMaxImagePixels    int64 = 64_000_000
	ChatTargetImageMaxDimension = 2048

	ChatMaxTextBytes            = 2 << 20
	ChatMaxPDFBytes             = 100 << 20
	ChatMaxPDFPages             = 1000
	ChatMaxPDFTimeout           = 25 * time.Second
	ChatMaxDocumentBytes        = 100 << 20
	ChatMaxZIPBytes             = 100 << 20
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
	ErrPDFTooManyPages           = errors.New("pdf exceeds maximum page count")
)

// AttachmentKind describes the validated class of a chat upload.
type AttachmentKind string

const (
	KindImage    AttachmentKind = "image"
	KindText     AttachmentKind = "text"
	KindPDF      AttachmentKind = "pdf"
	KindArchive  AttachmentKind = "archive"
	KindDocument AttachmentKind = "document"
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
	MaxExtractedTextBytes   int
	MaxImageDimension       int
	MaxImagePixels          int64
	ReencodeImages          bool
	TargetImageMaxDimension int
	AllowZipArchives        bool
	AllowedExtensions       map[string]AttachmentTypeSpec
}

// ValidatedAttachment holds sanitized metadata, extracted text context, and safe bytes or streamed file path.
type ValidatedAttachment struct {
	Filename      string
	MIMEType      string
	Kind          AttachmentKind
	Size          int64
	Width         int
	Height        int
	Data          []byte
	SourcePath    string
	ExtractedText string
	Warning       string
}

// DefaultChatAttachmentPolicy returns the strict upload policy for chat attachments with default limits.
func DefaultChatAttachmentPolicy() AttachmentPolicy {
	return DefaultChatAttachmentPolicyWithLimits(ChatMaxPDFBytes, ChatMaxExtractedTextBytes)
}

// DefaultChatAttachmentPolicyWithLimits builds the strict chat attachment policy with configurable document size and extracted text budgets.
func DefaultChatAttachmentPolicyWithLimits(maxFileBytes int64, maxExtractedBytes int) AttachmentPolicy {
	if maxFileBytes <= 0 {
		maxFileBytes = ChatMaxPDFBytes
	}
	if maxExtractedBytes <= 0 {
		maxExtractedBytes = ChatMaxExtractedTextBytes
	}
	textMax := min(ChatMaxTextBytes, maxFileBytes)
	imageMax := min(ChatMaxImageBytes, maxFileBytes)
	textSpec := AttachmentTypeSpec{MIMEType: "text/plain; charset=utf-8", Kind: KindText, MaxBytes: textMax}
	allowed := map[string]AttachmentTypeSpec{
		".jpg":  {MIMEType: "image/jpeg", Kind: KindImage, MaxBytes: imageMax},
		".jpeg": {MIMEType: "image/jpeg", Kind: KindImage, MaxBytes: imageMax},
		".png":  {MIMEType: "image/png", Kind: KindImage, MaxBytes: imageMax},
		".webp": {MIMEType: "image/webp", Kind: KindImage, MaxBytes: imageMax},
		".gif":  {MIMEType: "image/gif", Kind: KindImage, MaxBytes: imageMax},
		".pdf":  {MIMEType: "application/pdf", Kind: KindPDF, MaxBytes: maxFileBytes},
		".zip":  {MIMEType: "application/zip", Kind: KindArchive, MaxBytes: maxFileBytes},
		".docx": {MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Kind: KindDocument, MaxBytes: maxFileBytes},
		".xlsx": {MIMEType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Kind: KindDocument, MaxBytes: maxFileBytes},
		".pptx": {MIMEType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Kind: KindDocument, MaxBytes: maxFileBytes},
		".odt":  {MIMEType: "application/vnd.oasis.opendocument.text", Kind: KindDocument, MaxBytes: maxFileBytes},
		".epub": {MIMEType: "application/epub+zip", Kind: KindDocument, MaxBytes: maxFileBytes},
		".rtf":  {MIMEType: "application/rtf", Kind: KindDocument, MaxBytes: min(ChatMaxRTFBytes, maxFileBytes)},
	}
	for _, ext := range []string{
		".txt", ".md", ".csv", ".tsv", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".log",
		".html", ".htm", ".css", ".xml", ".svg",
		".js", ".jsx", ".ts", ".tsx", ".py", ".go", ".c", ".cpp", ".cc", ".h", ".hpp",
		".rs", ".java", ".sql", ".sh", ".cmake", ".dockerfile",
	} {
		allowed[ext] = textSpec
	}

	return AttachmentPolicy{
		MaxBytes:                maxFileBytes,
		MaxExtractedTextBytes:   maxExtractedBytes,
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
// image dimensions/re-encoding, PDF/office/RTF/EPUB text extraction, in-memory ZIP safety, and size limits.
func ValidateAttachment(policy AttachmentPolicy, filename string, data []byte) (ValidatedAttachment, error) {
	if len(data) == 0 {
		return ValidatedAttachment{}, ErrAttachmentContentMismatch
	}
	return validateAttachmentSource(context.Background(), policy, filename, int64(len(data)), data, "")
}

// ValidateAttachmentFile validates an already-streamed upload file on disk without loading large documents into RAM.
func ValidateAttachmentFile(ctx context.Context, policy AttachmentPolicy, filename, filePath string) (ValidatedAttachment, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return ValidatedAttachment{}, err
	}
	if info.Size() <= 0 {
		return ValidatedAttachment{}, ErrAttachmentContentMismatch
	}
	return validateAttachmentSource(ctx, policy, filename, info.Size(), nil, filePath)
}

func validateAttachmentSource(ctx context.Context, policy AttachmentPolicy, filename string, size int64, data []byte, filePath string) (ValidatedAttachment, error) {
	if policy.MaxBytes > 0 && size > policy.MaxBytes {
		return ValidatedAttachment{}, ErrAttachmentTooLarge
	}
	if size <= 0 {
		return ValidatedAttachment{}, ErrAttachmentContentMismatch
	}

	safeName := SanitizeFilename(filename)
	ext := strings.ToLower(filepath.Ext(safeName))
	if ext == "" && strings.EqualFold(safeName, "dockerfile") {
		ext = ".dockerfile"
	}

	head := data
	if head == nil {
		f, err := os.Open(filePath)
		if err != nil {
			return ValidatedAttachment{}, err
		}
		var buf [512]byte
		n, readErr := io.ReadFull(f, buf[:])
		_ = f.Close()
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return ValidatedAttachment{}, readErr
		}
		head = buf[:n]
	}

	if disallowedArchiveExtensions[ext] {
		return ValidatedAttachment{}, ErrArchiveDisallowed
	}
	if blockedExtensions[ext] || looksLikeExecutable(head) {
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
	spec, ok := policy.AllowedExtensions[ext]
	if !ok {
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
	if spec.MaxBytes > 0 && size > spec.MaxBytes {
		return ValidatedAttachment{}, ErrAttachmentTooLarge
	}

	maxExtracted := policy.MaxExtractedTextBytes
	if maxExtracted <= 0 {
		maxExtracted = ChatMaxExtractedTextBytes
	}

	readFullBytes := func() ([]byte, error) {
		if data != nil {
			return data, nil
		}
		return os.ReadFile(filePath)
	}

	switch spec.Kind {
	case KindImage:
		raw, err := readFullBytes()
		if err != nil {
			return ValidatedAttachment{}, err
		}
		expected := strings.TrimPrefix(spec.MIMEType, "image/")
		if policy.ReencodeImages {
			clean, outMIME, w, h, err := SanitizeRasterImage(
				raw, expected, policy.MaxImageDimension, policy.MaxImagePixels, policy.TargetImageMaxDimension,
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
		if DetectImageFormat(raw) != expected {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		return ValidatedAttachment{
			Filename: safeName,
			MIMEType: spec.MIMEType,
			Kind:     KindImage,
			Size:     int64(len(raw)),
			Data:     raw,
		}, nil

	case KindText:
		raw, err := readFullBytes()
		if err != nil {
			return ValidatedAttachment{}, err
		}
		if looksLikeArchive(raw) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		if ext == ".json" && !json.Valid(raw) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		extracted := clipValidUTF8(string(raw), maxExtracted)
		warning := ""
		if len(raw) > maxExtracted {
			warning = "Текст файла усечён по лимиту контекста."
		}
		// Force text/plain for all text/source/HTML/SVG/XML attachments so browsers never execute them.
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      "text/plain; charset=utf-8",
			Kind:          KindText,
			Size:          int64(len(raw)),
			Data:          raw,
			ExtractedText: extracted,
			Warning:       warning,
		}, nil

	case KindPDF:
		if !bytes.HasPrefix(head, []byte("%PDF-")) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		var (
			extracted string
			warning   string
			err       error
		)
		if filePath != "" {
			extracted, warning, err = extractPDFTextFromFile(ctx, filePath, size, maxExtracted)
		} else {
			extracted, warning, err = extractPDFTextSafeFromReader(bytes.NewReader(data), size, maxExtracted)
		}
		if err != nil {
			return ValidatedAttachment{}, err
		}
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      "application/pdf",
			Kind:          KindPDF,
			Size:          size,
			Data:          data,
			SourcePath:    filePath,
			ExtractedText: extracted,
			Warning:       warning,
		}, nil

	case KindArchive:
		if !policy.AllowZipArchives {
			return ValidatedAttachment{}, ErrArchiveDisallowed
		}
		// Require ZIP magic bytes strictly at offset 0 so JPG+ZIP polyglots are rejected as .zip.
		if !isZipMagicAtZero(head) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		var (
			extracted string
			err       error
		)
		if filePath != "" {
			f, openErr := os.Open(filePath)
			if openErr != nil {
				return ValidatedAttachment{}, openErr
			}
			extracted, err = inspectZipReader(f, size, maxExtracted)
			_ = f.Close()
		} else {
			extracted, err = inspectZipReader(bytes.NewReader(data), size, maxExtracted)
		}
		if err != nil {
			return ValidatedAttachment{}, err
		}
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      "application/zip",
			Kind:          KindArchive,
			Size:          size,
			Data:          data,
			SourcePath:    filePath,
			ExtractedText: extracted,
		}, nil

	case KindDocument:
		var (
			extracted string
			warning   string
			err       error
		)
		if ext == ".rtf" {
			if !bytes.HasPrefix(head, []byte(`{\rtf`)) {
				return ValidatedAttachment{}, ErrAttachmentContentMismatch
			}
			raw, readErr := readFullBytes()
			if readErr != nil {
				return ValidatedAttachment{}, readErr
			}
			extracted, warning, err = extractRTFText(raw, maxExtracted)
		} else if filePath != "" {
			f, openErr := os.Open(filePath)
			if openErr != nil {
				return ValidatedAttachment{}, openErr
			}
			extracted, warning, err = extractOfficeDocument(ext, f, size, maxExtracted)
			_ = f.Close()
		} else {
			extracted, warning, err = extractOfficeDocument(ext, bytes.NewReader(data), size, maxExtracted)
		}
		if err != nil {
			return ValidatedAttachment{}, err
		}
		return ValidatedAttachment{
			Filename:      safeName,
			MIMEType:      spec.MIMEType,
			Kind:          KindDocument,
			Size:          size,
			Data:          data,
			SourcePath:    filePath,
			ExtractedText: extracted,
			Warning:       warning,
		}, nil

	default:
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
}

func isZipMagicAtZero(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x03, 0x04}) ||
		bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x05, 0x06})
}

func minimalPopplerEnv() []string {
	return []string{"LC_ALL=C.UTF-8", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin"}
}

func pdfPageCountWithPoppler(ctx context.Context, filePath string) (int, bool, error) {
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		return 0, false, nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, ChatMaxPDFTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "pdfinfo", "--", filePath)
	cmd.Env = minimalPopplerEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, remaining: 64 << 10}
	if err := cmd.Run(); err != nil {
		return 0, true, err
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "Pages:"))
			if n, err := strconv.Atoi(raw); err == nil {
				return n, true, nil
			}
		}
	}
	return 0, true, nil
}

type limitedWriter struct {
	w         io.Writer
	remaining int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return len(p), nil
	}
	n := int64(len(p))
	if n > l.remaining {
		p = p[:l.remaining]
	}
	written, err := l.w.Write(p)
	l.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return len(p), nil
}

func extractPDFTextFromFile(ctx context.Context, filePath string, size int64, maxBytes int) (string, string, error) {
	if pages, usedPoppler, err := pdfPageCountWithPoppler(ctx, filePath); usedPoppler && err == nil && pages > ChatMaxPDFPages {
		return "", "", ErrPDFTooManyPages
	}

	if _, err := exec.LookPath("pdftotext"); err == nil {
		cmdCtx, cancel := context.WithTimeout(ctx, ChatMaxPDFTimeout)
		defer cancel()
		cmd := exec.CommandContext(cmdCtx, "pdftotext", "-enc", "UTF-8", "--", filePath, "-")
		cmd.Env = minimalPopplerEnv()
		stdout, pipeErr := cmd.StdoutPipe()
		if pipeErr == nil && cmd.Start() == nil {
			raw, readErr := io.ReadAll(io.LimitReader(stdout, int64(maxBytes)+1))
			waitErr := cmd.Wait()
			if readErr == nil && (waitErr == nil || len(raw) > maxBytes) {
				return finalizePDFText(raw, maxBytes), pdfWarning(raw, maxBytes), nil
			}
		}
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	return extractPDFTextSafeFromReader(f, size, maxBytes)
}

func finalizePDFText(raw []byte, maxBytes int) string {
	truncated := len(raw) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
	if text == "" {
		return "[PDF не содержит текстового слоя (вероятно, скан или изображения).]"
	}
	if truncated {
		const note = "\n\n[Текст PDF усечён по лимиту размера.]"
		return clipValidUTF8(text, max(128, maxBytes-len(note))) + note
	}
	return text
}

func pdfWarning(raw []byte, maxBytes int) string {
	truncated := len(raw) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
	if text == "" {
		return "В PDF не найден текстовый слой — возможно, это скан."
	}
	if truncated {
		return "Текст PDF усечён по лимиту контекста."
	}
	return ""
}

func extractPDFTextSafeFromReader(r io.ReaderAt, size int64, maxBytes int) (out string, warning string, retErr error) {
	defer func() {
		if recover() != nil {
			out = "[Не удалось извлечь текстовый слой PDF.]"
			warning = "Не удалось извлечь текстовый слой PDF."
			retErr = nil
		}
	}()
	reader, err := pdf.NewReader(r, size)
	if err != nil {
		return "[Не удалось разобрать структуру PDF.]", "Не удалось разобрать структуру PDF.", nil
	}
	if reader.NumPage() > ChatMaxPDFPages {
		return "", "", ErrPDFTooManyPages
	}
	plainReader, err := reader.GetPlainText()
	if err != nil {
		return "[PDF не содержит читаемого текстового слоя.]", "В PDF не найден текстовый слой — возможно, это скан.", nil
	}
	raw, err := io.ReadAll(io.LimitReader(plainReader, int64(maxBytes)+1))
	if err != nil {
		return "[Ошибка чтения текстового слоя PDF.]", "Ошибка чтения текстового слоя PDF.", nil
	}
	return finalizePDFText(raw, maxBytes), pdfWarning(raw, maxBytes), nil
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

func inspectZipReader(r io.ReaderAt, size int64, maxExtracted int) (string, error) {
	if maxExtracted <= 0 {
		maxExtracted = ChatMaxExtractedTextBytes
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", ErrAttachmentContentMismatch
	}
	if len(zr.File) > ChatMaxZIPEntries {
		return "", ErrZipBomb
	}

	var totalRead int64
	var tree []string
	var contents strings.Builder
	contentBudget := maxExtracted - 8192
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
	return clipValidUTF8(result.String(), maxExtracted), nil
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
