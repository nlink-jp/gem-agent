package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// ErrPromptBlocked is returned by Run when an operator user_prompt_submit
// hook refused the prompt (ADR-0069 §3). Nothing was recorded: the
// prompt never reached the history, the transcript, or the model —
// Claude Code's "block and erase" semantics, measured.
var ErrPromptBlocked = errors.New("prompt blocked by a user_prompt_submit hook")

// PromptHook is the operator's user_prompt_submit hooks, evaluated on
// the typed input before a turn starts (ADR-0069). extra is context to
// place beside the input; block refuses the turn with reason.
type PromptHook func(ctx context.Context, input string) (extra string, block bool, reason string)

// HookAttachmentKind labels hook-injected context in the transcript and
// in the model's view: it rides the data lane exactly like piped stdin
// (ADR-0055) — quoted as data inside the turn's nonce tag, beside the
// typed text and never inside it. The input string is the risk
// evaluator's trusted instruction channel (ADR-0038/0054) precisely
// because only the operator writes it; a hook's output is produced by
// code running over whatever that code read, so it must not share that
// standing.
const HookAttachmentKind = "hook"

// runPromptHook consults the prompt hook, if any, and queues its
// context for the message Run is about to build. It returns the block
// error when the hook refused the prompt.
func (a *Agent) runPromptHook(ctx context.Context, input string) error {
	if a.promptHook == nil {
		return nil
	}
	extra, block, reason := a.promptHook(ctx, input)
	if block {
		if reason == "" {
			reason = "no reason given"
		}
		return fmt.Errorf("%w: %s", ErrPromptBlocked, reason)
	}
	if extra == "" {
		return nil
	}
	a.pendingAtts = append(a.pendingAtts, llm.Attachment{
		Ref: "user_prompt_submit", Kind: HookAttachmentKind, Content: extra,
	})
	if a.onNotice != nil {
		a.onNotice(fmt.Sprintf("user_prompt_submit hook attached %d bytes of context as data", len(extra)))
	}
	return nil
}
