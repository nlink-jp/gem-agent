package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// TestSlashHelpFollowsLanguage pins ADR-0029: /help is monolingual in
// the resolved language — the historical mixed EN/JA text must not
// come back.
func TestSlashHelpFollowsLanguage(t *testing.T) {
	out, isErr, quit := slashOutput("/help", nil, nil, nil, nil, slashReloads{}, nil, nil, nil, "", uitext.For(uitext.JA))
	if isErr || quit {
		t.Fatalf("/help: isErr=%v quit=%v", isErr, quit)
	}
	if !strings.Contains(out, "コマンド:") || strings.Contains(out, "commands:") {
		t.Errorf("ja /help not Japanese:\n%s", out)
	}

	out, _, _ = slashOutput("/help", nil, nil, nil, nil, slashReloads{}, nil, nil, nil, "", uitext.For(uitext.EN))
	if !strings.Contains(out, "commands:") || strings.Contains(out, "送信") {
		t.Errorf("en /help not English:\n%s", out)
	}
}

// TestTrustPromptFollowsLanguage covers the startup-safety strings —
// the prompt the operator answers before anything loads.
func TestTrustPromptFollowsLanguage(t *testing.T) {
	msgs := uitext.For(uitext.JA)
	err := confirmBroadRoot("home", "/Users/op", true, strings.NewReader("\n"), &strings.Builder{}, msgs)
	if err == nil || !strings.Contains(err.Error(), "プロジェクトディレクトリ") {
		t.Errorf("ja broad-root refusal not Japanese: %v", err)
	}
	var out strings.Builder
	_ = confirmBroadRoot("home", "/Users/op", true, strings.NewReader("\n"), &out, msgs)
	if !strings.Contains(out.String(), "ホームディレクトリ") {
		t.Errorf("ja broad-root prompt not localized:\n%s", out.String())
	}
}
