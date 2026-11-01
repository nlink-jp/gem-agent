package cmd

import (
	"bytes"
	"log"
	"reflect"
	"testing"
)

func TestQuietStdLogRecordsInsteadOfPrinting(t *testing.T) {
	var term bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&term)
	defer func() { log.SetOutput(prev); log.SetFlags(flags) }()

	var got []string
	restore := quietStdLog(func(line string) { got = append(got, line) })
	log.Printf("Error %v", "http2: client connection lost")
	_, _ = log.Writer().Write([]byte("a line in "))
	_, _ = log.Writer().Write([]byte("two pieces\n"))
	if term.Len() != 0 {
		t.Fatalf("the standard logger reached the terminal under the TUI: %q", term.String())
	}
	want := []string{"Error http2: client connection lost", "a line in two pieces"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recorded %q, want %q", got, want)
	}
	restore()
	log.Print("after")
	if term.Len() == 0 {
		t.Fatal("restore did not give the standard logger back")
	}
}
