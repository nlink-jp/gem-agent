package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Review after v0.68.0: the symlink check and the open are one
// operation. A link that pointed inside the project when resolvePath
// looked, and outside by the time the tool opened it, must be refused
// at the open — os.Root resolves every component inside the root.
func TestOpenRefusesALinkSwappedAfterTheCheck(t *testing.T) {
	r := newRegistry(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(r.ProjectDir(), "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.ProjectDir(), "link.txt")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	abs, err := r.resolvePath("link.txt")
	if err != nil {
		t.Fatalf("an in-project link was refused: %v", err)
	}
	// The swap between check and use.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if f, err := r.openRead(abs); err == nil {
		_ = f.Close()
		t.Fatal("the swapped link was opened for reading")
	}
	if f, err := r.openWrite(abs, 0o644); err == nil {
		_ = f.Close()
		t.Fatal("the swapped link was opened for writing")
	}
	if data, _ := os.ReadFile(secret); string(data) != "outside" {
		t.Fatal("the outside file was touched")
	}
}

// A sparse file the size of a large disk is read through a window
// with bounded memory: the tool never holds it whole.
func TestReadFileStreamsASparseFile(t *testing.T) {
	r := newRegistry(t)
	p := filepath.Join(r.ProjectDir(), "sparse.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(256 << 20); err != nil { // 256 MiB of zeros, one line
		t.Fatal(err)
	}
	_ = f.Close()
	out, err := run(t, r, "read_file", map[string]any{"path": "sparse.bin", "start_line": float64(1), "end_line": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > readCap+256 {
		t.Fatalf("output is %d bytes; the cap is %d", len(out), readCap)
	}
	if !strings.Contains(out, "[showing lines 1–1 of 1]") {
		t.Errorf("window note missing: %q", out[len(out)-80:])
	}
	// Whole-file read of the same: capped, and the marker names the
	// real size.
	out, err = run(t, r, "read_file", map[string]any{"path": "sparse.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of 268435456 bytes shown") {
		t.Errorf("truncation marker does not name the file size: %q", out[len(out)-80:])
	}
}

// A line window past the cap still reaches the requested lines.
func TestReadFileWindowBeyondTheCap(t *testing.T) {
	r := newRegistry(t)
	var b strings.Builder
	for i := 1; i <= 20000; i++ {
		b.WriteString(strings.Repeat("x", 40) + " line-" + itoa(i) + "\n")
	}
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "read_file", map[string]any{"path": "big.txt", "start_line": float64(19998), "end_line": float64(20000)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line-19998") || !strings.Contains(out, "line-20000") || strings.Contains(out, "line-19997") {
		t.Errorf("window wrong:\n%s", out)
	}
	if !strings.Contains(out, "[showing lines 19998–20000 of 20000]") {
		t.Errorf("note wrong:\n%s", out)
	}
	if _, err := run(t, r, "read_file", map[string]any{"path": "big.txt", "start_line": float64(30000)}); err == nil {
		t.Error("a start past the end was accepted")
	}
}

// An image over the limit is refused by its size, before any read.
func TestReadImageRefusesOversizeBeforeReading(t *testing.T) {
	r := newRegistry(t)
	p := filepath.Join(r.ProjectDir(), "huge.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImageBytes + 1<<20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, _, err := r.ReadImage("huge.png"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize image not refused: %v", err)
	}
}

// edit_file consults its context: a cancelled call writes nothing.
func TestEditFileHonoursCancellation(t *testing.T) {
	r := newRegistry(t)
	p := filepath.Join(r.ProjectDir(), "e.txt")
	if err := os.WriteFile(p, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool, _ := r.Get("edit_file")
	_, err := tool.Run(ctx, map[string]any{"path": "e.txt", "old_string": "hello", "new_string": "bye"})
	if err == nil {
		t.Fatal("a cancelled edit succeeded")
	}
	if data, _ := os.ReadFile(p); string(data) != "hello world\n" {
		t.Fatalf("the file changed under a cancelled call: %q", data)
	}
}

// Review after v0.68.1: the walks list and read through the roots too.
// A directory swapped for a link that leads out between resolvePath
// and the listing is refused at the open.
func TestReadDirRefusesADirectorySwappedAfterTheCheck(t *testing.T) {
	r := newRegistry(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.ProjectDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	abs, err := r.resolvePath("d")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if entries, _, err := r.readDirIn(abs); err == nil {
		t.Fatalf("the swapped directory was listed: %d entries", len(entries))
	}
	if _, _, err := r.readFileCapped(filepath.Join(abs, "secret.txt"), 1024); err == nil {
		t.Fatal("a file behind the swapped directory was read")
	}
}

// Review after v0.68.1: rotating the work directory while an abandoned
// call still resolves and opens must be a consistent snapshot, not a
// torn pair or a closed handle — exercised under the race detector.
func TestWorkRootRotationIsSafeUnderConcurrentOpens(t *testing.T) {
	r := newRegistry(t)
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, d := range dirs {
		if err := os.WriteFile(filepath.Join(d, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.UseWorkDir(dirs[0]); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = r.UseWorkDir(dirs[i%2])
		}
		_ = r.UseWorkDir("")
	}()
	for i := 0; i < 200; i++ {
		st := r.rootState()
		if st.workDir == "" {
			continue
		}
		abs := filepath.Join(st.workDir, "f.txt")
		if _, _, err := r.readFileCapped(abs, 16); err != nil {
			// A rotation between the snapshot and the open is the
			// documented outcome (the path no longer sits under a
			// root) — never a panic or a read through a closed root.
			continue
		}
	}
	<-done
}

// A Subset reads the parent's roots, rotation included.
func TestSubsetFollowsTheParentsWorkRoot(t *testing.T) {
	r := newRegistry(t)
	sub, err := r.Subset("read_file")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "w.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.UseWorkDir(work); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(work)
	if sub.WorkDir() != real {
		t.Fatalf("subset work dir = %q, want %q", sub.WorkDir(), real)
	}
	out, err := run(t, sub, "read_file", map[string]any{"path": filepath.Join(real, "w.txt")})
	if err != nil || !strings.Contains(out, "work") {
		t.Fatalf("subset could not read the rotated work directory: %q %v", out, err)
	}
}

// Review after v0.68.2: .gitignore is read through the roots, and a
// link where a .gitignore should be contributes nothing — content from
// outside the project must not enter the ignore decision.
func TestGitignoreReaderRefusesLinksAndOversize(t *testing.T) {
	r := newRegistry(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "rules"), []byte("*.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(r.ProjectDir(), "linked")
	if err := os.Mkdir(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "rules"), filepath.Join(linked, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.gitignoreReader(filepath.Join(linked, ".gitignore"), 1<<20); err == nil {
		t.Fatal("a symlinked .gitignore was read")
	}
	big := filepath.Join(r.ProjectDir(), "big")
	if err := os.Mkdir(big, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(big, ".gitignore"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.gitignoreReader(filepath.Join(big, ".gitignore"), 1024); err == nil {
		t.Fatal("an oversized .gitignore was read")
	}
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := r.gitignoreReader(filepath.Join(r.ProjectDir(), ".gitignore"), 1<<20)
	if err != nil || string(data) != "*.log\n" {
		t.Fatalf("a regular .gitignore was not read: %q %v", data, err)
	}
}

// A file that exceeds the search cap is not searched at all: a
// partial search must not be presented as a complete one.
func TestReadForSearchSkipsOversize(t *testing.T) {
	r := newRegistry(t)
	p := filepath.Join(r.ProjectDir(), "grown.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", searchFileCap+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.readForSearch(p); ok {
		t.Fatal("a file past the cap was searched partially")
	}
	if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, ok := r.readForSearch(p); !ok || !strings.Contains(string(data), "needle") {
		t.Fatal("a file within the cap was not read")
	}
}

// Review after v0.68.2: a rotated-out work root closes when its last
// holder releases it — not before (the holder is an abandoned call
// mid-open) and not never (a descriptor per /clear).
func TestRotatedWorkRootClosesAfterItsLastHolder(t *testing.T) {
	r := newRegistry(t)
	first, second := t.TempDir(), t.TempDir()
	if err := r.UseWorkDir(first); err != nil {
		t.Fatal(err)
	}
	st := r.rootState()
	old := st.workRoot
	_, _, release, err := r.rootFor(filepath.Join(st.workDir, "x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UseWorkDir(second); err != nil {
		t.Fatal(err)
	}
	if old.closed.Load() {
		t.Fatal("the old root closed while a holder still had it")
	}
	release()
	if !old.closed.Load() {
		t.Fatal("the old root did not close after its last holder released it")
	}
	// With no holder, rotation closes immediately.
	current := r.rootState().workRoot
	if err := r.UseWorkDir(""); err != nil {
		t.Fatal(err)
	}
	if !current.closed.Load() {
		t.Fatal("a root with no holder was not closed on rotation")
	}
}

// Review after v0.68.2: cuts land on rune boundaries.
func TestTruncateKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("あ", 100) // 300 bytes
	out := truncate(s, 10)
	head := strings.SplitN(out, "\n", 2)[0]
	if !utf8.ValidString(head) || head != strings.Repeat("あ", 3) {
		t.Errorf("truncate cut through a rune: %q", head)
	}
	if cutRunes("ab", 5) != "ab" || cutRunes("あい", 3) != "あ" || cutRunes("あい", 2) != "" {
		t.Error("cutRunes boundaries wrong")
	}
}

// edit_file refuses a file past its cap by size, before any read.
func TestEditFileRefusesOversize(t *testing.T) {
	r := newRegistry(t)
	p := filepath.Join(r.ProjectDir(), "huge.txt")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEditBytes + 1<<20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	tool, _ := r.Get("edit_file")
	_, err = tool.Run(context.Background(), map[string]any{"path": "huge.txt", "old_string": "a", "new_string": "b"})
	if err == nil || !strings.Contains(err.Error(), "edit_file handles files up to") {
		t.Fatalf("oversize edit not refused by size: %v", err)
	}
}

// The truncation note names the bytes actually shown after the rune
// cut, not the limit.
func TestTruncateNoteNamesBytesShown(t *testing.T) {
	out := truncate(strings.Repeat("あ", 10), 10) // 9 bytes fit
	if !strings.Contains(out, "[output truncated: 9 of 30 bytes shown]") {
		t.Errorf("note wrong: %q", out)
	}
}

// ADR-0072 §4.5: shell_exec holds one cap's worth of output however
// much the command prints; the note names the real total.
func TestShellExecOutputIsBoundedAsItArrives(t *testing.T) {
	r := newRegistry(t)
	out, err := run(t, r, "shell_exec", map[string]any{"command": "yes | head -c 3000000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > OutputCap+200 {
		t.Fatalf("output is %d bytes; the cap is %d", len(out), OutputCap)
	}
	if !strings.Contains(out, "of 3000000 bytes shown]") {
		t.Errorf("note does not name the real total: %q", out[len(out)-80:])
	}
	b := &boundedOutput{limit: 4}
	n, _ := b.Write([]byte("abcdefgh"))
	if n != 8 {
		t.Errorf("Write must accept everything: %d", n)
	}
	if s := b.String(); !strings.HasPrefix(s, "abcd\n[output truncated: 4 of 8 bytes shown]") {
		t.Errorf("String = %q", s)
	}
}

// A line longer than the cap is shown cut, and the reader is told.
func TestReadWindowReportsCutLines(t *testing.T) {
	r := newRegistry(t)
	p := filepath.Join(r.ProjectDir(), "long.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", readCap+100)+"\nshort\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "read_file", map[string]any{"path": "long.txt", "start_line": float64(1), "end_line": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 line(s) longer than") {
		t.Errorf("the cut line was not disclosed: %q", out[len(out)-120:])
	}
}

// A directory listing is bounded before it is held.
func TestDirectoryListingsAreBounded(t *testing.T) {
	r := newRegistry(t)
	dir := filepath.Join(r.ProjectDir(), "many")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= DirEntryCap; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, more, err := r.readDirIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != DirEntryCap || !more {
		t.Fatalf("got %d entries more=%v, want %d and more", len(entries), more, DirEntryCap)
	}
	out, err := run(t, r, "list_files", map[string]any{"path": "many"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the listing stopped there") {
		t.Errorf("list_files did not disclose the cut: %q", out[len(out)-120:])
	}
}

// N02: the bounded shell output cuts on a rune boundary and counts
// complete characters.
func TestBoundedOutputKeepsRunesWhole(t *testing.T) {
	b := &boundedOutput{limit: 4}
	_, _ = b.Write([]byte("あいう"))
	s := b.String()
	if !utf8.ValidString(s) || !strings.HasPrefix(s, "あ\n[output truncated: 3 of 9 bytes shown]") {
		t.Errorf("String = %q", s)
	}
}

// ADR-0072 §4.8: a cut line ends on a rune boundary for 2-, 3- and
// 4-byte characters wherever the cap lands.
func TestReadLineCappedKeepsRunesWhole(t *testing.T) {
	for _, ch := range []string{"é", "あ", "😀"} {
		line := strings.Repeat(ch, 50)
		for cap := len(ch) * 10; cap < len(ch)*10+len(ch); cap++ {
			got, cut, err := readLineCapped(bufio.NewReader(strings.NewReader(line+"\n")), cap)
			if err != nil {
				t.Fatal(err)
			}
			if !cut || !utf8.ValidString(got) || len(got) > cap {
				t.Errorf("%q cap %d: got %q cut=%v", ch, cap, got, cut)
			}
		}
	}
	got, cut, _ := readLineCapped(bufio.NewReader(strings.NewReader("short\n")), 100)
	if got != "short" || cut {
		t.Errorf("short line = %q cut=%v", got, cut)
	}
}

// ADR-0072 §4.8: HEIC/HEIF is identified by its ftyp box; a forged
// extension, a truncated header and a bad box length are refused.
func TestHEIFSniff(t *testing.T) {
	ftyp := func(size int, brands ...string) []byte {
		b := []byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size), 'f', 't', 'y', 'p'}
		for _, br := range brands {
			b = append(b, []byte(br)...)
		}
		for len(b) < size {
			b = append(b, 0)
		}
		return append(b, make([]byte, 64)...)
	}
	if m := heifMIME(ftyp(24, "heic", "\x00\x00\x00\x00", "mif1", "heic")); m != "image/heic" {
		t.Errorf("heic = %q", m)
	}
	if m := heifMIME(ftyp(20, "mif1", "\x00\x00\x00\x00", "heif")); m != "image/heif" {
		t.Errorf("heif = %q", m)
	}
	if m := heifMIME(ftyp(20, "isom", "\x00\x00\x00\x00", "mp41")); m != "" {
		t.Errorf("mp4 brand accepted as %q", m)
	}
	if m := heifMIME([]byte("\x00\x00\x00\x18ftypheic")); m != "" {
		t.Errorf("truncated header accepted as %q", m)
	}
	bad := append([]byte{0x7f, 0xff, 0xff, 0x00, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c', 0, 0, 0, 0}, make([]byte, 64)...)
	if m := heifMIME(bad); m != "" {
		t.Errorf("bad box length accepted as %q", m)
	}
	if m := heifMIME([]byte("not an image at all, just text.....")); m != "" {
		t.Errorf("text accepted as %q", m)
	}
	// Through the tool, with the sniff in place of the extension.
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "p.heic"), ftyp(24, "heic", "\x00\x00\x00\x00", "mif1", "heic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, mime, err := r.ReadImage("p.heic"); err != nil || mime != "image/heic" {
		t.Errorf("ReadImage(heic) = %q, %v", mime, err)
	}
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "fake.heic"), []byte("plain text pretending"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ReadImage("fake.heic"); err == nil {
		t.Error("a forged .heic was accepted")
	}
}

// Pre-release review: a window landing exactly on the cap says how
// many of its lines were not shown; a complete line of exactly cap
// bytes is not reported cut.
func TestWindowDropsAreDisclosedAndExactLinesAreWhole(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, strings.Repeat("x", 9)) // 10 bytes with the newline
	}
	content, note, err := readWindow(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), 1, 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > 110 { // one line past the cap is kept so the caller's byte marker fires
		t.Errorf("content is %d bytes for cap 100", len(content))
	}
	if !strings.Contains(note, "more line(s) of the window not shown") {
		t.Errorf("dropped lines not disclosed: %q", note)
	}
	got, cut, err := readLineCapped(bufio.NewReader(strings.NewReader(strings.Repeat("y", 100)+"\n")), 100)
	if err != nil || cut || len(got) != 100 {
		t.Errorf("exact-cap line: len=%d cut=%v err=%v", len(got), cut, err)
	}
}

// The major brand decides HEIF: an AVIF or MP4 carrying mif1 in its
// compatible list is not HEIF.
func TestHEIFMajorBrandDecides(t *testing.T) {
	box := func(major string, compat ...string) []byte {
		size := 16 + 4*len(compat)
		b := []byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size), 'f', 't', 'y', 'p'}
		b = append(b, []byte(major)...)
		b = append(b, 0, 0, 0, 0)
		for _, c := range compat {
			b = append(b, []byte(c)...)
		}
		return append(b, make([]byte, 64)...)
	}
	if m := heifMIME(box("avif", "mif1", "miaf")); m != "" {
		t.Errorf("avif read as %q", m)
	}
	if m := heifMIME(box("isom", "mif1", "mp41")); m != "" {
		t.Errorf("mp4 read as %q", m)
	}
	if m := heifMIME(box("mif1", "heic")); m != "image/heic" {
		t.Errorf("mif1+heic = %q", m)
	}
	if m := heifMIME(box("heic", "mif1")); m != "image/heic" {
		t.Errorf("heic = %q", m)
	}
}
