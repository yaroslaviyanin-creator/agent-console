package uploads

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeManyPagePDF(t *testing.T, pages int) []byte {
	t.Helper()
	var kids strings.Builder
	var objs []string
	objs = append(objs, "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	for i := 0; i < pages; i++ {
		objNum := 3 + i
		kids.WriteString(fmt.Sprintf("%d 0 R ", objNum))
	}
	objs = append(objs, fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", kids.String(), pages))
	for i := 0; i < pages; i++ {
		objNum := 3 + i
		objs = append(objs, fmt.Sprintf("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\n", objNum))
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = out.Len()
		out.WriteString(o)
	}
	xrefOffset := out.Len()
	out.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objs)+1))
	for i := 1; i <= len(objs); i++ {
		out.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	out.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefOffset))
	return out.Bytes()
}

func makeSyntheticDOCX(t *testing.T) []byte {
	t.Helper()
	return makeZIPWithFiles(t, map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`),
		"word/document.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Привет, лекция по линейной алгебре!</w:t></w:r></w:p>
    <w:tbl><w:tr><w:tc><w:p><w:r><w:t>Колонка 1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Значение 42</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
  </w:body>
</w:document>`),
		"word/header1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Верхний колонтитул</w:t></w:r></w:p></w:hdr>`),
	})
}

func makeSyntheticXLSX(t *testing.T) []byte {
	t.Helper()
	return makeZIPWithFiles(t, map[string][]byte{
		"xl/workbook.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="Отчёт" sheetId="1"/></sheets></workbook>`),
		"xl/sharedStrings.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Название</t></si><si><t>Выручка</t></si><si><t>Проект Альфа</t></si></sst>`),
		"xl/worksheets/sheet1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>1500</v></c></row></sheetData></worksheet>`),
	})
}

func makeSyntheticPPTX(t *testing.T) []byte {
	t.Helper()
	slideXML := func(txt string) []byte {
		return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>%s</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, txt))
	}
	return makeZIPWithFiles(t, map[string][]byte{
		"ppt/presentation.xml":     []byte(`<?xml version="1.0" encoding="UTF-8"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`),
		"ppt/slides/slide1.xml":    slideXML("Первый слайд"),
		"ppt/slides/slide2.xml":    slideXML("Второй слайд"),
		"ppt/slides/slide10.xml":   slideXML("Десятый слайд"),
		"ppt/notesSlides/notesSlide1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Заметка лектора</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>`),
	})
}

func makeSyntheticODT(t *testing.T) []byte {
	t.Helper()
	return makeZIPWithFiles(t, map[string][]byte{
		"mimetype": []byte("application/vnd.oasis.opendocument.text"),
		"content.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0">
  <office:body><office:text>
    <text:h>Заголовок ODT</text:h>
    <text:p>Русский текст в документе LibreOffice.</text:p>
  </office:text></office:body>
</office:document-content>`),
	})
}

func makeSyntheticEPUB(t *testing.T) []byte {
	t.Helper()
	return makeZIPWithFiles(t, map[string][]byte{
		"mimetype":               []byte("application/epub+zip"),
		"META-INF/container.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OEBPS/content.opf": []byte(`<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/><item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>`),
		"OEBPS/ch1.xhtml": []byte(`<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><script>alert(1)</script></head><body><h1>Глава 1</h1><p>Начало книги&nbsp;&mdash; тест.</p></body></html>`),
		"OEBPS/ch2.xhtml": []byte(`<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Глава 2</h1><p>Продолжение текста.</p></body></html>`),
	})
}

func TestOfficeRTFEPUBTSVAndPDFLimits(t *testing.T) {
	policy := DefaultChatAttachmentPolicy()

	// 1. DOCX, XLSX, PPTX, ODT, EPUB, RTF, TSV with Russian text + strict download headers.
	rtfBytes := []byte(`{\rtf1\ansi\ansicpg1251{\fonttbl{\f0 Arial;}}\uc1\u1055?\u1088?\u1080?\u1074?\u1077?\u1090? \'ec\'e8\'f0\par}`)
	cases := []struct {
		name     string
		data     []byte
		wantKind AttachmentKind
		wantSub  []string
	}{
		{"lecture.docx", makeSyntheticDOCX(t), KindDocument, []string{"Привет, лекция по линейной алгебре!", "Колонка 1\tЗначение 42", "Верхний колонтитул"}},
		{"report.xlsx", makeSyntheticXLSX(t), KindDocument, []string{"--- Лист: Отчёт ---", "Название\tВыручка", "Проект Альфа\t1500"}},
		{"slides.pptx", makeSyntheticPPTX(t), KindDocument, []string{"--- Слайд 1 ---", "Первый слайд", "[Заметки спикера]\nЗаметка лектора", "--- Слайд 2 ---", "--- Слайд 10 ---"}},
		{"notes.odt", makeSyntheticODT(t), KindDocument, []string{"Заголовок ODT", "Русский текст в документе LibreOffice."}},
		{"book.epub", makeSyntheticEPUB(t), KindDocument, []string{"Глава 1", "Начало книги\u00a0— тест.", "Глава 2"}},
		{"memo.rtf", rtfBytes, KindDocument, []string{"Привет мир"}},
		{"metrics.tsv", []byte("colA\tcolB\n1\tдва\n"), KindText, []string{"colA\tcolB"}},
	}

	for _, tc := range cases {
		att, err := ValidateAttachment(policy, tc.name, tc.data)
		if err != nil {
			t.Fatalf("%s failed: %v", tc.name, err)
		}
		if att.Kind != tc.wantKind {
			t.Fatalf("%s kind=%q, want %q", tc.name, att.Kind, tc.wantKind)
		}
		for _, sub := range tc.wantSub {
			if !strings.Contains(att.ExtractedText, sub) {
				t.Fatalf("%s missing %q in extracted:\n%s", tc.name, sub, att.ExtractedText)
			}
		}
		// EPUB must never retain <script> content.
		if tc.name == "book.epub" && strings.Contains(att.ExtractedText, "alert(1)") {
			t.Fatalf("epub retained script content: %s", att.ExtractedText)
		}
		// Verify PPTX slide order: slide 2 before slide 10.
		if tc.name == "slides.pptx" {
			i2 := strings.Index(att.ExtractedText, "--- Слайд 2 ---")
			i10 := strings.Index(att.ExtractedText, "--- Слайд 10 ---")
			if i2 < 0 || i10 < 0 || i2 > i10 {
				t.Fatalf("pptx slides out of numeric order: %s", att.ExtractedText)
			}
		}

		rec := httptest.NewRecorder()
		ApplyDownloadHeaders(rec, att.Filename, att.MIMEType, att.Kind, true)
		if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
			t.Fatalf("%s Content-Disposition=%q, want attachment", tc.name, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s nosniff=%q", tc.name, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
			t.Fatalf("%s csp=%q", tc.name, got)
		}
	}

	// 2. Zip bomb disguised as .docx/.xlsx is rejected with ErrZipBomb.
	var bombBuf bytes.Buffer
	zw := zip.NewWriter(&bombBuf)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	fw, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(`<?xml version="1.0"?><w:document><w:body><w:p><w:r><w:t>`))
	_, _ = fw.Write(bytes.Repeat([]byte{'A'}, int(ChatMaxOfficeUncompressedBytes)+4096))
	_, _ = fw.Write([]byte(`</w:t></w:r></w:p></w:body></w:document>`))
	_ = zw.Close()
	if _, err := ValidateAttachment(policy, "bomb.docx", bombBuf.Bytes()); !errors.Is(err, ErrZipBomb) {
		t.Fatalf("expected ErrZipBomb for bomb.docx, got %v", err)
	}

	// 3. JPG+ZIP polyglot disguised as .docx is rejected (magic bytes not at offset 0).
	baseJPG := encodeJPEG(t, makeSampleImage(32, 32, false))
	polyglotDOCX := append(baseJPG, makeSyntheticDOCX(t)...)
	if _, err := ValidateAttachment(policy, "polyglot.docx", polyglotDOCX); !errors.Is(err, ErrAttachmentContentMismatch) {
		t.Fatalf("expected ErrAttachmentContentMismatch for polyglot.docx, got %v", err)
	}

	// 4. PDF > 1000 pages rejected with ErrPDFTooManyPages.
	tooManyPagesPDF := makeManyPagePDF(t, ChatMaxPDFPages+1)
	if _, err := ValidateAttachment(policy, "huge.pdf", tooManyPagesPDF); !errors.Is(err, ErrPDFTooManyPages) {
		t.Fatalf("expected ErrPDFTooManyPages for 1001-page PDF, got %v", err)
	}

	// 5. Scan PDF returns non-empty Warning without crashing.
	scanAtt, err := ValidateAttachment(policy, "scan.pdf", makeMinimalPDF(t, ""))
	if err != nil {
		t.Fatalf("scan pdf unexpected err: %v", err)
	}
	if scanAtt.Warning == "" {
		t.Fatal("expected warning for scan PDF without text layer")
	}

	// 6. Image > 8000x8000 rejected; 8000px cap is preserved.
	if ChatMaxImageDimension != 8000 || ChatMaxImageBytes != 20<<20 {
		t.Fatalf("unexpected image limits: dim=%d bytes=%d", ChatMaxImageDimension, ChatMaxImageBytes)
	}
}
