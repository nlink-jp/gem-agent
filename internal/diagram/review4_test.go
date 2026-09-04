package diagram

import (
	"strings"
	"testing"
)

// Review round 4: a quoted label is literal — the parens inside
// `A["read_file(path)"]` are not a shape, and the drawing must show the
// label as written, not as the rewrite `read_file[path]`.
func TestQuotedLabelIsLiteral(t *testing.T) {
	out := rejoin(fence("graph LR\n  A[\"read_file(path)\"] --> B[Done]\n"))
	if strings.Contains(out, "graph LR") {
		t.Fatalf("not drawn:\n%s", out)
	}
	if !strings.Contains(out, "read_file(path)") {
		t.Errorf("quoted label not drawn as written:\n%s", out)
	}
	if strings.Contains(out, "read_file[path]") {
		t.Errorf("quoted label rewritten as a shape:\n%s", out)
	}
}

// Review round 4: `;` separates statements; `A-->B; B-->C` on one line
// drew a phantom node `B[b]; B` that passed both guards.
func TestSemicolonSeparatesStatements(t *testing.T) {
	out := rejoin(fence("graph LR\n  A[a]-->B[b]; B-->C[c]\n"))
	if strings.Contains(out, "graph LR") {
		t.Fatalf("not drawn:\n%s", out)
	}
	if strings.Contains(out, "; B") || strings.Contains(out, "B[b]") {
		t.Errorf("phantom node drawn:\n%s", out)
	}
	if arrowheads(out) != 2 {
		t.Errorf("arrowheads = %d, want 2:\n%s", arrowheads(out), out)
	}
}

// Review round 4: a node id that starts with a keyword is a node.
func TestKeywordPrefixedIdIsANode(t *testing.T) {
	out := rejoin(fence("graph LR\n  direction_check[Check] --> B[b]\n  subgraph_x[Sub] --> B\n"))
	if strings.Contains(out, "graph LR") {
		t.Fatalf("faithful drawing refused:\n%s", out)
	}
	if flowEdgeCount("direction_check[Check] --> B[b]") != 1 {
		t.Error("edge from a keyword-prefixed id not counted")
	}
}

// Review round 4: a bidirectional edge draws two heads and counts two.
func TestBidirectionalEdgeCountsTwoHeads(t *testing.T) {
	if n := flowEdgeCount("A[a] <--> B[b]"); n != 2 {
		t.Errorf("flowEdgeCount(<-->) = %d, want 2", n)
	}
	out := rejoin(fence("graph LR\n  A[a] <--> B[b]\n"))
	if strings.Contains(out, "graph LR") {
		t.Fatalf("bidirectional edge refused:\n%s", out)
	}
}
