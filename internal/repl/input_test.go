package repl

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestSingleLine(t *testing.T) {
	var out bytes.Buffer
	r := NewReader(strings.NewReader("hello\n"), &out)
	got, err := r.Read("> ")
	if err != nil || got != "hello" {
		t.Fatalf("got %q, %v", got, err)
	}
	if out.String() != "> " {
		t.Errorf("prompt = %q", out.String())
	}
}

// TestMultiLinePasteAggregated is the paste-safety contract: everything
// the terminal delivered in one flush becomes one input.
func TestMultiLinePasteAggregated(t *testing.T) {
	r := NewReader(strings.NewReader("分析要件:\nrecruit.example.jp を調べて\n結果をまとめて\n"), io.Discard)
	got, err := r.Read("> ")
	if err != nil {
		t.Fatal(err)
	}
	want := "分析要件:\nrecruit.example.jp を調べて\n結果をまとめて"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Nothing left over to leak.
	if _, err := r.Read("> "); err != io.EOF {
		t.Errorf("expected EOF after aggregation, got %v", err)
	}
}

func TestEOFWithoutNewline(t *testing.T) {
	r := NewReader(strings.NewReader("partial"), io.Discard)
	got, err := r.Read("> ")
	if err != nil || got != "partial" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := r.Read("> "); err != io.EOF {
		t.Errorf("second read should be EOF, got %v", err)
	}
}

func TestEOFEmpty(t *testing.T) {
	r := NewReader(strings.NewReader(""), io.Discard)
	if _, err := r.Read("> "); err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestCRLFStripped(t *testing.T) {
	r := NewReader(strings.NewReader("line\r\n"), io.Discard)
	got, err := r.Read("> ")
	if err != nil || got != "line" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestSharedBufioReader documents the sharing contract with the approval
// gate: wrapping the same *bufio.Reader returns it unchanged, so input
// buffered during an approval prompt is not stranded.
func TestSharedBufioReader(t *testing.T) {
	shared := bufio.NewReader(strings.NewReader("first\nsecond\n"))
	r := NewReader(shared, io.Discard)
	got, err := r.Read("> ")
	if err != nil {
		t.Fatal(err)
	}
	// Both lines were buffered in one flush — aggregated by design.
	if got != "first\nsecond" {
		t.Errorf("got %q", got)
	}
}
