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
	for _, s := range Split(md) {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n")
}

// artSegments returns just the drawn segments.
func artSegments(md string) []string {
	var arts []string
	for _, s := range Split(md) {
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
		segs := Split(md)
		if len(segs) != 1 || segs[0].Art || segs[0].Text != md {
			t.Errorf("unsupported block was not passed through untouched:\n%v", segs)
		}
	}
}

// A supported kind that cannot be drawn keeps its fence and gains the
// reader-facing note, as its own paragraph on BOTH sides — a note
// glued to the next paragraph reads as its prefix. The sequence-CJK
// case is the deterministic failure: the renderer misaligns wide runes.
func TestAttemptedFailureLeavesFencePlusNote(t *testing.T) {
	md := fence("sequenceDiagram\n  participant U as 操作者\n  U->>A: 質問\n")
	out := rejoin(md)
	if !strings.Contains(out, "sequenceDiagram") || !strings.Contains(out, "操作者") {
		t.Fatalf("source lost:\n%s", out)
	}
	if !strings.Contains(out, "```\n\n*diagram shown as source: ") {
		t.Errorf("note missing or not separated from the fence:\n%s", out)
	}
	noteEnd := strings.Index(out, "labels*")
	if noteEnd < 0 || !strings.HasPrefix(out[noteEnd+len("labels*"):], "\n\n") {
		t.Errorf("note not followed by a blank line:\n%s", out)
	}
	if len(artSegments(md)) != 0 {
		t.Error("a failed diagram produced an art segment")
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
	segs := Split(quoted)
	if len(segs) != 1 || segs[0].Art || segs[0].Text != quoted {
		t.Fatalf("mermaid example inside a ````markdown block was rewritten:\n%v", segs)
	}
	inner := "```text\nliteral lines\n```mermaid\ngraph LR\n  A[a] --> B[b]\n```\n"
	segs = Split(inner)
	for _, s := range segs {
		if s.Art {
			t.Fatalf("mermaid-labeled content line inside a ```text block was drawn:\n%v", segs)
		}
	}
}

// Shapes the renderer does not parse are drawn as boxes carrying the
// label — never as a literal "{text}" plus a stray node.
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
	if segs := Split(unclosed); len(segs) != 1 || segs[0].Art || segs[0].Text != unclosed {
		t.Error("unclosed fence rewritten")
	}
	if segs := Split("plain text"); len(segs) != 1 || segs[0].Text != "plain text" {
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

// The classifier accepts exactly the renderable families.
func TestClassify(t *testing.T) {
	for src, want := range map[string]kind{
		"graph LR\nA-->B":           kindFlow,
		"flowchart TD\nA-->B":       kindFlow,
		"sequenceDiagram\nA->>B: x": kindSequence,
		"erDiagram\nA ||--o{ B : c": kindER,
		"stateDiagram-v2\nA-->B":    kindUnsupported,
		"classDiagram\nclass A":     kindUnsupported,
	} {
		if k := classify(src); k != want {
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
	out := rejoin(md)
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
	art, why, attempted := render(src)
	if !attempted || why != "" {
		t.Fatalf("not drawn: %s", why)
	}
	if strings.Contains(art, "InputType --") {
		t.Errorf("edge text parsed as a node:\n%s", art)
	}
	if got := arrowheads(art); got != 4 {
		t.Errorf("arrowheads = %d, want 4 (one per edge):\n%s", got, art)
	}
}

// Structural guard: the edge count, and the subgraph-id edge that a
// blacklist once wrongly refused.
func TestFlowStructuralGuards(t *testing.T) {
	if n := flowEdgeCount("graph TD\n  A[a] --> B{b}\n  B -->|x| C[c] & D[d]\n  E[e] & F[f] --> G[g]\n  H --- I\n"); n != 5 {
		t.Errorf("flowEdgeCount = %d, want 5", n)
	}
	if arrowheads("┌a┐ ─► ┌b┐\n ▼\n◄ ▲") != 4 {
		t.Error("arrowheads miscounted")
	}
	// An edge whose endpoint is a subgraph id draws correctly (measured
	// v0.37.6; the v0.37.2 refusal was an unverified assumption). The
	// edge-count guard is what proves it, not a syntax blacklist.
	sub := "flowchart LR\n    subgraph Passive_Sources [Passive Investigation Layer]\n        DNS[DoH]\n    end\n    Passive_Sources --> Aggregator[Indicator Aggregator]\n"
	art, why, attempted := render(sub)
	if !attempted || why != "" {
		t.Fatalf("edge to a subgraph id refused: %s", why)
	}
	if arrowheads(art) != flowEdgeCount(prepare(kindFlow, sub)) {
		t.Errorf("subgraph-id edge miscounted:\n%s", art)
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

// The renderer pads edge labels with its own line art: a horizontal
// edge label becomes "──IP─/─CIDR──" and a label crossing a subgraph
// border "Domain│/ FQDN". The fidelity guard compares through the
// decoration — stripping only whitespace read those as lost labels and
// refused correct diagrams (v0.37.5, field report).
func TestFaithfulSeesThroughLineArt(t *testing.T) {
	src := "flowchart TD\n  A[Start] --> B{Target Type?}\n  B -->|Domain / FQDN| C[WHOIS Lookup]\n  B -->|IP / CIDR| D[ASN Lookup]\n"
	art, why, attempted := render(src)
	if !attempted || why != "" {
		t.Fatalf("multi-word edge labels refused (%s) — the guard is reading padded labels as lost", why)
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
	if got, want := arrowheads(art), flowEdgeCount(prepare(kindFlow, src)); got != want {
		t.Errorf("arrowheads %d != source edges %d:\n%s", got, want, art)
	}
	// Compare the way the production guard does — through the
	// renderer's decoration (v0.37.5).
	flat := decorationRe.ReplaceAllString(art, "")
	for _, want := range []string{"種別判定", "ドメイン調査", "IP調査", "相関分析", "Domain / FQDN"} {
		if !strings.Contains(flat, decorationRe.ReplaceAllString(want, "")) {
			t.Errorf("label %q lost", want)
		}
	}
}
