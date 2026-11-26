package uploads

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// MaxAvatarDecodedBytes caps decoded avatar payload size before base64 decoding.
	MaxAvatarDecodedBytes = 2 << 20
	// MaxAvatarDimension caps decoded avatar width and height to prevent decompression bombs.
	MaxAvatarDimension = 4096
	// MaxAvatarPixels caps total decoded pixel count to prevent decompression bombs.
	MaxAvatarPixels int64 = 16_000_000
	// TargetAvatarMaxDimension caps the longer edge of re-encoded avatars.
	TargetAvatarMaxDimension = 512
	// MaxAvatarOutputURLLen bounds the stored re-encoded data URL size.
	MaxAvatarOutputURLLen = 1 << 20
)

var (
	ErrInvalidAvatar      = errors.New("invalid avatar data")
	ErrAvatarTooLarge     = errors.New("avatar exceeds maximum allowed size")
	ErrImageDimensions    = errors.New("image dimensions exceed safe limits")
	ErrUnsupportedImage   = errors.New("unsupported or mismatched image format")
	ErrImageDecodeFailed  = errors.New("failed to decode image")
)

var avatarDataPrefixes = []struct {
	prefix string
	format string
}{
	{prefix: "data:image/jpeg;base64,", format: "jpeg"},
	{prefix: "data:image/png;base64,", format: "png"},
	{prefix: "data:image/webp;base64,", format: "webp"},
	{prefix: "data:image/gif;base64,", format: "gif"},
}

// SanitizeAvatarDataURL validates an avatar data URL, rejects SVG/HTML/JS/mismatched
// magic bytes and decompression bombs, scales down to at most 512x512, and
// re-encodes pixels into a clean JPEG or PNG (preserving transparency) without
// EXIF/XMP/IPTC metadata or trailing polyglot payloads.
func SanitizeAvatarDataURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) || strings.ContainsFunc(raw, unicode.IsSpace) {
		return "", ErrInvalidAvatar
	}
	maxBase64Len := base64.StdEncoding.EncodedLen(MaxAvatarDecodedBytes)
	var expectedFormat, b64Payload string
	for _, candidate := range avatarDataPrefixes {
		if strings.HasPrefix(raw, candidate.prefix) {
			expectedFormat = candidate.format
			b64Payload = raw[len(candidate.prefix):]
			break
		}
	}
	if expectedFormat == "" || b64Payload == "" {
		return "", ErrInvalidAvatar
	}
	if len(b64Payload) > maxBase64Len {
		return "", ErrAvatarTooLarge
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(b64Payload)
	if err != nil {
		return "", ErrInvalidAvatar
	}
	if len(decoded) == 0 || len(decoded) > MaxAvatarDecodedBytes {
		return "", ErrAvatarTooLarge
	}

	cleanBytes, outMIME, _, _, err := SanitizeRasterImage(
		decoded,
		expectedFormat,
		MaxAvatarDimension,
		MaxAvatarPixels,
		TargetAvatarMaxDimension,
	)
	if err != nil {
		return "", err
	}

	outURL := "data:" + outMIME + ";base64," + base64.StdEncoding.EncodeToString(cleanBytes)
	if len(outURL) > MaxAvatarOutputURLLen {
		return "", ErrAvatarTooLarge
	}
	return outURL, nil
}

// DetectImageFormat identifies raster image magic bytes without trusting caller MIME.
func DetectImageFormat(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg"
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png"
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	default:
		return ""
	}
}

// SanitizeRasterImage verifies magic bytes, checks header dimensions before decoding,
// scales optionally to targetMaxDim, and re-encodes pixels into clean PNG or JPEG bytes.
func SanitizeRasterImage(data []byte, expectedFormat string, maxDim int, maxPixels int64, targetMaxDim int) ([]byte, string, int, int, error) {
	detected := DetectImageFormat(data)
	if detected == "" || (expectedFormat != "" && detected != expectedFormat) {
		return nil, "", 0, 0, ErrUnsupportedImage
	}

	cfg, cfgFormat, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfgFormat != detected {
		return nil, "", 0, 0, ErrUnsupportedImage
	}
	if err := checkDimensions(cfg.Width, cfg.Height, maxDim, maxPixels); err != nil {
		return nil, "", 0, 0, err
	}

	img, decFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decFormat != detected {
		return nil, "", 0, 0, ErrImageDecodeFailed
	}
	bounds := img.Bounds()
	if err := checkDimensions(bounds.Dx(), bounds.Dy(), maxDim, maxPixels); err != nil {
		return nil, "", 0, 0, err
	}

	hasAlpha := imageHasAlpha(img)
	if targetMaxDim > 0 && (bounds.Dx() > targetMaxDim || bounds.Dy() > targetMaxDim) {
		dstW, dstH := fitWithin(bounds.Dx(), bounds.Dy(), targetMaxDim)
		var dst draw.Image
		if hasAlpha {
			dst = image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
		} else {
			dst = image.NewRGBA(image.Rect(0, 0, dstW, dstH))
		}
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Src, nil)
		img = dst
		bounds = img.Bounds()
	}

	var out bytes.Buffer
	if hasAlpha {
		if err := png.Encode(&out, img); err != nil {
			return nil, "", 0, 0, fmt.Errorf("encode png: %w", err)
		}
		return out.Bytes(), "image/png", bounds.Dx(), bounds.Dy(), nil
	}

	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}
	return out.Bytes(), "image/jpeg", bounds.Dx(), bounds.Dy(), nil
}

func checkDimensions(width, height, maxDim int, maxPixels int64) error {
	if width <= 0 || height <= 0 {
		return ErrImageDimensions
	}
	if maxDim > 0 && (width > maxDim || height > maxDim) {
		return ErrImageDimensions
	}
	if maxPixels > 0 && int64(width)*int64(height) > maxPixels {
		return ErrImageDimensions
	}
	return nil
}

func fitWithin(width, height, maxDim int) (int, int) {
	if width >= height {
		scaledH := max(1, (height*maxDim+width/2)/width)
		return maxDim, scaledH
	}
	scaledW := max(1, (width*maxDim+height/2)/height)
	return scaledW, maxDim
}

func imageHasAlpha(img image.Image) bool {
	if opaque, ok := img.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		return false
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0xffff {
				return true
			}
		}
	}
	return false
}
