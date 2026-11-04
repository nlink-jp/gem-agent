# ADR-0104: when a turn waits on the operator, ring the terminal — stopping nothing, timed like Claude Code's permission prompt

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-11-04) |
| Date | 2026-11-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A third party's usage summary (agent-report): approval prompts left waiting for minutes, many times a month |
| Relates to | ADR-0044 (pre-tool hooks; the hook event this ADR does not add), ADR-0069 (measure the real contract first), ADR-0093 (what reaches the terminal), ADR-0080 (the read-only ceiling), ADR-0096 §7 (RT7 rejected), ADR-0036 (ask_user), ADR-0040 (round-limit intervention) |

## Context

A gem-agent turn sometimes stops for the operator's answer: the approval prompt, the approval of an MCP call under the read-only ceiling, the question whether to lift that ceiling, `ask_user`, and the round-limit intervention (ADR-0040). The turn waits, and nothing tells anyone outside the screen. The runtime writes neither BEL nor OSC 9 to the terminal (read in the source; the only OSC it writes is iTerm2's image sequence).

A `pre_tool_use` hook (ADR-0044) can already make a sound, but at the wrong moment: it runs before the approval ladder, so it fires for every call — including the ones the allowlist or auto-approve answer at once — and it cannot know whether a dialog opened or whether the operator is already looking at it.

In 2026-11 a third party summarised a month of use with agent-report v0.2.0 (measured, on that operator's machine; the figures are theirs and are not reproduced here):

- The typical wait at the approval prompt was a matter of seconds: the operator is usually at the screen. Yet more than 40 times in the month a prompt sat for several minutes, and the longest waits ran to hours. Those are waits a notice would shorten.
- The longest MCP calls, measured from call to result, were long only because of that waiting; their execution itself took about a minute at most.
- The round-limit intervention and `ask_user` also came up. Their waits are not in the summary (they carry no approval record).

The same summary for lagent showed no such long waits.

Claude Code handles this moment with two mechanisms: a built-in notification (`preferredNotifChannel`, default `auto`) and a `Notification` hook that can neither block nor change anything. When they fire and what they carry were measured on the real thing (see "Measured").

What RT7 asked for, and ADR-0096 §7 declined, was a gate that checks the answer at the end of a turn and **stops** it. This ADR stops nothing. It only tells; the course of the turn, the approval decisions and the records are unchanged.

## Decision

### 1. Notify only when a running turn in the TUI has stopped for the operator's answer

The dialogs that notify, all in the TUI:

- the approval prompt (a call answered by the allowlist or by auto-approve opens no dialog and is not notified);
- the approval of an MCP call under the read-only ceiling (`ApproveOnce`, ADR-0080);
- the question whether to lift the ceiling (`ApproveLift`);
- the question dialog: `ask_user`, the round-limit intervention, and the `/riskbook learn` confirmation. The last follows a call to the summary model, long enough for the operator to step away, and it uses the same dialog, so it is treated the same.

**Timing**, as measured in Claude Code's `permission_prompt`: from the moment a dialog opens it is checked every six seconds, and the notice goes out at the first check with no keystroke in the six seconds before it. A keystroke does not restart a timer; it makes the next check pass. An answer cancels it. A dialog left open gets one notice, never a second. An operator at the screen is therefore not pinged.

**Not notified:**

- **The plain REPL.** It is the fallback when stdin or stdout is not a terminal. Its prompts read whole lines in cooked mode, so a keystroke before Enter cannot be seen and the timing above cannot be built; and when stdin is a pipe, what is being waited on is not a person.
- **A one-shot run (`-p`).** Nothing waits there: approvals are refused without asking (`denyGate`), `ask_user` returns an error, and the round-limit intervention has no callback.
- **A turn that has ended** (decided by the operator, 2026-11-04). The wait at the empty input line is not a turn stopped midway, and no number measures it; see Alternatives.

### 2. The notice is handed to the terminal: `[tui].notify`

Notifying is the terminal application's job: the terminal holds the operating system's notification permission, and gem-agent is not an app bundle. `[tui].notify` chooses:

- `"bell"` — write one BEL.
- `"osc9"` — write an OSC 9 desktop notification with a fixed body, `ESC ] 9 ; gem-agent needs your answer ESC \`, ended by ST, then a separate BEL. Where OSC 9 is not understood it is dropped without being shown (Terminal.app, and tmux, which does not pass it on), so the BEL is what still arrives.
- `"off"` — do nothing.

**The default is `"bell"`** (decided by the operator, 2026-11-04). An operator who configures nothing is reached. BEL is the control every terminal is built to take, and the one tmux was measured to pass on; iTerm2 marked it in the window title. The six-second check keeps it from an operator at the screen. This default is louder than Claude Code's: under tmux, Claude Code's `auto` wrote nothing (measured), and its documentation says it rings Terminal.app's bell only when that terminal's audible bell is off. In Terminal.app with its audible bell on, gem-agent's default may beep; that reaction was not observed. `"off"` restores the behaviour before this ADR.

The key lives with the other `[tui]` keys: global configuration only, as `[tui]` already is (a project's settings cannot set it), shown with its source in `/settings`, and listed in `config.example.toml`.

**How the bytes reach the terminal.** The notice is constant bytes the runtime writes itself; nothing in it comes from outside, so ADR-0093 — which governs text from outside the runtime — neither covers it nor needs to neutralise it. It is written once, through the TUI's terminal writer, as a write of its own serialised with the renderer's flushes: never inside a flush, and never in `View()`, which repaints.

### 3. No new record or telemetry

A notice is not written to the transcript. The operator can see its effect by comparing agent-report's `operator_wait_sec` before and after; records are not added for the sake of measuring.

## Measured (2026-11-04)

As in ADR-0069, the contract was taken from the real thing, not the documentation.

**Claude Code 2.1.311**, interactive, in the default permission mode, inside a private tmux server, with a hook registered through `--settings` that wrote down the input and the time of every `Notification`, `PermissionRequest`, `PreToolUse`, `Stop` and `UserPromptSubmit`:

- **Approval prompt.** `permission_prompt` arrived 6.0 s after `PermissionRequest`, with `message` `"Claude needs your permission"`. The fields were `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `message` and `notification_type`, plus `prompt_id` and `scratchpad_dir`, which the documentation does not list. There was no `title`, which the documentation calls optional.
- **Keystrokes.** With keys pressed 4.3 s and 8.3 s after the prompt opened, the notice came at 18.0 s — at the third six-second check, the first with no keystroke in the six seconds before it.
- **`AskUserQuestion`.** It goes through `PermissionRequest` like any tool, and the same `permission_prompt` with the same `message` arrived 6.0 s later. Left open for 75 s, it got that one notice and nothing else.
- **After a turn ends.** `idle_prompt`, with `message` `"Claude is waiting for your input"`, arrived 60.1 s after `Stop`, in both of two trials, once each; nothing followed in the next 80 s.
- **Built-in notification.** Inside tmux, Claude Code wrote no BEL and no OSC 9, 99 or 777 to the terminal for any of these (the only OSC was the window title).

The hook payload is recorded here for a later ADR that may add a notification hook (see Alternatives).

**Terminals**, sent BEL, OSC 9 (ended by BEL and by ST) and OSC 777 (the `tools/escprobe` method: the screen read back, and what a client receives recorded byte for byte):

| Terminal | BEL | OSC 9 | Body shown as text |
|---|---|---|---|
| tmux 3.8 (defaults: `bell-action any`, `allow-passthrough off`) | passed to the outer terminal | swallowed, not passed on (OSC 777 too) | no |
| Terminal.app | not observed | dropped; no notification from a window in the background | no |
| iTerm2 (profile notifications on) | 🔔 in the window title | from a window in the background, a notification `Session <name>: <body>` | no |
| kitty 0.50.0 | not observed | the first one made macOS register kitty for notifications (its first-use notice appeared); the notification itself was not seen | no |

No terminal printed the body of an OSC it did not act on. So `"osc9"` is safe to write everywhere, reaches a desktop notification only in some terminals, and under tmux comes down to its BEL.

## Consequences

- A turn stopped at a dialog in the TUI reaches an operator who has stepped away. How quickly they answer after that is up to them; this ADR does not promise it.
- **Behaviour changes for operators who configure nothing:** after this ships, a TUI dialog left for six seconds rings the terminal bell. The release notes say so and name `[tui].notify = "off"`.
- Each open dialog uses one ticker. With `notify = "off"`, nothing runs.
- The plain REPL and one-shot runs behave as before.
- lagent is not bound. The same summary gives no grounds for it there; whether to port this is lagent's decision.

## Alternatives considered

- **gem-agent posts the operating system's notification itself** (`osascript`'s `display notification`, UserNotifications) — rejected: the terminal application owns the permission and the presentation. A CLI posting by another route would show the operator two streams of notices from different sources.
- **A notification hook** (`[[hooks.notification]]` on Claude Code's `Notification` input) — not adopted now. ADR-0044 §1 admits a hook event on demonstrated demand; the summary shows long waits, which the built-in notice answers, not a demand for a hook. A later ADR can add one in the measured shape above; it would have to settle what an asynchronous hook does when the dialog is answered, at session end and at `/clear`, and how its failures are reported. Claude Code's `terminalSequence` output belongs to that question too.
- **Using the existing `pre_tool_use` hook** — not enough: it fires before the approval ladder, for every call, with no knowledge of a dialog or of the operator's keystrokes.
- **Notifying after a turn ends** (Claude Code's `idle_prompt`, measured as one notice 60 s after the turn) — not adopted now: telling the operator that a long autonomous turn finished may be worth it, but no number measures that wait. agent-report does not summarise it today, so it would take a measurement of its own first.
- **Notifying in the plain REPL** — not adopted: see §1.
- **Repeating or escalating the notice** — rejected: what was measured is that waits run long, not that one notice is not enough.
- **Checking or stopping at the end of a turn** (RT7) — still rejected by ADR-0096 §7. This ADR has no mechanism that stops anything.

## References

- ADR-0044 (the hook mechanism and §1's demand rule), ADR-0069 (measure the real contract first), ADR-0093 (what reaches the terminal), ADR-0080 (the read-only ceiling)
- ADR-0096 §7 (RT7), ADR-0036 (ask_user), ADR-0040 (round-limit intervention)
- Claude Code documentation: hooks (Notification), settings (`preferredNotifChannel`), terminal-config (terminal bell and notifications)
- agent-report v0.2.0 (`run_sec`, `operator_wait_sec`)
- `tools/escprobe` (the terminal measurement method)
