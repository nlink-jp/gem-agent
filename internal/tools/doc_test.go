package tools

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMiniDocx(t *testing.T, path, text string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte(`<w:document xmlns:w="x"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`))
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ADR-0026: read_document extracts Office text locally and marks PDFs
// for multimodal attachment; the extension is never trusted.
func TestReadDocumentTool(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, directExec, 5e9)
	if err != nil {
		t.Fatal(err)
	}

	writeMiniDocx(t, filepath.Join(dir, "memo.docx"), "予算は確定した")
	out, err := run(t, r, "read_document", map[string]any{"path": "memo.docx"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "予算は確定した") || !strings.Contains(out, "docx") {
		t.Errorf("docx extraction: %q", out)
	}

	// A tiny (invalid-but-magic) PDF: the tool marks it attached and
	// ReadDocumentPDF hands the agent the bytes.
	pdf := []byte("%PDF-1.4\nfake body for routing tests")
	os.WriteFile(filepath.Join(dir, "r.pdf"), pdf, 0o644)
	out, err = run(t, r, "read_document", map[string]any{"path": "r.pdf"})
	if err != nil || !strings.Contains(out, "document part") {
		t.Errorf("pdf marker: out=%q err=%v", out, err)
	}
	data, err := r.ReadDocumentPDF("r.pdf")
	if err != nil || !bytes.Equal(data, pdf) {
		t.Errorf("ReadDocumentPDF: %v", err)
	}
	// Office files must refuse the PDF byte path (the agent relies on
	// this to not attach them).
	if _, err := r.ReadDocumentPDF("memo.docx"); err == nil {
		t.Error("ReadDocumentPDF accepted a docx")
	}

	// A renamed text file is rejected by content, extension ignored.
	os.WriteFile(filepath.Join(dir, "fake.docx"), []byte("plain text"), 0o644)
	if _, err := run(t, r, "read_document", map[string]any{"path": "fake.docx"}); err == nil ||
		!strings.Contains(err.Error(), "out of scope") && !strings.Contains(err.Error(), "not a supported") {
		t.Errorf("renamed text accepted: %v", err)
	}

	// Confinement: escaping paths refuse like every reading tool.
	if _, err := run(t, r, "read_document", map[string]any{"path": "../outside.pdf"}); err == nil {
		t.Error("escaping path accepted")
	}
}
