package mention

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0026: @doc.pdf attaches bytes; @sheet.xlsx attaches extracted
// text; oversized PDFs refuse whole with the limit named.
func TestExpandDocuments(t *testing.T) {
	dir := realTempDir(t)
	pdf := []byte("%PDF-1.4\nfake")
	os.WriteFile(filepath.Join(dir, "spec.pdf"), pdf, 0o644)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte(`<w:document xmlns:w="x"><w:body><w:p><w:r><w:t>合意事項メモ</w:t></w:r></w:p></w:body></w:document>`))
	zw.Close()
	os.WriteFile(filepath.Join(dir, "note.docx"), buf.Bytes(), 0o644)

	atts, problems := Expand(context.Background(), "読んで @spec.pdf と @note.docx", dir, DefaultLimits())
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(atts) != 2 {
		t.Fatalf("attachments = %d, want 2", len(atts))
	}
	if atts[0].Kind != "document" || atts[0].MIME != "application/pdf" || !bytes.Equal(atts[0].Data, pdf) {
		t.Errorf("pdf attachment = %+v", atts[0])
	}
	if atts[1].Kind != "document" || !strings.Contains(atts[1].Content, "合意事項メモ") || len(atts[1].Data) != 0 {
		t.Errorf("docx attachment = %+v", atts[1])
	}

	// Oversized PDF: refused whole, limit named.
	lim := DefaultLimits()
	lim.DocumentBytes = 8
	_, problems = Expand(context.Background(), "@spec.pdf", dir, lim)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "limit is 8") {
		t.Errorf("oversize refusal: %v", problems)
	}

	// A renamed text file is a reported problem, not a silent attach.
	os.WriteFile(filepath.Join(dir, "fake.pdf"), []byte("just text"), 0o644)
	_, problems = Expand(context.Background(), "@fake.pdf", dir, DefaultLimits())
	if len(problems) != 1 {
		t.Errorf("renamed file: %v", problems)
	}
}
