// Package docext extracts readable text from the Office XML formats —
// .docx, .xlsx, .pptx — with the standard library only (ADR-0026).
// The formats are zip archives of XML; the slice needed here (text
// out, structure hinted) is small enough that a dependency would cost
// more than it saves. Legacy binary formats (.doc/.xls/.ppt) are out
// of scope by decision, not omission.
package docext

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Limits bound extraction so a huge or crafted file cannot balloon
// memory or context.
type Limits struct {
	// TextBytes caps the extracted text; overflow is reported.
	TextBytes int
	// MemberBytes caps how much of any single zip member is
	// decompressed — the zip-bomb guard.
	MemberBytes int64
}

// DefaultLimits match read_file's order of magnitude.
func DefaultLimits() Limits {
	return Limits{TextBytes: 256 * 1024, MemberBytes: 64 * 1024 * 1024}
}

// Kind identifies a supported document by file signature probing.
type Kind string

const (
	KindDocx    Kind = "docx"
	KindXlsx    Kind = "xlsx"
	KindPptx    Kind = "pptx"
	KindUnknown Kind = ""
)

// Detect sniffs which Office format the zip archive at data holds.
// Content-based (marker members), never extension-based.
func Detect(data []byte) Kind {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return KindUnknown
	}
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml":
			return KindDocx
		case f.Name == "xl/workbook.xml":
			return KindXlsx
		case strings.HasPrefix(f.Name, "ppt/slides/slide"):
			return KindPptx
		}
	}
	return KindUnknown
}

// Extract pulls the text out of a supported document. note reports any
// truncation; an unsupported archive is an error naming what is
// supported.
func Extract(data []byte, lim Limits) (text, note string, err error) {
	switch Detect(data) {
	case KindDocx:
		return extractDocx(data, lim)
	case KindXlsx:
		return extractXlsx(data, lim)
	case KindPptx:
		return extractPptx(data, lim)
	}
	return "", "", fmt.Errorf("not a supported document (supported: .docx/.xlsx/.pptx as Office XML, .pdf natively; legacy .doc/.xls/.ppt are out of scope)")
}

// --- docx ---

// extractDocx walks word/document.xml collecting w:t runs in document
// order; </w:p> ends a paragraph, tabs and breaks become their text
// equivalents. Table cell text arrives in document order for free.
func extractDocx(data []byte, lim Limits) (string, string, error) {
	raw, err := readMember(data, "word/document.xml", lim.MemberBytes)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("parse word/document.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					b.WriteString(s)
				}
			case "tab":
				b.WriteByte('\t')
			case "br":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			if t.Name.Local == "p" {
				b.WriteByte('\n')
			}
		}
	}
	return clipText(strings.TrimSpace(b.String()), lim.TextBytes)
}

// --- xlsx ---

type xlsxSheetRef struct {
	Name string `xml:"name,attr"`
	ID   string `xml:"sheetId,attr"`
	RID  string `xml:"id,attr"`
}

// extractXlsx renders every worksheet as tab-separated rows under a
// "### sheet: <name>" heading. Shared strings, inline strings, and raw
// values are covered; formats (dates as serial numbers) are shown as
// stored, which the note discloses.
func extractXlsx(data []byte, lim Limits) (string, string, error) {
	shared := xlsxSharedStrings(data, lim)

	names := xlsxSheetNames(data, lim)
	var b strings.Builder
	for i := 1; ; i++ {
		// Aggregate budget: the per-member cap bounds ONE member, but
		// many under-cap members accumulated unbounded text before the
		// final clip ever ran — a crafted workbook was a model-
		// reachable OOM, read_document being ungated (review round 2).
		// One member past the budget is fine; clipText trims and
		// reports.
		if b.Len() > lim.TextBytes {
			break
		}
		raw, err := readMember(data, fmt.Sprintf("xl/worksheets/sheet%d.xml", i), lim.MemberBytes)
		if err != nil {
			break // sheets are numbered contiguously
		}
		name := fmt.Sprintf("sheet%d", i)
		if i-1 < len(names) {
			name = names[i-1]
		}
		fmt.Fprintf(&b, "### sheet: %s\n", name)
		xlsxSheetText(&b, raw, shared)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return "", "", fmt.Errorf("no worksheets found")
	}
	text, note, err := clipText(strings.TrimSpace(b.String()), lim.TextBytes)
	if note == "" {
		note = "cell values are shown as stored (dates appear as serial numbers)"
	}
	return text, note, err
}

func xlsxSharedStrings(data []byte, lim Limits) []string {
	raw, err := readMember(data, "xl/sharedStrings.xml", lim.MemberBytes)
	if err != nil {
		return nil
	}
	var out []string
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	depth := 0
	var cur strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				depth = 1
				cur.Reset()
			case "t":
				if depth == 1 {
					var s string
					if err := dec.DecodeElement(&s, &t); err == nil {
						cur.WriteString(s)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "si" && depth == 1 {
				out = append(out, cur.String())
				depth = 0
			}
		}
	}
	return out
}

func xlsxSheetNames(data []byte, lim Limits) []string {
	raw, err := readMember(data, "xl/workbook.xml", lim.MemberBytes)
	if err != nil {
		return nil
	}
	var wb struct {
		Sheets struct {
			Sheet []xlsxSheetRef `xml:"sheet"`
		} `xml:"sheets"`
	}
	if xml.Unmarshal(raw, &wb) != nil {
		return nil
	}
	names := make([]string, 0, len(wb.Sheets.Sheet))
	for _, s := range wb.Sheets.Sheet {
		names = append(names, s.Name)
	}
	return names
}

func xlsxSheetText(b *strings.Builder, raw []byte, shared []string) {
	var sheet struct {
		Rows []struct {
			Cells []struct {
				T  string `xml:"t,attr"`
				V  string `xml:"v"`
				IS struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if xml.Unmarshal(raw, &sheet) != nil {
		return
	}
	for _, row := range sheet.Rows {
		vals := make([]string, 0, len(row.Cells))
		for _, c := range row.Cells {
			switch c.T {
			case "s": // shared-string index
				if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
					vals = append(vals, shared[i])
					continue
				}
				vals = append(vals, c.V)
			case "inlineStr":
				vals = append(vals, c.IS.T)
			default: // numbers, booleans, formula results
				vals = append(vals, c.V)
			}
		}
		b.WriteString(strings.Join(vals, "\t"))
		b.WriteByte('\n')
	}
}

// --- pptx ---

var slideNum = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

// extractPptx renders each slide's a:t runs as a numbered text block,
// slides in numeric order.
func extractPptx(data []byte, lim Limits) (string, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", err
	}
	nums := []int{}
	for _, f := range zr.File {
		if m := slideNum.FindStringSubmatch(f.Name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				nums = append(nums, n)
			}
		}
	}
	if len(nums) == 0 {
		return "", "", fmt.Errorf("no slides found")
	}
	sort.Ints(nums)
	var b strings.Builder
	for _, n := range nums {
		// Same aggregate budget as xlsx (review round 2): slides that
		// individually fit the member cap must not accumulate past the
		// text budget.
		if b.Len() > lim.TextBytes {
			break
		}
		raw, err := readMember(data, fmt.Sprintf("ppt/slides/slide%d.xml", n), lim.MemberBytes)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "### slide %d\n", n)
		dec := xml.NewDecoder(strings.NewReader(string(raw)))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "t" {
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					b.WriteString(s)
					b.WriteByte('\n')
				}
			}
		}
		b.WriteByte('\n')
	}
	return clipText(strings.TrimSpace(b.String()), lim.TextBytes)
}

// --- shared helpers ---

// readMember decompresses one archive member through the size cap: a
// crafted zip must not balloon memory.
func readMember(data []byte, name string, cap int64) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		raw, err := io.ReadAll(io.LimitReader(rc, cap+1))
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) > cap {
			return nil, fmt.Errorf("%s decompresses past the %d-byte member cap", name, cap)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("member %s not found", name)
}

// clipText enforces the extracted-text cap, reporting the truncation
// rather than letting a part masquerade as the whole (the read_file
// convention).
func clipText(text string, cap int) (string, string, error) {
	if cap > 0 && len(text) > cap {
		return text[:cap], fmt.Sprintf("truncated: %d of %d bytes shown", cap, len(text)), nil
	}
	return text, "", nil
}
