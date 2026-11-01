package cmd

import (
	"bytes"
	"log"
	"testing"
)

func TestQuietStdLogDropsAndRestores(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	restore := quietStdLog()
	log.Print("raw library line")
	if buf.Len() != 0 {
		t.Fatalf("the standard logger reached the terminal under the TUI: %q", buf.String())
	}
	restore()
	log.Print("after")
	if buf.Len() == 0 {
		t.Fatal("restore did not give the standard logger back")
	}
}
