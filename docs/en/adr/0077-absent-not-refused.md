# ADR-0077: A tool the operator did not choose is absent, not refused

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-08, implemented and unreleased; §5 corrected against the code during implementation, §7 carries the measurement) |
| Date | 2026-09-07 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: an MCP server that offers read tools and write tools together, in a session that should have the read half only — "the write tools should just not be there". Secondarily: a global server list of two dozen servers, of which any one session uses a handful |
| Amends | ADR-0009 §1 — the panel's rows gain a level (a server, then its tools), and the approval rows adopt it. Only the presentation changes: §2's test for what may be edited (it must be able to take effect now), §3's machine-owned file and §4's project scoping are reused unchanged, and the tool set is editable because it passes §2's test, not by an exception to it |
| Relates to | ADR-0008 (the same argument one level down; the approval table's values are unchanged), ADR-0009 (the panel, its provenance column, and the machine-owned policy file), ADR-0037 (an allowlist that silently drops a typo hides the mistake it exists to prevent), ADR-0039 (the reload that lets the set change mid-session), ADR-0044 (why a standing decision is not a hook), ADR-0063 (why the tool is absent rather than refused), ADR-0073 (what this is not: the lanes do not reach an MCP server) |

## Context — the gap this fills

**The lanes stop at gem-agent's own children.** An MCP server is started
with a plain `exec.Command` (`internal/mcp/client.go`), outside every
Seatbelt profile: ADR-0073 bounds what a shell command may do, and says
nothing about what a remote server does when it is asked. For an MCP
tool, the runtime's only control today is the MITL gate — ADR-0008's
default that every MCP tool asks, every time.

That control is thinnest exactly where the concern is. Under `--auto`
the ladder may answer for the operator; under `-p` there is nobody to
ask at all. A session left to run is precisely the session in which
"this vault is read-only today" needs to be true without anyone
watching.

**And the gate answers a different question.** `[approval.tools]` says
*when to ask* about a tool: `always`, `never`, or the default. There is
no way to say *this tool is not part of this session* — and for a server
that offers `get_*`/`search_*` beside `patch_*`/`delete_*`, that is the
sentence the operator wants.

**The counterparty cannot be the mechanism.** Where an upstream server
has a read-only mode it is worth using — the GitHub remote MCP honours
an `X-MCP-Readonly` header, measured 2026-08-30 — and it is strictly
better than anything a client can do, because the tools then genuinely
do not exist. But most servers have no such mode and no one to ask for
one. A control that depends on the counterparty implementing it is not a
control; upstream read-only shrinks the problem and cannot own it. The
same boundary rules out expressing this in `.mcp.json`: that file is the
drop-in origin, shared with Claude Code, and a gem-agent-only preference
must not be written into it.

**And the protocol carries no capability to filter on.** A `Tool` in MCP
is a `name`, an optional `title`, a `description`, an `inputSchema`, an
optional `outputSchema`, and optional `annotations` — and that is the
whole of the type (specification 2025-06-18, server/tools §Data Types).
No field states what a tool does to the world. The annotations are the
nearest thing, and the specification disposes of them for this purpose
itself: a client must treat tool annotations as "untrusted unless they
come from trusted servers". They are also optional, and most servers do
not set them.

So "give up this server's write capability" is not decidable from
anything on the wire. What carries that meaning is the function name and
the prose description, and of the two only the name is stable enough to
key on. **Filtering by name is not this ADR's preference among
alternatives; it is the only expressible form the operator's intent
has.** That is worth stating plainly, because it also bounds what the
result can be worth — see the framing at the head of the Decision.

**And a capability model would not settle it either**, which is what
turns a forced choice into the right one. The operator's sentence is not
"read" versus "write". Against a Jira server it is: do not create
issues, but do write comments — and both of those are writes. A
vocabulary general enough to span every server is too coarse to say
that, and one fine enough would be the server's own domain vocabulary,
which someone would then have to define, maintain and keep translated
per server. That is the shape of the trap this project has walked into
before and measured: the predecessor that produced mcp-bridge carried
about 26 profile keys with 5 in use, and its unintelligibility came from
the volume of machinery nobody used. The function name is where the
server's domain and the operator's intent already meet — `create_issue`
and `add_comment` are the distinction, spelled by the person who wrote
the server. Keying on it is not a fallback from a capability model; it
is the granularity at which the operator's actual sentence can be said
at all, and the protocol's silence only means nothing else was competing
for the job.

So the decision has to be made on gem-agent's own side of the ownership
boundary. This is ADR-0008's argument one level down. That ADR said: the
information gap is real, the client cannot close it, the operator can —
what is missing is a place to say so. It gave the operator a place to
say "this server is safe to run unasked". This one gives them a place to
say "of this server, only these tools exist for me".

Prompt size is a real secondary effect — the declarations are the fixed
prefix of every request, and a session that needs five tools should not
carry two hundred — but it is not the justification, it has not been
measured here, and nothing below rests on it.

## Context — why not "declared but refused"

The obvious alternative is to keep declaring the tool and refuse every
call with "blocked by policy". It is rejected, for three measured
reasons.

1. **It is a prohibition, delivered in the worst position available.**
   The refusal lands in the function response, stays in the history, and
   is re-read every round while the model is pursuing a concrete goal.
   ADR-0063 measured what a prohibition does over two months: it
   generalises past its subject and breeds a path nobody wrote down.
   Here the third path is not hypothetical — the capability behind an
   MCP tool usually has a command-line spelling, so a refused
   `mcp__github__*` becomes `gh` in the write lane, which is less
   reviewable, not more contained. This is the reason the file tools
   already record for keeping the work directory reachable.
2. **It puts the model in a state it has no behaviour for.** "A server
   that is not configured" is the state every session handles: the tools
   are absent, and the model picks another route or asks. "A declared
   tool that always fails" is not in that repertoire, and the model's
   default readings of it — a transient fault, bad arguments, my own
   usage is wrong — are exactly the misreading ADR-0075 measured, at the
   cost of 56 rounds and 6.5M prompt tokens. Declaring a tool and
   refusing it makes that misreading the standing state of the session.
3. **It buys no observability that is worth the distortion.** The one
   thing refusal offers is the record of what the model wanted. That
   record already exists, undistorted, in the transcripts of sessions
   where the tool was available (`gate_decision`, `auto_decision`, the
   tool calls themselves). Building the set from measured use is
   better evidence than counting refusals under a distribution the
   refusals themselves created.

## Decision

Mechanically this is one thing: **a filter on tool names, applied at two
points** — the declarations the model is given, and the dispatch of a
call. Everything below is the shape of the name the filter matches
(§1–§2), the place the operator edits it (§3), or the reason for a
default (§4–§6). If the implementation grows past one predicate and two
call sites, something has been smuggled in.

Saying it that way also fixes the limit. A filter on names is exactly as
strong as the convention that a name says what a tool does: the runtime
does not know what `patch_vault_file` does, it knows what it is called.
Mapping names to intent is the operator's judgement — which is why the
server's own `readOnlyHint` is not consulted (§6) and why the same
function returning under a different name is not something this
mechanism can notice (§5).

### 1. The declared set is a setting, and absence is the mechanism

Everything is declared unless the operator turns it off, and there are
two levels to turn off — no third.

- **A server.** It is then not started at all: no process, no
  credentials touched, nothing of it in the declarations. Its row stays
  in the panel, because server names come from `.mcp.json` and not from
  the server, so an off server can always be turned back on — turning it
  on starts it and its tools appear (ADR-0039's reload). The thing that
  would otherwise be unreachable is exactly the thing still listed.
- **A function of a server.** It is not declared, and a call naming it
  is refused before any gate. The server keeps running for its other
  tools; turning off the last one leaves it running and declaring
  nothing. Stopping a process is an explicit act at the level above, not
  something arithmetic arrives at behind the operator.

An operator who turns nothing off sees today's behaviour, and
`.mcp.json` keeps exactly the meaning it has.

**Built-ins are not selectable**: removing a reviewable built-in pushes
the same work into `shell_exec` and shell redirection — less reviewable,
not more contained — which is the trade the file tools already refused
for the work directory. The schema does not offer the mistake.

### 2. One key, two levels, one precedence rule

```toml
# ~/.config/gem-agent/config.toml — hand-written, never rewritten
[mcp]
exclude = [
  "chrome-pilot",                    # a server
  "obsidian/patch_vault_file",       # a function of a server
  "obsidian/search_and_replace",
]
```

```toml
# ~/.config/gem-agent/policy.toml — gem-agent rewrites this file
[mcp]
exclude = ["obsidian/delete_vault_file"]
```

```toml
# <project>/.gem-agent.toml — may only add to off
[mcp]
exclude = ["github"]
```

- **Entries are exact names**: a server as `.mcp.json` spells it, or
  `server/function` as the server offers it. No patterns — the two
  levels supply the grouping a pattern was faking, since
  `mcp__server__*` was how to say "this whole server" in a flat list.
  (An earlier draft had that flat list, and then a rule making a partial
  wildcard — `mcp__obsidian__search_*`, which matches
  `search_and_replace` — a configuration error. The operator's review
  rejected the prohibition; the restructuring that followed removed the
  question along with it.)
- **Per server, the nearest scope decides, whole.** If `policy.toml`
  has an opinion about a server, that is the answer for that server, and
  `config.toml`'s word about it is not consulted. The opinion is
  recorded, not inferred from the presence of an entry: `policy.toml`
  carries a `decided` list beside `exclude`, because "this server has
  nothing excluded" is itself an opinion — the one that undoes a
  `config.toml` exclusion — and inferring the opinion from the entries
  made exactly that state unrepresentable, so a server the operator's
  own file turned off could never be turned back on from the panel
  (found by the pre-release review, which is the review's whole job). No set arithmetic across scopes: what
  the operator last said in the panel replaces the file they wrote last
  month, for that server and no other.
- **The project file may only add to `exclude`.** Narrowing is the only
  composition available to it, so ADR-0008 §4's direction rule holds by
  construction and no trust condition is needed at all.
- **A name that matches nothing is reported, not ignored** (ADR-0037's
  rule): a server or function renamed upstream must not leave a line
  that quietly does nothing.
- **The key is `exclude`, under `[mcp]`.** An earlier draft spelled it
  `[tools] off`, which reads as a switch rather than a list and says
  nothing about what may be named. The section supplies the scope —
  built-ins are not selectable, and `[mcp]` says so without a word in
  the key — and `exclude` supplies the direction, which `filter` alone
  would leave open in exactly the place §4 had to settle. The panel's
  control stays a toggle: an operator turns a row off, and the file
  records what is excluded.

### 3. The panel is where the set is decided

`/tools` gains a two-level list. The outer level is the servers, from
`.mcp.json`: on or off, present whether or not the server is running.
Expanding one shows its functions, each row on or off and carrying
**where that came from** — `config.toml`, `policy.toml`, the project
file, or the default. That provenance column is ADR-0009's.

The hierarchy is not decoration. Flat, this list runs to two hundred
rows on the operator's own machine and is unreadable; and it is the only
way a server that is not running can still be offered, since its row
does not depend on it having answered `tools/list`. **The approval rows
adopt the same two levels** (ADR-0009 decision 1 amended):
`[approval.tools]` has taken `mcp__server__*` since ADR-0008 §3, so the
server has always been that table's natural unit too, and one panel
component serves both lists.

Toggling takes effect immediately through the ADR-0039 reload and is
**session-scoped until it is persisted**; persisting writes
`policy.toml`. Trying a set before committing to it is the ordinary
case, and it keeps the pressure off getting the hand-written file right
in one pass.

### 4. What the exclusion direction costs, and what already covers it

`exclude` names what is removed, so a function the server gains in a later
version arrives **on**. That is the ergonomic direction — splitting a
server means turning off six functions, not turning on forty-five — and
its cost is that a decision made before a function existed does not
cover it.

Two mechanisms were drafted for that cost. Both are dropped, and why is
worth more than either would have been.

**A startup line per server** was the first: `obsidian: 8 of 51 declared
(+2 since last confirmed)`, every start. The operator rejected it, and
the objection generalises past this ADR. **A report is not a control.** A
line printed before anyone has a question is a status; a status printed
on every start is read on none of them; printing it buys the runtime the
feeling of having disclosed something and buys the operator nothing.
This repository has the failure on record — AGENTS.md keeps `make
labels` and the release read-through because "four releases shipped
explanatory banners because nobody had the whole in one place" — and the
banner it would have joined is assembled by teeing every startup write
into it (`cmd/root.go`), which is how a wall accumulates without anyone
deciding to build one.

**A recorded baseline** was the second: `policy.toml` keeping, per
server, the function names present when the operator last confirmed it,
with a first call outside that set stopping once. It collapses on its
own terms:

- **The confirmation it compares against has no moment.** An operator
  who never opens the panel has either a day-one baseline, which makes
  everything since then "new", or one that updates on every listing,
  which never fires. Between those ends is a ritual nobody performs — a
  trigger invented for a mechanism instead of found in the work, which
  is ADR-0062's measured lesson read from the other side.
- **A server that is off cannot be listed**, so its baseline is whatever
  it was when it last ran, and turning it on months later reports a
  change that is only the passage of time.
- **A tool set is not a stable property of a name.** mcp-bridge fronts
  remote servers whose sets change server-side and can differ by header
  (`X-MCP-Toolsets`), so one entry legitimately lists differently on
  different days.
- **A degraded `tools/list` reads as a mass disappearance** followed by a
  mass arrival — the transient-server-fault class ADR-0075 measured. A
  detector whose false positives are produced by ordinary server faults
  is a detector that gets ignored, and then it is decoration with a
  maintenance cost.
- **The protocol's own `notifications/tools/list_changed` does not
  rescue it.** It says a list changed during a session, for servers that
  declared `listChanged`; it does not say what changed relative to a
  decision the operator made last month, which is the only comparison
  that would have meant anything.

So there is no drift mechanism, and none is needed to state the
position. What covers the hazard is what already exists: **ADR-0008's
default is that every MCP tool asks**, so a function that appears later
asks like the rest of its server. The exposure is exactly and only where
the operator has removed that — a `never` on the server, or `--auto` —
which is a property of `never`, a setting ADR-0008 owns, and not a hole
this ADR opens. An operator who wants a guarantee about what a server
adds next turns its write functions off **and** leaves its approval
default alone.

### 5. What this guarantees, and what it does not

It guarantees exactly one thing, and the guarantee is the operator's own
to verify: **this runtime never emits a call to a tool the operator
turned off.** Not in the declarations, and not through a name the model
supplies anyway: the executor already refuses a name it cannot resolve
in the registry — before the pre-tool hook, before the ladder, before
the gate (`execCallInner`). An excluded tool is simply not registered,
so it inherits that refusal and needs no new path. **This ADR's first
draft said otherwise** — that an unregistered name reached the gate as a
`Review` "unknown tool" — reading `decide` without noticing that the
executor returns first. The `Review` verdict exists, but only the
telemetry and auto-approve paths ever see it. The correction makes the
mechanism smaller, not larger.

It does **not** confine the server. The capability still exists there,
the operator's credentials still reach it, and a tool that reads may
write as a side effect. This is a reduction of what the session can ask
for, not a boundary. ADR-0073's standard applies: confinement is
measured, and nothing here was measured at the kernel. A later design
must not treat this setting as a wall.

Nor does it survive a rename. `exclude` holds names, so a function that
comes back as `write_note` after being turned off as `patch_vault_file`
is declared again — the same class as the drift this ADR decided not to
detect (§4), and left undetected for the same reasons. What the operator
gets is a faithful filter on what they named, not a standing judgement
about what a server can do.

Two audiences, two texts, both true:

- To the model, the refusal is existence-shaped — *no tool by that name
  exists in this session*. It is not a euphemism: the session's tool set
  is what the operator's list leaves it, and this is the same sentence
  the model gets for any server that is not configured. Saying "blocked by
  policy" instead would re-create the state §"why not declared but
  refused" exists to avoid.
- To the operator, the transcript records that a removed tool was named,
  under its own record kind. Nothing is hidden from the person who set
  the list.

The model-facing text is therefore the one an unregistered name already
gets, and deliberately so: a tool the operator excluded and a tool that
never existed are the same fact from where the model stands, and giving
the first its own wording would be the "blocked by policy" state under
another name. The runtime keeps the distinction where it belongs — in
the transcript, for the operator who drew the line.

### 6. What is not done, and why

- **No `deny` value in `[approval.tools]`.** The two tables stay
  separate axes — when to ask, and what exists. Fusing them would make
  "declared but refused" expressible as a combination of values, which
  is the state this ADR is written to prevent. The asymmetry is visible
  in one spelling: a bare `"*"` is a configuration error in the approval
  table (it disarms every gate) and would be meaningless in `exclude` (it
  would leave the session with no MCP tools at all, which the panel
  reaches server by server).
- **No standing deny via `[[hooks.pre_tool_use]]`**, although it works
  today. A hook is given the call's arguments because it judges per
  call; a standing "this tool is not part of this session" has no
  arguments to read, and lives invisibly outside the panel where the
  operator looks for it. Availability is not an argument for the right
  home.
- **MCP annotations are not the source of the split** — see the Context:
  the specification itself requires a client to treat them as untrusted
  unless the server is, they are optional, and gem-agent does not read
  them today. At most they may order or pre-tick rows in the panel for
  the operator to confirm; they never decide the setting.
- **No filtering in mcp-bridge.** Its RFP put masking out of scope, and
  it sits under the HTTP/OAuth servers only.
- **No named sets ("profiles").** A `[tools.profiles]` table holding an
  `ir` and a `dev`, selected by name, was in this ADR's first draft and
  the operator cut it. Switching a whole set by name is a second naming
  system laid over a set the panel already shows, and the measurement
  that produced mcp-bridge is about exactly this feature: in the
  predecessor it replaced, about 26 profile keys were carried and 5 were
  used, and the recorded conclusion was that the unintelligibility came
  from the volume of unused features rather than from any single defect.
  Switching context costs toggling servers in the panel instead. If the
  need proves real, a named set can be layered on this one later — it
  does not start here.
- **No capability vocabulary laid over the names** — no
  `[tools.capabilities] jira = "read"`, and no gem-agent-side taxonomy
  of what a function does. The Context says why: the distinction the
  operator needs is the server's domain (issue versus comment), a
  general vocabulary cannot reach it, and a per-server one is a second
  system to maintain for every server. If this is ever proposed again,
  the question to ask first is which sentence it lets an operator say
  that `exclude` does not.
- **No strict mode per server** — no way to say "this server's functions
  are an allowlist, and a future addition stays out". It is the safer
  direction and it was in the draft; it costs listing forty-five names
  to exclude six, and §4 says what covers the case instead. If the gate
  turns out not to — that is, if an operator both splits a server and
  needs `never` on it — this is the first thing to add, and it is a
  small addition on top of the same key.

### 7. What is measured, and how

The question is not "how large are the declarations" but "what does a
smaller set recover, in the unit that is spent". None of these numbers gates
the decision — the feature stands on the Context above — they calibrate
what the release note is allowed to claim.

1. **Prompt tokens, end to end.** Two `-p` runs of the same trivial
   prompt, one with nothing off, one with the operator's working set.
   ADR-0057's
   `usage` record already carries `prompt`, `cached`, `thoughts`,
   `tool_prompt` and `total` per call, so the difference in `prompt` is
   the declarations' cost in the unit the API bills — and, through
   ADR-0057's catalog price, in money. Three runs each; the prompt is
   fixed, so the tool set is the only variable.
2. **The cached share, not the first round alone.** Declarations sit in
   the request prefix, so a first-round figure overstates what a long
   session saves. `cached` is in the same record: report a first round
   and a steady-state round separately, and never quote the first-round
   number by itself.
3. **Startup, now that a server can be off.** Wall-clock to the first
   prompt, and the child-process count, for the same two
   configurations. §1's server level added this one, and it is not a
   token count.

Not measured and not claimed: tool-selection accuracy. A smaller set is
plausibly chosen from better, but saying so honestly needs a task set
and repetitions, and nothing here rests on it.

The exact request-side byte count, if it is ever wanted, is a live test
with a tapping `http.RoundTripper` — `internal/llm/wirebytes_live_test.go`
already taps the response body and is the pattern to copy. It is a
curiosity beside (1): bytes are not what is billed.

**Measured 2026-09-08** (v0.71.0, before implementation; the reduced
configuration was produced by editing a scratch `mcp.json`, which is
what a server-level exclusion will do). One fixed trivial prompt,
`gemini-3.8-flash`, same project directory, isolated state, same
`config.toml`; the server list is the only variable.

| configuration | servers | tools | prompt tokens (round 1) |
|---|---|---|---|
| no MCP servers | 0 | 0 | 5,804 / 5,805 / 5,809 |
| three largest removed | 21 | 130 | 32,647 |
| the operator's list | 24 | 249 | 73,143 / 73,144 / 73,151 |

- **The declarations are 67,340 tokens — 92% of the prompt.** Run to run
  the figure moves by five tokens; the tool set is the whole of the
  variance.
- **Three of the twenty-four servers are 60% of that cost** (40,499
  tokens for 119 tools). Declaration bytes read straight from
  `tools/list` — 242,350 over 249 tools, about 3.6 bytes per token —
  predicted that saving within 6%, so a byte count is a serviceable
  estimator once the total is known.
- **The first-round figure must not be quoted alone**, exactly as (2)
  said. In a second round of the same session the prompt was 73,382 with
  **68,660 cached**: the declarations are paid in full once per prefix
  and at the cached rate on every round after. What an exclusion saves
  is therefore one full payment per session (and per prefix change) plus
  a cached-rate share thereafter — real, and not the headline number.
- **Startup wall-clock produced nothing usable at three runs**: model
  latency dominates it. The solid startup fact is the process count, 24
  against 0.

## Alternatives considered

- **Declared but refused** — rejected; see the second Context section.
- **A flat list of patterns with named profiles selecting between
  them** — this ADR's first draft, cut by the operator as complexity
  with a measured history (§6). The two levels of §1 carry what the
  patterns were for, with no names to invent.
- **An allowlist rather than an exclusion list** — rejected for the reason in §6:
  the safer direction costs listing what is kept, which is nearly
  everything.
- **Prohibiting partial wildcards** — rejected by the operator's review:
  it solved an invisible expansion by removing expressiveness. The
  display in §3 solves it where the problem is.
- **Asking upstream implementers for read-only modes** — not a control.
  There is no counterparty to ask for most servers, and no guarantee
  about the version actually running.
- **Editing `.mcp.json`** — rejected: that file is the drop-in origin,
  shared with Claude Code.
- **Deriving the read/write split from MCP annotations** — rejected as
  a setting; see §6.

## Consequences

- One key, `[mcp] exclude`, in the global config, in `policy.toml` and in
  the project file (where it may only add). Entries are `server` or
  `server/function`. Strict decode, as every other key is.
- The registry keeps the names it removed, so a call to one is a
  refusal rather than an unknown name. `toolDefs` builds declarations
  from the effective set; the ADR-0039 reload path rebuilds it.
- A server turned off is not started; `/mcp` says so, and the panel
  lists it regardless, from `.mcp.json`.
- `/tools` gains the two-level list, provenance and toggles; the same
  component renders `/settings`' approval rows (ADR-0009 amended).
- **Nothing is added to the startup banner, and no state is recorded
  about what a server used to offer.** The only new startup output is a
  name in `exclude` that matches nothing (§2).
- A transcript record for a call to an excluded tool — the one thing the
  dispatch site adds, since the refusal itself already exists. A server
  excluded whole never lists, so it has no function names to record one
  by one; the registry keeps its name prefix instead, or a call to one of
  its tools would read as a tool that never existed. Telemetry is
  unchanged in the sense that nothing was added to it, but a `tool.call`
  event IS emitted for the refused call, with `outcome=error`: the
  executor reports every call it was handed.
- Tests: per-server precedence between the three files; a project file
  that can only add to `exclude`; an excluded name reaching the executor
  (refused with the unregistered-name text, recorded distinctly, no hook
  and no gate); a name matching nothing; **resume with a changed set**,
  where
  the history holds calls to tools that are no longer declared; and the
  mid-session reload of the declared set.
- Docs: README and README.ja (the paragraph on what the model can see),
  the configuration, tools, approval and integration references in both
  languages, the RFP's security layer, `config.example.toml`,
  AGENTS.md's structure table, CHANGELOG, and both INDEX files. New
  operator-facing strings go in both `uitext` catalogs.
- The measurement of §7, taken before or with the implementation. It
  belongs in the release note, not in the justification.
