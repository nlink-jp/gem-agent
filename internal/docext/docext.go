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
	"unicode/utf8"
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
	byMember := xlsxSheetNamesByMember(data, lim)
	var b strings.Builder
	// Sheets come in the workbook's display order — the sheets array
	// of workbook.xml, each resolved to its member through the
	// relationships (ADR-0072 §4.5); a member the workbook does not
	// reference follows, in numeric order. Numbers alone have gaps
	// (a deleted sheet) and do not follow a reorder.
	for k, n := range xlsxSheetOrder(data, lim) {
		// Aggregate budget: the per-member cap bounds ONE member, but
		// many under-cap members accumulated unbounded text before the
		// final clip ever ran — a crafted workbook was a model-
		// reachable OOM, read_document being ungated (review round 2).
		// One member past the budget is fine; clipText trims and
		// reports.
		if b.Len() > lim.TextBytes {
			break
		}
		raw, err := readMember(data, fmt.Sprintf("xl/worksheets/sheet%d.xml", n), lim.MemberBytes)
		if err != nil {
			continue
		}
		// The name comes from the sheet's relationship (r:id →
		// worksheets/sheetN.xml): a reordered workbook does not number
		// its files in display order (review after v0.68.2). Position
		// is the fallback for a workbook without relationships.
		member := fmt.Sprintf("xl/worksheets/sheet%d.xml", n)
		name := fmt.Sprintf("sheet%d", n)
		if nm, ok := byMember[member]; ok {
			name = nm
		} else if k < len(names) {
			name = names[k]
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

// xlsxSheetNamesByMember maps a worksheet member path to its display
// name through xl/_rels/workbook.xml.rels. Empty when the workbook has
// no relationships.
func xlsxSheetNamesByMember(data []byte, lim Limits) map[string]string {
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
	target := xlsxRelTargets(data, lim)
	if target == nil {
		return nil
	}
	out := map[string]string{}
	for _, s := range wb.Sheets.Sheet {
		if member, ok := target[s.RID]; ok && s.RID != "" {
			out[member] = s.Name
		}
	}
	return out
}

func xlsxSheetText(b *strings.Builder, raw []byte, shared []string) {
	var sheet struct {
		Rows []struct {
			Cells []struct {
				R  string `xml:"r,attr"`
				T  string `xml:"t,attr"`
				V  string `xml:"v"`
				IS struct {
					T    string   `xml:"t"`
					Runs []string `xml:"r>t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if xml.Unmarshal(raw, &sheet) != nil {
		return
	}
	for _, row := range sheet.Rows {
		vals := make([]string, 0, len(row.Cells))
		col := 0
		for _, c := range row.Cells {
			// Empty cells are not stored: the r attribute says where a
			// stored cell sits, and the gap before it is kept as empty
			// columns so A1 and C1 do not become neighbours (ADR-0072
			// §4.5).
			if want := columnIndex(c.R); want > col {
				for ; col < want; col++ {
					vals = append(vals, "")
				}
			}
			col++
			switch c.T {
			case "s": // shared-string index
				if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
					vals = append(vals, shared[i])
					continue
				}
				vals = append(vals, c.V)
			case "inlineStr":
				// A rich inline string carries its text in runs.
				if len(c.IS.Runs) > 0 {
					vals = append(vals, strings.Join(c.IS.Runs, ""))
				} else {
					vals = append(vals, c.IS.T)
				}
			default: // numbers, booleans, formula results
				vals = append(vals, c.V)
			}
		}
		b.WriteString(strings.Join(vals, "\t"))
		b.WriteByte('\n')
	}
}

// columnIndex converts a cell reference's column letters (A1 → 0,
// C7 → 2, AA1 → 26) to a zero-based index; -1 when absent.
func columnIndex(ref string) int {
	n := 0
	seen := false
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
		seen = true
	}
	if !seen {
		return -1
	}
	return n - 1
}

// --- pptx ---

var slideNum = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
var sheetNum = regexp.MustCompile(`^xl/worksheets/sheet(\d+)\.xml$`)

// pptxSlideOrder orders slide numbers as the presentation shows them:
// p:sldIdLst entries resolved through ppt/_rels/presentation.xml.rels,
// then any slide member the list does not reference, numerically.
func pptxSlideOrder(data []byte, lim Limits, present []int) []int {
	raw, err := readMember(data, "ppt/presentation.xml", lim.MemberBytes)
	if err != nil {
		return present
	}
	var pres struct {
		IDs []struct {
			RID string `xml:"id,attr"`
		} `xml:"sldIdLst>sldId"`
	}
	if xml.Unmarshal(raw, &pres) != nil || len(pres.IDs) == 0 {
		return present
	}
	relRaw, err := readMember(data, "ppt/_rels/presentation.xml.rels", lim.MemberBytes)
	if err != nil {
		return present
	}
	var rels struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(relRaw, &rels) != nil {
		return present
	}
	target := map[string]string{}
	for _, r := range rels.Rel {
		t := strings.TrimPrefix(r.Target, "/")
		if !strings.HasPrefix(t, "ppt/") {
			t = "ppt/" + t
		}
		target[r.ID] = t
	}
	memberNum := map[string]int{}
	for _, n := range present {
		memberNum[fmt.Sprintf("ppt/slides/slide%d.xml", n)] = n
	}
	var order []int
	used := map[int]bool{}
	for _, id := range pres.IDs {
		if member, ok := target[id.RID]; ok {
			if n, ok := memberNum[member]; ok && !used[n] {
				order = append(order, n)
				used[n] = true
			}
		}
	}
	for _, n := range present {
		if !used[n] {
			order = append(order, n)
		}
	}
	return order
}

// xlsxSheetOrder is the display order of the worksheet members: the
// workbook's sheets array through the relationships first, then any
// member it does not reference, numerically.
func xlsxSheetOrder(data []byte, lim Limits) []int {
	present := xlsxSheetNumbers(data)
	byMember := xlsxSheetNamesByMember(data, lim)
	if len(byMember) == 0 {
		return present
	}
	// The workbook's order: parse the sheets array once more, in order.
	raw, err := readMember(data, "xl/workbook.xml", lim.MemberBytes)
	if err != nil {
		return present
	}
	var wb struct {
		Sheets struct {
			Sheet []xlsxSheetRef `xml:"sheet"`
		} `xml:"sheets"`
	}
	if xml.Unmarshal(raw, &wb) != nil {
		return present
	}
	memberNum := map[string]int{}
	for _, n := range present {
		memberNum[fmt.Sprintf("xl/worksheets/sheet%d.xml", n)] = n
	}
	relTarget := xlsxRelTargets(data, lim)
	var order []int
	used := map[int]bool{}
	for _, s := range wb.Sheets.Sheet {
		if member, ok := relTarget[s.RID]; ok {
			if n, ok := memberNum[member]; ok && !used[n] {
				order = append(order, n)
				used[n] = true
			}
		}
	}
	for _, n := range present {
		if !used[n] {
			order = append(order, n)
		}
	}
	return order
}

// xlsxRelTargets maps a relationship id to its member path.
func xlsxRelTargets(data []byte, lim Limits) map[string]string {
	relRaw, err := readMember(data, "xl/_rels/workbook.xml.rels", lim.MemberBytes)
	if err != nil {
		return nil
	}
	var rels struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(relRaw, &rels) != nil {
		return nil
	}
	target := map[string]string{}
	for _, r := range rels.Rel {
		t := strings.TrimPrefix(r.Target, "/")
		if !strings.HasPrefix(t, "xl/") {
			t = "xl/" + t
		}
		target[r.ID] = t
	}
	return target
}

// xlsxSheetNumbers lists the worksheet members present, in numeric
// order.
func xlsxSheetNumbers(data []byte) []int {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	var nums []int
	for _, f := range zr.File {
		if m := sheetNum.FindStringSubmatch(f.Name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				nums = append(nums, n)
			}
		}
	}
	sort.Ints(nums)
	return nums
}

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
	// Display order: presentation.xml's sldIdLst through the
	// relationships (ADR-0072 §4.5); a slide the list does not name
	// follows numerically.
	nums = pptxSlideOrder(data, lim, nums)
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
		// On a rune boundary: a byte cut through a multibyte character
		// left a broken tail (review after v0.68.2).
		n := cap
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		return text[:n], fmt.Sprintf("truncated: %d of %d bytes shown", n, len(text)), nil
	}
	return text, "", nil
}
