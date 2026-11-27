package uploads

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	ChatMaxOfficeUncompressedBytes int64 = 20 << 20
	ChatMaxOfficeEntries                 = 2000
	ChatMaxRTFBytes                int64 = 20 << 20
)

type safeZipReader struct {
	zr        *zip.Reader
	byName    map[string]*zip.File
	totalRead int64
	maxTotal  int64
}

func openSafeZip(r io.ReaderAt, size int64, maxEntries int, maxTotal int64) (*safeZipReader, error) {
	var magic [4]byte
	if size < 4 {
		return nil, ErrAttachmentContentMismatch
	}
	if _, err := r.ReadAt(magic[:], 0); err != nil {
		return nil, ErrAttachmentContentMismatch
	}
	if !bytes.Equal(magic[:], []byte{0x50, 0x4B, 0x03, 0x04}) {
		return nil, ErrAttachmentContentMismatch
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, ErrAttachmentContentMismatch
	}
	if len(zr.File) > maxEntries {
		return nil, ErrZipBomb
	}

	byName := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		cleanPath, err := validateZipEntryPath(f.Name)
		if err != nil {
			return nil, err
		}
		if f.Mode()&0o170000 == 0o120000 {
			return nil, ErrZipSlip
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") || cleanPath == "." {
			continue
		}
		byName[cleanPath] = f
	}

	return &safeZipReader{
		zr:       zr,
		byName:   byName,
		maxTotal: maxTotal,
	}, nil
}

func (s *safeZipReader) has(name string) bool {
	_, ok := s.byName[name]
	return ok
}

func (s *safeZipReader) readEntry(name string) ([]byte, error) {
	f, ok := s.byName[name]
	if !ok {
		return nil, ErrAttachmentContentMismatch
	}
	remaining := s.maxTotal - s.totalRead
	if remaining <= 0 {
		return nil, ErrZipBomb
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrAttachmentContentMismatch
	}
	data, readErr := io.ReadAll(io.LimitReader(rc, remaining+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		return nil, ErrAttachmentContentMismatch
	}
	s.totalRead += int64(len(data))
	if s.totalRead > s.maxTotal {
		return nil, ErrZipBomb
	}
	if int64(len(data)) > ChatMaxZIPRatioMinBytes {
		comp := int64(f.CompressedSize64)
		if comp == 0 || int64(len(data))/comp > ChatMaxZIPRatio {
			return nil, ErrZipBomb
		}
	}
	return data, nil
}

func newSafeXMLDecoder(data []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(data))
	// Populate only standard HTML entities; Go's encoding/xml never resolves external DTD/XXE.
	d.Entity = xml.HTMLEntity
	return d
}

func extractOfficeDocument(ext string, r io.ReaderAt, size int64, maxExtractedBytes int) (string, string, error) {
	sz, err := openSafeZip(r, size, ChatMaxOfficeEntries, ChatMaxOfficeUncompressedBytes)
	if err != nil {
		return "", "", err
	}

	var text string
	switch ext {
	case ".docx":
		text, err = extractDOCXText(sz)
	case ".xlsx":
		text, err = extractXLSXText(sz)
	case ".pptx":
		text, err = extractPPTXText(sz)
	case ".odt":
		text, err = extractODTText(sz)
	case ".epub":
		text, err = extractEPUBText(sz)
	default:
		return "", "", ErrAttachmentDisallowed
	}
	if err != nil {
		return "", "", err
	}
	return finalizeExtractedDocumentText(text, maxExtractedBytes)
}

func finalizeExtractedDocumentText(raw string, maxBytes int) (string, string, error) {
	text := strings.TrimSpace(strings.ToValidUTF8(raw, ""))
	if text == "" {
		return "[Документ не содержит извлекаемого текста.]", "В документе не найден извлекаемый текст.", nil
	}
	if maxBytes > 0 && len(text) > maxBytes {
		const note = "\n\n[Текст документа усечён по лимиту размера.]"
		clipped := clipValidUTF8(text, max(128, maxBytes-len(note))) + note
		return clipped, "Текст документа усечён по лимиту контекста.", nil
	}
	return text, "", nil
}

func extractDOCXText(sz *safeZipReader) (string, error) {
	if !sz.has("word/document.xml") {
		return "", ErrAttachmentContentMismatch
	}
	var parts []string
	if body, err := sz.readEntry("word/document.xml"); err != nil {
		return "", err
	} else if parsed, err := parseWordprocessingXML(body); err != nil {
		return "", err
	} else if parsed != "" {
		parts = append(parts, parsed)
	}

	var extras []string
	for name := range sz.byName {
		if strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml") ||
			strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml") ||
			name == "word/footnotes.xml" || name == "word/endnotes.xml" {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	for _, name := range extras {
		data, err := sz.readEntry(name)
		if err != nil {
			return "", err
		}
		parsed, err := parseWordprocessingXML(data)
		if err != nil {
			return "", err
		}
		if parsed != "" {
			parts = append(parts, "--- "+path.Base(name)+" ---\n"+parsed)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func parseWordprocessingXML(data []byte) (string, error) {
	d := newSafeXMLDecoder(data)
	var b strings.Builder
	inText := false
	inDeleted := 0
	inRow := false
	cellInRow := 0

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", ErrAttachmentContentMismatch
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "delText", "del":
				inDeleted++
			case "tr":
				inRow = true
				cellInRow = 0
			case "tc":
				if inRow && cellInRow > 0 {
					b.WriteByte('\t')
				}
				cellInRow++
			case "t":
				if inDeleted == 0 {
					inText = true
				}
			case "tab":
				if inDeleted == 0 {
					b.WriteByte('\t')
				}
			case "br", "cr":
				if inDeleted == 0 {
					b.WriteByte('\n')
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "delText", "del":
				if inDeleted > 0 {
					inDeleted--
				}
			case "t":
				inText = false
			case "p":
				if !inRow && b.Len() > 0 {
					b.WriteByte('\n')
				}
			case "tr":
				inRow = false
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText && inDeleted == 0 {
				b.Write(t)
			}
		}
	}
	return normalizeExtractedLines(b.String()), nil
}

var xlsxSheetRe = regexp.MustCompile(`^xl/worksheets/sheet(\d+)\.xml$`)

func extractXLSXText(sz *safeZipReader) (string, error) {
	var sheetPaths []string
	for name := range sz.byName {
		if xlsxSheetRe.MatchString(name) {
			sheetPaths = append(sheetPaths, name)
		}
	}
	if len(sheetPaths) == 0 && !sz.has("xl/workbook.xml") {
		return "", ErrAttachmentContentMismatch
	}
	sort.Slice(sheetPaths, func(i, j int) bool {
		return extractTrailingInt(xlsxSheetRe, sheetPaths[i]) < extractTrailingInt(xlsxSheetRe, sheetPaths[j])
	})

	var shared []string
	if sz.has("xl/sharedStrings.xml") {
		data, err := sz.readEntry("xl/sharedStrings.xml")
		if err != nil {
			return "", err
		}
		shared, err = parseXLSXSharedStrings(data)
		if err != nil {
			return "", err
		}
	}

	var sheetNames []string
	if sz.has("xl/workbook.xml") {
		if data, err := sz.readEntry("xl/workbook.xml"); err == nil {
			sheetNames = parseXLSXSheetNames(data)
		} else {
			return "", err
		}
	}

	var out strings.Builder
	for idx, sp := range sheetPaths {
		data, err := sz.readEntry(sp)
		if err != nil {
			return "", err
		}
		rows, err := parseXLSXSheet(data, shared)
		if err != nil {
			return "", err
		}
		name := fmt.Sprintf("Лист %d", idx+1)
		if idx < len(sheetNames) && sheetNames[idx] != "" {
			name = sheetNames[idx]
		}
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString("--- Лист: ")
		out.WriteString(name)
		out.WriteString(" ---\n")
		for _, row := range rows {
			out.WriteString(strings.Join(row, "\t"))
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}

func parseXLSXSharedStrings(data []byte) ([]string, error) {
	d := newSafeXMLDecoder(data)
	var stringsOut []string
	var cur strings.Builder
	inSI := false
	inT := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrAttachmentContentMismatch
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				inSI = true
				cur.Reset()
			} else if inSI && t.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			} else if t.Name.Local == "si" {
				stringsOut = append(stringsOut, cur.String())
				inSI = false
			}
		case xml.CharData:
			if inSI && inT {
				cur.Write(t)
			}
		}
	}
	return stringsOut, nil
}

func parseXLSXSheetNames(data []byte) []string {
	d := newSafeXMLDecoder(data)
	var names []string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			for _, attr := range se.Attr {
				if attr.Name.Local == "name" {
					names = append(names, attr.Value)
					break
				}
			}
		}
	}
	return names
}

func parseXLSXSheet(data []byte, shared []string) ([][]string, error) {
	d := newSafeXMLDecoder(data)
	var rows [][]string
	var curRow []string
	inRow := false
	inCell := false
	cellType := ""
	var valBuf strings.Builder
	inVal := false
	inInlineStr := false
	inT := false

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrAttachmentContentMismatch
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				inRow = true
				curRow = curRow[:0]
			case "c":
				if inRow {
					inCell = true
					cellType = ""
					valBuf.Reset()
					for _, a := range t.Attr {
						if a.Name.Local == "t" {
							cellType = a.Value
						}
					}
				}
			case "v":
				if inCell {
					inVal = true
				}
			case "is":
				if inCell {
					inInlineStr = true
				}
			case "t":
				if inCell && (inInlineStr || cellType == "str" || cellType == "inlineStr") {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inVal = false
			case "t":
				inT = false
			case "is":
				inInlineStr = false
			case "c":
				if inCell {
					raw := valBuf.String()
					if cellType == "s" {
						if idx, convErr := strconv.Atoi(strings.TrimSpace(raw)); convErr == nil && idx >= 0 && idx < len(shared) {
							raw = shared[idx]
						}
					}
					curRow = append(curRow, strings.TrimSpace(raw))
					inCell = false
				}
			case "row":
				if inRow {
					nonEmpty := false
					for _, c := range curRow {
						if c != "" {
							nonEmpty = true
							break
						}
					}
					if nonEmpty {
						copied := make([]string, len(curRow))
						copy(copied, curRow)
						rows = append(rows, copied)
					}
					inRow = false
				}
			}
		case xml.CharData:
			if inCell && (inVal || inT) {
				valBuf.Write(t)
			}
		}
	}
	return rows, nil
}

var pptxSlideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func extractPPTXText(sz *safeZipReader) (string, error) {
	var slides []string
	for name := range sz.byName {
		if pptxSlideRe.MatchString(name) {
			slides = append(slides, name)
		}
	}
	if len(slides) == 0 && !sz.has("ppt/presentation.xml") {
		return "", ErrAttachmentContentMismatch
	}
	sort.Slice(slides, func(i, j int) bool {
		return extractTrailingInt(pptxSlideRe, slides[i]) < extractTrailingInt(pptxSlideRe, slides[j])
	})

	var out strings.Builder
	for idx, slidePath := range slides {
		num := extractTrailingInt(pptxSlideRe, slidePath)
		if num <= 0 {
			num = idx + 1
		}
		data, err := sz.readEntry(slidePath)
		if err != nil {
			return "", err
		}
		slideText, err := parseDrawingMLText(data)
		if err != nil {
			return "", err
		}

		notesText := ""
		if notesPath := findPPTXNotesPath(sz, slidePath, num); notesPath != "" {
			nData, err := sz.readEntry(notesPath)
			if err != nil {
				return "", err
			}
			notesText, err = parseDrawingMLText(nData)
			if err != nil {
				return "", err
			}
		}

		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(fmt.Sprintf("--- Слайд %d ---\n", num))
		if slideText != "" {
			out.WriteString(slideText)
		}
		if notesText != "" {
			if slideText != "" {
				out.WriteByte('\n')
			}
			out.WriteString("[Заметки спикера]\n")
			out.WriteString(notesText)
		}
	}
	return out.String(), nil
}

func findPPTXNotesPath(sz *safeZipReader, slidePath string, slideNum int) string {
	relsPath := "ppt/slides/_rels/" + path.Base(slidePath) + ".rels"
	if sz.has(relsPath) {
		if data, err := sz.readEntry(relsPath); err == nil {
			d := newSafeXMLDecoder(data)
			for {
				tok, err := d.Token()
				if err != nil {
					break
				}
				if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
					var relType, target string
					for _, a := range se.Attr {
						switch a.Name.Local {
						case "Type":
							relType = a.Value
						case "Target":
							target = a.Value
						}
					}
					if strings.HasSuffix(relType, "/notesSlide") && target != "" {
						resolved, err := validateZipEntryPath(path.Join("ppt/slides", target))
						if err == nil && sz.has(resolved) {
							return resolved
						}
					}
				}
			}
		}
	}
	fallback := fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", slideNum)
	if sz.has(fallback) {
		return fallback
	}
	return ""
}

func parseDrawingMLText(data []byte) (string, error) {
	d := newSafeXMLDecoder(data)
	var b strings.Builder
	inText := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", ErrAttachmentContentMismatch
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return normalizeExtractedLines(b.String()), nil
}

func extractODTText(sz *safeZipReader) (string, error) {
	if !sz.has("content.xml") {
		return "", ErrAttachmentContentMismatch
	}
	if sz.has("mimetype") {
		mt, err := sz.readEntry("mimetype")
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(strings.TrimSpace(string(mt)), "application/vnd.oasis.opendocument") {
			return "", ErrAttachmentContentMismatch
		}
	}
	data, err := sz.readEntry("content.xml")
	if err != nil {
		return "", err
	}
	d := newSafeXMLDecoder(data)
	var b strings.Builder
	inBody := false
	inRow := false
	cellInRow := 0

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", ErrAttachmentContentMismatch
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "body":
				inBody = true
			case "table-row":
				if inBody {
					inRow = true
					cellInRow = 0
				}
			case "table-cell":
				if inBody && inRow {
					if cellInRow > 0 {
						b.WriteByte('\t')
					}
					cellInRow++
				}
			case "tab":
				if inBody {
					b.WriteByte('\t')
				}
			case "line-break":
				if inBody {
					b.WriteByte('\n')
				}
			case "s":
				if inBody {
					count := 1
					for _, a := range t.Attr {
						if a.Name.Local == "c" {
							if n, err := strconv.Atoi(a.Value); err == nil && n > 1 && n <= 64 {
								count = n
							}
						}
					}
					b.WriteString(strings.Repeat(" ", count))
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "body":
				inBody = false
			case "p", "h":
				if inBody && !inRow && b.Len() > 0 {
					b.WriteByte('\n')
				}
			case "table-row":
				if inBody {
					inRow = false
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
				}
			}
		case xml.CharData:
			if inBody {
				b.Write(t)
			}
		}
	}
	return normalizeExtractedLines(b.String()), nil
}

func extractEPUBText(sz *safeZipReader) (string, error) {
	if !sz.has("META-INF/container.xml") {
		return "", ErrAttachmentContentMismatch
	}
	if sz.has("mimetype") {
		mt, err := sz.readEntry("mimetype")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(mt)) != "application/epub+zip" {
			return "", ErrAttachmentContentMismatch
		}
	}
	containerData, err := sz.readEntry("META-INF/container.xml")
	if err != nil {
		return "", err
	}
	opfPath := parseEPUBContainerOPF(containerData)
	if opfPath == "" {
		return "", ErrAttachmentContentMismatch
	}
	cleanOPF, err := validateZipEntryPath(opfPath)
	if err != nil || !sz.has(cleanOPF) {
		return "", ErrAttachmentContentMismatch
	}
	opfData, err := sz.readEntry(cleanOPF)
	if err != nil {
		return "", err
	}
	chapterPaths := parseEPUBSpinePaths(cleanOPF, opfData, sz)
	if len(chapterPaths) == 0 {
		for name := range sz.byName {
			ext := strings.ToLower(path.Ext(name))
			if ext == ".xhtml" || ext == ".html" || ext == ".htm" {
				chapterPaths = append(chapterPaths, name)
			}
		}
		sort.Strings(chapterPaths)
	}
	var parts []string
	for _, cp := range chapterPaths {
		if !sz.has(cp) {
			continue
		}
		raw, err := sz.readEntry(cp)
		if err != nil {
			return "", err
		}
		text := stripHTMLToText(raw)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func parseEPUBContainerOPF(data []byte) string {
	d := newSafeXMLDecoder(data)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "rootfile" {
			for _, a := range se.Attr {
				if a.Name.Local == "full-path" {
					return a.Value
				}
			}
		}
	}
	return ""
}

func parseEPUBSpinePaths(opfPath string, opfData []byte, sz *safeZipReader) []string {
	d := newSafeXMLDecoder(opfData)
	manifest := make(map[string]string)
	var spineIDs []string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "item":
			var id, href, mediaType string
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "id":
					id = a.Value
				case "href":
					href = a.Value
				case "media-type":
					mediaType = a.Value
				}
			}
			if id != "" && href != "" && (mediaType == "" || strings.Contains(mediaType, "html") || strings.Contains(mediaType, "xml")) {
				manifest[id] = href
			}
		case "itemref":
			for _, a := range se.Attr {
				if a.Name.Local == "idref" && a.Value != "" {
					spineIDs = append(spineIDs, a.Value)
					break
				}
			}
		}
	}
	opfDir := path.Dir(opfPath)
	var out []string
	for _, id := range spineIDs {
		href, ok := manifest[id]
		if !ok {
			continue
		}
		if idx := strings.IndexByte(href, '#'); idx >= 0 {
			href = href[:idx]
		}
		joined := href
		if opfDir != "." && opfDir != "" {
			joined = path.Join(opfDir, href)
		}
		clean, err := validateZipEntryPath(joined)
		if err == nil && sz.has(clean) {
			out = append(out, clean)
		}
	}
	return out
}

func stripHTMLToText(data []byte) string {
	d := newSafeXMLDecoder(data)
	var b strings.Builder
	skipDepth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Fallback to simple angle-bracket stripping if XHTML is slightly malformed.
			return stripTagsFallback(string(data))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			if local == "script" || local == "style" || local == "head" {
				skipDepth++
				continue
			}
			if skipDepth == 0 && (local == "br" || local == "hr") {
				b.WriteByte('\n')
			}
		case xml.EndElement:
			local := strings.ToLower(t.Name.Local)
			if local == "script" || local == "style" || local == "head" {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth == 0 {
				switch local {
				case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr", "blockquote", "section", "article":
					b.WriteByte('\n')
				case "td", "th":
					b.WriteByte('\t')
				}
			}
		case xml.CharData:
			if skipDepth == 0 {
				b.Write(t)
			}
		}
	}
	return normalizeExtractedLines(b.String())
}

func stripTagsFallback(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteByte(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	return normalizeExtractedLines(b.String())
}

var ignorableRTFDestinations = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true,
	"pict": true, "object": true, "fldinst": true, "datafield": true,
	"themedata": true, "colorschememapping": true, "latentstyles": true,
	"datastore": true, "xmlnstbl": true, "listtable": true, "listoverridetable": true,
	"revtbl": true, "rsidtbl": true, "generator": true,
}

func extractRTFText(data []byte, maxExtractedBytes int) (string, string, error) {
	if !bytes.HasPrefix(data, []byte(`{\rtf`)) {
		return "", "", ErrAttachmentContentMismatch
	}
	type rtfGroup struct {
		uc   int
		skip bool
	}
	stack := []rtfGroup{{uc: 1, skip: false}}
	codepage := 1252
	if bytes.Contains(data, []byte(`\ansicpg1251`)) || bytes.Contains(data, []byte(`\fcharset204`)) || bytes.Contains(data, []byte(`\lang1049`)) {
		codepage = 1251
	}

	var out strings.Builder
	var highSurrogate rune
	flushSurrogate := func() {
		if highSurrogate != 0 {
			highSurrogate = 0
		}
	}
	writeRune := func(r rune) {
		if utf16.IsSurrogate(r) {
			if r >= 0xD800 && r <= 0xDBFF {
				highSurrogate = r
				return
			}
			if highSurrogate != 0 && r >= 0xDC00 && r <= 0xDFFF {
				out.WriteRune(utf16.DecodeRune(highSurrogate, r))
				highSurrogate = 0
				return
			}
			highSurrogate = 0
			return
		}
		flushSurrogate()
		out.WriteRune(r)
	}

	skipFallback := 0
	i := 0
	for i < len(data) {
		top := &stack[len(stack)-1]
		ch := data[i]
		switch ch {
		case '{':
			skipFallback = 0
			stack = append(stack, rtfGroup{uc: top.uc, skip: top.skip})
			i++
		case '}':
			skipFallback = 0
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '\r', '\n':
			i++
		case '\\':
			if i+1 >= len(data) {
				i++
				continue
			}
			next := data[i+1]
			if next == '\\' || next == '{' || next == '}' {
				if skipFallback > 0 {
					skipFallback--
				} else if !top.skip {
					writeRune(rune(next))
				}
				i += 2
				continue
			}
			if next == '*' {
				top.skip = true
				i += 2
				continue
			}
			if next == '~' {
				if skipFallback > 0 {
					skipFallback--
				} else if !top.skip {
					writeRune(' ')
				}
				i += 2
				continue
			}
			if next == '_' {
				if skipFallback > 0 {
					skipFallback--
				} else if !top.skip {
					writeRune('-')
				}
				i += 2
				continue
			}
			if next == '\'' && i+3 < len(data) {
				hexVal, err := strconv.ParseUint(string(data[i+2:i+4]), 16, 8)
				i += 4
				if err == nil {
					if skipFallback > 0 {
						skipFallback--
					} else if !top.skip {
						writeRune(decodeRTFByte(byte(hexVal), codepage))
					}
				}
				continue
			}
			if !((next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z')) {
				i += 2
				continue
			}
			j := i + 1
			for j < len(data) && ((data[j] >= 'a' && data[j] <= 'z') || (data[j] >= 'A' && data[j] <= 'Z')) {
				j++
			}
			word := string(data[i+1 : j])
			hasParam := false
			param := 0
			if j < len(data) && (data[j] == '-' || (data[j] >= '0' && data[j] <= '9')) {
				k := j
				if data[k] == '-' {
					k++
				}
				for k < len(data) && data[k] >= '0' && data[k] <= '9' {
					k++
				}
				if v, err := strconv.Atoi(string(data[j:k])); err == nil {
					hasParam = true
					param = v
				}
				j = k
			}
			if j < len(data) && data[j] == ' ' {
				j++
			}
			i = j

			if ignorableRTFDestinations[word] {
				top.skip = true
				continue
			}
			switch word {
			case "ansicpg":
				if hasParam {
					codepage = param
				}
			case "uc":
				if hasParam && param >= 0 {
					top.uc = param
				}
			case "u":
				if hasParam && !top.skip {
					writeRune(rune(uint16(param)))
					skipFallback = top.uc
				}
			case "par", "line":
				if !top.skip {
					flushSurrogate()
					out.WriteByte('\n')
				}
			case "tab":
				if !top.skip {
					flushSurrogate()
					out.WriteByte('\t')
				}
			case "emdash":
				if !top.skip {
					writeRune('—')
				}
			case "endash":
				if !top.skip {
					writeRune('–')
				}
			case "lquote":
				if !top.skip {
					writeRune('‘')
				}
			case "rquote":
				if !top.skip {
					writeRune('’')
				}
			case "ldblquote":
				if !top.skip {
					writeRune('“')
				}
			case "rdblquote":
				if !top.skip {
					writeRune('”')
				}
			}
		default:
			if skipFallback > 0 {
				skipFallback--
			} else if !top.skip {
				if ch < 0x80 {
					writeRune(rune(ch))
				} else {
					writeRune(decodeRTFByte(ch, codepage))
				}
			}
			i++
		}
	}
	return finalizeExtractedDocumentText(normalizeExtractedLines(out.String()), maxExtractedBytes)
}

var cp1251Upper = [128]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021,
	0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0xFFFD, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7,
	0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7,
	0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
	0x0410, 0x0411, 0x0412, 0x0413, 0x0414, 0x0415, 0x0416, 0x0417,
	0x0418, 0x0419, 0x041A, 0x041B, 0x041C, 0x041D, 0x041E, 0x041F,
	0x0420, 0x0421, 0x0422, 0x0423, 0x0424, 0x0425, 0x0426, 0x0427,
	0x0428, 0x0429, 0x042A, 0x042B, 0x042C, 0x042D, 0x042E, 0x042F,
	0x0430, 0x0431, 0x0432, 0x0433, 0x0434, 0x0435, 0x0436, 0x0437,
	0x0438, 0x0439, 0x043A, 0x043B, 0x043C, 0x043D, 0x043E, 0x043F,
	0x0440, 0x0441, 0x0442, 0x0443, 0x0444, 0x0445, 0x0446, 0x0447,
	0x0448, 0x0449, 0x044A, 0x044B, 0x044C, 0x044D, 0x044E, 0x044F,
}

func decodeRTFByte(b byte, codepage int) rune {
	if b < 0x80 {
		return rune(b)
	}
	if codepage == 1251 {
		return cp1251Upper[b-0x80]
	}
	r := rune(b)
	if utf8.ValidRune(r) {
		return r
	}
	return '\uFFFD'
}

func extractTrailingInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func normalizeExtractedLines(raw string) string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	emptyRun := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			emptyRun++
			if emptyRun <= 1 && len(out) > 0 {
				out = append(out, "")
			}
			continue
		}
		emptyRun = 0
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
