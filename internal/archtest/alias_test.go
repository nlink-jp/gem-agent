package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

// The collector must see through the spellings a rule-dodger would use
// (review F2): an import alias, a dot-import, a function value taken
// without a call, and a local identifier named `bounded`.
func TestCollectorSeesAliasesAndValues(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package probe

import (
	rk "github.com/nlink-jp/gem-agent/internal/risk"
	stdos "os"
	"os/exec"
	. "io"
)

func aliased() {
	_ = rk.Classify("t", true, nil, "", "")
	_, _ = stdos.ReadFile("/etc/hosts")
	bounded := exec.Command("true")
	_, _ = bounded.CombinedOutput()
	f := rk.Classify
	_ = f
	_, _ = ReadAll(nil)
}
`
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := collectCalls(t, root, []string{"internal"}, []string{"risk.Classify", "os.ReadFile", "io.ReadAll", ".CombinedOutput"})
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.callee] = true
	}
	for _, want := range []string{"risk.Classify", "os.ReadFile", ".CombinedOutput", "io.ReadAll"} {
		if !seen[want] {
			t.Errorf("collector missed %s through an alias, a dot-import, a value or a shadowing name: %v", want, calls)
		}
	}
}
