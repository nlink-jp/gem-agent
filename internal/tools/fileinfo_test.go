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

// The majors, plus the collisions that make a magic table earn its keep.
func TestDetectTypeMajorsAndCollisions(t *testing.T) {
	cases := map[string]struct {
		head []byte
		want string
	}{
		"png":  {[]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0}, "PNG image"},
		"jpeg": {[]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, "JPEG image"},
		"gif":  {[]byte("GIF89a\x01\x00"), "GIF image"},
		"webp": {[]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), "WebP image"},
		"wav":  {[]byte("RIFF\x24\x00\x00\x00WAVEfmt "), "WAV audio"},
		"tiff": {[]byte("II*\x00\x08\x00"), "TIFF image (little-endian)"},
		"heic": {append([]byte{0, 0, 0, 0x18}, []byte("ftypheic")...), "HEIC/HEIF image"},
		"mp4":  {append([]byte{0, 0, 0, 0x18}, []byte("ftypisom")...), "MP4 media"},
		"mov":  {append([]byte{0, 0, 0, 0x14}, []byte("ftypqt  ")...), "QuickTime movie (MOV)"},
		"xz":   {[]byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, "xz compressed data"},
		"7z":   {[]byte("7z\xbc\xaf\x27\x1c\x00"), "7-zip archive"},
		"zstd": {[]byte{0x28, 0xb5, 0x2f, 0xfd, 0x01}, "zstd compressed data"},
		"bz2":  {[]byte("BZh9\x31\x41"), "bzip2 compressed data"},
		"wasm": {[]byte("\x00asm\x01\x00\x00\x00"), "WebAssembly binary"},
		"ole":  {[]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, "OLE compound document (legacy Office / msi)"},
		"pem":  {[]byte("-----BEGIN CERTIFICATE-----\nMIIB"), "PEM encoded data (text)"},

		// The famous 0xCAFEBABE collision: a fat Mach-O carries a small
		// architecture count; a Java class carries a version word there.
		"fat-macho":  {[]byte{0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x02}, "Mach-O universal (fat) binary"},
		"java-class": {[]byte{0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x43}, "Java class file"},

		// Mach-O filetype refinement: 6 = dylib.
		"dylib": {[]byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0, 0, 0, 0, 0, 0, 0, 0x06, 0, 0, 0}, "Mach-O 64-bit dynamic library"},
	}
	for name, tc := range cases {
		kind, _ := detectType(tc.head, name)
		if kind != tc.want {
			t.Errorf("%s: detectType = %q, want %q", name, kind, tc.want)
		}
	}
}

// tar's magic sits at offset 257 — the one offset-based entry.
func TestDetectTypeTarAtOffset(t *testing.T) {
	head := make([]byte, 512)
	copy(head, "somefile.txt")
	copy(head[257:], "ustar\x00")
	if kind, _ := detectType(head, "a.tar"); kind != "tar archive" {
		t.Errorf("tar = %q", kind)
	}
}

// Short ASCII magics collide with ordinary text; the valid checks keep
// prose from being misread as media — the exact mistake a type
// judgement exists to prevent.
func TestDetectTypeTextCollisionsStayText(t *testing.T) {
	for name, content := range map[string]string{
		"bm":  "BMW is a car maker.\nSecond line.\n",
		"id3": "ID3 tags are metadata containers used in MP3.\n",
		"bzh": "BZh is how bzip2 streams begin.\n",
	} {
		kind, isText := detectType([]byte(content), name)
		if !isText || kind != "text (UTF-8)" {
			t.Errorf("%s: detectType = %q (isText=%v), want text", name, kind, isText)
		}
	}
	// And a real BMP still detects: reserved words zero.
	bmp := []byte{'B', 'M', 0x46, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x36, 0x00}
	if kind, _ := detectType(bmp, "x.bmp"); kind != "BMP image" {
		t.Errorf("real BMP = %q", kind)
	}
}
