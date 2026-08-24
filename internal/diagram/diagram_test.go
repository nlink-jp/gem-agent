package diagram

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// drawn reports whether a source renders at the given width; the old
// helper wrapped it in a Markdown fence, but the fence path is gone —
// diagrams are drawn by the tool now (ADR-0043).
func drawn(src string, width int) bool {
	_, _, ok := Render(src, width)
	return ok
}

// why returns the refusal reason, which the tool hands to the model.
func why(src string, width int) string {
	_, w, _ := Render(src, width)
	return w
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

// A Japanese flowchart — the case that decides usability — is drawn and
// nothing is wider than the width budget.
func TestFlowchartJapaneseRenders(t *testing.T) {
	src := "graph TD\n  A[開始] --> B[承認が必要か]\n  B -->|はい| C[ダイアログ表示]\n  B -->|いいえ| D[そのまま実行]\n"
	art, w, ok := Render(src, 100)
	if !ok {
		t.Fatalf("not drawn: %s", w)
	}
	for _, want := range []string{"┌", "開始", "ダイアログ表示", "はい", "いいえ"} {
		if !strings.Contains(art, want) {
			t.Errorf("missing %q in:\n%s", want, art)
		}
	}
	if got := maxWidth(art); got > 100-widthMargin {
		t.Errorf("art width %d exceeds budget", got)
	}
}

// Unsupported types and a sequence diagram with wide labels are refused,
// and every refusal carries a reason: the model is told, in its own tool
// result, what to do instead (ADR-0043 §1).
func TestUnsupportedStaysSource(t *testing.T) {
	for _, src := range []string{
		"stateDiagram-v2\n  [*] --> A\n  A --> B\n",
		"pie title T\n  \"a\" : 1\n",
		"sequenceDiagram\n  participant U as 操作者\n  U->>A: 質問\n",
		"gantt\n  title x\n",
	} {
		if _, reason, ok := Render(src, 100); ok {
			t.Errorf("unsupported source was drawn: %q", src)
		} else if reason == "" {
			t.Errorf("refusal for %q carried no reason — the model would learn nothing", src)
		}
	}
}

func TestSequenceASCIIAndERRender(t *testing.T) {
	out, _, _ := Render("sequenceDiagram\n  participant U as User\n  participant A as Agent\n  U->>A: question\n  A-->>U: answer\n", 100)
	if strings.Contains(out, "sequenceDiagram") || !strings.Contains(out, "question") || !strings.Contains(out, "┌") {
		t.Errorf("sequence not drawn:\n%s", out)
	}
	out, _, _ = Render("erDiagram\n  SESSION ||--o{ MESSAGE : contains\n", 100)
	if strings.Contains(out, "erDiagram") || !strings.Contains(out, "contains") {
		t.Errorf("ER not drawn:\n%s", out)
	}
}

// Rule 2 is one rule: the diagram fits or it is refused — and the
// refusal now says how wide it came out and how much room there was,
// because that reason is what the model receives (ADR-0043 §1).
func TestWidthBudget(t *testing.T) {
	wide := "graph LR\n  A[Parse config] --> B[Resolve project] --> C[Connect MCP] --> D[Discover skills] --> E[Build prompt] --> F[Start TUI]\n"
	for _, narrow := range []int{60, 100} {
		art, reason, ok := Render(wide, narrow)
		if ok {
			t.Errorf("too-wide diagram drawn at %d cols:\n%s", narrow, art)
			continue
		}
		if !strings.Contains(reason, "columns wide") || !strings.Contains(reason, "usable") {
			t.Errorf("refusal at %d cols does not say the measurements: %q", narrow, reason)
		}
	}
	art, reason, ok := Render(wide, 140)
	if !ok {
		t.Fatalf("wide chain not drawn at 140 cols: %s", reason)
	}
	if got := maxWidth(art); got > 140-widthMargin {
		t.Errorf("art width %d exceeds budget", got)
	}
}

// Shapes the renderer does not parse are drawn as boxes carrying the
// label — never as a literal "{text}" plus a stray node.
func TestShapesNormalizedToBoxes(t *testing.T) {
	src := "flowchart TD\n  A[開始] --> B{承認?}\n  B -->|はい| C((実行))\n  B -->|いいえ| D([停止])\n  D --> E[(DB)]:::cls\n  classDef cls fill:#f9f\n"
	art, w, ok := Render(src, 100)
	if !ok {
		t.Fatalf("not drawn: %s", w)
	}
	for _, bad := range []string{"{承認", "((実行", "([停止", "[(DB", ":::"} {
		if strings.Contains(art, bad) {
			t.Errorf("raw shape syntax leaked into the art: %q", bad)
		}
	}
	for _, want := range []string{"承認?", "実行", "停止", "DB"} {
		if !strings.Contains(art, want) {
			t.Errorf("label %q missing from the art", want)
		}
	}
}

// The fence path is gone (ADR-0043): the package no longer inspects or
// rewrites Markdown at all. It renders one source at a time, and a
// fenced block handed to it is accepted rather than refused on
// punctuation — the tool strips it before calling.
func TestPackageNoLongerTouchesMarkdown(t *testing.T) {
	if _, _, ok := Render("```mermaid\ngraph LR\n  A[a] --> B[b]\n```", 100); ok {
		t.Error("Render accepted a fenced block; stripping belongs to the caller")
	}
	if _, _, ok := Render("graph LR\n  A[a] --> B[b]\n", 100); !ok {
		t.Error("bare source not drawn")
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
	out, _, _ := Render("graph TD\n  A[開始] --> R([レポート作成 & 確度評価])\n", 100)
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
	art, _, ok := Render(src, 120)
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
	// An edge whose endpoint is a subgraph id draws correctly (measured
	// v0.37.6; the v0.37.2 refusal was an unverified assumption). The
	// edge-count guard is what proves it, not a syntax blacklist.
	sub := "flowchart LR\n    subgraph Passive_Sources [Passive Investigation Layer]\n        DNS[DoH]\n    end\n    Passive_Sources --> Aggregator[Indicator Aggregator]\n"
	art, _, ok := Render(sub, 120)
	if !ok {
		t.Fatalf("edge to a subgraph id refused")
	}
	if arrowheads(art) != flowEdgeCount(prepare(kindFlow, sub)) {
		t.Errorf("subgraph-id edge miscounted:\n%s", art)
	}
}

// `direction` inside a subgraph is a layout hint the renderer draws as a
// node and which fused adjacent subgraph titles; it is dropped, and the
// multi-subgraph flowchart draws with its titles intact (v0.37.3).
func TestSubgraphDirectionDropped(t *testing.T) {
	out, _, _ := Render("flowchart LR\n    subgraph A [Client Zone]\n        U[App] --> G[CLI]\n    end\n    subgraph B [MCP Servers]\n        direction TB\n        W[whois]\n    end\n    G --> W\n", 120)
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
	out, _, _ := Render(dense, 250)
	if strings.Contains(out, "erDiagram") {
		t.Errorf("dense ER that fits was not drawn:\n%s", out)
	}
	simple := "erDiagram\n  DOMAIN ||--o{ IP : resolves\n  IP }|--|| ASN : belongs\n\n  DOMAIN {\n    string fqdn PK\n  }\n  IP {\n    string v4 PK\n  }\n  ASN {\n    int asn PK\n  }\n"
	if _, _, ok := Render(simple, 120); !ok {
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
	art, _, ok := Render(src, 120)
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
	out, _, _ := Render(src, 140)
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
	art, _, ok := Render(src, 185)
	if !ok {
		t.Fatal("field flowchart with subgraph-id edges refused")
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

// The tight-padding retry corrupts double-width labels ("種別判定" →
// "種別┬定"), so a CJK diagram that does not fit is shown as source
// rather than retried (v0.37.6).
func TestNoCompactRetryForWideRunes(t *testing.T) {
	src := "graph LR\n  A[とても長い日本語のラベルその一] --> B[とても長い日本語のラベルその二] --> C[とても長い日本語のラベルその三]\n"
	if _, _, ok := Render(src, 60); ok {
		t.Error("wide-rune diagram was drawn through the corrupting compact retry")
	}
	if _, _, ok := Render(src, 200); !ok {
		t.Error("wide-rune diagram that fits the default layout was refused")
	}
}

// The prompt teaches the dialect that draws (v0.38.0): the translation
// table is the backstop, not the mechanism. Each construct the table
// handles must be named in the guidance, or the model is left to
// discover it by having its diagram silently rewritten.
func TestPromptTeachesTheDialect(t *testing.T) {
	p := PromptSection()
	for _, want := range []string{"[square-bracket]", "-->|label|", "direction", "classDef", "&"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt does not teach %q:\n%s", want, p)
		}
	}
	// ADR-0043: the prompt must send the model to the tool and forbid
	// the fence, and must point at where the budget lives. A prompt
	// that only offers a capability does not get used — measured.
	for _, want := range []string{"render_diagram", "do NOT write a ```mermaid", "agent_info", "render budget"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

// The budget is the usable columns and the FIXED line cap — never the
// terminal's rows, because the inline TUI scrolls (ADR-0043 §3).
func TestBudgetIsSpaceNotConsoleSize(t *testing.T) {
	cols, rows := Budget(150)
	if cols != 150-widthMargin {
		t.Errorf("cols = %d, want %d", cols, 150-widthMargin)
	}
	if rows != maxArtLines {
		t.Errorf("rows = %d, want the fixed cap %d", rows, maxArtLines)
	}
	if _, tall := Budget(40); tall != maxArtLines {
		t.Error("the height cap moved with the width — it is fixed")
	}
}

// tallChain builds a narrow vertical flowchart far past the line cap.
func tallChain(n int) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  N%d[s%d] --> N%d[s%d]\n", i, i, i+1, i+1)
	}
	return b.String()
}

// Every refusal must name something the author can act on: the reason
// is the model's only signal, and "false" taught it nothing (ADR-0043).
func TestEveryRefusalIsActionable(t *testing.T) {
	cases := map[string]string{
		"unsupported type": "stateDiagram-v2\n  [*] --> A\n",
		"wide sequence":    "sequenceDiagram\n  participant U as 操作者\n  U->>A: 質問\n",
		"too tall":         tallChain(40),
		"too wide":         "graph LR\n  A[Parse config] --> B[Resolve project] --> C[Connect MCP] --> D[Discover skills] --> E[Build prompt] --> F[Start TUI]\n",
	}
	for name, src := range cases {
		art, reason, ok := Render(src, 60)
		if ok {
			t.Errorf("%s: drawn unexpectedly:\n%s", name, art)
			continue
		}
		if len(reason) < 25 {
			t.Errorf("%s: reason too thin to act on: %q", name, reason)
		}
		if strings.HasSuffix(strings.TrimSpace(reason), ".") && !strings.Contains(reason, " ") {
			t.Errorf("%s: reason is not a sentence: %q", name, reason)
		}
	}
}
