package docext

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip builds an in-memory archive from name → content.
func makeZip(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const docxXML = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
 <w:body>
  <w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>World</w:t></w:r></w:p>
  <w:p><w:r><w:t>第二段落: 予算は</w:t></w:r><w:r><w:t>1000円</w:t></w:r></w:p>
  <w:tbl><w:tr><w:tc><w:p><w:r><w:t>cellA</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
 </w:body>
</w:document>`

func TestExtractDocxFixture(t *testing.T) {
	data := makeZip(t, map[string]string{"word/document.xml": docxXML})
	if Detect(data) != KindDocx {
		t.Fatal("docx not detected")
	}
	text, note, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello\tWorld", "第二段落: 予算は1000円", "cellA"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if note != "" {
		t.Errorf("unexpected note %q", note)
	}
	// Paragraphs stay in order.
	if strings.Index(text, "Hello") > strings.Index(text, "第二段落") {
		t.Error("paragraph order lost")
	}
}

// The real-producer ground truth (ADR-0026 §5): a .docx written by
// macOS textutil, not by the XML we parse.
func TestExtractDocxFromTextutil(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("ground truth token WQ-9917\nsecond line here"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.docx")
	if err := exec.Command("textutil", "-convert", "docx", "-output", out, src).Run(); err != nil {
		t.Skipf("textutil unavailable: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "WQ-9917") || !strings.Contains(text, "second line") {
		t.Errorf("textutil docx extraction lost content:\n%s", text)
	}
}

func xlsxFixture(t *testing.T) []byte {
	return makeZip(t, map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
 <sheets><sheet name="経費" sheetId="1" r:id="rId1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></sheets>
</workbook>`,
		"xl/sharedStrings.xml": `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2">
 <si><t>項目</t></si><si><r><t>交通</t></r><r><t>費</t></r></si>
</sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
 <sheetData>
  <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><t>金額</t></is></c></row>
  <row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><v>1980</v></c></row>
 </sheetData>
</worksheet>`,
	})
}

func TestExtractXlsxFixture(t *testing.T) {
	data := xlsxFixture(t)
	if Detect(data) != KindXlsx {
		t.Fatal("xlsx not detected")
	}
	text, note, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### sheet: 経費", "項目\t金額", "交通費\t1980"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if !strings.Contains(note, "as stored") {
		t.Errorf("note %q must disclose raw cell values", note)
	}
}

func TestExtractPptxFixture(t *testing.T) {
	slide := func(txts ...string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">`)
		for _, s := range txts {
			b.WriteString(`<p:sp><a:t>` + s + `</a:t></p:sp>`)
		}
		b.WriteString(`</p:sld>`)
		return b.String()
	}
	data := makeZip(t, map[string]string{
		"ppt/slides/slide2.xml":  slide("まとめ", "以上"),
		"ppt/slides/slide1.xml":  slide("タイトル: 計画"),
		"ppt/slides/slide10.xml": slide("付録"),
	})
	if Detect(data) != KindPptx {
		t.Fatal("pptx not detected")
	}
	text, _, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// Numeric order: 1, 2, 10 — not lexicographic (1, 10, 2).
	i1 := strings.Index(text, "### slide 1\n")
	i2 := strings.Index(text, "### slide 2\n")
	i10 := strings.Index(text, "### slide 10\n")
	if i1 < 0 || i2 < 0 || i10 < 0 || i1 >= i2 || i2 >= i10 {
		t.Errorf("slide order wrong (1=%d 2=%d 10=%d):\n%s", i1, i2, i10, text)
	}
	if !strings.Contains(text, "タイトル: 計画") || !strings.Contains(text, "付録") {
		t.Errorf("slide text lost:\n%s", text)
	}
}

func TestExtractRejectsUnknownAndCaps(t *testing.T) {
	if _, _, err := Extract([]byte("not a zip"), DefaultLimits()); err == nil ||
		!strings.Contains(err.Error(), "out of scope") {
		t.Error("unknown data must fail naming the supported set")
	}
	// Member cap: a document.xml above the decompression cap refuses.
	big := makeZip(t, map[string]string{"word/document.xml": strings.Repeat("x", 4096)})
	if _, _, err := Extract(big, Limits{TextBytes: 1024, MemberBytes: 100}); err == nil ||
		!strings.Contains(err.Error(), "member cap") {
		t.Errorf("member cap not enforced: %v", err)
	}
	// Text cap: reported, not silent.
	data := makeZip(t, map[string]string{"word/document.xml": `<w:document xmlns:w="x"><w:body><w:p><w:r><w:t>` + strings.Repeat("a", 500) + `</w:t></w:r></w:p></w:body></w:document>`})
	text, note, err := Extract(data, Limits{TextBytes: 100, MemberBytes: 1 << 20})
	if err != nil || len(text) != 100 || !strings.Contains(note, "truncated") {
		t.Errorf("text cap: len=%d note=%q err=%v", len(text), note, err)
	}
}

// Aggregate budget (review round 2): the per-member cap bounds one
// member, but many under-cap members must not accumulate unbounded
// text before the final clip — read_document is ungated, so a crafted
// workbook was a model-reachable memory exhaustion.
func TestExtractStopsAccumulatingPastTheTextBudget(t *testing.T) {
	cell := `<row><c t="inlineStr"><is><t>` + strings.Repeat("x", 1000) + `</t></is></c></row>`
	members := map[string]string{}
	for i := 1; i <= 50; i++ {
		members[fmt.Sprintf("xl/worksheets/sheet%d.xml", i)] =
			`<worksheet><sheetData>` + cell + `</sheetData></worksheet>`
	}
	data := makeZip(t, members)
	lim := Limits{TextBytes: 4 * 1024, MemberBytes: 1 << 20}
	text, note, err := extractXlsx(data, lim)
	if err != nil {
		t.Fatal(err)
	}
	// Bounded by the budget plus at most one member's text — never the
	// 50-member total (~50KB).
	if len(text) > lim.TextBytes+2048 {
		t.Errorf("extracted %d bytes; budget %d — aggregation unbounded", len(text), lim.TextBytes)
	}
	if note == "" {
		t.Error("truncation must be reported, not silent")
	}
}

// Review after v0.68.2: worksheet numbers have gaps once a sheet was
// deleted (sheet1.xml, sheet3.xml); every member present is extracted,
// in numeric order, named by workbook order.
func TestExtractXlsxWithASheetGap(t *testing.T) {
	sheet := func(text string) string {
		return `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>` + text + `</t></is></c></row></sheetData></worksheet>`
	}
	data := makeZip(t, map[string]string{
		"xl/workbook.xml":          `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="first" sheetId="1"/><sheet name="third" sheetId="3"/></sheets></workbook>`,
		"xl/worksheets/sheet1.xml": sheet("ONE"),
		"xl/worksheets/sheet3.xml": sheet("THREE"),
	})
	text, _, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### sheet: first", "ONE", "### sheet: third", "THREE"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Index(text, "ONE") > strings.Index(text, "THREE") {
		t.Error("sheets out of numeric order")
	}
}

// clipText cuts on a rune boundary.
func TestClipTextKeepsRunesWhole(t *testing.T) {
	out, note, err := clipText(strings.Repeat("あ", 10), 4)
	if err != nil {
		t.Fatal(err)
	}
	if out != "あ" || !strings.Contains(note, "3 of 30") {
		t.Errorf("clipText = %q, %q", out, note)
	}
}

// Sheet names follow the workbook's relationships, not file order: a
// reordered workbook lists sheet3.xml first.
func TestExtractXlsxNamesFollowRelationships(t *testing.T) {
	sheet := func(text string) string {
		return `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>` + text + `</t></is></c></row></sheetData></worksheet>`
	}
	data := makeZip(t, map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Later" sheetId="3" r:id="rId3"/><sheet name="Earlier" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x" Target="worksheets/sheet1.xml"/><Relationship Id="rId3" Type="x" Target="/xl/worksheets/sheet3.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet("ONE"),
		"xl/worksheets/sheet3.xml":   sheet("THREE"),
	})
	text, _, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "### sheet: Earlier\nONE") || !strings.Contains(text, "### sheet: Later\nTHREE") {
		t.Errorf("names not matched through relationships:\n%s", text)
	}
	if strings.Index(text, "### sheet: Later") > strings.Index(text, "### sheet: Earlier") {
		t.Errorf("display order not followed (workbook lists Later first):\n%s", text)
	}
}

// ADR-0072 §4.5: sheets come in the workbook's display order, empty
// columns keep their place, and rich inline strings are joined.
func TestXlsxDisplayOrderCellsAndRichStrings(t *testing.T) {
	data := makeZip(t, map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Later" sheetId="3" r:id="rId3"/><sheet name="Earlier" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x" Target="worksheets/sheet1.xml"/><Relationship Id="rId3" Type="x" Target="worksheets/sheet3.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>a</t></is></c><c r="C1" t="inlineStr"><is><r><t>ri</t></r><r><t>ch</t></r></is></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet3.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>THREE</t></is></c></row></sheetData></worksheet>`,
	})
	text, _, err := Extract(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(text, "### sheet: Later") > strings.Index(text, "### sheet: Earlier") {
		t.Errorf("sheets not in display order:\n%s", text)
	}
	if !strings.Contains(text, "a\t\trich") {
		t.Errorf("empty column not kept or rich string not joined:\n%s", text)
	}
}

// Slides come in the presentation's order, not the member numbers'.
func TestPptxDisplayOrder(t *testing.T) {
	slide := func(s string) string {
		return `<?xml version="1.0"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sp><a:t>` + s + `</a:t></p:sp></p:sld>`
	}
	data := makeZip(t, map[string]string{
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/></Types>`,
		"ppt/presentation.xml":            `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId id="257" r:id="rId3"/><p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x" Target="slides/slide1.xml"/><Relationship Id="rId3" Type="x" Target="slides/slide3.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           slide("FIRST-FILE"),
		"ppt/slides/slide3.xml":           slide("THIRD-FILE"),
	})
	text, _, err := extractPptx(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(text, "THIRD-FILE") > strings.Index(text, "FIRST-FILE") {
		t.Errorf("slides not in presentation order:\n%s", text)
	}
}

// N01: a cell reference past XFD is not a column, and padding never
// runs past the text budget.
func TestColumnExpansionIsBounded(t *testing.T) {
	if n := columnIndex("ZZZZZZZ1"); n != -1 {
		t.Errorf("columnIndex(ZZZZZZZ1) = %d, want -1", n)
	}
	if n := columnIndex("XFD1"); n != 16383 {
		t.Errorf("columnIndex(XFD1) = %d, want 16383", n)
	}
	var b strings.Builder
	raw := []byte("<worksheet><sheetData>" + strings.Repeat(`<row><c r="XFD1"><v>1</v></c></row>`, 200) + "</sheetData></worksheet>")
	xlsxSheetText(&b, raw, nil, 20000)
	if b.Len() > 20000+16384*2 {
		t.Errorf("padding ran past the budget: %d bytes", b.Len())
	}
}

// R09: the relationship id is read by namespace, whatever the attribute
// order.
func TestPptxOrderIsAttributeOrderIndependent(t *testing.T) {
	slide := func(s string) string {
		return `<?xml version="1.0"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sp><a:t>` + s + `</a:t></p:sp></p:sld>`
	}
	data := makeZip(t, map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId r:id="rId3" id="257"/><p:sldId r:id="rId1" id="256"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"/><Relationship Id="rId3" Target="slides/slide3.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           slide("FIRST-FILE"),
		"ppt/slides/slide3.xml":           slide("THIRD-FILE"),
	})
	text, _, err := extractPptx(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(text, "THIRD-FILE") > strings.Index(text, "FIRST-FILE") {
		t.Errorf("r:id before id reversed the order:\n%s", text)
	}
}
