package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/skills"
)

func TestSlashCompletionsSource(t *testing.T) {
	complete := slashCompletions(func() []skills.Skill { return []skills.Skill{{Name: "meeting-notes"}, {Name: "news-digest"}} })

	if got := complete("/us"); len(got) != 1 || got[0] != "/usage" {
		t.Errorf("/us → %v", got)
	}
	got := complete("/s")
	joined := strings.Join(got, " ")
	for _, want := range []string{"/settings", "/skill", "/skills"} {
		if !strings.Contains(joined, want) {
			t.Errorf("/s missing %s: %v", want, got)
		}
	}
	if got := complete("/v"); len(got) != 1 || got[0] != "/version" {
		t.Errorf("/v → %v", got)
	}
	if got := complete("/skill me"); len(got) != 1 || got[0] != "/skill meeting-notes" {
		t.Errorf("/skill me → %v", got)
	}
	if got := complete("/skill zz"); len(got) != 0 {
		t.Errorf("/skill zz → %v", got)
	}
	if got := complete("/nonexistent"); len(got) != 0 {
		t.Errorf("unknown prefix → %v", got)
	}
}
