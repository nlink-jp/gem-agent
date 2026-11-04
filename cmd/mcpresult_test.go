package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func textBlock(s string) []mcp.Content { return []mcp.Content{{Type: "text", Text: s}} }

// A result that fits is passed through untouched — the common case must
// not gain a preview, a path, or any other ceremony.
func TestSmallTextIsUntouched(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	out := textOf(in.render("srv", "tool", textBlock(`{"is_exit": false}`)))
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

	out := textOf(in.render("rdns-lookup", "lookup_rdns", textBlock(big)))
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
	out := textOf(in.render("srv", "tool", textBlock(big)))
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

	first := extractPath(t, textOf(in.render("srv", "tool", textBlock(big))), work)
	second := extractPath(t, textOf(in.render("srv", "tool", textBlock(big))), work)
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

	out := textOf(in.render("chrome-pilot", "take_screenshot", []mcp.Content{
		{Type: "text", Text: `{"page":"example.test"}`},
		{Type: "image", Data: png, MIME: "image/png"},
	}))

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
	out := textOf(in.render("srv", "tool", textBlock(strings.Repeat("C", tools.OutputCap+1))))
	if !strings.Contains(out, "lost") {
		t.Errorf("a lossy fallback must say so: %q", out)
	}
	if len(out) > 4000 {
		t.Errorf("the fallback still flooded the context: %d bytes", len(out))
	}
}

// ADR-0075 §1: the intake renders a server-marked error like any text;
// saying whose words it is (and prefixing `error:`) is the adapter's and
// the executor's job, by provenance — the intake no longer marks it.
func TestServerErrorsRenderUnmarked(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	if out := textOf(in.render("srv", "tool", textBlock("quota exceeded"))); out != "quota exceeded" {
		t.Errorf("out = %q", out)
	}
}

func TestEmptyResultIsStillAnAnswer(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	if out := textOf(in.render("srv", "tool", nil)); out != "(no content)" {
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

// textOf keeps the text of a render and drops its save records, for the
// tests that are about what the model reads.
func textOf(s string, _ []tools.Saved) string { return s }

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
	out := textOf(in.render("srv", "tool", blocks))
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
	out := textOf(in.render("srv", "tool", blocks))
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
	out := textOf(in.render("srv", "tool", blocks))
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

// Pre-release review: a first block of exactly the cap renders inline
// as it always did; a non-text block whose note would not fit is not
// written to disk.
func TestExactCapBlockAndUnsavedBinaries(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	exact := strings.Repeat("x", in.cap)
	out := textOf(in.render("srv", "tool", []mcp.Content{{Type: "text", Text: exact}}))
	if out != exact {
		t.Errorf("exact-cap block not inline: %d bytes rendered", len(out))
	}
	in.cap = 170
	blocks := make([]mcp.Content, 500)
	for i := range blocks {
		blocks[i] = mcp.Content{Type: "image", MIME: "image/png", Data: []byte("TEST-DATA")}
	}
	out = textOf(in.render("srv", "tool", blocks))
	entries, _ := os.ReadDir(work)
	if len(entries) > 1 {
		t.Errorf("%d files written for blocks whose note could not be listed", len(entries))
	}
	if !strings.Contains(out, "0 saved in the work directory") && strings.Contains(out, "saved in the work directory but not listed") {
		t.Errorf("blocks were saved without being listed:\n%s", out)
	}
}

// ADR-0096 §1: metadata a server appends to its result — the case that
// motivated the record — is in front of the model, and the notice gives
// the byte spans read_file's offset takes.
func TestSpillPreviewShowsHeadAndTailWithByteSpans(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	tail := `], "truncated": true, "total_rows": 200}`
	big := `{"results":[` + strings.Repeat(`{"_raw":"x"},`, tools.OutputCap/10) + `{"_raw":"y"}` + tail
	out := textOf(in.render("splunk", "run_query", textBlock(big)))
	if !strings.Contains(out, tail) {
		t.Fatalf("the tail metadata is not in the preview:\n%s", out)
	}
	if !strings.HasPrefix(out, `{"results":[`) {
		t.Errorf("the head is not in the preview: %q", out[:40])
	}
	n := len(big)
	want := "Shown above: bytes 0–600 and " + strconv.Itoa(n-200) + "–" + strconv.Itoa(n) + "."
	if !strings.Contains(out, want) {
		t.Errorf("notice lacks the byte spans %q:\n%s", want, out)
	}
	if !strings.Contains(out, "[… bytes 600–"+strconv.Itoa(n-200)+" not shown …]") {
		t.Errorf("no elision marker between head and tail:\n%s", out)
	}
	// The notice keeps the shape the operator's measurement matches.
	if !regexp.MustCompile(`too large to hold inline.*?read_file (\S+?)\]`).MatchString(strings.ReplaceAll(out, "\n", " ")) {
		t.Error("the notice no longer ends in read_file <path>]")
	}
}

// Spans are bytes, cut on rune boundaries: for Japanese text a rune
// count would put read_file's offset in the wrong place.
func TestSpillPreviewSpansAreBytesOnRuneBoundaries(t *testing.T) {
	in := newMCPIntake(fixedDir(t.TempDir()))
	big := strings.Repeat("あ", tools.OutputCap) // 3 bytes each
	out := textOf(in.render("obsidian", "get_vault_file", textBlock(big)))
	n := len(big)
	if !strings.Contains(out, "bytes 0–1800 and "+strconv.Itoa(n-600)+"–"+strconv.Itoa(n)) {
		t.Errorf("spans are not in bytes:\n%s", out[len(out)-300:])
	}
	if !utf8.ValidString(out) {
		t.Error("the preview split a rune")
	}
}

func TestHeadAndTailOfAShortStringIsWhole(t *testing.T) {
	if p, shown := headAndTail("abc", 600, 200); p != "abc" || shown != "bytes 0–3" {
		t.Errorf("got %q %q", p, shown)
	}
}

// ADR-0105 §1: a result small enough to ride inline is saved all the
// same, and the record carries its path, size and full sha256. The
// inline text is still exactly the server's: the note that says so is
// the executor's, outside the nonce tag.
func TestSmallTextIsSavedAndRecorded(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	body := `{"rows":[{"event_id":"evt-1"}]}`
	out, saved := in.render("bigquery", "query", textBlock(body))
	if out != body {
		t.Fatalf("inline text changed: %q", out)
	}
	if len(saved) != 1 {
		t.Fatalf("saved = %+v, want one record", saved)
	}
	rec := saved[0]
	sum := sha256.Sum256([]byte(body))
	if rec.SHA256 != hex.EncodeToString(sum[:]) || rec.Bytes != int64(len(body)) || rec.Err != "" {
		t.Errorf("record = %+v", rec)
	}
	if !strings.Contains(filepath.Base(rec.Path), rec.SHA256) || filepath.Ext(rec.Path) != ".json" {
		t.Errorf("name %q does not carry the full hash and a JSON extension", rec.Path)
	}
	got, err := os.ReadFile(rec.Path)
	if err != nil || string(got) != body {
		t.Errorf("saved bytes = %q, %v", got, err)
	}
}

// Every block that reaches disk is recorded: an oversized block's spill
// and the joined remainder past the budget alike.
func TestSpilledAndLeftoverBlocksAreRecorded(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	in.cap = 2000
	blocks := []mcp.Content{
		{Type: "text", Text: strings.Repeat("a", 2500)},
		{Type: "text", Text: strings.Repeat("b", 900)},
		{Type: "text", Text: strings.Repeat("c", 900)},
	}
	_, saved := in.render("srv", "tool", blocks)
	if len(saved) != 2 {
		t.Fatalf("saved = %+v, want the spill and the joined remainder", saved)
	}
	for _, rec := range saved {
		got, err := os.ReadFile(rec.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != rec.SHA256 || int64(len(got)) != rec.Bytes {
			t.Errorf("record %+v does not describe the file on disk", rec)
		}
	}
	if want := strings.Repeat("b", 900) + "\n" + strings.Repeat("c", 900); saved[1].Bytes != int64(len(want)) {
		t.Errorf("remainder record = %+v, want the two blocks joined", saved[1])
	}
}

// With no work directory nothing is saved, and the record says why, so
// the model is told there is no file to copy.
func TestWithoutAWorkDirTheRecordSaysNotSaved(t *testing.T) {
	in := newMCPIntake(fixedDir(""))
	out, saved := in.render("srv", "tool", textBlock("small"))
	if out != "small" {
		t.Errorf("out = %q", out)
	}
	if len(saved) != 1 || saved[0].Path != "" || saved[0].Err == "" {
		t.Errorf("saved = %+v, want one record with an error and no path", saved)
	}
}

// A file already at the content-addressed name is reused only when its
// bytes hash to the name: anything can write the work directory, and a
// planted file must not be presented as this result.
func TestAPlantedFileAtTheNameIsReplaced(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	body := "the server's answer"
	path := in.pathFor("srv", "tool", ".txt", []byte(body))
	if err := os.WriteFile(path, []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, saved := in.render("srv", "tool", textBlock(body))
	if len(saved) != 1 || saved[0].Path != path {
		t.Fatalf("saved = %+v", saved)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("file holds %q, want the result", got)
	}
}

// Independent review: however many inline blocks a server returns, they
// are one saved file and one record — a note per block would have been
// paid from no budget at all.
func TestManyInlineBlocksAreOneRecord(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	blocks := make([]mcp.Content, 5000)
	for i := range blocks {
		blocks[i] = mcp.Content{Type: "text", Text: "x"}
	}
	_, saved := in.render("srv", "tool", blocks)
	if len(saved) > 3 {
		t.Fatalf("%d records for one response", len(saved))
	}
	entries, _ := os.ReadDir(work)
	if len(entries) != len(saved) {
		t.Errorf("%d files on disk, %d recorded", len(entries), len(saved))
	}
}

// Independent review: a spill whose preview does not fit the remaining
// budget is not written, so no file lies in the work directory that no
// record names — every file on disk is recorded.
func TestEveryFileOnDiskIsRecorded(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	big := "[" + strings.Repeat(`"y",`, 6000) + `"y"]`
	png := []byte("\x89PNG\r\n\x1a\npixels")
	_, saved := in.render("srv", "tool", []mcp.Content{
		{Type: "text", Text: strings.Repeat("a", in.cap-500)},
		{Type: "text", Text: big},
		{Type: "image", Data: png, MIME: "image/png"},
	})
	recorded := map[string]bool{}
	for _, r := range saved {
		if r.Path != "" {
			recorded[filepath.Base(r.Path)] = true
		}
	}
	entries, _ := os.ReadDir(work)
	for _, e := range entries {
		if !recorded[e.Name()] {
			t.Errorf("%s is on disk and in no record (%+v)", e.Name(), saved)
		}
	}
	if len(entries) == 0 {
		t.Fatal("nothing was saved")
	}
}

// A saved binary block is recorded like a text one.
func TestBinaryBlocksAreRecorded(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	png := []byte("\x89PNG\r\n\x1a\npixels")
	_, saved := in.render("srv", "shot", []mcp.Content{{Type: "image", Data: png, MIME: "image/png"}})
	if len(saved) != 1 || filepath.Ext(saved[0].Path) != ".png" || saved[0].Bytes != int64(len(png)) {
		t.Errorf("saved = %+v", saved)
	}
}

// What sits at the name is reused only when it is a regular file of the
// result's size and hash: a FIFO planted there must not hang the call.
func TestAFIFOAtTheNameDoesNotHang(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work))
	body := "answer"
	path := in.pathFor("srv", "tool", ".txt", []byte(body))
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	_, saved := in.render("srv", "tool", textBlock(body))
	if len(saved) != 1 || saved[0].Err != "" {
		t.Fatalf("saved = %+v", saved)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("file holds %q", got)
	}
}
