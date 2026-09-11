package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0084 §4: the prompt says compile, vet and test are read-lane
// work, names the read lane for verification, and no longer carries
// the "denial is a decision — ask" rule; the shell_exec description
// agrees with it.
func TestPromptSaysWhatTheLanesNowDo(t *testing.T) {
	sys := buildSystemPrompt("/tmp/proj", "", "")
	for _, want := range []string{"compiling, vetting and testing", "toolchain cache lives in the lane's scratch", "writes a binary into the project", "in the read lane — no approval needed"} {
		if !strings.Contains(sys, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, gone := range []string{"write their caches", "a denial is a decision", "ask how to proceed instead of retrying", `access: "write") and report`} {
		if strings.Contains(sys, gone) {
			t.Errorf("prompt still says %q", gone)
		}
	}
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := reg.Get(tools.ShellExecName)
	if !strings.Contains(sh.Description, "compiling, vetting and testing") || strings.Contains(sh.Description, "write their caches") {
		t.Errorf("shell_exec description disagrees with the prompt: %q", sh.Description)
	}
}
