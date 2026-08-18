# AGENTS.md — gem-agent

Interactive CLI agent backed by Vertex AI Gemini 3.x. Continuity (backup) tool
for when Claude Code is unavailable. macOS-only. Pre-release: scaffold complete,
agent loop not yet implemented.

- **Module:** `github.com/nlink-jp/gem-agent`
- **Series:** lab-series (promotion to cli-series considered after E2E + drill
  operations prove it — criteria to be written in development Phase 3)
- **Spec:** `docs/en/gem-agent-rfp.md` / `docs/ja/gem-agent-rfp.ja.md` (canonical)

## Build / test

| Task | Command |
|------|---------|
| Build | `make build` → `dist/gem-agent` (never `go build` directly) |
| Test | `make test` (or `go test ./...`) |
| Vet + test + build | `make check` |
| Release archive | `make package` → `dist/gem-agent-vX.Y.Z-darwin-arm64.zip` |

Version is injected via `-X main.version` from `git describe` — never edit the
`version` var default.

## Structure

```
main.go            entry point (package main, calls cmd.Execute(version))
cmd/               cobra commands (root.go, root_test.go)
internal/          private packages (created as features land)
scripts/           codesign-darwin.sh / notarize-darwin.sh (org templates, verbatim)
docs/en/, docs/ja/ RFP, ADRs (en: no suffix; ja: .ja.md)
```

## Gotchas

- **macOS-only by design** — isolation is built on sandbox-exec (ADR-0001).
  Do not add linux/windows targets to the Makefile.
- **Gemini 3 thought signatures** — every response Part carries an opaque
  `ThoughtSignature` that must be echoed back on the next request; dropping it
  fails the second tool-call round with 400 INVALID_ARGUMENT.
- **`--version` must always answer** (pinned by `cmd/root_test.go`) — a future
  Homebrew formula's `brew test` runs it.
- **Drop-in compatibility is the point** — gem-agent reads the *target*
  project's AGENTS.md / CLAUDE.md / .mcp.json. Changes that require per-project
  setup in target repos defeat the tool's purpose.
- Config (planned): `~/.config/gem-agent/config.toml`, org-standard schema
  (`[gcp]` project/location, `[model]` name), precedence
  `GEMAGENT_*` > `GOOGLE_CLOUD_*` > file > defaults.
