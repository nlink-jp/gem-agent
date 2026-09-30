package diagram

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func fence(src string) string { return "before\n\n```mermaid\n" + src + "```\n\nafter\n" }

// rejoin flattens Split for assertions on overall content; per-segment
// properties (which parts are art) are asserted directly where they
// matter.
func rejoin(md string) string {
	var parts []string
	for _, s := range Split(md, nil) {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n")
}

// artSegments returns just the drawn segments.
func artSegments(md string) []string {
	var arts []string
	for _, s := range Split(md, nil) {
		if s.Art {
			arts = append(arts, s.Text)
		}
	}
	return arts
}

func maxWidth(s string) int {
	w := 0
	for _, l := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// A Japanese flowchart — the case that decides usability — is drawn as
// its own art segment, the source is gone, and the surrounding text
// survives in markdown segments.
func TestFlowchartJapaneseRenders(t *testing.T) {
	md := fence("graph TD\n  A[開始] --> B[承認が必要か]\n  B -->|はい| C[ダイアログ表示]\n  B -->|いいえ| D[そのまま実行]\n")
	out := rejoin(md)
	if strings.Contains(out, "graph TD") {
		t.Fatalf("source not replaced:\n%s", out)
	}
	for _, want := range []string{"before", "after", "┌", "開始", "ダイアログ表示", "はい", "いいえ"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	arts := artSegments(md)
	if len(arts) != 1 || !strings.Contains(arts[0], "開始") {
		t.Fatalf("expected exactly one art segment carrying the drawing, got %d", len(arts))
	}
}

// Unsupported diagram types pass through byte for byte, with no note
// and no art segment: a gantt in the chat is not an error (ADR-0063 §4).
func TestUnsupportedStaysSourceSilently(t *testing.T) {
	for _, src := range []string{
		"stateDiagram-v2\n  [*] --> A\n  A --> B\n",
		"pie title T\n  \"a\" : 1\n",
		"gantt\n  title x\n",
		"classDiagram\n  class A\n",
	} {
		md := fence(src)
		segs := Split(md, nil)
		if len(segs) != 1 || segs[0].Art || segs[0].Text != md {
			t.Errorf("unsupported block was not passed through untouched:\n%v", segs)
		}
	}
}

// A supported kind that cannot be drawn keeps its fence and gains the
// reader-facing note, as its own paragraph on BOTH sides — a note
// glued to the next paragraph reads as its prefix. An unclosed label is
// the deterministic failure.
func TestAttemptedFailureLeavesFencePlusNote(t *testing.T) {
	md := fence("graph TD\n  A[開始 --> B\n")
	out := rejoin(md)
	if !strings.Contains(out, "graph TD") || !strings.Contains(out, "開始") {
		t.Fatalf("source lost:\n%s", out)
	}
	if !strings.Contains(out, "```\n\n*diagram shown as source: ") {
		t.Errorf("note missing or not separated from the fence:\n%s", out)
	}
	note := out[strings.Index(out, "*diagram shown as source: ")+1:]
	end := strings.Index(note, "*")
	if end < 0 || !strings.HasPrefix(note[end+1:], "\n\n") {
		t.Errorf("note not followed by a blank line:\n%s", out)
	}
	if len(artSegments(md)) != 0 {
		t.Error("a failed diagram produced an art segment")
	}
}

// A refusal inside the engine is the same: a label character that takes
// no cell (a combining mark) cannot be placed cell by cell.
func TestEngineRefusalLeavesFencePlusNote(t *testing.T) {
	md := fence("graph TD\n  A[e\u0301] --> B[b]\n")
	if len(artSegments(md)) != 0 || !strings.Contains(rejoin(md), "*diagram shown as source: ") {
		t.Errorf("a refused label drew:\n%s", rejoin(md))
	}
}

func TestSequenceASCIIAndERRender(t *testing.T) {
	seq := fence("sequenceDiagram\n  participant U as User\n  participant A as Agent\n  U->>A: question\n  A-->>U: answer\n")
	out := rejoin(seq)
	if strings.Contains(out, "sequenceDiagram") || !strings.Contains(out, "question") || !strings.Contains(out, "┌") {
		t.Errorf("sequence not drawn:\n%s", out)
	}
	er := fence("erDiagram\n  SESSION ||--o{ MESSAGE : contains\n")
	out = rejoin(er)
	if strings.Contains(out, "erDiagram") || !strings.Contains(out, "contains") {
		t.Errorf("ER not drawn:\n%s", out)
	}
}

// A sequence diagram with Japanese labels draws (ADR-0095): mermaid-ascii
// misaligned wide runes there and the lane refused them. The engine
// places them by the TUI's own measure and checks its grid.
func TestSequenceJapaneseDraws(t *testing.T) {
	arts := artSegments(fence("sequenceDiagram\n  participant U as 操作者\n  participant A as エージェント\n  U->>A: 質問する\n  A-->>U: 回答する\n"))
	if len(arts) != 1 {
		t.Fatal("Japanese sequence not drawn")
	}
	for _, want := range []string{"操作者", "エージェント", "質問する", "回答する"} {
		if !strings.Contains(arts[0], want) {
			t.Errorf("missing %q:\n%s", want, arts[0])
		}
	}
}

// There is no width gate (ADR-0063 §3): a chain that needs far more
// than a typical terminal draws anyway, as a verbatim art segment the
// TUI hands to the terminal (whose own wrap splits rows in order).
func TestNoWidthGate(t *testing.T) {
	wide := fence("graph LR\n  A[Parse config] --> B[Resolve project] --> C[Connect MCP] --> D[Discover skills] --> E[Build prompt] --> F[Start TUI]\n")
	arts := artSegments(wide)
	if len(arts) != 1 {
		t.Fatalf("wide chain not drawn: %d art segments", len(arts))
	}
	if w := maxWidth(arts[0]); w <= 100 {
		t.Errorf("expected art wider than 100 cells (proving no gate), got %d", w)
	}
	// Wide runes draw too — the compact retry that corrupted them is
	// long gone, and without a width gate there is nothing to squeeze
	// for.
	cjk := fence("graph LR\n  A[とても長い日本語のラベルその一] --> B[とても長い日本語のラベルその二] --> C[とても長い日本語のラベルその三]\n")
	if len(artSegments(cjk)) != 1 {
		t.Error("wide-rune diagram not drawn")
	}
}

// There is no height cap either: a tall chain draws and scrolls.
func TestNoHeightCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("graph TD\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "  N%d[step %d] --> N%d[step %d]\n", i, i, i+1, i+1)
	}
	arts := artSegments(fence(b.String()))
	if len(arts) != 1 {
		t.Fatalf("tall chain not drawn")
	}
	if n := strings.Count(arts[0], "\n"); n <= 80 {
		t.Errorf("expected art taller than the old 80-line cap, got %d lines", n)
	}
}

// A ```mermaid line that is content of an enclosing fence is data: an
// example inside a ````markdown block, or a quoted fence inside a
// ```text block, must never be replaced by art (independent review of
// ADR-0063 caught the scanner treating them as openers).
func TestMermaidInsideEnclosingFenceUntouched(t *testing.T) {
	quoted := "````markdown\nHow to write a diagram:\n\n```mermaid\ngraph LR\n  A[a] --> B[b]\n```\n````\ntail\n"
	segs := Split(quoted, nil)
	if len(segs) != 1 || segs[0].Art || segs[0].Text != quoted {
		t.Fatalf("mermaid example inside a ````markdown block was rewritten:\n%v", segs)
	}
	inner := "```text\nliteral lines\n```mermaid\ngraph LR\n  A[a] --> B[b]\n```\n"
	segs = Split(inner, nil)
	for _, s := range segs {
		if s.Art {
			t.Fatalf("mermaid-labeled content line inside a ```text block was drawn:\n%v", segs)
		}
	}
}

// Every shape is drawn as a box carrying its label — never as a literal
// "{text}" plus a stray node.
func TestShapesNormalizedToBoxes(t *testing.T) {
	md := fence("flowchart TD\n  A[開始] --> B{承認?}\n  B -->|はい| C((実行))\n  B -->|いいえ| D([停止])\n  D --> E[(DB)]:::cls\n  classDef cls fill:#f9f\n")
	out := rejoin(md)
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
	out := rejoin(md)
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
	if segs := Split(unclosed, nil); len(segs) != 1 || segs[0].Art || segs[0].Text != unclosed {
		t.Error("unclosed fence rewritten")
	}
	if segs := Split("plain text", nil); len(segs) != 1 || segs[0].Text != "plain text" {
		t.Error("text without mermaid altered")
	}
	// The fast path is case-insensitive like the fence matcher: a
	// ```Mermaid fence draws with or without a lowercase "mermaid"
	// elsewhere in the segment.
	upper := "```Mermaid\ngraph LR\n  A[a] --> B[b]\n```\n"
	if len(artSegments(upper)) != 1 {
		t.Error("```Mermaid fence not drawn")
	}
}

// heads counts the arrowheads in art.
func heads(art string) int { return strings.Count(art, "►") + strings.Count(art, "◄") + strings.Count(art, "▲") + strings.Count(art, "▼") }

// A '&' inside a label is text, and the label is drawn as written.
func TestAmpersandInLabel(t *testing.T) {
	out := rejoin(fence("graph TD\n  A[開始] --> R([レポート作成 & 確度評価])\n"))
	if strings.Contains(out, "graph TD") {
		t.Fatalf("not drawn:\n%s", out)
	}
	if !strings.Contains(out, "レポート作成 & 確度評価") {
		t.Errorf("label not drawn as written:\n%s", out)
	}
}

// `A -- text --> B` edge labels are read as mermaid reads them: the
// decision node keeps its branches, one head per edge (the field case
// mermaid-ascii read as a node, v0.37.2).
func TestEdgeTextSyntaxRendersCorrectly(t *testing.T) {
	src := "flowchart TD\n    Start[Investigation Start] --> InputType{Indicator Type?}\n    InputType -- IP Address --> CheckTor[Tor Exit Node Check]\n    InputType -- Domain --> CheckWhois[WHOIS / RDAP Lookup]\n    CheckTor --> CheckASN[ASN & GeoIP Resolution]\n"
	art, why, attempted := render(src)
	if !attempted || why != "" {
		t.Fatalf("not drawn: %s", why)
	}
	if strings.Contains(art, "InputType --") {
		t.Errorf("edge text parsed as a node:\n%s", art)
	}
	for _, want := range []string{"IP Address", "Domain", "Tor Exit Node Check"} {
		if !strings.Contains(art, want) {
			t.Errorf("missing %q:\n%s", want, art)
		}
	}
	if got := heads(art); got != 4 {
		t.Errorf("heads = %d, want 4 (one per edge):\n%s", got, art)
	}
}

// An edge whose endpoint is a subgraph id is an edge to the subgraph,
// not a phantom node named after it (ADR-0042's `Z --> S`).
func TestSubgraphIDEdgeIsNoPhantomNode(t *testing.T) {
	src := "flowchart LR\n    subgraph Passive_Sources [Passive Investigation Layer]\n        DNS[DoH]\n    end\n    Passive_Sources --> Aggregator[Indicator Aggregator]\n"
	art, why, attempted := render(src)
	if !attempted || why != "" {
		t.Fatalf("edge to a subgraph id refused: %s", why)
	}
	if strings.Contains(art, "Passive_Sources") {
		t.Errorf("the subgraph id drawn as a node:\n%s", art)
	}
	if !strings.Contains(art, "Passive Investigation Layer") || heads(art) != 1 {
		t.Errorf("subgraph edge not drawn:\n%s", art)
	}
}

// An unsupported type is not attempted; the engine's parse decides,
// not a classifier here.
func TestRenderAttemptsOnlyTheThreeTypes(t *testing.T) {
	for src, want := range map[string]bool{
		"graph LR\nA-->B":           true,
		"flowchart TD\nA-->B":       true,
		"sequenceDiagram\nA->>B: x": true,
		"erDiagram\nA ||--o{ B : c": true,
		"stateDiagram-v2\nA-->B":    false,
		"pie\n\"a\" : 1":           false,
		"classDiagram\nclass A":     false,
	} {
		if _, _, attempted := render(src); attempted != want {
			t.Errorf("render(%q) attempted = %v, want %v", src, attempted, want)
		}
	}
}

// `direction` inside a subgraph is a layout hint the renderer draws as a
// node and which fused adjacent subgraph titles; it is dropped, and the
// multi-subgraph flowchart draws with its titles intact (v0.37.3).
func TestSubgraphDirectionDropped(t *testing.T) {
	md := fence("flowchart LR\n    subgraph A [Client Zone]\n        U[App] --> G[CLI]\n    end\n    subgraph B [MCP Servers]\n        direction TB\n        W[whois]\n    end\n    G --> W\n")
	out := rejoin(md)
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

// A dense ER diagram is drawn — readability is the operator's call
// (v0.37.4 revert of the complexity cap; ADR-0063 removed the width
// bound that remained).
func TestDenseERDraws(t *testing.T) {
	ents := "\n  DOMAIN {\n    string fqdn PK\n  }\n  IP {\n    string v4 PK\n  }\n  ASN {\n    int asn PK\n  }\n  CERT {\n    string sha PK\n  }\n  ABUSE {\n    string id PK\n  }\n  PULSE {\n    string id PK\n  }\n"
	dense := "erDiagram\n  DOMAIN ||--o{ IP : a\n  DOMAIN ||--o{ CERT : b\n  DOMAIN ||--|| ASN : c\n  DOMAIN ||--o{ ABUSE : d\n  DOMAIN ||--o{ PULSE : e\n  IP }|--|| ASN : f\n  IP ||--o{ ABUSE : g\n" + ents
	if len(artSegments(fence(dense))) != 1 {
		t.Error("dense ER was not drawn")
	}
	simple := "erDiagram\n  DOMAIN ||--o{ IP : resolves\n  IP }|--|| ASN : belongs\n\n  DOMAIN {\n    string fqdn PK\n  }\n  IP {\n    string v4 PK\n  }\n  ASN {\n    int asn PK\n  }\n"
	if len(artSegments(fence(simple))) != 1 {
		t.Error("simple ER not drawn")
	}
}

// Multi-word edge labels draw whole (the field case mermaid-ascii padded
// with line art, v0.37.5).
func TestMultiWordEdgeLabels(t *testing.T) {
	src := "flowchart TD\n  A[Start] --> B{Target Type?}\n  B -->|Domain / FQDN| C[WHOIS Lookup]\n  B -->|IP / CIDR| D[ASN Lookup]\n"
	art, why, attempted := render(src)
	if !attempted || why != "" {
		t.Fatalf("multi-word edge labels refused: %s", why)
	}
	for _, want := range []string{"Domain / FQDN", "IP / CIDR"} {
		if !strings.Contains(art, want) {
			t.Errorf("label %q not drawn whole:\n%s", want, art)
		}
	}
	if got := heads(art); got != 3 {
		t.Errorf("heads = %d, want 3:\n%s", got, art)
	}
}

// A flowchart whose edge labels cross subgraph borders draws (the
// field case that stopped rendering in v0.37.4).
func TestSubgraphFlowchartWithLabelledEdges(t *testing.T) {
	src := "flowchart TD\n    Start([Investigation Target]) --> CheckType{Target Type?}\n    subgraph Domain_Flow[Domain Attribution]\n        CheckType -->|Domain / FQDN| D1[WHOIS / RDAP Lookup]\n        D1 --> D2[DNS / DoH Resolution]\n    end\n    subgraph IP_Flow[IP Attribution]\n        CheckType -->|IP / CIDR| I1[ASN & GeoIP Lookup]\n        I1 --> I2[Tor / Relay Check]\n    end\n    D2 --> R[Report]\n    I2 --> R\n"
	out := rejoin(fence(src))
	if strings.Contains(out, "flowchart TD") {
		t.Fatalf("field flowchart not drawn:\n%s", out)
	}
	for _, want := range []string{"Domain Attribution", "IP Attribution", "WHOIS / RDAP Lookup", "Report"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// The field flowchart that stopped drawing in v0.37.2–v0.37.5: edges
// whose endpoints are subgraph ids, CJK labels, multi-word edge labels.
func TestSubgraphIDEdgesWithJapaneseLabels(t *testing.T) {
	src := "flowchart TD\n    Start([調査開始 / Indicator Input]) --> CheckType{種別判定}\n    CheckType -->|Domain / FQDN| StepDomain[ドメイン帰属調査 SOP]\n    CheckType -->|IP Address| StepIP[IPアドレス帰属調査 SOP]\n    subgraph DomainFlow [ドメイン調査]\n        StepDomain --> D1[WHOIS / RDAP 照会]\n        D1 --> D2[DoH DNSレコード解決]\n    end\n    subgraph IPFlow [IP調査]\n        StepIP --> I1[ASN / GeoIP 特定]\n        I1 --> I2[Tor 判定]\n    end\n    DomainFlow --> Correlate[相関分析]\n    IPFlow --> Correlate\n    Correlate --> Report[レポート出力]\n"
	art, why, attempted := render(src)
	if !attempted || why != "" {
		t.Fatalf("field flowchart with subgraph-id edges refused: %s", why)
	}
	if got := heads(art); got != 10 {
		t.Errorf("heads = %d, want 10 (one per edge):\n%s", got, art)
	}
	for _, want := range []string{"種別判定", "ドメイン調査", "IP調査", "相関分析", "Domain / FQDN"} {
		if !strings.Contains(art, want) {
			t.Errorf("label %q lost:\n%s", want, art)
		}
	}
}
