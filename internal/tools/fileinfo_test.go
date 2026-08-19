package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileInfoTextFileWithKnownHashes(t *testing.T) {
	r := newRegistry(t)
	// "hello\n" has famous, externally verifiable digests.
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "file_info", map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"size: 6 bytes",
		"type: text (UTF-8)",
		"lines: 1",
		"md5:    b1946ac92492d2347c6235b4d2611184",
		"sha1:   f572d396fae9206628714fb2ce00f72e94f2258f",
		"sha256: 5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		"modified:", "created:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// The extension is shown but never trusted — catching the mismatch is
// the point of a type judgement.
func TestFileInfoJudgesTypeFromContentNotExtension(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	cases := map[string]struct {
		content []byte
		want    string
	}{
		"app.txt":    {[]byte{0xcf, 0xfa, 0xed, 0xfe, 0x00, 0x01}, "Mach-O 64-bit executable"},
		"lib.doc":    {[]byte{0x7f, 'E', 'L', 'F', 2, 1}, "ELF executable"},
		"tool.png":   {[]byte("MZ\x90\x00\x03"), "PE/DOS executable (Windows)"},
		"bundle.dat": {[]byte("PK\x03\x04rest"), "zip archive"},
		"notes.bin":  {[]byte("%PDF-1.7\n"), "PDF document"},
		"cache.log":  {[]byte("SQLite format 3\x00more"), "SQLite database"},
		"run":        {[]byte("#!/usr/bin/env python3\nprint(1)\n"), "script (#!/usr/bin/env python3)"},
		"fat.x":      {[]byte{0xca, 0xfe, 0xba, 0xbe, 0, 0}, "Mach-O universal (fat) binary"},
		"blob.txt":   {[]byte{'a', 0x00, 'b', 0x01}, "data (binary, unrecognised)"},
	}
	for name, tc := range cases {
		if err := os.WriteFile(filepath.Join(dir, name), tc.content, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, r, "file_info", map[string]any{"path": name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out, "type: "+tc.want) {
			t.Errorf("%s: want type %q, got:\n%s", name, tc.want, out)
		}
	}
	// A mismatching extension is called out.
	out, _ := run(t, r, "file_info", map[string]any{"path": "tool.png"})
	if !strings.Contains(out, "judged from content") {
		t.Errorf("extension mismatch not called out:\n%s", out)
	}
}

func TestFileInfoExecutableBitAndDirectory(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "file_info", map[string]any{"path": "run.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(executable)") {
		t.Errorf("executable bit not called out:\n%s", out)
	}

	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, r, "file_info", map[string]any{"path": "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "type: directory") || !strings.Contains(out, "1 files, 0 dirs") {
		t.Errorf("directory report:\n%s", out)
	}
	if strings.Contains(out, "sha256") {
		t.Error("directories must not be hashed")
	}
}

// Symlinks: reported with the target; an out-of-project target is never
// inspected — same containment as everything else.
func TestFileInfoSymlinkReporting(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	outside := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(outside, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "esc")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "file_info", map[string]any{"path": "esc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "symlink →") || !strings.Contains(out, "outside the project — not inspected") {
		t.Errorf("escaping symlink report:\n%s", out)
	}
	if strings.Contains(out, "Mach-O") || strings.Contains(out, "sha256") {
		t.Errorf("an out-of-project target was inspected:\n%s", out)
	}

	// An in-project link is followed with a note.
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, r, "file_info", map[string]any{"path": "alias"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "inside the project") || !strings.Contains(out, "sha256") {
		t.Errorf("in-project symlink report:\n%s", out)
	}
}

// A batch reports every path; one bad entry must not hide the others.
func TestFileInfoBatch(t *testing.T) {
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "file_info", map[string]any{"paths": []any{"a.txt", "missing.txt", "../escape"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt:") || !strings.Contains(out, "missing.txt: error: not found") {
		t.Errorf("batch output:\n%s", out)
	}
	if !strings.Contains(out, "../escape: error:") {
		t.Errorf("escape must be an in-batch error:\n%s", out)
	}

	// Both forms at once, empty batch, oversized batch: errors.
	for i, args := range []map[string]any{
		{"path": "a.txt", "paths": []any{"a.txt"}},
		{"paths": []any{}},
		{},
	} {
		if _, err := run(t, r, "file_info", args); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
