package uploads

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrAttachmentTooLarge    = errors.New("attachment exceeds maximum allowed size")
	ErrAttachmentDisallowed  = errors.New("attachment file type is not allowed")
	ErrArchiveDisallowed     = errors.New("archive files and server-side extraction are prohibited")
	ErrAttachmentContentMismatch = errors.New("attachment content does not match declared file type")
)

// AttachmentKind describes the validated class of a chat upload.
type AttachmentKind string

const (
	KindImage    AttachmentKind = "image"
	KindDocument AttachmentKind = "document"
	KindText     AttachmentKind = "text"
)

// AttachmentTypeSpec defines an allowlisted file extension and content validator.
type AttachmentTypeSpec struct {
	MIMEType string
	Kind     AttachmentKind
}

// AttachmentPolicy controls validation and sanitization for future chat file uploads.
// Any chat upload endpoint must validate through this policy and never unpack archives server-side.
type AttachmentPolicy struct {
	MaxBytes                int64
	MaxImageDimension       int
	MaxImagePixels          int64
	ReencodeImages          bool
	TargetImageMaxDimension int
	AllowArchives           bool // Always keep false; server-side archive extraction is prohibited.
	AllowedExtensions       map[string]AttachmentTypeSpec
}

// ValidatedAttachment holds sanitized metadata and bytes ready for storage or safe delivery.
type ValidatedAttachment struct {
	Filename string
	MIMEType string
	Kind     AttachmentKind
	Size     int64
	Width    int
	Height   int
	Data     []byte
}

// DefaultChatAttachmentPolicy returns the standard strict upload policy for chat attachments.
func DefaultChatAttachmentPolicy() AttachmentPolicy {
	return AttachmentPolicy{
		MaxBytes:                10 << 20, // 10 MiB
		MaxImageDimension:       MaxAvatarDimension,
		MaxImagePixels:          MaxAvatarPixels,
		ReencodeImages:          true,
		TargetImageMaxDimension: 0, // Preserve original dimensions within MaxImageDimension.
		AllowArchives:           false,
		AllowedExtensions: map[string]AttachmentTypeSpec{
			".jpg":  {MIMEType: "image/jpeg", Kind: KindImage},
			".jpeg": {MIMEType: "image/jpeg", Kind: KindImage},
			".png":  {MIMEType: "image/png", Kind: KindImage},
			".webp": {MIMEType: "image/webp", Kind: KindImage},
			".gif":  {MIMEType: "image/gif", Kind: KindImage},
			".pdf":  {MIMEType: "application/pdf", Kind: KindDocument},
			".txt":  {MIMEType: "text/plain; charset=utf-8", Kind: KindText},
			".md":   {MIMEType: "text/markdown; charset=utf-8", Kind: KindText},
			".json": {MIMEType: "application/json", Kind: KindText},
			".csv":  {MIMEType: "text/csv; charset=utf-8", Kind: KindText},
		},
	}
}

var blockedExtensions = map[string]bool{
	".svg": true, ".svgz": true,
	".html": true, ".htm": true, ".xhtml": true, ".mhtml": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true,
	".php": true, ".phtml": true, ".jsp": true, ".asp": true, ".aspx": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
	".sh": true, ".bash": true, ".zsh": true, ".bat": true, ".cmd": true, ".ps1": true,
	".msi": true, ".jar": true, ".war": true, ".wasm": true,
}

var archiveExtensions = map[string]bool{
	".zip": true, ".tar": true, ".gz": true, ".tgz": true,
	".bz2": true, ".xz": true, ".7z": true, ".rar": true,
}

// ValidateAttachment checks filename extension, magic bytes, archive/executable signatures,
// image dimensions/re-encoding, and size limits according to policy.
func ValidateAttachment(policy AttachmentPolicy, filename string, data []byte) (ValidatedAttachment, error) {
	if policy.MaxBytes > 0 && int64(len(data)) > policy.MaxBytes {
		return ValidatedAttachment{}, ErrAttachmentTooLarge
	}
	if len(data) == 0 {
		return ValidatedAttachment{}, ErrAttachmentContentMismatch
	}
	safeName := SanitizeFilename(filename)
	ext := strings.ToLower(filepath.Ext(safeName))
	if archiveExtensions[ext] || looksLikeArchive(data) {
		if !policy.AllowArchives {
			return ValidatedAttachment{}, ErrArchiveDisallowed
		}
	}
	if blockedExtensions[ext] || looksLikeExecutable(data) {
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
	spec, ok := policy.AllowedExtensions[ext]
	if !ok {
		return ValidatedAttachment{}, ErrAttachmentDisallowed
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

	case KindDocument:
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		return ValidatedAttachment{
			Filename: safeName,
			MIMEType: spec.MIMEType,
			Kind:     KindDocument,
			Size:     int64(len(data)),
			Data:     data,
		}, nil

	case KindText:
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		if ext == ".json" && !json.Valid(data) {
			return ValidatedAttachment{}, ErrAttachmentContentMismatch
		}
		return ValidatedAttachment{
			Filename: safeName,
			MIMEType: spec.MIMEType,
			Kind:     KindText,
			Size:     int64(len(data)),
			Data:     data,
		}, nil

	default:
		return ValidatedAttachment{}, ErrAttachmentDisallowed
	}
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
func DownloadHeaders(filename, mimeType string) http.Header {
	h := make(http.Header)
	safeName := SanitizeFilename(filename)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": safeName})
	if disposition == "" {
		disposition = `attachment; filename="attachment"`
	}
	h.Set("Content-Type", mimeType)
	h.Set("Content-Disposition", disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	return h
}

// ApplyDownloadHeaders writes strict download headers to w before streaming attachment bytes.
func ApplyDownloadHeaders(w http.ResponseWriter, filename, mimeType string) {
	for k, vals := range DownloadHeaders(filename, mimeType) {
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
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) ||
		bytes.HasPrefix(data, []byte("PK\x05\x06")) ||
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
		bytes.HasPrefix(data, []byte{0x00, 'a', 's', 'm'}) ||
		bytes.HasPrefix(data, []byte("#!"))
}
