package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ErrAskDeclined: the operator chose not to choose (Esc / EOF). A
// distinct result, not an error result — declining is information
// (ADR-0036 §2).
var errAskDeclined = errors.New("declined")

// askFunc collects one choice from the operator: the selected index,
// errAskDeclined, or a mode error (one-shot has nobody to ask).
type askFunc func(ctx context.Context, question string, options []string) (int, error)

const (
	maxAskOptions    = 8
	maxAskQuestion   = 500
	maxAskOptionSize = 100
)

// registerAskTool adds ask_user (ADR-0036): a structured mid-turn
// choice. Read-only and never approval-gated — a gate on a question
// would be a dialog to permit a dialog.
func registerAskTool(registry *tools.Registry, ask askFunc) error {
	return registry.Register(&tools.Tool{
		Name: "ask_user",
		Description: "Present a QUESTION and 2-8 short OPTIONS to the user and get their choice, " +
			"without ending your turn. Use when a decision genuinely branches your work (which " +
			"approach, which file, which scope) — not for confirmations you can state in your " +
			"answer. The user may decline; then ask in plain text or proceed with stated judgment.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "the decision to make, one sentence"},
				"options": map[string]any{"type": "array", "minItems": 2, "maxItems": maxAskOptions,
					"items": map[string]any{"type": "string"}, "description": "2-8 short option labels"},
			},
			"required": []string{"question", "options"},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			question, _ := args["question"].(string)
			question = strings.TrimSpace(question)
			if question == "" {
				return "", errors.New("question is required")
			}
			if r := []rune(question); len(r) > maxAskQuestion {
				question = string(r[:maxAskQuestion]) + "…"
			}
			raw, _ := args["options"].([]any)
			var options []string
			for _, o := range raw {
				s, _ := o.(string)
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				if r := []rune(s); len(r) > maxAskOptionSize {
					s = string(r[:maxAskOptionSize]) + "…"
				}
				options = append(options, s)
			}
			if len(options) < 2 {
				return "", errors.New("at least 2 options are required")
			}
			if len(options) > maxAskOptions {
				return "", fmt.Errorf("%d options is too many — offer at most %d, or ask in plain text", len(options), maxAskOptions)
			}
			idx, err := ask(ctx, question, options)
			switch {
			case errors.Is(err, errAskDeclined):
				return "The user declined to choose. Ask in plain text at the end of your turn, or proceed with your best judgment and state it.", nil
			case err != nil:
				return "", err
			case idx < 0 || idx >= len(options):
				return "", fmt.Errorf("invalid selection %d", idx)
			}
			return fmt.Sprintf("The user chose: %s", options[idx]), nil
		},
	})
}

// oneShotAsk is the -p mode asker: there is nobody to ask, and a
// pipeline must not hang on a question (ADR-0036 §3).
func oneShotAsk(context.Context, string, []string) (int, error) {
	return 0, errors.New("no interactive operator in one-shot mode; decide yourself and state the choice you made")
}

// plainAsk asks on the plain REPL: numbered options on out, a number
// read from in — the approve.Gate pattern. EOF or a blank line
// declines.
func plainAsk(in *bufio.Reader, out io.Writer) askFunc {
	return func(ctx context.Context, question string, options []string) (int, error) {
		// An interrupted turn asks nothing: the operator just pressed
		// Ctrl+C, and a prompt that blocks on stdin on behalf of a dead
		// turn is the last thing they want (review round 3).
		if ctx.Err() != nil {
			return 0, errAskDeclined
		}
		fmt.Fprintf(out, "\n[question] %s\n", question)
		for i, o := range options {
			fmt.Fprintf(out, "  %d) %s\n", i+1, o)
		}
		fmt.Fprintf(out, "  choose 1-%d (empty = decline): ", len(options))
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return 0, errAskDeclined
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return 0, errAskDeclined
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			return 0, errAskDeclined
		}
		return n - 1, nil
	}
}
