# Configuration

Install, the config file, precedence, and the provider-side behaviours
(content filters, endpoints) worth knowing about.

## Install

```sh
brew install nlink-jp/tap/gem-agent
```

or download a signed, notarized archive from the
[releases page](https://github.com/nlink-jp/gem-agent/releases)
(macOS arm64). To build from source: `make build` (outputs
`dist/gem-agent`).

Requirements: macOS (Apple Silicon), a Google Cloud project with
Vertex AI enabled, and Application Default Credentials
(`gcloud auth application-default login`) with `roles/aiplatform.user`.

## The config file

Start from the template in the repository:

```sh
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml
cp mcp.example.json    ~/.config/gem-agent/mcp.json   # optional, MCP servers
```

`~/.config/gem-agent/config.toml`:

```toml
[gcp]
project  = "your-project-id"
location = "global"        # default; Gemini 3 models are global-endpoint-only
# bucket = "your-bucket"   # optional; routes audio/video through GCS (ADR-0027)

[model]
name = "<gemini model id>"
# summary = "<light model>" # optional; summarize_file / web_fetch digests (ADR-0014)
# context_window = 1048576  # optional; exact window for the footer and compaction
# thinking = "high"         # optional; Gemini 3 thinking level: minimal|low|medium|high
#                           # (unset = model default; supported levels are model-dependent;
#                           #  summarize model unaffected — ADR-0025)
# safety = "default"        # default | relaxed | off (see Content filters)

[sandbox]
enabled = true             # default

[agent]
max_turns = 50             # default; an intervention checkpoint, extensions up to 3× (ADR-0040)
shell_timeout_sec = 120    # default
auto_approve = false       # default; start sessions in auto-approve mode
auto_compact = true        # default; summarise older history near the window
compact_at_pct = 80        # default; share of the window that triggers it

[mcp]
enabled = true             # default; false disables ALL MCP servers
call_timeout_sec = 60      # default

[tui]
theme = "auto"             # auto | dark | light | plain
language = "auto"          # auto | ja | en (ADR-0029 — see interface.md)
show_thoughts = true       # live thought summaries in the TUI (ADR-0033)

[telemetry]
enabled = false            # audit logging (ADR-0035) — see below
backend = "gcp"            # gcp (Cloud Logging, default) | otlp-grpc | otlp-http
endpoint = "localhost:4317" # otlp-* only
insecure = false            # otlp-* only
# headers_file = "~/.config/gem-agent/auth.json"  # otlp-* auth headers, mode 0600

[approval]
# trusted_projects = ["/path/to/repo"]  # full project trust — see the warning below
[approval.tools]
# "mcp__tor-exit-lookup__*" = "never"   # per-tool policy — see approval.md
```

Precedence: flags (`--model`) > `GEMAGENT_*` > `GOOGLE_CLOUD_*` >
config file > defaults. Unknown keys in the file are rejected (strict
decode), and invalid values fail at startup with the key named — a
typo must not surface as a confusing runtime failure far from its
cause.

`/settings` shows every setting with the layer its value came from,
live. Machine-persisted decisions (policy edits, project trust) live in
`~/.config/gem-agent/policy.toml`, which gem-agent owns; your
hand-written `config.toml` is never rewritten.

`~/.config/gem-agent/risk-rules.md` is the hand-written base layer of
the risk rulebook (ADR-0050): free-prose guidance the auto-mode risk
reviewer reads on every call it judges. gem-agent never writes this
file. See [approval](approval.md) for what the rulebook can and
cannot do — it biases judgment and never skips a gate.

`trusted_projects` grants **full project trust**, not just approval
relaxation. Listing a directory there does four things at once: its
`.gem-agent.toml` may remove approvals, its `.mcp.json` servers are
spawned, its `.claude/skills` are discovered, and its instruction files
are read. Add a repository only when you would also answer yes to the
startup trust prompt for it — relaxing one approval is not what you get.

### `[hooks]` — operator pre-tool hooks

Each `[[hooks.pre_tool_use]]` entry (ADR-0044) runs a command of yours
before every model tool call its matcher covers. One block per hook;
matching hooks run in order, first denial wins.

```toml
[[hooks.pre_tool_use]]
matcher     = "shell_exec"
command     = "python3 /Users/you/hooks/guard.py --strict"
timeout_sec = 10
```

**`matcher`** — what the hook *matches*: the tool name of each call,
nothing else. Three forms only: an exact name (`shell_exec`), a
`|`-alternation (`shell_exec|write_file`), or `"*"` for every tool —
MCP tools included, whose names look like `mcp__server__tool`. Claude
Code's names also match their gem-agent equivalents (`Bash` ↔
`shell_exec`, `Write` ↔ `write_file`, `Edit` ↔ `edit_file`, `Read` ↔
`read_file`), so a hooks block copied from Claude Code settings works
unchanged. No globs or regular expressions beyond these.

**`command`** — what the hook *runs*: a shell command line, not a
pattern. It executes via `/bin/sh -c` in the project directory with
gem-agent's environment, so arguments, flags, `~`, `$VAR` and pipes
all work; name the script by absolute path (the org's own path rule).
The command receives the call as one JSON object on stdin:

```json
{"hook_event_name": "PreToolUse",
 "tool_name": "shell_exec",
 "tool_input": {"command": "sed -i '' 's/a/b/' notes.txt"},
 "cwd": "/path/to/project"}
```

`tool_input` carries the tool's arguments exactly as the model sent
them — for `shell_exec` the command string is `tool_input.command`.

**The verdict** — a hook denies in either of two ways:

- print `{"hookSpecificOutput": {"permissionDecision": "deny",
  "permissionDecisionReason": "why"}}` on stdout and exit 0 — the form
  the org's Claude Code guard emits, so such a script registers here
  unchanged; or
- exit with code 2, the reason on stderr.

Everything else is a pass: exit 0 with no output (or informational
output) sends the call on to the normal approval ladder — a hook can
refuse a call but never approve one. A crash, a timeout
(`timeout_sec`, default 10), or unparseable output proceeds with a
warning in the session.

A complete minimal hook:

```sh
#!/bin/sh
# refuse any shell command that pipes a download into a shell
payload=$(cat)
case "$payload" in
  *curl*"| sh"*|*curl*"|sh"*)
    echo "piping a download into a shell is banned here" >&2
    exit 2 ;;
esac
exit 0
```

registered as `matcher = "shell_exec"`,
`command = "/Users/you/hooks/no-curl-sh.sh"`.

A deny is final — the approval ladder, auto-approve, and the session
allowlist never see the call — and the reason is returned to the model,
which corrects and retries. Global config only: a project-level hook
would let a cloned repository run arbitrary commands. Behaviour and
placement in the gate order are described in
[approval](approval.md).

The `GEMAGENT_STATE_DIR` environment variable relocates the state root
(sessions and memory) for test/drill isolation. Two more environment
variables exist for debugging and are read directly, outside the config
file: `GEMAGENT_MCP_STDERR=1` forwards MCP servers' stderr to the
terminal, and `RUNEWIDTH_EASTASIAN` overrides the pinned width model
used to measure box art under a CJK locale.

## CLI flags

| Flag | Does |
|---|---|
| `-p "<prompt>"` | one-shot mode: single turn, stdout, mutating tools denied (unless `--auto` — ADR-0053); piped stdin is attached as nonce-wrapped data, never as prompt text (ADR-0055) |
| `--auto` | start in auto-approve mode (ADR-0004); the only way to arm it in one-shot `-p`, where `[agent].auto_approve` is ignored (ADR-0053) |
| `--allow <names>` | per-run approval grants: tool names or `mcp__server__*` prefixes that never ask this run (repeatable or comma-separated; the Block floor still applies — ADR-0053) |
| `-c` / `--continue` | resume this project's most recent session |
| `--resume <id>` | resume a specific session |
| `--model <id>` | override the configured model |
| `--thinking <level>` | override `[model].thinking` for this run: `minimal`\|`low`\|`medium`\|`high`, or `default` to clear a configured level (model-dependent — ADR-0025) |
| `--config <path>` | use another config file |
| `--mcp <on\|off>` | override `[mcp].enabled` for this run — `off` skips every MCP server spawn, which is what a `-p` pipeline usually wants (ADR-0039) |
| `--no-sandbox` | disable the Seatbelt wrapper (debugging only) |
| `sessions` | list resumable sessions |

## Telemetry (ADR-0035)

With `[telemetry].enabled = true`, audit events are exported. The
default backend `gcp` writes them into **Cloud Logging of your
[gcp].project** via the same ADC Vertex uses — zero collector
infrastructure, log name `gem-agent` in the Logs Explorer (needs
`logging.googleapis.com` enabled and `roles/logging.logWriter`). The
`otlp-grpc` / `otlp-http` backends send OpenTelemetry log records to
your own collector instead. Events: `session.start/end`, `tool.call` (name,
clipped detail, duration, outcome — `ok`, `error`, `denied`, `skipped`,
`interrupted`, `abandoned`), `tool.late_return` (an abandoned call
that returned after all — ADR-0065), `approval.decision` (decision and
which layer made it), `turn.end`, `model.usage`, `compaction`,
`media.upload`, `integration.reload` (an in-session `/mcp reload` or
`/skills reload` changed the tool surface — ADR-0039) — with
service/session/project/host resource attributes. **Metadata only**: prompts, responses, file contents and
thought summaries never leave the machine through this channel; the
local transcript stays the full record. Only your global config can
enable telemetry or set the endpoint — a project's `.gem-agent.toml`
structurally cannot. OTLP auth headers come from
`[telemetry].headers_file` — a mode-0600 JSON file (by convention
`~/.config/gem-agent/auth.json`) of header name → value; a file
survives launchd/cron/fresh shells where the environment variable
does not. Unset falls back to the standard
`OTEL_EXPORTER_OTLP_HEADERS`. Telemetry never
blocks the session: export failures warn once on stderr and degrade
silently; shutdown flushes with a 3s cap.

## Content filters

Vertex applies content filters to both the request and the response.
When one fires, gem-agent says so explicitly — naming the reason the
API reported — rather than showing an empty answer, and **retries
once**, because the filter is not deterministic: what gets rated is
the text that attempt happened to generate, so the same request
usually goes through on the next try. Measured on ordinary security
material (an incident-response runbook in context): identical requests
were blocked on some attempts and passed on others, at every
`[model].safety` setting.

If the retry is blocked too, the error says so; narrowing the request,
or `/clear` to drop large documents from the context, is what helps.

`[model].safety` adjusts the four configurable harm categories —
useful for `SAFETY`-category blocks, but note that `PROHIBITED_CONTENT`
comes from a filter these settings do not cover:

| Value | Effect |
|---|---|
| `default` | the provider's own thresholds |
| `relaxed` | block only high-confidence hits |
| `off` | do not block on those categories |

Loosening it is a deliberate choice, so the default is left alone.

## Endpoints

As of 2026-08, the Gemini 3 family (verified with gemini-3.7-flash and
gemini-3-flash-preview) is served only from the global endpoint —
regional locations return 404. Gemini 2.5 models work from regional
endpoints such as `us-central1`; set `location` accordingly if you use
one. Transient Vertex failures (429/5xx) retry with exponential
backoff.
