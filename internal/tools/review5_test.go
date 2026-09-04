package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if entries, err := r.readDirIn(abs); err == nil {
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
