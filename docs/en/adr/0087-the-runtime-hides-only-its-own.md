# ADR-0087: The runtime hides only its own — the environment scrub is withdrawn and the prefix rule is inverted

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-13) — implemented and unreleased |
| Date | 2026-09-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "a variable is visible in the process space anyway — why hide it only when it goes through the agent? If it must not leak, do not put it in the environment." Then, on the shape of what remains: "which variables a read-lane program needs cannot be judged, so the environment is not something to touch; but the variables the runtime holds for itself, it knows by name, so those it can hide." |
| Amends | ADR-0073 §6 (the read lane's environment scrub is withdrawn), ADR-0071 (the three exports are unchanged; what surrounds them is not) |
| Relates to | ADR-0086 (the same question where a kernel exists), ADR-0084 (the read lane must stay able to compile, vet and test) |

## Context

The read lane's environment scrub was added as a review finding under
ADR-0073 §6: a bare `env` printed the operator's exported tokens into
the transcript, so variables whose names match
`(token|secret|passw|api[_-]?key|credential|private[_-]?key|access[_-]?key|auth)`
were withheld from read-lane children. ADR-0085's release then replaced
a `GEMAGENT_` prefix exemption inside that scrub with a name list,
because the same prefix exemption leaks `LAGENT_API_KEY` in the sibling
runtime (system risk review 2026-09-13, R01).

Two facts, measured 2026-09-13, say the control was never one.

**It covers one door of six.** Every other child gets the operator's
environment whole:

| child | environment |
|---|---|
| read-lane shell | scrubbed |
| write-lane shell | full |
| operator-lane shell | full |
| `!` direct shell | full |
| MCP servers | full (`cmd.Env = os.Environ()`, plus the config's own `env` block) |
| hooks | full (`cmd.Env` unset, so inherited) |

**And the one door leaks.** Against the shipped regex these pass:

```
OPENAI_KEY  GH_PAT  STRIPE_SK  ANTHROPIC_KEY  GPG_PASSPHRASE
SESSION_COOKIE  BEARER  LICENSE_KEY  PAT
```

`NPM_TOKEN` is caught and `OPENAI_KEY` is not. The system risk review
reached the same conclusion in its chapter 10 and wrote it plainly:
one cannot explain this as "put a secret in an environment variable
and it is uniformly protected".

### Why an allowlist is not the fix

The obvious repair — invert the denylist, give the read lane only the
variables it needs — was proposed and is rejected here. The set of
variables a legitimate toolchain reads is as open-ended as the set of
names a secret can have: `GOFLAGS`, `GOMODCACHE`, `GOPRIVATE`,
`PYTHONPATH`, `VIRTUAL_ENV`, `JAVA_HOME`, `CARGO_HOME`, and one more
per tool released next month. An allowlist moves the unbounded list
from "names that look secret" to "names something needs", and changes
the failure from a silent leak to a read-lane build that breaks until
the list catches up — the friction ADR-0084 was written to remove.

So the environment has no bounded domain on either side. That is the
difference from ADR-0086: there, a kernel exists and the judgment can
move to it. Here no kernel exists — the environment is whatever the
parent hands to `exec`, and no operating-system mechanism filters it.
Where there is no bounded domain to move the judgment to, the rule
should not exist.

### What is bounded

One set is finite, enumerable, and known exhaustively by the party
writing the code: **the variables in the runtime's own namespace.**
Nobody else reads `GEMAGENT_*`. The runtime knows every one of them
because it wrote them. Today, completely:

| purpose | variables |
|---|---|
| read by the runtime for itself | `GEMAGENT_STATE_DIR`, `GEMAGENT_PROJECT`, `GEMAGENT_LOCATION`, `GEMAGENT_MODEL`, `GEMAGENT_MCP_STDERR` |
| exported by the runtime for children | `GEMAGENT_WORK_DIR`, `GEMAGENT_PROJECT_DIR`, `GEMAGENT_SESSION_ID` |

The original prefix rule was not wrong about the domain. It was
inverted: it *kept* everything `GEMAGENT_*` and guessed about the rest.
The correct reading of the same domain is to *remove* everything
`GEMAGENT_*` that is not one of the three exports, and to guess about
nothing.

*Amended 2026-09-13, after the release review: §Context's "Nobody else
reads `GEMAGENT_*`" is false, and the counter-example is the org's own.
`gem-usage-lens` reads `GEMAGENT_STATE_DIR` on purpose
(`core/platform/paths.go`), so that an isolated gem-agent is measured
where it actually writes. The decision stands: the variable stays in
the removed half. An operator running an isolated state root and then
launching `gem-usage-lens` from inside that session's shell lane now
gets the default sessions root instead of the isolated one, and the
remedy is the one the sibling tool already ships — `--sessions-root`,
or `[sources]` in its own configuration. A child that needs a fact
about the session should be told it, not inherit it: that is the same
rule that keeps a nested runtime from taking its identity from an
environment it did not choose. What the premise should have said is
narrower and still true: nobody else may be assumed to read it, so the
runtime may act on the whole namespace, and a sibling that does read it
takes it as a parameter.*

## Decision

### 1. The operator's environment is not touched

`sandbox.ScrubEnv`, `secretEnvRe` and `runtimeExports` are deleted, and
with them the claim in the approval reference's read-lane row. A
read-lane command receives the operator's environment as the operator
left it, because which variable the program it runs needs is not a
question this runtime can answer.

### 2. The runtime's own variables do not reach any child

A child spawned by this runtime — read, write and operator lane shells,
the `!` shell, MCP servers, hooks — receives the parent environment
minus the runtime's own configuration variables. The three exports
(ADR-0071) are added as they are today; they exist for children.

One function, applied at every spawn site, so that a child cannot be
added without inheriting the rule.

### 3. The partition is closed by a test, not by care

Every `GEMAGENT_` string literal in non-test code must appear in
exactly one of the two lists. A new variable fails the build until it
is classified as *mine* or *theirs*.

This is what R01 actually asked for. A future `GEMAGENT_API_KEY` never
reaches a child, and not because a regex recognised the word `key`: it
is in the runtime's namespace, it is not an export, so it is removed.
The class is closed by construction, and the construction is a
partition of a set the authors enumerate rather than a guess about an
unbounded one.

### 4. The true sentence is said once

The environment gem-agent was launched with reaches every child it
spawns. The documents say that, in place of a protection claim, and
name the remedy the operator actually has: launch it from a shell that
does not hold what you would not give the model, or drop the variable
at launch.

## Consequences

- A bare `env` in the read lane now prints the operator's exported
  variables to the model. That was already true of the write lane, the
  operator lane, MCP servers and hooks; the read lane stops being the
  exception that implied a rule.
- One unbounded-domain rule is deleted and no unbounded-domain rule
  replaces it.
- `GEMAGENT_*` stops reaching children, which is a small, real gain
  today (`GEMAGENT_PROJECT` is an environment-specific value the org
  rules keep out of shared artifacts) and the whole gain tomorrow, for
  any variable the runtime later reads for itself.
- **A nested `gem-agent` in a shell lane no longer inherits the
  parent's configuration variables.** It reads its own config file, as
  a separately launched runtime should — the runtime identity of a
  child is a parameter, not something inferred from an inherited
  environment.
- An MCP server that was reading an ambient `GEMAGENT_*` value gets it
  from the `env` block of the MCP configuration instead, which is
  where a server's own inputs belong.
- lagent takes the same decision in its own numbering; `LAGENT_API_KEY`
  is exactly the variable §3 removes by construction.

## Alternatives considered

- **Keep the denylist and add the missing words.** Rejected: that is
  the road the operator named, and `OPENAI_KEY` shows it does not end.
- **An allowlist for the read lane.** Rejected: §Context — the needed
  set is as unbounded as the secret set, and it fails by breaking work.
- **Scrub every child, not just the read lane.** Rejected: it would
  break `gh`, `aws` and any tool the operator legitimately drives from
  the write or operator lane, and it would break MCP servers that are
  given credentials on purpose.
- **Do nothing, document the truth only.** This is §1 and §4, and it
  would have been the whole ADR. §2 is added because one bounded set
  exists and costs nothing to handle correctly.
- **A startup warning listing the operator's secret-looking
  variables.** Rejected: a report is not a control, and a line printed
  at every start is one nobody reads.
