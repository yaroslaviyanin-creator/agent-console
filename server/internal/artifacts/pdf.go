package artifacts

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

//go:embed fonts/DejaVuSans.ttf
var fontDejaVuSans []byte

//go:embed fonts/DejaVuSans-Bold.ttf
var fontDejaVuSansBold []byte

//go:embed fonts/DejaVuSansMono.ttf
var fontDejaVuSansMono []byte

// BuildPDFFromMarkdown renders Markdown into an A4 PDF with embedded UTF-8 Cyrillic DejaVu fonts.
func BuildPDFFromMarkdown(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("empty PDF content")
	}
	blocks := parseMarkdownBlocks(raw)
	if len(blocks) == 0 {
		blocks = []mdBlock{{kind: blockParagraph, text: strings.TrimSpace(raw)}}
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(16, 16, 16)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AddUTF8FontFromBytes("DejaVu", "", fontDejaVuSans)
	pdf.AddUTF8FontFromBytes("DejaVu", "B", fontDejaVuSansBold)
	pdf.AddUTF8FontFromBytes("DejaVuMono", "", fontDejaVuSansMono)
	pdf.AddPage()

	const usableWidth = 178.0 // 210 - 16 - 16
	listCounter := 0

	for _, b := range blocks {
		if b.kind != blockListItem || !b.ordered {
			listCounter = 0
		}
		switch b.kind {
		case blockHeading:
			fontSize := 13.0
			lineH := 6.5
			switch b.level {
			case 1:
				fontSize = 18.0
				lineH = 8.5
			case 2:
				fontSize = 15.0
				lineH = 7.5
			case 3:
				fontSize = 13.5
				lineH = 6.8
			}
			pdf.Ln(2.5)
			pdf.SetTextColor(15, 23, 42)
			pdf.SetFont("DejaVu", "B", fontSize)
			pdf.MultiCell(usableWidth, lineH, stripInlineMarkdown(b.text), "", "L", false)
			pdf.Ln(1.5)

		case blockParagraph:
			pdf.SetTextColor(30, 41, 59)
			pdf.SetFont("DejaVu", "", 10.5)
			writePDFInlineRuns(pdf, parseInlineRuns(b.text), 5.6)
			pdf.Ln(2.0)

		case blockQuote:
			pdf.SetDrawColor(148, 163, 184)
			pdf.SetTextColor(71, 85, 105)
			pdf.SetFont("DejaVu", "", 10.0)
			x, y := pdf.GetXY()
			pdf.SetX(x + 4)
			pdf.MultiCell(usableWidth-4, 5.4, stripInlineMarkdown(b.text), "L", "L", false)
			_ = y
			pdf.Ln(1.5)

		case blockListItem:
			prefix := "• "
			if b.ordered {
				listCounter++
				prefix = fmt.Sprintf("%d. ", listCounter)
			}
			pdf.SetTextColor(30, 41, 59)
			pdf.SetFont("DejaVu", "", 10.5)
			pdf.SetX(20)
			pdf.MultiCell(usableWidth-4, 5.5, prefix+stripInlineMarkdown(b.text), "", "L", false)
			pdf.Ln(0.8)

		case blockCode:
			pdf.SetFillColor(241, 245, 249)
			pdf.SetTextColor(15, 23, 42)
			pdf.SetFont("DejaVuMono", "", 9.0)
			pdf.MultiCell(usableWidth, 4.8, b.text, "", "L", true)
			pdf.Ln(2.0)

		case blockTable:
			if len(b.rows) == 0 {
				continue
			}
			maxCols := 0
			for _, r := range b.rows {
				if len(r) > maxCols {
					maxCols = len(r)
				}
			}
			if maxCols == 0 {
				continue
			}
			colW := usableWidth / float64(maxCols)
			for rIdx, row := range b.rows {
				isHeader := rIdx == 0
				if isHeader {
					pdf.SetFillColor(241, 245, 249)
					pdf.SetFont("DejaVu", "B", 9.5)
				} else {
					pdf.SetFillColor(255, 255, 255)
					pdf.SetFont("DejaVu", "", 9.5)
				}
				pdf.SetDrawColor(203, 213, 225)
				pdf.SetTextColor(15, 23, 42)

				rowLines := 1
				cells := make([]string, maxCols)
				for cIdx := 0; cIdx < maxCols; cIdx++ {
					if cIdx < len(row) {
						cells[cIdx] = stripInlineMarkdown(row[cIdx])
					}
					nLines := len(pdf.SplitText(cells[cIdx], colW-3))
					if nLines > rowLines {
						rowLines = nLines
					}
				}
				rowH := float64(rowLines)*5.0 + 2.5
				_, pageH := pdf.GetPageSize()
				_, _, _, bMargin := pdf.GetMargins()
				if pdf.GetY()+rowH > pageH-bMargin {
					pdf.AddPage()
				}
				startX := 16.0
				startY := pdf.GetY()
				for cIdx := 0; cIdx < maxCols; cIdx++ {
					cx := startX + float64(cIdx)*colW
					style := "D"
					if isHeader {
						style = "FD"
					}
					pdf.Rect(cx, startY, colW, rowH, style)
					pdf.SetXY(cx+1.5, startY+1.2)
					pdf.MultiCell(colW-3, 5.0, cells[cIdx], "", "L", false)
				}
				pdf.SetXY(startX, startY+rowH)
			}
			pdf.Ln(2.5)
		}
	}

	if err := pdf.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writePDFInlineRuns(pdf *fpdf.Fpdf, runs []inlineRun, lineH float64) {
	if len(runs) == 1 && !runs[0].bold && !runs[0].code {
		pdf.MultiCell(0, lineH, runs[0].text, "", "L", false)
		return
	}
	for _, r := range runs {
		switch {
		case r.code:
			pdf.SetFont("DejaVuMono", "", 9.5)
		case r.bold:
			pdf.SetFont("DejaVu", "B", 10.5)
		default:
			pdf.SetFont("DejaVu", "", 10.5)
		}
		pdf.Write(lineH, r.text)
	}
	pdf.Ln(lineH)
}

func stripInlineMarkdown(text string) string {
	runs := parseInlineRuns(text)
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.text)
	}
	return b.String()
}
