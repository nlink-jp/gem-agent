# ADR-0068: The telemetry client declares its Cloud Logging resource instead of probing for one

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | `gem-agent -p 'Reply with the single word OK.' --mcp off < /dev/null` with telemetry on (`backend = "gcp"`) reached its stdin read after 0.0 s in four of six runs and after 5.9 s / 7.2 s in the other two, with nothing on stderr in between |
| Amends | ADR-0035 §2a (constructing the GCP backend is now network-free) |

## Context

The stdin read itself was traced first and cleared: it started late.
So the startup path ahead of the one-shot branch — config, project
trust, session log, work directory, tools, MCP, skills, memory,
telemetry, the Vertex client, instructions, agent, rulebook — was
instrumented with an env-gated per-step trace and the command run 24
times in two batches. Every step measured under 5 ms in every run
except one: `telemetry.New` with `backend = "gcp"` took 0 ms in 18
runs and 4.5 s, 5.7 s, 6.1 s, 6.6 s, 7.1 s and 7.2 s in the other
six. Inside it, `logging.NewClient` returned at once every time; the
whole wait sat in `client.Logger(...)`. `genai.NewClient` was instant
in all 24 runs — ADC token acquisition, the first suspect, was never
involved.

The mechanism, read from the libraries:

- `Logger` called without a `CommonResource` option runs the
  library's environment detection: it classifies the host as App
  Engine, Cloud Functions, Cloud Run, GKE or GCE by fetching from the
  GCE metadata server. The first thing it does is `metadata.Get("")`
  — a plain HTTP GET to `http://169.254.169.254/computeMetadata/v1/`
  through the compute/metadata client, whose dialer times out after
  2 s. A dial timeout is a `Temporary()` error, so that client retries
  it, up to five attempts with a doubling backoff from 100 ms.
- On macOS the link-local address gets a cloned host route on the
  primary interface. While the kernel is still probing ARP for a
  neighbour that does not exist, each connect blocks until the 2 s
  timeout; when the kernel gives up it marks the route rejecting
  ("host is down") for a few seconds, and every connect fails at once.
  A run that lands while that negative entry is fresh pays nothing; a
  run that lands after it expired pays two or three timeouts plus
  backoff — the 4.5–7.2 s measured. A standalone program calling only
  `metadata.Get("")` reproduced it: 6.40 s once, then 0.00 s in nine
  following runs, all ending in "host is down".

Every branch of the detection needs the metadata server, and
gem-agent is macOS-only by design (ADR-0001): on this platform the
detection can never find anything, and when it finds nothing the
library falls back to the `global` resource labelled with the project
id. The probe buys nothing and costs up to seven silent seconds ahead
of the banner (interactive) or ahead of the stdin read and the first
model call (`-p`). It is silent because it happens before any status
machinery exists, and it is intermittent because it depends on the
kernel's neighbour cache — which is exactly why it read as "sometimes
slow, no idea why".

## Decision

### 1. The resource is declared, not detected

The GCP exporter builds its logger with
`logging.CommonResource({type: global, labels: {project_id: <[gcp].project>}})`
— exactly the value the detection falls back to on this platform.
The records are unchanged; the probe is gone. A test pins it by
pointing `GCE_METADATA_HOST` (which the metadata client honours) at a
counting fake: building gem-agent's logger reaches it zero times,
and the library's default path — the control — reaches it at least
once, so the test observes the probe it guards against rather than
assuming it.

### 2. No notice, because there is no wait left

ADR-0033 §2 and ADR-0067 say a deliberate wait must be announced.
This wait was not deliberate: nothing in the runtime chose it, and a
notice ("still creating the telemetry client after 2 s") would have
documented the defect instead of fixing it — and would have fired
only in the runs that paid the ARP timeout, telling the operator to
wait for something that should never happen. The rule this ADR adds
to the two above: a startup step earns a wait notice when the wait is
its contract (reading stdin to EOF is one); a wait that exists only
because a library's environment auto-detection was left switched on
is removed by switching the detection off.

### 3. Startup stays network-free before the first model call

Measured after the fix, 12 of 12 runs reached the stdin read
within 16 ms (4–16 ms) of launch — no slow mode. What still constructs a client
ahead of the first turn, `logging.NewClient` and `genai.NewClient`,
dials lazily and reads ADC from disk; the first bytes on the wire are
the first model call, and the first audit batch goes out on the
exporter's own goroutine. This is the property to keep: a future
startup step that must block on the network takes ADR-0067's shape
(a grace, one stderr line naming the cause and the remedy, a closing
line) — and first asks whether the block is a contract or an
auto-detection.

## Alternatives considered

- **Build the sink asynchronously, like `resolveWindow`** — rejected:
  it moves a pointless wait off the critical path instead of deleting
  it, and buys a queue for `SessionStart` and a race with the exit
  flush for the privilege.
- **Shorten the probe's timeout** — rejected: the logging library
  exposes no knob for its detection, and the dial timeout and retry
  policy are package-level defaults of the metadata client.
- **Announce it after a 2 s grace** — rejected, §2.
- **Point `GCE_METADATA_HOST` at an unroutable address at startup** —
  rejected: a process-wide environment variable set to steer one
  library is inherited by every child (shell commands, MCP servers,
  hooks), and it tells the same library's `OnGCE()` that this process
  *is* Google-hosted.

## Consequences

- The 4.5–7.2 s startup mode is gone in every mode; the interactive
  banner and the one-shot first call are no longer gated on the
  kernel's neighbour cache.
- The monitored-resource proto package becomes a direct dependency;
  it was already in the tree through the logging client.
- The declaration is unconditional. If gem-agent ever ran on a
  Google-hosted machine (it does not — ADR-0001) its entries would
  carry `global` rather than the host's resource. Accepted, and
  stated here rather than left to a detection that costs seconds.
- The trace that found this is not kept. A permanent timing trace
  would be a knob nobody reads; the method — an env-gated per-step
  trace and a dozen runs to catch an intermittent mode — is recorded
  here for the next time startup looks slow.

## References

- ADR-0001 (macOS-only: the platform on which the detection cannot succeed)
- ADR-0033 §2 (deliberate waiting must not look like a hang)
- ADR-0035 §2a / §4 (the GCP backend; telemetry must never hurt the session)
- ADR-0067 (the wait notice this ADR deliberately does not add)
