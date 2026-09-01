# ADR-0064: a positional argument is the first interactive turn

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-02 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "the first move is decided, but from the next turn on I want to work interactively" — a quietly recurring shape of session |

## Context

The two existing entry points each miss this shape. `-p` runs one turn
and exits: scripted, but the session is over before the human can
steer. Interactive mode steers, but turn 1 must be typed by hand even
when it was decided before launch. There is no way to hand the session
its opening move and then take the keyboard.

## Decision

### 1. One optional positional argument, submitted as turn 1

```
gem-agent "first message"          # TUI starts, the message runs as turn 1
gem-agent -c "pick up where we left off"
gem-agent --auto "run the tests and fix what breaks"
```

The message is submitted once the banner has printed, then the session
is ordinary interactive gem-agent. `--continue`/`--resume`/`--auto`
compose by doing nothing special: the argument is just the first turn
of whatever session the other flags select. This is the same surface
shape as `claude "prompt"` — the ecosystem-compatibility requirement
cuts in gem-agent's favor here.

### 2. The argument travels the exact typed path

In the TUI it enters the same `submit()` the Enter key uses: `!`
shell escape, slash commands, `/skill` expansion, and `@` mentions all
work, and the message is echoed as `> line` so the scrollback reads
like a session that started by typing. argv is operator input — the
same trust as the keyboard, which is precisely the premise the mention
expander requires (ADR-0041 §high-1).

The trigger fires once: it is cleared when the first window-size
report queues it, so a later resize cannot resubmit the message.

### 3. Combining `-p` with the argument is an error

Two first-turn channels selecting different session shapes (answer-
and-exit vs. start-and-stay) is an ambiguity; it is refused with a
sentence naming both meanings, not resolved by precedence.

### 4. Piped stdin keeps its meaning; the pipe fallback runs the argument first

ADR-0055's boundary stands: piped stdin is never prompt text. In the
plain-REPL pipe fallback the argument runs before the first stdin read
— echoed as `> line` — and stdin lines follow as they always have.
The attractive combination "stdin as data attachment + argument as
first turn + keyboard on /dev/tty" needs a tty-reopen plumbing layer
and is deliberately out of scope; it would be its own ADR.

## Consequences

- No new mode. One argument, three lines of decision: run it first,
  through the typed path, never beside `-p`.
- A subcommand name still wins over a first message (`gem-agent
  sessions` lists sessions); a first message that collides with a
  subcommand name must be phrased differently — accepted, same as the
  ecosystem precedent.
- The transcript records an ordinary user message, so `--continue`
  and `--resume` need no awareness of how turn 1 arrived.
