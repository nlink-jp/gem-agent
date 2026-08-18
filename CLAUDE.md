# CLAUDE.md — gem-agent

Project-specific rules for AI agents. Org rules: nlink-jp/.github CONVENTIONS.md.

## Canonical specification

- The RFP (`docs/{en,ja}/gem-agent-rfp*.md`) is the canonical spec. Do not add
  features outside its scope without an ADR — scope minimalism is a design
  goal, not an accident (backup tool: "the core 20% of Claude Code").
- Non-obvious design decisions get an ADR in `docs/{en,ja}/adr/` (four-digit,
  `Binds: gem-agent`) **before** implementation.

## Build & test

- `make build` only — never `go build` directly (outputs to `dist/`).
- Tests are mandatory and written with the implementation.
- macOS-only by design (sandbox-exec). Do not add linux/windows build targets.

## Implementation rules

- Vertex AI via `google.golang.org/genai` (`BackendVertexAI`). Never the
  retired `cloud.google.com/go/vertexai/genai`.
- Gemini 3.x requires **thought signature echo-back** on every Part
  (capture/replay). Missing it fails tool-call round 2 with 400.
- Model names are config-driven — never hardcoded.
- Untrusted data (tool output, file contents) is wrapped with nlk/guard
  nonce-tagged XML before entering the prompt; defensive instructions sit at
  the top of the system prompt.
- No secrets or environment-specific values (GCP project IDs, SA emails) in
  code, docs, or tests — placeholders only.
- Consult `nlink-jp/knowledge` docs (llm-integration, security,
  config-and-io, mcp-server-design) before implementing in those domains.

## Docs

- README.md / README.ja.md and docs/{en,ja} stay in sync in the same commit.
- English docs carry no language suffix; Japanese docs use `.ja.md`.
