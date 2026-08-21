# ADR-0035: OpenTelemetry audit logging

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the tool is getting practical enough for workplace use — that calls for logging, ideally via OpenTelemetry |

## Context

The JSONL transcript is a full-fidelity, conversation-shaped, local
record — the resume source, not an audit trail. A workplace needs the
ops-shaped view: which sessions ran, which tools executed with what
outcome, what was approved by whom (operator, allowlist, policy,
auto-approve tier), what left the machine, and what it all cost — in
the org's own collector, next to everything else it monitors.

## Decision

### 1. OTLP **logs**, v1 — not traces, not metrics

Audit events as OTel log records over OTLP (gRPC or HTTP), because a
SIEM consumes logs natively and the operator's stack (a collector in
front of Splunk or anything else) is exactly the target. Traces would
be pretty but answer a different question; if a real need appears they
get their own ADR.

Events: `session.start` / `session.end`, `turn.start` / `turn.end`
(rounds, duration, token totals, error class), `tool.call` (name,
mutating, clipped detail, duration, outcome ok/error/denied),
`approval.decision` (tool, decision, source: operator / allowlist /
policy / auto-rule / auto-model, must_prompt, clipped reason),
`compaction`, `media.upload` (bytes, deduped). Resource attributes:
service name/version, session id, project directory, host.

### 2. Default OFF, and only YOUR config can turn it on

```toml
[telemetry]
enabled  = false            # default
backend  = "gcp"            # gcp | otlp-grpc | otlp-http
endpoint = "localhost:4317" # otlp-* only
insecure = false            # otlp-* only
```

The exporter is a NEW egress channel from a security tool, so it is
opt-in, and the `[telemetry]` table exists only in the operator's
global config — the project-side `.gem-agent.toml` structurally
cannot enable telemetry or redirect it: a cloned repository must not
be able to plant an exfiltration sink. Auth headers for OTLP ride the
standard `OTEL_EXPORTER_OTLP_HEADERS` environment variable (secrets
belong in the environment, not the config file).

### 2a. The default backend is GCP — the credentials already exist

The tool authenticates to a GCP project for every model call; Cloud
Logging in that SAME project is the natural home for its audit trail
(operator observation). `backend = "gcp"` writes the events straight
into Cloud Logging via the ADC that Vertex already uses — log name
`gem-agent`, event name and attributes as a structured JSON payload,
session/version/project-dir as labels. Zero collector infrastructure:
`enabled = true` is the entire setup (plus
`logging.googleapis.com` enabled and `roles/logging.logWriter`). The
OTLP backends remain for org collectors in front of a SIEM. Under the
hood the GCP backend is just another exporter on the same OTel SDK
pipeline, so batching, the no-op zero value, and the never-hurt rules
(§4) are identical.

### 3. Metadata, never payloads

Prompts, model responses, file contents, and thought summaries never
leave the machine through this channel — the transcript remains the
full record, local. What IS sent is what an audit is for: tool names
and outcomes, the shell command line (clipped — an audit trail of
"shell_exec ran" without the command is not an audit trail; commands
occasionally embed secrets, and this trade is stated here rather than
hidden), file paths, egress URLs and search queries (exactly what a
DLP review wants), token counts, model names, approval verdicts with
their reasons.

### 4. Telemetry must never hurt the session

A backup tool that fails because its logging failed has inverted its
priorities. Records are emitted through the SDK's batching processor
(fire-and-forget), an unreachable collector produces ONE stderr
warning and silent degradation after, and shutdown flushes with a 3s
cap. The no-op sink is the zero value: disabled costs nothing and no
call site checks a flag.

## Consequences

- Workplace deployments point `[otel]` at their collector and get the
  audit trail in the same place as everything else; the default user
  experience is unchanged and offline.
- The dependency tree grows by the OTel log SDK and OTLP exporters.
- Command lines and URLs go to the collector by design; an
  organization that considers those too sensitive for its own SIEM
  should leave telemetry off.
