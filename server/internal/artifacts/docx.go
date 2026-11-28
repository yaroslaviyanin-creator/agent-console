package artifacts

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type mdBlockKind int

const (
	blockParagraph mdBlockKind = iota
	blockHeading
	blockListItem
	blockCode
	blockQuote
	blockTable
)

type mdBlock struct {
	kind    mdBlockKind
	level   int // heading 1..6, or ordered list index (0 = bullet)
	text    string
	rows    [][]string
	ordered bool
}

type inlineRun struct {
	text   string
	bold   bool
	italic bool
	code   bool
}

var (
	orderedListRe = regexp.MustCompile(`^(\d+)[.)]\s+(.*)$`)
	tableSepRe    = regexp.MustCompile(`^\|?[\s:-]+\|[\s|:-]*$`)
)

func parseMarkdownBlocks(raw string) []mdBlock {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var blocks []mdBlock

	inCode := false
	var codeLines []string
	var paraLines []string

	flushPara := func() {
		if len(paraLines) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(paraLines, " "))
		if text != "" {
			blocks = append(blocks, mdBlock{kind: blockParagraph, text: text})
		}
		paraLines = nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			flushPara()
			if !inCode {
				inCode = true
				codeLines = nil
			} else {
				inCode = false
				blocks = append(blocks, mdBlock{kind: blockCode, text: strings.Join(codeLines, "\n")})
				codeLines = nil
			}
			continue
		}
		if inCode {
			codeLines = append(codeLines, line)
			continue
		}

		if trimmed == "" {
			flushPara()
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushPara()
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			lvl := 0
			for lvl < len(trimmed) && lvl < 6 && trimmed[lvl] == '#' {
				lvl++
			}
			if lvl > 0 && (lvl == len(trimmed) || trimmed[lvl] == ' ') {
				flushPara()
				blocks = append(blocks, mdBlock{
					kind:  blockHeading,
					level: lvl,
					text:  strings.TrimSpace(trimmed[lvl:]),
				})
				continue
			}
		}

		if strings.HasPrefix(trimmed, ">") {
			flushPara()
			q := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			if q != "" {
				blocks = append(blocks, mdBlock{kind: blockQuote, text: q})
			}
			continue
		}

		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "• ") {
			flushPara()
			item := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "* "), "• "))
			blocks = append(blocks, mdBlock{kind: blockListItem, ordered: false, text: item})
			continue
		}

		if m := orderedListRe.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			blocks = append(blocks, mdBlock{kind: blockListItem, ordered: true, text: strings.TrimSpace(m[2])})
			continue
		}

		// Markdown table detection
		if strings.Contains(trimmed, "|") && i+1 < len(lines) && tableSepRe.MatchString(strings.TrimSpace(lines[i+1])) {
			flushPara()
			var tableRows [][]string
			tableRows = append(tableRows, splitMarkdownTableRow(trimmed))
			i += 2
			for i < len(lines) {
				rowLine := strings.TrimSpace(lines[i])
				if rowLine == "" || !strings.Contains(rowLine, "|") {
					i--
					break
				}
				tableRows = append(tableRows, splitMarkdownTableRow(rowLine))
				i++
			}
			blocks = append(blocks, mdBlock{kind: blockTable, rows: tableRows})
			continue
		}

		paraLines = append(paraLines, trimmed)
	}
	flushPara()
	if inCode && len(codeLines) > 0 {
		blocks = append(blocks, mdBlock{kind: blockCode, text: strings.Join(codeLines, "\n")})
	}
	return blocks
}

func splitMarkdownTableRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseInlineRuns(text string) []inlineRun {
	var runs []inlineRun
	var buf strings.Builder
	flush := func(bold, italic, code bool) {
		if buf.Len() > 0 {
			runs = append(runs, inlineRun{text: buf.String(), bold: bold, italic: italic, code: code})
			buf.Reset()
		}
	}

	i := 0
	for i < len(text) {
		if text[i] == '`' {
			if end := strings.IndexByte(text[i+1:], '`'); end >= 0 {
				flush(false, false, false)
				runs = append(runs, inlineRun{text: text[i+1 : i+1+end], code: true})
				i = i + 1 + end + 1
				continue
			}
		}
		if strings.HasPrefix(text[i:], "**") {
			if end := strings.Index(text[i+2:], "**"); end >= 0 {
				flush(false, false, false)
				runs = append(runs, inlineRun{text: text[i+2 : i+2+end], bold: true})
				i = i + 2 + end + 2
				continue
			}
		}
		if text[i] == '*' && (i+1 >= len(text) || text[i+1] != '*') {
			if end := strings.IndexByte(text[i+1:], '*'); end > 0 {
				flush(false, false, false)
				runs = append(runs, inlineRun{text: text[i+1 : i+1+end], italic: true})
				i = i + 1 + end + 1
				continue
			}
		}
		buf.WriteByte(text[i])
		i++
	}
	flush(false, false, false)
	if len(runs) == 0 && text != "" {
		runs = append(runs, inlineRun{text: text})
	}
	return runs
}

// BuildDOCXFromMarkdown builds a valid OOXML .docx document from Markdown with full UTF-8/Cyrillic support.
func BuildDOCXFromMarkdown(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("empty document content")
	}
	blocks := parseMarkdownBlocks(raw)
	if len(blocks) == 0 {
		blocks = []mdBlock{{kind: blockParagraph, text: strings.TrimSpace(raw)}}
	}

	var docBody strings.Builder
	listCounter := 0
	for _, b := range blocks {
		if b.kind != blockListItem || !b.ordered {
			listCounter = 0
		}
		switch b.kind {
		case blockHeading:
			sizeHalfPts := 28
			switch b.level {
			case 1:
				sizeHalfPts = 36
			case 2:
				sizeHalfPts = 30
			case 3:
				sizeHalfPts = 26
			default:
				sizeHalfPts = 24
			}
			docBody.WriteString(`<w:p><w:pPr><w:spacing w:before="200" w:after="100"/></w:pPr>`)
			writeDocxRuns(&docBody, parseInlineRuns(b.text), true, sizeHalfPts, "1E293B")
			docBody.WriteString(`</w:p>`)

		case blockParagraph:
			docBody.WriteString(`<w:p><w:pPr><w:spacing w:after="140" w:line="276" w:lineRule="auto"/></w:pPr>`)
			writeDocxRuns(&docBody, parseInlineRuns(b.text), false, 22, "0F172A")
			docBody.WriteString(`</w:p>`)

		case blockQuote:
			docBody.WriteString(`<w:p><w:pPr><w:pBdr><w:left w:val="single" w:sz="18" w:space="8" w:color="94A3B8"/></w:pBdr><w:ind w:left="360"/><w:spacing w:after="140"/></w:pPr>`)
			writeDocxRuns(&docBody, parseInlineRuns(b.text), false, 22, "475569")
			docBody.WriteString(`</w:p>`)

		case blockListItem:
			prefix := "•  "
			if b.ordered {
				listCounter++
				prefix = fmt.Sprintf("%d. ", listCounter)
			}
			docBody.WriteString(`<w:p><w:pPr><w:ind w:left="360" w:hanging="200"/><w:spacing w:after="80"/></w:pPr>`)
			runs := append([]inlineRun{{text: prefix, bold: b.ordered}}, parseInlineRuns(b.text)...)
			writeDocxRuns(&docBody, runs, false, 22, "0F172A")
			docBody.WriteString(`</w:p>`)

		case blockCode:
			for _, codeLine := range strings.Split(b.text, "\n") {
				docBody.WriteString(`<w:p><w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F1F5F9"/><w:ind w:left="200"/><w:spacing w:after="40"/></w:pPr>`)
				writeDocxSingleRun(&docBody, inlineRun{text: codeLine, code: true}, false, 19, "0F172A")
				docBody.WriteString(`</w:p>`)
			}

		case blockTable:
			if len(b.rows) == 0 {
				continue
			}
			docBody.WriteString(`<w:tbl><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="CBD5E1"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="CBD5E1"/><w:left w:val="single" w:sz="4" w:space="0" w:color="CBD5E1"/><w:right w:val="single" w:sz="4" w:space="0" w:color="CBD5E1"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="E2E8F0"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="E2E8F0"/></w:tblBorders></w:tblPr>`)
			for rIdx, row := range b.rows {
				docBody.WriteString(`<w:tr>`)
				for _, cell := range row {
					docBody.WriteString(`<w:tc><w:tcPr>`)
					if rIdx == 0 {
						docBody.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="F1F5F9"/>`)
					}
					docBody.WriteString(`</w:tcPr><w:p><w:pPr><w:spacing w:after="60"/></w:pPr>`)
					writeDocxRuns(&docBody, parseInlineRuns(cell), rIdx == 0, 20, "0F172A")
					docBody.WriteString(`</w:p></w:tc>`)
				}
				docBody.WriteString(`</w:tr>`)
			}
			docBody.WriteString(`</w:tbl><w:p/>`)
		}
	}

	documentXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		docBody.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/></w:sectPr></w:body></w:document>`

	const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

	const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name, body string
	}{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"word/document.xml", documentXML},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeDocxRuns(b *strings.Builder, runs []inlineRun, forceBold bool, sizeHalfPts int, colorHex string) {
	for _, run := range runs {
		writeDocxSingleRun(b, run, forceBold, sizeHalfPts, colorHex)
	}
}

func writeDocxSingleRun(b *strings.Builder, run inlineRun, forceBold bool, sizeHalfPts int, colorHex string) {
	font := "DejaVu Sans"
	if run.code {
		font = "DejaVu Sans Mono"
	}
	b.WriteString(`<w:r><w:rPr>`)
	fmt.Fprintf(b, `<w:rFonts w:ascii="%s" w:hAnsi="%s" w:cs="%s"/>`, font, font, font)
	if forceBold || run.bold {
		b.WriteString(`<w:b/>`)
	}
	if run.italic {
		b.WriteString(`<w:i/>`)
	}
	fmt.Fprintf(b, `<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, sizeHalfPts, sizeHalfPts)
	if colorHex != "" {
		fmt.Fprintf(b, `<w:color w:val="%s"/>`, colorHex)
	}
	b.WriteString(`</w:rPr><w:t xml:space="preserve">`)
	_ = xml.EscapeText(b, []byte(run.text))
	b.WriteString(`</w:t></w:r>`)
}
