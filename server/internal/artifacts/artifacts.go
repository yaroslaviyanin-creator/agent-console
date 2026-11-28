package artifacts

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"agent-console/server/internal/uploads"
)

const (
	MaxArtifactsPerResponse = 5
	MaxArtifactSourceBytes  = 2 << 20
)

// ArtifactSystemPrompt instructs remote chat models to emit downloadable files via <console_file> tags.
const ArtifactSystemPrompt = `Правило создания файлов (артефактов):
Если пользователь просит создать, сгенерировать, подготовить или сохранить файл (документ, конспект, отчёт, таблицу, код, конфиг, скрипт и т.п.), НЕ пиши «я не могу прикрепить файл» или «у меня нет доступа к диску». Вместо этого оберни готовое содержимое файла в тег:
<console_file name="имя_файла.ext">
...содержимое файла...
</console_file>
Требования:
- Указывай безопасное имя файла с расширением (например: report.pdf, notes.docx, table.xlsx, script.py, summary.md).
- Для .pdf и .docx пиши внутрь <console_file> чистый структурированный Markdown (заголовки #, ##, списки, таблицы, **выделение**).
- Для .xlsx пиши внутрь <console_file> валидный CSV (через запятую) или JSON-таблицу (массив объектов или массив строк).
- Для текстовых файлов и исходного кода (.md, .txt, .py, .go, .ts, .csv, .json, .html, .svg и др.) пиши готовый текст файла.
- Не более 5 файлов за один ответ; вне тега <console_file> дай пользователю краткий комментарий.`

// ParsedFile describes one <console_file> block extracted from a model response.
type ParsedFile struct {
	Name    string
	Content string
}

// ParseResult contains the cleaned display message and extracted artifact specs.
type ParseResult struct {
	CleanText string
	Files     []ParsedFile
}

var (
	openTagRe    = regexp.MustCompile(`(?i)<console_file\s+name\s*=\s*(?:"([^"]*)"|'([^']*)')\s*>`)
	closeTagRe   = regexp.MustCompile(`(?i)</console_file\s*>`)
	excessNLRe   = regexp.MustCompile(`\n{3,}`)
	fenceStartRe = regexp.MustCompile(`^ {0,3}(` + "```+" + `|~~~+)`)
)

// ParseConsoleFiles extracts <console_file name="...">...</console_file> blocks from raw assistant output.
// Tags inside Markdown fenced code blocks are preserved literally and never treated as artifacts.
func ParseConsoleFiles(raw string) ParseResult {
	if raw == "" || !strings.Contains(strings.ToLower(raw), "<console_file") {
		return ParseResult{CleanText: raw}
	}

	var clean strings.Builder
	var files []ParsedFile

	pos := 0
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0

	for pos < len(raw) {
		lineStart := pos
		nlRel := strings.IndexByte(raw[pos:], '\n')
		var lineEnd, nextPos int
		if nlRel >= 0 {
			lineEnd = pos + nlRel
			nextPos = lineEnd + 1
		} else {
			lineEnd = len(raw)
			nextPos = len(raw)
		}
		line := raw[lineStart:lineEnd]

		if m := fenceStartRe.FindStringSubmatch(line); m != nil {
			marker := m[1]
			ch := marker[0]
			n := len(marker)
			if !inFence {
				inFence = true
				fenceChar = ch
				fenceLen = n
				clean.WriteString(raw[lineStart:nextPos])
				pos = nextPos
				continue
			}
			if ch == fenceChar && n >= fenceLen && strings.TrimSpace(line[len(m[0]):]) == "" {
				inFence = false
				fenceChar = 0
				fenceLen = 0
				clean.WriteString(raw[lineStart:nextPos])
				pos = nextPos
				continue
			}
		}

		if inFence {
			clean.WriteString(raw[lineStart:nextPos])
			pos = nextPos
			continue
		}

		// Outside fenced code blocks: look for <console_file ...> starting anywhere on this line or across lines.
		loc := openTagRe.FindStringSubmatchIndex(raw[lineStart:])
		if loc == nil {
			clean.WriteString(raw[lineStart:nextPos])
			pos = nextPos
			continue
		}
		tagStart := lineStart + loc[0]
		// Only handle tag if it starts on the current line (before nextPos).
		if tagStart >= nextPos {
			clean.WriteString(raw[lineStart:nextPos])
			pos = nextPos
			continue
		}

		// Copy any text on this line before the tag.
		clean.WriteString(raw[lineStart:tagStart])

		tagEnd := lineStart + loc[1]
		var rawName string
		if loc[2] >= 0 {
			rawName = raw[lineStart+loc[2] : lineStart+loc[3]]
		} else if loc[4] >= 0 {
			rawName = raw[lineStart+loc[4] : lineStart+loc[5]]
		}

		closeLoc := closeTagRe.FindStringIndex(raw[tagEnd:])
		if closeLoc == nil {
			// Unclosed tag: leave the rest as plain text so partial output is never silently lost.
			clean.WriteString(raw[tagStart:])
			break
		}

		bodyStart := tagEnd
		bodyEnd := tagEnd + closeLoc[0]
		afterClose := tagEnd + closeLoc[1]

		body := trimSingleEdgeNewlines(raw[bodyStart:bodyEnd])
		safeName := uploads.SanitizeFilename(rawName)

		if len(files) < MaxArtifactsPerResponse && safeName != "" && len(body) > 0 {
			if len(body) > MaxArtifactSourceBytes {
				body = clipUTF8(body, MaxArtifactSourceBytes)
			}
			files = append(files, ParsedFile{
				Name:    safeName,
				Content: body,
			})
		} else if len(files) >= MaxArtifactsPerResponse && safeName != "" {
			clean.WriteString(fmt.Sprintf("\n[Файл %s пропущен: превышен лимит (%d файлов за ответ)]\n", safeName, MaxArtifactsPerResponse))
		}

		// Consume a single trailing newline right after </console_file> to avoid awkward gaps.
		if afterClose < len(raw) && raw[afterClose] == '\r' {
			afterClose++
		}
		if afterClose < len(raw) && raw[afterClose] == '\n' {
			afterClose++
		}
		pos = afterClose
	}

	if len(files) > 0 {
		exts := make([]string, 0, len(files))
		for _, f := range files {
			exts = append(exts, strings.ToLower(filepath.Ext(f.Name)))
		}
		slog.Info("artifacts extracted", "count", len(files), "exts", exts)
	}
	cleaned := strings.TrimSpace(excessNLRe.ReplaceAllString(clean.String(), "\n\n"))
	return ParseResult{
		CleanText: cleaned,
		Files:     files,
	}
}

func trimSingleEdgeNewlines(s string) string {
	s = strings.TrimPrefix(s, "\r\n")
	if strings.HasPrefix(s, "\n") {
		s = s[1:]
	}
	s = strings.TrimSuffix(s, "\r\n")
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	return s
}

func clipUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		if maxBytes <= 0 {
			return ""
		}
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// BuildValidatedAttachment converts a ParsedFile into a validated upload attachment ready for Store.CreateAttachment.
// Binary format errors safely fall back to a .md file so the assistant's work is preserved.
func BuildValidatedAttachment(ctx context.Context, policy uploads.AttachmentPolicy, pf ParsedFile) (uploads.ValidatedAttachment, error) {
	name := uploads.SanitizeFilename(pf.Name)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		name += ".txt"
		ext = ".txt"
	}

	var (
		rawBytes []byte
		convErr  error
	)

	switch ext {
	case ".xlsx":
		rawBytes, convErr = BuildXLSXFromText(pf.Content)
	case ".docx":
		rawBytes, convErr = BuildDOCXFromMarkdown(pf.Content)
	case ".pdf":
		rawBytes, convErr = BuildPDFFromMarkdown(pf.Content)
	default:
		rawBytes = []byte(pf.Content)
	}

	if convErr == nil {
		validated, err := validateWithContext(ctx, policy, name, rawBytes)
		if err == nil {
			if validated.Kind == uploads.KindPDF {
				maxExtracted := policy.MaxExtractedTextBytes
				if maxExtracted <= 0 {
					maxExtracted = uploads.ChatMaxExtractedTextBytes
				}
				validated.ExtractedText = clipUTF8(pf.Content, maxExtracted)
				validated.Warning = ""
			}
			return validated, nil
		}
		convErr = err
	}

	// Safe fallback: store raw text as .md with clear suffix if binary conversion or strict validation failed.
	slog.Warn("artifact fallback", "ext", ext, "fallback", ".md", "error", convErr)
	fallbackName := fallbackMarkdownName(name)
	fallbackBytes := []byte(fmt.Sprintf("<!-- Исходный файл %s не удалось собрать (%v); сохранён исходный текст -->\n\n%s", name, convErr, pf.Content))
	validated, err := validateWithContext(ctx, policy, fallbackName, fallbackBytes)
	if err != nil {
		// Last-resort plain .txt fallback
		txtName := strings.TrimSuffix(fallbackName, ".md") + ".txt"
		return uploads.ValidateAttachment(policy, txtName, []byte(pf.Content))
	}
	validated.Warning = fmt.Sprintf("Не удалось собрать %s — сохранено как Markdown.", ext)
	return validated, nil
}

func validateWithContext(_ context.Context, policy uploads.AttachmentPolicy, name string, data []byte) (uploads.ValidatedAttachment, error) {
	return uploads.ValidateAttachment(policy, name, data)
}

func fallbackMarkdownName(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem = "artifact"
	}
	return uploads.SanitizeFilename(stem + "-fallback.md")
}
