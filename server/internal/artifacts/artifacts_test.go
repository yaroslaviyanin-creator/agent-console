package artifacts

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-console/server/internal/uploads"
	"github.com/xuri/excelize/v2"
)

func TestParseConsoleFilesSecurityAndFences(t *testing.T) {
	raw := strings.Join([]string{
		"Вот пример использования тега (внутри блока кода — должен игнорироваться):",
		"```xml",
		`<console_file name="fake.txt">should not become a file</console_file>`,
		"```",
		"",
		"~~~markdown",
		`<console_file name="fake2.md">also ignored</console_file>`,
		"~~~",
		"",
		`<console_file name="../../etc/passwd.txt">`,
		"безопасный текст",
		`</console_file>`,
		"",
		`<console_file name="guide.md">`,
		"# Заголовок",
		"```go",
		`fmt.Println("Привет")`,
		"```",
		`</console_file>`,
		"",
		"Готово!",
	}, "\n")

	res := ParseConsoleFiles(raw)
	if len(res.Files) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(res.Files), res.Files)
	}
	if res.Files[0].Name != "passwd.txt" {
		t.Fatalf("expected sanitized filename passwd.txt, got %q", res.Files[0].Name)
	}
	if res.Files[0].Content != "безопасный текст" {
		t.Fatalf("unexpected file[0] content: %q", res.Files[0].Content)
	}
	if res.Files[1].Name != "guide.md" || !strings.Contains(res.Files[1].Content, `fmt.Println("Привет")`) {
		t.Fatalf("unexpected file[1]: %+v", res.Files[1])
	}
	if !strings.Contains(res.CleanText, `<console_file name="fake.txt">should not become a file</console_file>`) {
		t.Fatalf("code-fenced example was wrongly stripped from CleanText: %q", res.CleanText)
	}
	if !strings.Contains(res.CleanText, `<console_file name="fake2.md">also ignored</console_file>`) {
		t.Fatalf("tilde-fenced example was wrongly stripped from CleanText: %q", res.CleanText)
	}
	if strings.Contains(res.CleanText, "безопасный текст") {
		t.Fatalf("extracted artifact content leaked into CleanText: %q", res.CleanText)
	}
	if !strings.HasSuffix(res.CleanText, "Готово!") {
		t.Fatalf("expected trailing text preserved, got %q", res.CleanText)
	}
}

func TestParseConsoleFilesMaxLimit(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 7; i++ {
		b.WriteString(`<console_file name="f`)
		b.WriteByte(byte('0' + i))
		b.WriteString(`.txt">content</console_file>`)
		b.WriteByte('\n')
	}
	res := ParseConsoleFiles(b.String())
	if len(res.Files) != MaxArtifactsPerResponse {
		t.Fatalf("expected max %d files, got %d", MaxArtifactsPerResponse, len(res.Files))
	}
}

func TestBuildXLSXFromCSVAndJSON(t *testing.T) {
	policy := uploads.DefaultChatAttachmentPolicy()
	csvContent := "Имя,Возраст,Город\nАлексей,31,Москва\nМария,28,Казань\n"
	att, err := BuildValidatedAttachment(context.Background(), policy, ParsedFile{
		Name:    "users.xlsx",
		Content: csvContent,
	})
	if err != nil {
		t.Fatalf("BuildValidatedAttachment xlsx from csv: %v", err)
	}
	if att.Kind != uploads.KindDocument || att.Filename != "users.xlsx" {
		t.Fatalf("unexpected xlsx attachment metadata: %+v", att)
	}
	if !strings.Contains(att.ExtractedText, "Алексей") || !strings.Contains(att.ExtractedText, "Казань") {
		t.Fatalf("expected Cyrillic spreadsheet text extracted, got %q", att.ExtractedText)
	}

	xl, err := excelize.OpenReader(bytes.NewReader(att.Data))
	if err != nil {
		t.Fatalf("excelize.OpenReader: %v", err)
	}
	defer xl.Close()
	val, err := xl.GetCellValue("Sheet1", "C3")
	if err != nil || val != "Казань" {
		t.Fatalf("expected cell C3=Казань, got %q (err=%v)", val, err)
	}

	jsonContent := `[{"name":"Отчёт","count":42},{"name":"План","count":7}]`
	attJSON, err := BuildValidatedAttachment(context.Background(), policy, ParsedFile{
		Name:    "report.xlsx",
		Content: jsonContent,
	})
	if err != nil {
		t.Fatalf("BuildValidatedAttachment xlsx from json: %v", err)
	}
	if !strings.Contains(attJSON.ExtractedText, "Отчёт") || !strings.Contains(attJSON.ExtractedText, "42") {
		t.Fatalf("expected JSON xlsx values, got %q", attJSON.ExtractedText)
	}
}

func TestBuildDOCXWithCyrillic(t *testing.T) {
	policy := uploads.DefaultChatAttachmentPolicy()
	md := "# Еженедельный отчёт\n\nПривет, **команда**! Это тест `кириллицы`.\n\n- Пункт один\n- Пункт два\n\n| Показатель | Значение |\n| --- | --- |\n| Выручка | 1200 ₽ |\n"
	att, err := BuildValidatedAttachment(context.Background(), policy, ParsedFile{
		Name:    "report.docx",
		Content: md,
	})
	if err != nil {
		t.Fatalf("BuildValidatedAttachment docx: %v", err)
	}
	if att.Kind != uploads.KindDocument || att.Filename != "report.docx" {
		t.Fatalf("unexpected docx attachment: %+v", att)
	}
	for _, want := range []string{"Еженедельный отчёт", "команда", "кириллицы", "Пункт один", "Выручка"} {
		if !strings.Contains(att.ExtractedText, want) {
			t.Fatalf("docx extracted text missing %q: %q", want, att.ExtractedText)
		}
	}
}

func TestBuildPDFWithCyrillic(t *testing.T) {
	policy := uploads.DefaultChatAttachmentPolicy()
	md := "# Финансовый обзор\n\nСводка по проекту **Альфа**: всё работает стабильно.\n\n1. Первый этап завершён\n2. Второй этап в работе\n\n| Раздел | Статус |\n| --- | --- |\n| Бэкенд | Готово |\n"
	att, err := BuildValidatedAttachment(context.Background(), policy, ParsedFile{
		Name:    "summary.pdf",
		Content: md,
	})
	if err != nil {
		t.Fatalf("BuildValidatedAttachment pdf: %v", err)
	}
	if att.Kind != uploads.KindPDF || att.Filename != "summary.pdf" {
		t.Fatalf("unexpected pdf attachment: %+v", att)
	}
	if !bytes.HasPrefix(att.Data, []byte("%PDF-")) {
		t.Fatalf("missing PDF header")
	}
	if outPath := os.Getenv("TEST_WRITE_PDF"); outPath != "" {
		if err := os.WriteFile(outPath, att.Data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Contains(att.Data, []byte("/ToUnicode")) {
		t.Fatalf("expected /ToUnicode CMap in PDF")
	}
	if _, err := exec.LookPath("pdftotext"); err == nil {
		tmpPDF := filepath.Join(t.TempDir(), "summary.pdf")
		if err := os.WriteFile(tmpPDF, att.Data, 0600); err != nil {
			t.Fatal(err)
		}
		validatedFile, err := uploads.ValidateAttachmentFile(context.Background(), policy, "summary.pdf", tmpPDF)
		if err != nil {
			t.Fatalf("ValidateAttachmentFile pdf: %v", err)
		}
		for _, want := range []string{"Финансовый обзор", "Альфа", "Первый этап", "Бэкенд"} {
			if !strings.Contains(validatedFile.ExtractedText, want) {
				t.Fatalf("pdftotext extracted text missing %q (warning=%q): %q", want, validatedFile.Warning, validatedFile.ExtractedText)
			}
		}
	}
}

func TestBuildFallbackOnInvalidBinary(t *testing.T) {
	policy := uploads.DefaultChatAttachmentPolicy()
	// Empty JSON array cannot build an xlsx workbook, so it should safely fall back to .md
	att, err := BuildValidatedAttachment(context.Background(), policy, ParsedFile{
		Name:    "broken.xlsx",
		Content: "[]",
	})
	if err != nil {
		t.Fatalf("expected fallback attachment, got error: %v", err)
	}
	if att.Kind != uploads.KindText || att.Filename != "broken-fallback.md" || att.Warning == "" {
		t.Fatalf("unexpected fallback result: %+v", att)
	}
}
