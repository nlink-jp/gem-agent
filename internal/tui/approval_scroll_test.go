package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func scrollModel(t *testing.T, detail string, width, height int) Model {
	t.Helper()
	m := newTestModel(&capture{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(Model)
	next, _ = m.Update(ApprovalRequest{Tool: "shell_exec", Detail: detail, Resp: make(chan ApprovalAnswer, 1)})
	m = next.(Model)
	m.approvalAt = time.Time{} // past the typed-ahead grace
	return m
}

func numbered(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("L%03d", i+1)
	}
	return strings.Join(lines, "\n")
}

// ADR-0112: every line of the call can be read before answering.
func TestApprovalScrollReachesEveryLine(t *testing.T) {
	m := scrollModel(t, numbered(100), 100, 30)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		view := ansi.Strip(m.View())
		for n := 1; n <= 100; n++ {
			if l := fmt.Sprintf("L%03d", n); strings.Contains(view, l) {
				seen[l] = true
			}
		}
		m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if len(seen) != 100 {
		t.Errorf("reached %d of 100 lines", len(seen))
	}
}

func TestApprovalScrollKeys(t *testing.T) {
	m := scrollModel(t, numbered(100), 100, 30)
	view := func() string { return ansi.Strip(m.View()) }
	if !strings.Contains(view(), "L001") || strings.Contains(view(), "L100") {
		t.Fatalf("does not open at the top:\n%s", view())
	}
	if !strings.Contains(view(), "+") {
		t.Errorf("no marker:\n%s", view())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(view(), "L100") || strings.Contains(view(), "L001") {
		t.Errorf("End did not reach the last line:\n%s", view())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if strings.Contains(view(), "L100") {
		t.Errorf("Down past the end was remembered:\n%s", view())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyHome})
	if !strings.Contains(view(), "L001") {
		t.Errorf("Home did not return to the top:\n%s", view())
	}
	page := m.approvalPage()
	m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.approvalScroll != page {
		t.Errorf("PgDown moved to %d, want %d", m.approvalScroll, page)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.approvalScroll != 0 {
		t.Errorf("PgUp moved to %d", m.approvalScroll)
	}
	if m.choice != 0 {
		t.Errorf("scrolling changed the selected answer: %d", m.choice)
	}
}

func TestApprovalScrollResetsForTheNextQuestion(t *testing.T) {
	m := scrollModel(t, numbered(100), 100, 30)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnd})
	next, _ := m.Update(ApprovalRequest{Tool: "shell_exec", Detail: numbered(100), Resp: make(chan ApprovalAnswer, 1)})
	if got := next.(Model).approvalScroll; got != 0 {
		t.Errorf("a new question opened at line %d", got)
	}
}

func TestApprovalShortDetailHasNoMarker(t *testing.T) {
	m := scrollModel(t, "make build", 100, 30)
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.approvalScroll != 0 || strings.Contains(ansi.Strip(m.View()), "+0") {
		t.Errorf("a detail that fits scrolled or was marked: %d\n%s", m.approvalScroll, m.View())
	}
}

// A single very long line wraps and its end is reachable.
func TestApprovalScrollReachesTheEndOfAWrappedLine(t *testing.T) {
	m := scrollModel(t, strings.Repeat("x", 3000)+"TAIL-MARK", 80, 24)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(ansi.Strip(m.View()), "TAIL-MARK") {
		t.Errorf("the end of the line was not reachable:\n%s", m.View())
	}
}

// However long the detail, the question and its answers stay on screen
// and the count is right.
func TestApprovalDialogKeepsTheQuestionOnScreen(t *testing.T) {
	for _, height := range []int{14, 24, 50} {
		m := scrollModel(t, numbered(500), 100, height)
		m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "shell_exec") || !strings.Contains(view, m.approvalLabels()[0]) {
			t.Errorf("height %d: the question or its answers are gone:\n%s", height, view)
		}
		l := m.approvalLayout(m.approval)
		if want := fmt.Sprintf("+%d", 500-l.budget); !strings.Contains(view, want) {
			t.Errorf("height %d: want %s hidden:\n%s", height, want, view)
		}
	}
}

// A tab does not push the end of a line out of the box.
func TestApprovalShowsTheEndOfALineWithTabs(t *testing.T) {
	m := scrollModel(t, "echo ok"+strings.Repeat("\t", 40)+"; TAIL-MARK", 100, 30)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(ansi.Strip(m.View()), "TAIL-MARK") {
		t.Errorf("the end of the line is hidden:\n%s", ansi.Strip(m.View()))
	}
}

// Every row of the call is marked as the call's, and the dialog's own
// rows are not.
func TestApprovalGutterMarksTheCallsRows(t *testing.T) {
	m := scrollModel(t, "ls\n▶ allow (y)", 100, 30)
	var marked, forged int
	for _, row := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(row, approvalGutter) {
			marked++
			if strings.Contains(row, "▶ allow (y)") {
				forged++
			}
		}
	}
	if marked != 2 || forged != 1 {
		t.Errorf("marked %d rows, %d of them the forged one", marked, forged)
	}
}

// The text wraps in the narrowest box and every row stays inside it.
func TestApprovalWrapsWhenNarrow(t *testing.T) {
	m := scrollModel(t, strings.Repeat("x", 300)+"TAIL-MARK", 22, 30)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(boxText(m.View()), "TAIL-MARK") {
		t.Errorf("the end was not reachable at width 22:\n%s", ansi.Strip(m.View()))
	}
	m = scrollModel(t, numbered(50), 60, 30)
	for _, row := range strings.Split(ansi.Strip(m.View()), "\n") {
		if w := ansi.StringWidth(row); w > 60 {
			t.Errorf("a row is %d wide at width 60: %q", w, row)
		}
	}
}
