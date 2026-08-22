package diagram

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func fence(src string) string { return "before\n\n```mermaid\n" + src + "```\n\nafter\n" }

func maxWidth(s string) int {
	w := 0
	for _, l := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// A Japanese flowchart — the case that decides usability — is drawn,
// the source is gone, the surrounding text survives, and nothing is
// wider than the width budget.
func TestFlowchartJapaneseRenders(t *testing.T) {
	md := fence("graph TD\n  A[開始] --> B[承認が必要か]\n  B -->|はい| C[ダイアログ表示]\n  B -->|いいえ| D[そのまま実行]\n")
	out := Rewrite(md, 100)
	if strings.Contains(out, "graph TD") {
		t.Fatalf("source not replaced:\n%s", out)
	}
	for _, want := range []string{"before", "after", "```text", "┌", "開始", "ダイアログ表示", "はい", "いいえ"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if w := maxWidth(out); w > 100-widthMargin {
		t.Errorf("art width %d exceeds budget", w)
	}
}

// Unsupported types and a sequence diagram with wide labels stay source,
// byte for byte.
func TestUnsupportedStaysSource(t *testing.T) {
	for _, src := range []string{
		"stateDiagram-v2\n  [*] --> A\n  A --> B\n",
		"pie title T\n  \"a\" : 1\n",
		"sequenceDiagram\n  participant U as 操作者\n  U->>A: 質問\n",
		"gantt\n  title x\n",
	} {
		md := fence(src)
		if got := Rewrite(md, 100); got != md {
			t.Errorf("rewrote an unsupported block:\n%s", got)
		}
	}
}

func TestSequenceASCIIAndERRender(t *testing.T) {
	seq := fence("sequenceDiagram\n  participant U as User\n  participant A as Agent\n  U->>A: question\n  A-->>U: answer\n")
	out := Rewrite(seq, 100)
	if strings.Contains(out, "sequenceDiagram") || !strings.Contains(out, "question") || !strings.Contains(out, "┌") {
		t.Errorf("sequence not drawn:\n%s", out)
	}
	er := fence("erDiagram\n  SESSION ||--o{ MESSAGE : contains\n")
	out = Rewrite(er, 100)
	if strings.Contains(out, "erDiagram") || !strings.Contains(out, "contains") {
		t.Errorf("ER not drawn:\n%s", out)
	}
}

// Width: a wide chain stays source at 60 columns and is drawn (with
// tight padding) at 100.
func TestWidthBudget(t *testing.T) {
	wide := fence("graph LR\n  A[Parse config] --> B[Resolve project] --> C[Connect MCP] --> D[Discover skills] --> E[Build prompt] --> F[Start TUI]\n")
	if got := Rewrite(wide, 60); got != wide {
		t.Errorf("too-wide diagram was drawn at 60 cols:\n%s", got)
	}
	out := Rewrite(wide, 100)
	if strings.Contains(out, "graph LR") {
		t.Fatalf("wide chain not drawn at 100 cols")
	}
	if w := maxWidth(out); w > 100-widthMargin {
		t.Errorf("art width %d exceeds budget", w)
	}
}

// Shapes the renderer does not parse are drawn as boxes carrying the
// label — never as a literal "{text}" plus a stray node.
func TestShapesNormalizedToBoxes(t *testing.T) {
	md := fence("flowchart TD\n  A[開始] --> B{承認?}\n  B -->|はい| C((実行))\n  B -->|いいえ| D([停止])\n  D --> E[(DB)]:::cls\n  classDef cls fill:#f9f\n")
	out := Rewrite(md, 100)
	if strings.Contains(out, "flowchart TD") {
		t.Fatalf("not drawn:\n%s", out)
	}
	for _, bad := range []string{"{承認", "((実行", "([停止", "[(DB", ":::"} {
		if strings.Contains(out, bad) {
			t.Errorf("raw shape syntax leaked into the art: %q", bad)
		}
	}
	for _, want := range []string{"承認?", "実行", "停止", "DB"} {
		if !strings.Contains(out, want) {
			t.Errorf("label %q missing from the art", want)
		}
	}
}

// Non-mermaid fences and unclosed fences are untouched; several blocks
// are handled independently.
func TestFencesUntouchedAndMultiple(t *testing.T) {
	md := "```go\nfunc main() {}\n```\n\n```mermaid\ngraph LR\n  A[a] --> B[b]\n```\n\n```mermaid\nstateDiagram-v2\n  [*] --> X\n```\n"
	out := Rewrite(md, 100)
	if !strings.Contains(out, "```go\nfunc main() {}\n```") {
		t.Error("go fence altered")
	}
	if strings.Contains(out, "graph LR") {
		t.Error("first mermaid block not drawn")
	}
	if !strings.Contains(out, "stateDiagram-v2") {
		t.Error("unsupported second block not preserved")
	}
	unclosed := "```mermaid\ngraph LR\n  A --> B\n"
	if got := Rewrite(unclosed, 100); got != unclosed {
		t.Error("unclosed fence rewritten")
	}
	if got := Rewrite("plain text", 100); got != "plain text" {
		t.Error("text without mermaid altered")
	}
}

// The prompt promises exactly the supported list (ADR-0042 §1) — the
// two can never drift because they are one slice.
func TestPromptSectionListsSupported(t *testing.T) {
	p := PromptSection()
	for _, s := range Supported() {
		if !strings.Contains(p, s) {
			t.Errorf("prompt lacks %q", s)
		}
	}
	if !strings.Contains(p, "raw source") || !strings.Contains(p, "Files are unaffected") {
		t.Errorf("prompt must state the fallback and the file exemption: %q", p)
	}
	// And the classifier accepts exactly those families.
	for src, want := range map[string]kind{
		"graph LR\nA-->B":           kindFlow,
		"flowchart TD\nA-->B":       kindFlow,
		"sequenceDiagram\nA->>B: x": kindSequence,
		"erDiagram\nA ||--o{ B : c": kindER,
		"stateDiagram-v2\nA-->B":    kindUnsupported,
		"classDiagram\nclass A":     kindUnsupported,
	} {
		if k, _ := classify(src); k != want {
			t.Errorf("classify(%q) = %v, want %v", src, k, want)
		}
	}
}

// The fidelity guard refuses art that lost a label.
func TestFaithfulGuard(t *testing.T) {
	src := "graph LR\n  A[alpha] -->|edge| B[beta]\n"
	if !faithful(kindFlow, src, "┌alpha┐ ─edge► ┌beta┐") {
		t.Error("complete art rejected")
	}
	if faithful(kindFlow, src, "┌alpha┐ ─edge► ┌B┐") {
		t.Error("art missing a node label accepted")
	}
}

// A '&' inside a label is a fan-in operator to the renderer; it is
// drawn as the full-width ＆ so the graph stays right and the label
// stays readable (v0.37.1).
func TestAmpersandInLabel(t *testing.T) {
	md := fence("graph TD\n  A[開始] --> R([レポート作成 & 確度評価])\n")
	out := Rewrite(md, 100)
	if strings.Contains(out, "graph TD") {
		t.Fatalf("not drawn:\n%s", out)
	}
	if !strings.Contains(out, "レポート作成 ＆ 確度評価") {
		t.Errorf("label not drawn with ＆:\n%s", out)
	}
}
