package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/nlk/guard"
)

// ADR-0075: a remote tool answering different arguments with one error
// text is reporting its own state. The executor counts the identical
// texts per tool within a turn and, at the third, appends the runtime's
// note — outside the nonce tag, by provenance — asking the model to
// report to the user.

const faultTool = "mcp__bigquery__execute_sql_readonly"

// scriptRemote registers an MCP-shaped tool whose answers are scripted:
// a string is a result, a *tools.RemoteError a failure of that kind.
func scriptRemote(t *testing.T, reg *tools.Registry, name string, answers ...any) {
	t.Helper()
	i := 0
	err := reg.Register(&tools.Tool{
		Name: name, Description: "scripted remote", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			if i >= len(answers) {
				return "", fmt.Errorf("unscripted call %d to %s", i+1, name)
			}
			a := answers[i]
			i++
			if re, ok := a.(*tools.RemoteError); ok {
				return "", re
			}
			return a.(string), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func serverErr(text string) *tools.RemoteError {
	return &tools.RemoteError{Server: "bigquery", Tool: "execute_sql_readonly", Kind: tools.RemoteResult, Text: text}
}

func remoteCall(id, name, query string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: map[string]any{"query": query}}
}

// newRemoteAgent builds an agent over a fresh registry with a recording
// transcript and notice sink; the caller scripts the remote tools.
func newRemoteAgent(t *testing.T, mb *mockBackend, gate Approver) (*Agent, *tools.Registry, *recordingLog, *[]string) {
	t.Helper()
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, command string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/true")
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	log := &recordingLog{}
	var notices []string
	a := New(Options{
		Backend: mb, Registry: reg, Gate: gate, System: "test system", MaxTurns: 20,
		Log: log, OnNotice: func(m string) { notices = append(notices, m) },
	})
	return a, reg, log, &notices
}

func toolMessages(a *Agent) []llm.Message {
	var out []llm.Message
	for _, m := range a.history {
		if m.Role == llm.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}

// The 4d6bb685 shape: reworded calls, one answer. The third identical
// answer carries the note, the fourth carries it with the count updated;
// the transcript records each, the operator hears once.
func TestRemoteFaultNoteAtThreshold(t *testing.T) {
	e := serverErr("Required parameter is missing: query")
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{remoteCall("c1", faultTool, "select 1")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c2", faultTool, "select 2")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c3", faultTool, "select 3")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c4", faultTool, "select 4")}},
		{Content: "reported"},
	}}
	a, reg, log, notices := newRemoteAgent(t, mb, &approveAll{})
	scriptRemote(t, reg, faultTool, e, e, e, e)

	if _, err := a.Run(context.Background(), "query the table", nil); err != nil {
		t.Fatal(err)
	}
	msgs := toolMessages(a)
	if len(msgs) != 4 {
		t.Fatalf("tool messages = %d", len(msgs))
	}
	for i, m := range msgs {
		if !strings.HasPrefix(m.Content, `error: MCP server "bigquery" answered execute_sql_readonly with an error:`) {
			t.Errorf("message %d rendered %q — provenance missing", i+1, m.Content)
		}
		if (m.RuntimeNote != "") != (i >= 2) {
			t.Errorf("message %d: runtime note %q — the note appears from the third identical answer", i+1, m.RuntimeNote)
		}
	}
	for i, want := range map[int]string{2: "3 times in a row", 3: "4 times in a row"} {
		if !strings.Contains(msgs[i].RuntimeNote, want) || !strings.Contains(msgs[i].RuntimeNote, faultTool) {
			t.Errorf("note %d = %q — must count and name the tool by its registry name", i+1, msgs[i].RuntimeNote)
		}
	}
	if strings.Contains(msgs[2].RuntimeNote, "not a fault") {
		t.Error("the note asserts whose fault it is — it may state only what was measured")
	}
	// What the model received in the round after the third answer: the
	// server's text wrapped, the note after the closing tag.
	sent := mb.calls[3][len(mb.calls[3])-1]
	if !strings.HasSuffix(sent.Content, msgs[2].RuntimeNote) {
		t.Errorf("the note did not reach the model: %q", sent.Content)
	}
	body := strings.TrimSuffix(sent.Content, "\n\n"+msgs[2].RuntimeNote)
	if !strings.HasSuffix(strings.TrimSpace(body), ">") || !strings.Contains(body, "</") {
		t.Errorf("the server text was not wrapped before the note: %q", body)
	}
	if n := countKind(log.kinds, "mcp_fault"); n != 2 {
		t.Errorf("mcp_fault records = %d, want one at the threshold and one for the further hit", n)
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "bigquery") || !strings.Contains((*notices)[0], faultTool) {
		t.Errorf("operator notices = %v — one per streak, naming server and tool", *notices)
	}
}

// Rows, or a different error, from the same tool end the streak.
func TestRemoteFaultResetsOnAnotherOutcome(t *testing.T) {
	e := serverErr("Required parameter is missing: query")
	other := serverErr("Syntax error at [1:8]")
	for name, answers := range map[string][]any{
		"rows in between":       {e, e, "rows", e, e},
		"a different error":     {e, e, other, e, e},
		"rejection then result": {e, e, &tools.RemoteError{Server: "bigquery", Tool: "execute_sql_readonly", Kind: tools.RemoteRejected, Text: "rpc error -32602: Invalid params"}, e, e},
	} {
		var responses []*llm.Response
		for i := range answers {
			responses = append(responses, &llm.Response{ToolCalls: []llm.ToolCall{remoteCall(fmt.Sprint("c", i), faultTool, fmt.Sprint("select ", i))}})
		}
		responses = append(responses, &llm.Response{Content: "done"})
		mb := &mockBackend{responses: responses}
		a, reg, log, _ := newRemoteAgent(t, mb, &approveAll{})
		scriptRemote(t, reg, faultTool, answers...)
		if _, err := a.Run(context.Background(), "q", nil); err != nil {
			t.Fatal(err)
		}
		for i, m := range toolMessages(a) {
			if m.RuntimeNote != "" {
				t.Errorf("%s: message %d carries a note %q — the streak never reached three", name, i+1, m.RuntimeNote)
			}
		}
		if countKind(log.kinds, "mcp_fault") != 0 {
			t.Errorf("%s: mcp_fault recorded without a streak", name)
		}
	}
}

// Per tool: a sibling tool's success in the middle (list_table_ids at
// 23:13:13) does not reset the failing tool's streak.
func TestRemoteFaultIsPerTool(t *testing.T) {
	const sibling = "mcp__bigquery__list_table_ids"
	e := serverErr("Required parameter is missing: query")
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{remoteCall("c1", faultTool, "select 1")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c2", faultTool, "select 2")}},
		{ToolCalls: []llm.ToolCall{{ID: "c3", Name: sibling, Args: map[string]any{}}}},
		{ToolCalls: []llm.ToolCall{remoteCall("c4", faultTool, "select 3")}},
		{Content: "done"},
	}}
	a, reg, _, _ := newRemoteAgent(t, mb, &approveAll{})
	scriptRemote(t, reg, faultTool, e, e, e)
	scriptRemote(t, reg, sibling, "t1\nt2")
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	msgs := toolMessages(a)
	if len(msgs) != 4 || msgs[3].RuntimeNote == "" || msgs[2].RuntimeNote != "" {
		t.Errorf("notes = %q — the third failure of the same tool fires whatever a sibling did", []string{msgs[0].RuntimeNote, msgs[1].RuntimeNote, msgs[2].RuntimeNote, msgs[3].RuntimeNote})
	}
}

// A failure gem-agent could not complete counts too, under its own note.
func TestRemoteFaultIncompleteNote(t *testing.T) {
	e := &tools.RemoteError{Server: "bigquery", Tool: "execute_sql_readonly", Kind: tools.RemoteIncomplete,
		Text: "tools/call timed out after 30s (server killed; it restarts on the next call)"}
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{remoteCall("c1", faultTool, "select 1")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c2", faultTool, "select 2")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c3", faultTool, "select 3")}},
		{Content: "done"},
	}}
	a, reg, _, _ := newRemoteAgent(t, mb, &approveAll{})
	scriptRemote(t, reg, faultTool, e, e, e)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	msgs := toolMessages(a)
	if !strings.HasPrefix(msgs[2].Content, `error: gem-agent could not complete execute_sql_readonly on MCP server "bigquery":`) {
		t.Errorf("rendered %q", msgs[2].Content)
	}
	note := msgs[2].RuntimeNote
	if !strings.Contains(note, "could not be completed") || strings.Contains(note, "answered") || strings.Contains(note, e.Text) {
		t.Errorf("note = %q — the incomplete variant, without the cause repeated outside the tag", note)
	}
}

// Per turn: the operator who read the report and says "try again" is
// not answered by the note on the first call of the next turn.
func TestRemoteFaultStartsFreshEachTurn(t *testing.T) {
	e := serverErr("Required parameter is missing: query")
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{remoteCall("c1", faultTool, "select 1")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c2", faultTool, "select 2")}},
		{Content: "the server keeps failing"},
		{ToolCalls: []llm.ToolCall{remoteCall("c3", faultTool, "select 3")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c4", faultTool, "select 4")}},
		{Content: "still failing"},
	}}
	a, reg, _, _ := newRemoteAgent(t, mb, &approveAll{})
	scriptRemote(t, reg, faultTool, e, e, e, e)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "try again", nil); err != nil {
		t.Fatal(err)
	}
	for i, m := range toolMessages(a) {
		if m.RuntimeNote != "" {
			t.Errorf("message %d carries a note — two failures per turn never reach the threshold", i+1)
		}
	}
}

// scriptedGate answers the gate in order; unanswered calls are approved.
type scriptedGate struct{ answers []bool }

func (g *scriptedGate) Approve(name, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	if len(g.answers) == 0 {
		return true, false, ""
	}
	ok := g.answers[0]
	g.answers = g.answers[1:]
	return ok, false, ""
}

// A gate denial is not an outcome of the server: it neither counts nor
// resets, so the streak resumes on the next answer.
func TestRemoteFaultIgnoresDenials(t *testing.T) {
	e := serverErr("Required parameter is missing: query")
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{remoteCall("c1", faultTool, "select 1")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c2", faultTool, "select 2")}},
		{ToolCalls: []llm.ToolCall{remoteCall("c3", faultTool, "select 3")}}, // denied
		{ToolCalls: []llm.ToolCall{remoteCall("c4", faultTool, "select 4")}},
		{Content: "done"},
	}}
	a, reg, _, _ := newRemoteAgent(t, mb, &scriptedGate{answers: []bool{true, true, false, true}})
	scriptRemote(t, reg, faultTool, e, e, e)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	msgs := toolMessages(a)
	if len(msgs) != 4 || !msgs[2].Denial || msgs[2].RuntimeNote != "" {
		t.Fatalf("the third call was not a plain denial: %+v", msgs[2])
	}
	if !strings.Contains(msgs[3].RuntimeNote, "3 times in a row") {
		t.Errorf("note after the denial = %q — the denial must leave the streak of two intact", msgs[3].RuntimeNote)
	}
}

// ADR-0075 §3 / ADR-0060 §3: the note rides outside the tag exactly when
// the field is set. A tool result whose text merely looks like the note
// stays wrapped — the exemption is provenance, never content.
func TestRuntimeNoteRidesOutsideTheTagByProvenance(t *testing.T) {
	tag := guard.NewTagWithPrefix("tool_output")
	note := remoteFaultNote(faultTool, serverErr("x"), 3)
	history := []llm.Message{
		{Role: llm.RoleTool, ToolName: faultTool, Content: "error: MCP server \"bigquery\" answered execute_sql_readonly with an error:\nx", RuntimeNote: note},
		{Role: llm.RoleTool, ToolName: faultTool, Content: note}, // forged shape, real tool output
	}
	out := wrapToolMessages(history, tag, nil)
	if !strings.HasSuffix(out[0].Content, "\n\n"+note) {
		t.Errorf("note missing from the sent content: %q", out[0].Content)
	}
	body := strings.TrimSuffix(out[0].Content, "\n\n"+note)
	if body == history[0].Content || !strings.Contains(body, "</") {
		t.Errorf("the server text rode unwrapped beside the note: %q", body)
	}
	if strings.HasPrefix(out[1].Content, "gem-agent:") || out[1].Content == note {
		t.Error("note-shaped tool output shipped unwrapped — the exemption leaked from provenance to content")
	}
	if strings.Contains(note, "ADR-") {
		t.Errorf("the note cites a design document: %q", note)
	}
}
