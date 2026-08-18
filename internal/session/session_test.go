package session

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func TestLogAppendsJSONL(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Log("user", map[string]string{"content": "こんにちは"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log("assistant", map[string]string{"content": "hi"}); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var kinds []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("line is not valid JSON: %v", err)
		}
		if rec.Time.IsZero() {
			t.Error("record timestamp missing")
		}
		kinds = append(kinds, rec.Kind)
	}
	if len(kinds) != 2 || kinds[0] != "user" || kinds[1] != "assistant" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestOpenCreatesUniqueFilePerSession(t *testing.T) {
	dir := t.TempDir()
	l1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	// A second session in the same second gets a suffixed file (O_EXCL +
	// retry) — never silent reuse of the same file.
	l2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l1.Path() == l2.Path() {
		t.Error("two sessions share one file")
	}
}
