package tools

import (
	"bytes"
	"context"
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/docext"
)

// ReadDocumentName is the tool that reads a document file (ADR-0026).
// The agent special-cases it like view_image: for a PDF the tool result
// is metadata and the actual bytes ride the function response as a
// multimodal document part (measured: accepted, and the conversation
// continues cleanly past the tool round); the Office XML formats are
// extracted to text locally and returned as an ordinary result.
const ReadDocumentName = "read_document"

// maxPDFBytes bounds one attached PDF. The whole request shares an
// inline budget with the conversation history, so this stays well
// under the API's request cap. Oversized PDFs are refused whole.
const maxPDFBytes = 12 * 1024 * 1024

// maxDocFileBytes bounds how much of any document file is read at all.
const maxDocFileBytes = 32 * 1024 * 1024

var pdfMagic = []byte("%PDF-")

// ReadDocumentPDF loads an in-project PDF for attachment: same
// confinement as every reading tool, content-sniffed (the extension is
// never trusted), size-capped.
func (r *Registry) ReadDocumentPDF(p string) ([]byte, error) {
	abs, err := r.resolvePath(p)
	if err != nil {
		return nil, err
	}
	data, err := r.readCapped(abs, maxDocFileBytes)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, pdfMagic) {
		return nil, fmt.Errorf("%s is not a PDF", p)
	}
	if len(data) > maxPDFBytes {
		return nil, fmt.Errorf("PDF is %d bytes; the attachment limit is %d — split the document or extract the relevant pages", len(data), maxPDFBytes)
	}
	return data, nil
}

func (r *Registry) readCapped(abs string, cap int64) ([]byte, error) {
	f, err := r.openRead(abs)
	if err != nil {
		return nil, fmt.Errorf("unreadable: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("unreadable: %w", err)
	}
	if st.Size() > cap {
		return nil, fmt.Errorf("file is %d bytes; the document limit is %d", st.Size(), cap)
	}
	data, more, err := readAllCapped(f, int(cap))
	if err != nil {
		return nil, fmt.Errorf("unreadable: %w", err)
	}
	if more {
		return nil, fmt.Errorf("file exceeds the document limit of %d bytes", cap)
	}
	return data, nil
}

func (r *Registry) readDocument() *Tool {
	return &Tool{
		Name: ReadDocumentName,
		Description: "Read a document file from the project: PDF (attached to the conversation " +
			"as-is — the model reads layout, tables, and scans natively), or Word/Excel/PowerPoint " +
			"in the Office XML formats (.docx/.xlsx/.pptx — text is extracted locally: paragraphs, " +
			"tab-separated sheet rows, numbered slides). Legacy .doc/.xls/.ppt are not supported. " +
			"Read-only; for plain text files use read_file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "document path relative to the project root"},
			},
			"required": []string{"path"},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			data, err := r.readCapped(abs, maxDocFileBytes)
			if err != nil {
				return "", err
			}
			if bytes.HasPrefix(data, pdfMagic) {
				if _, err := r.ReadDocumentPDF(p); err != nil {
					return "", err
				}
				return fmt.Sprintf("document attached: %s (application/pdf, %d bytes) — it follows in this response as a document part", p, len(data)), nil
			}
			text, note, err := docext.Extract(data, docext.DefaultLimits())
			if err != nil {
				return "", fmt.Errorf("%s: %w", p, err)
			}
			out := fmt.Sprintf("Extracted text of %s (%s):\n\n%s", p, docext.Detect(data), text)
			if note != "" {
				out += "\n\n[" + note + "]"
			}
			return out, nil
		},
	}
}
