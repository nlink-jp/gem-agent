package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func textBlock(s string) []mcp.Content { return []mcp.Content{{Type: "text", Text: s}} }

// A result that fits is passed through untouched — the common case must
// not gain a preview, a path, or any other ceremony.
func TestSmallTextIsUntouched(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	out := in.render("srv", "tool", textBlock(`{"is_exit": false}`), false)
	if out != `{"is_exit": false}` {
		t.Errorf("out = %q", out)
	}
}

// Past the cap the whole result is saved and the model gets a preview
// plus the path. Nothing is lost, which is what separates this from the
// truncation the built-in tools used to do.
func TestOversizedTextIsSavedWhole(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	big := strings.Repeat("A", tools.OutputCap+1)

	out := in.render("rdns-lookup", "lookup_rdns", textBlock(big), false)
	if len(out) > 4000 {
		t.Fatalf("the rendered result is %d bytes — the point was to keep it out of context", len(out))
	}
	if !strings.Contains(out, "read_file") {
		t.Errorf("the model is not told how to read it: %q", out)
	}
	path := extractPath(t, out, work)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("saved file unreadable: %v", err)
	}
	if string(saved) != big {
		t.Errorf("saved %d bytes, want the whole %d", len(saved), len(big))
	}
	if !strings.HasPrefix(out, "AAA") {
		t.Errorf("the head of the result should still be inline: %q", out[:20])
	}
}

// JSON keeps a .json name so the operator (and any shell command the
// model writes) can tell what is in the file.
func TestOversizedJSONGetsAJSONName(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	big := "[" + strings.Repeat(`"x",`, tools.OutputCap/4) + `"x"]`
	out := in.render("srv", "tool", textBlock(big), false)
	if !strings.HasSuffix(extractPath(t, out, work), ".json") {
		t.Errorf("a JSON body should be saved as .json: %q", out)
	}
}

// The same answer fetched twice occupies one file: the name is derived
// from the content, so a repeated call does not litter the directory.
func TestSavedFilesAreContentAddressed(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	big := strings.Repeat("B", tools.OutputCap+1)

	first := extractPath(t, in.render("srv", "tool", textBlock(big), false), work)
	second := extractPath(t, in.render("srv", "tool", textBlock(big), false), work)
	if first != second {
		t.Errorf("the same content produced two files: %q and %q", first, second)
	}
	entries, _ := os.ReadDir(work)
	if len(entries) != 1 {
		t.Errorf("work dir holds %d files, want 1", len(entries))
	}
}

// An image is saved and the model is told to use view_image on it. The
// bytes deliberately do NOT ride back inline: an attachment is replayed
// with the conversation every round (ADR-0027), so it belongs in
// history only when the model asks for it.
func TestImageIsSavedAndPointedAtViewImage(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	png := []byte("\x89PNG\r\n\x1a\npixels")

	out := in.render("chrome-pilot", "take_screenshot", []mcp.Content{
		{Type: "text", Text: `{"page":"example.test"}`},
		{Type: "image", Data: png, MIME: "image/png"},
	}, false)

	if !strings.Contains(out, `{"page":"example.test"}`) {
		t.Errorf("the text block was lost: %q", out)
	}
	if !strings.Contains(out, "view_image") {
		t.Errorf("the model is not told how to look at it: %q", out)
	}
	if strings.Contains(out, "non-text content") {
		t.Errorf("the image was flattened away again: %q", out)
	}
	path := extractPath(t, out, work)
	if filepath.Ext(path) != ".png" {
		t.Errorf("saved as %q, want a .png so view_image accepts it", path)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(png) {
		t.Errorf("image bytes not saved: %v %q", err, saved)
	}
}

// With nowhere to write, the model must be told plainly that part of
// the answer is gone rather than handed a quiet truncation.
func TestWithoutAWorkDirTheLossIsStated(t *testing.T) {
	in := newMCPIntake(fixedDir(""))
	out := in.render("srv", "tool", textBlock(strings.Repeat("C", tools.OutputCap+1)), false)
	if !strings.Contains(out, "lost") {
		t.Errorf("a lossy fallback must say so: %q", out)
	}
	if len(out) > 4000 {
		t.Errorf("the fallback still flooded the context: %d bytes", len(out))
	}
}

func TestServerErrorsStayMarked(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	out := in.render("srv", "tool", textBlock("quota exceeded"), true)
	if !strings.HasPrefix(out, "error: ") {
		t.Errorf("out = %q", out)
	}
}

func TestEmptyResultIsStillAnAnswer(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	if out := in.render("srv", "tool", nil, false); out != "(no content)" {
		t.Errorf("out = %q", out)
	}
}

func TestExtForMIME(t *testing.T) {
	cases := map[string]string{
		"image/png":                 ".png",
		"image/jpeg":                ".jpg",
		"application/pdf":           ".pdf",
		"text/plain; charset=utf-8": ".txt",
		"application/x-unknown":     ".bin",
	}
	for mime, want := range cases {
		if got := extForMIME(mime, "resource"); got != want {
			t.Errorf("extForMIME(%q) = %q, want %q", mime, got, want)
		}
	}
	// An image block with an unhelpful MIME still gets an extension
	// view_image will accept, rather than a .bin it refuses.
	if got := extForMIME("", "image"); got != ".png" {
		t.Errorf("extForMIME(\"\", image) = %q", got)
	}
}

// extractPath pulls the saved file's path out of the rendered text.
func extractPath(t *testing.T, out, work string) string {
	t.Helper()
	for _, f := range strings.Fields(strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", ",", " ").Replace(out)) {
		if strings.HasPrefix(f, work) {
			return f
		}
	}
	t.Fatalf("no path under %q in %q", work, out)
	return ""
}

// fixedDir is the intake's directory getter for tests: one directory
// for the test's lifetime.
func fixedDir(dir string) func() string { return func() string { return dir } }

// ADR-0072 §4.5: many blocks each under the cap share one budget per
// response — the inline text never exceeds one cap.
func TestManySmallBlocksShareOneBudget(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	in.cap = 1000
	var blocks []mcp.Content
	for i := 0; i < 20; i++ {
		blocks = append(blocks, mcp.Content{Type: "text", Text: strings.Repeat("x", 300)})
	}
	out := in.render("srv", "tool", blocks, false)
	if len(out) > in.cap+400 {
		t.Fatalf("rendered result is %d bytes; the cap is %d", len(out), in.cap)
	}
	if !strings.Contains(out, "17 more text block(s)") || !strings.Contains(out, "read_file") {
		t.Fatalf("blocks past the budget were not saved together:\n%s", out)
	}
}

// R06: previews of oversized blocks are paid from the response budget
// too — a hundred of them stay within one cap.
func TestOversizedBlockPreviewsShareTheBudget(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	blocks := make([]mcp.Content, 100)
	for i := range blocks {
		blocks[i] = mcp.Content{Type: "text", Text: strings.Repeat("x", in.cap+1)}
	}
	out := in.render("srv", "tool", blocks, false)
	if len(out) > in.cap+1000 {
		t.Fatalf("rendered %d bytes for cap %d", len(out), in.cap)
	}
	if !strings.Contains(out, "more text block(s)") {
		t.Fatal("the remainder was not saved together")
	}
}

// R06: non-text blocks past the budget are one line, not a note each,
// and are not saved.
func TestBinaryLeftoversAreOneLine(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	blocks := make([]mcp.Content, 10000)
	for i := range blocks {
		blocks[i] = mcp.Content{Type: "image", MIME: "image/png", Data: []byte("TEST-DATA")}
	}
	out := in.render("srv", "tool", blocks, false)
	if len(out) > in.cap+1000 {
		t.Fatalf("rendered %d bytes for cap %d", len(out), in.cap)
	}
	if !strings.Contains(out, "more non-text block(s) past the response budget") {
		t.Fatal("leftovers not summarised")
	}
	entries, _ := os.ReadDir(work)
	if len(entries) > 200 {
		t.Errorf("%d files saved for blocks past the budget", len(entries))
	}
}
