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

// `A -- text --> B` edge labels are normalized to the parsed form; the
// decision node keeps its branches (v0.37.2 — the field case: the
// renderer read "A -- text" as a node and the label guard let it pass).
func TestEdgeTextSyntaxRendersCorrectly(t *testing.T) {
	src := "flowchart TD\n    Start[Investigation Start] --> InputType{Indicator Type?}\n    InputType -- IP Address --> CheckTor[Tor Exit Node Check]\n    InputType -- Domain --> CheckWhois[WHOIS / RDAP Lookup]\n    CheckTor --> CheckASN[ASN & GeoIP Resolution]\n"
	art, ok := Render(src, 120)
	if !ok {
		t.Fatal("not drawn")
	}
	if strings.Contains(art, "InputType --") {
		t.Errorf("edge text parsed as a node:\n%s", art)
	}
	if got := arrowheads(art); got != 4 {
		t.Errorf("arrowheads = %d, want 4 (one per edge):\n%s", got, art)
	}
}

// Structural guard: the edge count and the subgraph-endpoint refusal.
func TestFlowStructuralGuards(t *testing.T) {
	if n := flowEdgeCount("graph TD\n  A[a] --> B{b}\n  B -->|x| C[c] & D[d]\n  E[e] & F[f] --> G[g]\n  H --- I\n"); n != 5 {
		t.Errorf("flowEdgeCount = %d, want 5", n)
	}
	if arrowheads("┌a┐ ─► ┌b┐\n ▼\n◄ ▲") != 4 {
		t.Error("arrowheads miscounted")
	}
	sub := fence("flowchart LR\n    subgraph Passive_Sources [Passive Investigation Layer]\n        DNS[DoH]\n    end\n    Passive_Sources --> Aggregator[Indicator Aggregator]\n")
	if got := Rewrite(sub, 120); got != sub {
		t.Errorf("edge to a subgraph id was drawn (phantom node):\n%s", got)
	}
}

// `direction` inside a subgraph is a layout hint the renderer draws as a
// node and which fused adjacent subgraph titles; it is dropped, and the
// multi-subgraph flowchart draws with its titles intact (v0.37.3).
func TestSubgraphDirectionDropped(t *testing.T) {
	md := fence("flowchart LR\n    subgraph A [Client Zone]\n        U[App] --> G[CLI]\n    end\n    subgraph B [MCP Servers]\n        direction TB\n        W[whois]\n    end\n    G --> W\n")
	out := Rewrite(md, 120)
	if strings.Contains(out, "flowchart LR") {
		t.Fatalf("not drawn:\n%s", out)
	}
	if strings.Contains(out, "direction TB") {
		t.Errorf("direction rendered as a node:\n%s", out)
	}
	if strings.Contains(out, "ZoneMCP") || !strings.Contains(out, "Client Zone") || !strings.Contains(out, "MCP Servers") {
		t.Errorf("subgraph titles fused or lost:\n%s", out)
	}
}

// A dense ER diagram that FITS the screen is drawn — readability is
// the operator's call (v0.37.4 revert of the v0.37.3 complexity cap);
// the width budget remains the only bound.
func TestDenseERDrawsWhenItFits(t *testing.T) {
	ents := "\n  DOMAIN {\n    string fqdn PK\n  }\n  IP {\n    string v4 PK\n  }\n  ASN {\n    int asn PK\n  }\n  CERT {\n    string sha PK\n  }\n  ABUSE {\n    string id PK\n  }\n  PULSE {\n    string id PK\n  }\n"
	dense := "erDiagram\n  DOMAIN ||--o{ IP : a\n  DOMAIN ||--o{ CERT : b\n  DOMAIN ||--|| ASN : c\n  DOMAIN ||--o{ ABUSE : d\n  DOMAIN ||--o{ PULSE : e\n  IP }|--|| ASN : f\n  IP ||--o{ ABUSE : g\n" + ents
	out := Rewrite(fence(dense), 250)
	if strings.Contains(out, "erDiagram") {
		t.Errorf("dense ER that fits was not drawn:\n%s", out)
	}
	simple := "erDiagram\n  DOMAIN ||--o{ IP : resolves\n  IP }|--|| ASN : belongs\n\n  DOMAIN {\n    string fqdn PK\n  }\n  IP {\n    string v4 PK\n  }\n  ASN {\n    int asn PK\n  }\n"
	if strings.Contains(Rewrite(fence(simple), 120), "erDiagram") {
		t.Error("simple ER not drawn")
	}
}

// The renderer pads edge labels with its own line art: a horizontal
// edge label becomes "──IP─/─CIDR──" and a label crossing a subgraph
// border "Domain│/ FQDN". The fidelity guard compares through the
// decoration — stripping only whitespace read those as lost labels and
// refused correct diagrams (v0.37.5, field report).
func TestFaithfulSeesThroughLineArt(t *testing.T) {
	src := "flowchart TD\n  A[Start] --> B{Target Type?}\n  B -->|Domain / FQDN| C[WHOIS Lookup]\n  B -->|IP / CIDR| D[ASN Lookup]\n"
	art, ok := Render(src, 120)
	if !ok {
		t.Fatal("multi-word edge labels refused — the guard is reading padded labels as lost")
	}
	if got := arrowheads(art); got != 3 {
		t.Errorf("arrowheads = %d, want 3:\n%s", got, art)
	}
	// Padding must not be mistaken for a present label either.
	if faithful(kindFlow, src, "┌Start┐ ─► ┌WHOIS Lookup┐ ─► ┌ASN Lookup┐") {
		t.Error("a genuinely missing edge label was accepted")
	}
}

// A flowchart whose edge labels cross subgraph borders draws (the
// field case that stopped rendering in v0.37.4).
func TestSubgraphFlowchartWithLabelledEdges(t *testing.T) {
	src := "flowchart TD\n    Start([Investigation Target]) --> CheckType{Target Type?}\n    subgraph Domain_Flow[Domain Attribution]\n        CheckType -->|Domain / FQDN| D1[WHOIS / RDAP Lookup]\n        D1 --> D2[DNS / DoH Resolution]\n    end\n    subgraph IP_Flow[IP Attribution]\n        CheckType -->|IP / CIDR| I1[ASN & GeoIP Lookup]\n        I1 --> I2[Tor / Relay Check]\n    end\n    D2 --> R[Report]\n    I2 --> R\n"
	out := Rewrite(fence(src), 140)
	if strings.Contains(out, "flowchart TD") {
		t.Fatalf("field flowchart not drawn:\n%s", out)
	}
	for _, want := range []string{"Domain Attribution", "IP Attribution", "WHOIS / RDAP Lookup", "Report"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
