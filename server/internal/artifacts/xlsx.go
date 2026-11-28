package artifacts

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// BuildXLSXFromText converts CSV or JSON table content into a formatted .xlsx workbook in memory.
func BuildXLSXFromText(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
	if trimmed == "" {
		return nil, errors.New("empty table content")
	}

	var rows [][]string
	var err error
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		rows, err = parseJSONTable(trimmed)
	} else {
		rows, err = parseCSVTable(trimmed)
	}
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("no rows parsed")
	}

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	const sheet = "Sheet1"
	headerStyle, styleErr := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"#F1F5F9"}, Pattern: 1},
	})

	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	colWidths := make([]int, maxCols)

	for rIdx, row := range rows {
		for cIdx, val := range row {
			cell, err := excelize.CoordinatesToCellName(cIdx+1, rIdx+1)
			if err != nil {
				return nil, err
			}
			// Preserve numeric values on data rows for spreadsheets while keeping strings safe.
			if rIdx > 0 {
				if num, ok := parseNumericCell(val); ok {
					if err := f.SetCellValue(sheet, cell, num); err != nil {
						return nil, err
					}
				} else if err := f.SetCellValue(sheet, cell, val); err != nil {
					return nil, err
				}
			} else {
				if err := f.SetCellValue(sheet, cell, val); err != nil {
					return nil, err
				}
			}
			runeLen := utf8.RuneCountInString(val)
			if runeLen > colWidths[cIdx] {
				colWidths[cIdx] = runeLen
			}
		}
	}

	if styleErr == nil && maxCols > 0 {
		endCell, err := excelize.CoordinatesToCellName(maxCols, 1)
		if err == nil {
			_ = f.SetCellStyle(sheet, "A1", endCell, headerStyle)
		}
	}

	for cIdx, w := range colWidths {
		colName, err := excelize.ColumnNumberToName(cIdx + 1)
		if err != nil {
			continue
		}
		width := float64(w + 4)
		if width < 10 {
			width = 10
		}
		if width > 52 {
			width = 52
		}
		_ = f.SetColWidth(sheet, colName, colName, width)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func parseNumericCell(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "0") && len(s) > 1 && s[1] >= '0' && s[1] <= '9' {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseCSVTable(raw string) ([][]string, error) {
	delimiter := ','
	firstLine := raw
	if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
		firstLine = raw[:idx]
	}
	if strings.Count(firstLine, ";") > strings.Count(firstLine, ",") && !strings.Contains(firstLine, ",") {
		delimiter = ';'
	} else if strings.Count(firstLine, "\t") > strings.Count(firstLine, ",") && !strings.Contains(firstLine, ",") {
		delimiter = '\t'
	}

	r := csv.NewReader(strings.NewReader(raw))
	r.Comma = delimiter
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var rows [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, rec)
	}
	return rows, nil
}

func parseJSONTable(raw string) ([][]string, error) {
	var asRows [][]any
	if err := json.Unmarshal([]byte(raw), &asRows); err == nil && len(asRows) > 0 {
		out := make([][]string, 0, len(asRows))
		for _, r := range asRows {
			line := make([]string, len(r))
			for i, v := range r {
				line[i] = formatJSONCell(v)
			}
			out = append(out, line)
		}
		return out, nil
	}

	var asObjects []map[string]any
	if err := json.Unmarshal([]byte(raw), &asObjects); err == nil && len(asObjects) > 0 {
		keySet := make(map[string]bool)
		var keys []string
		for _, obj := range asObjects {
			for k := range obj {
				if !keySet[k] {
					keySet[k] = true
					keys = append(keys, k)
				}
			}
		}
		sort.Strings(keys)
		out := make([][]string, 0, len(asObjects)+1)
		out = append(out, keys)
		for _, obj := range asObjects {
			line := make([]string, len(keys))
			for i, k := range keys {
				line[i] = formatJSONCell(obj[k])
			}
			out = append(out, line)
		}
		return out, nil
	}

	var wrapper struct {
		Columns []string `json:"columns"`
		Headers []string `json:"headers"`
		Rows    [][]any  `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil && len(wrapper.Rows) > 0 {
		header := wrapper.Columns
		if len(header) == 0 {
			header = wrapper.Headers
		}
		out := make([][]string, 0, len(wrapper.Rows)+1)
		if len(header) > 0 {
			out = append(out, header)
		}
		for _, r := range wrapper.Rows {
			line := make([]string, len(r))
			for i, v := range r {
				line[i] = formatJSONCell(v)
			}
			out = append(out, line)
		}
		return out, nil
	}

	return nil, errors.New("unsupported JSON table format")
}

func formatJSONCell(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}
