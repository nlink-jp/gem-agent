package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// ADR-0101: a session recorded under one Unicode spelling of its
// project is listed for the same directory reached under the other.
func TestListFindsTheProjectUnderAnotherSpelling(t *testing.T) {
	const nfc, nfd = "ドライブ", "ドライブ"
	base := t.TempDir()
	project := filepath.Join(base, nfd)
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(base, nfc)
	if _, err := os.Stat(typed); err != nil {
		t.Skipf("this volume does not treat the two spellings as one name: %v", err)
	}
	dir := t.TempDir()
	lg, err := Open(dir, project)
	if err != nil {
		t.Fatal(err)
	}
	_ = lg.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: project})
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "hello"})
	_ = lg.Close()
	metas, _, err := List(dir, typed)
	if err != nil || len(metas) != 1 {
		t.Fatalf("List under the other spelling = %d sessions (err=%v), want 1", len(metas), err)
	}
	if _, err := FindByPrefix(dir, typed, metas[0].ID); err != nil {
		t.Errorf("FindByPrefix under the other spelling: %v", err)
	}
}
