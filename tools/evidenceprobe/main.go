// Command evidenceprobe measures whether an agent that is asked to save a
// database query result as evidence writes down what the database
// returned — or loses rows, alters values, or invents them.
//
// The operator's report (2026-11-04): when a DWH result is small enough
// to be handed to the model inline, nothing of it exists on disk, so the
// only way to keep it as evidence is for the model to re-type it from its
// context with write_file. Values went missing on the way, and evidence
// appeared that the database never returned. A result above the inline
// cap (tools.OutputCap) is saved by the runtime and can be copied byte
// for byte. The candidate fix is to save every MCP result whatever its
// size. This probe measures the premise before anything is designed:
// does the failure reproduce, and does the existence of the saved file
// change it.
//
// Nothing on the model side is simulated. Each run is the real runtime
// binary (gem-agent or lagent) in one-shot mode (-p --auto) against its
// configured model, with one MCP server: this program's `serve`, which
// presents as the fleet's bigquery-mcp under the operator's server name
// (bigquery-nlink-dwh) — the same tool names, descriptions and schemas,
// results shaped the way that server shapes them. Queries really run:
// GoogleSQL is translated for the system sqlite3 and executed against a
// fixed, seeded table of 300 EDR process events, 60 of which match the
// task's host and hour. Every result the stub returns is logged; that
// log is the ground truth.
//
// Every run is isolated: HOME and the runtime's state directory point
// into the work area, so the operator's global AGENTS.md, memory, skills
// and MCP fleet are absent and nothing is written to the real state root.
// gem-agent's credentials come from GOOGLE_APPLICATION_CREDENTIALS and
// its project from GEMAGENT_PROJECT; the driver commits neither.
//
// Scenarios (fixed before the runs; prompts in run.go):
//
//	s1 small    — fetch the host's events for the hour and save all of
//	              the result as evidence under evidence/. The full result
//	              (60 rows, 8 columns) is under the inline cap.
//	s2 report   — investigate a suspected exfiltration in the same hour,
//	              write report.md, and save every query result the report
//	              rests on as evidence under evidence/. Same table.
//	s3 large    — s1's prompt against the table with a command_line
//	              column, so a full result is above the inline cap and the
//	              runtime saves it to the work directory. Whether a run's
//	              result was in fact spilled is recorded per run (a query
//	              that leaves command_line out is not).
//
// Scoring (score.go, fixed before the runs). Evidence files are the files
// under the project's evidence/ (failing that, any file but report.md,
// flagged misplaced). Token classes are read from the evidence and from
// the rows the stub returned in that run: event_id, sha256, remote_ip
// (documentation ranges only), bytes_out (presence only), event_time
// (reported, not part of the category). A token present in evidence but
// never returned is alien: "altered" when within a quarter of its length
// (edit distance) of a returned token, "fabricated" otherwise; a 64-hex
// token equal to the SHA-256 of a project file or of a returned result is
// a checksum, not data. Categories per run:
//
//	exact     — an evidence file holds a returned result verbatim, or is
//	            JSON equal to one or to its rows
//	faithful  — not exact, no headline token missing or alien
//	lossy     — headline tokens missing, none alien
//	corrupted — any alien headline token
//	none      — no evidence file
//
// Reading of the results (fixed before the runs):
//
//	R1 reproduced — on a runtime, s1 or s2 has lossy + corrupted in at
//	   least 2 of the n valid runs.
//	R2 the file helps — faithful rate (exact + faithful) on s3, over the
//	   runs whose result was spilled, exceeds s1's by at least 30 points.
//	   Unspilled s3 runs are reported beside it, not pooled.
//
// Amended after the pilot (one lagent s1 run, discarded, not scored): the
// agent re-typed the result as CSV with write_file, every headline value
// intact, every timestamp rewritten as "2026-10-28 01:00:04.976952". The
// scorer now counts the same instant in another layout as reformatted,
// not missing. Scenarios, prompts, categories and readings are unchanged.
// The pilot spent 132,911 prompt tokens (105,533 cached) and 10,021
// output+thought tokens in 156 s.
//
// Amended 2026-11-04, before any s3 run (the operator's direction): the
// question was the wrong one. A re-typed file has no provenance however
// accurate it is, and how often re-typing errs is not a number evidence
// can rest on — ten clean runs bound the rate only to about 26% (95%).
// What the pilots showed is the structure: with no saved file, all four
// s1 runs so far (gem 1 and lagent 1 pilot, lagent 2 counted) regenerated
// the evidence through write_file, one of them scored "exact" only
// because its JSON parses equal. So:
//
//   - s1 and s2 are dropped. The two counted lagent s1 runs are kept on
//     disk and reported, not scored; R1 is withdrawn. No gem runs.
//   - The scorer gains the method axis (classifyMethod: copied /
//     derived / regenerated / mixed / unknown), the one that matters for
//     evidence. The categories stay, beside it.
//   - The one premise left to measure is the fix's own: when the
//     runtime HAS saved the result (s3, spilled), does the agent copy or
//     transform the saved file, or still regenerate the evidence?
//     R3, reported, not decided on: of the spilled s3 runs, how many are
//     copied or derived, how many regenerated or mixed. R2 is withdrawn
//     with s1. lagent only, n = 10.
//
// Amended 2026-11-04 again, before any run against the implementation
// (ADR-0105 §5, pre-registered here as that ADR requires). The lagent s3
// cell above found 0 copies in 10. ADR-0105 makes every MCP result a
// saved file, records it where no server can write, offers the copy in
// gem-agent's own note outside the nonce tag, and gives write_file a
// `from` that copies a file byte for byte at the same tier as writing
// content. Whether the agent now takes that route is measured:
//
//   - Cells: gem-agent s1 (inline) and s3 (spilled), prompts unchanged,
//     n = 10 valid runs each. s3 counts only runs whose result was
//     spilled; the spill marker ("too large to hold inline") is kept in
//     the runtime's wording.
//   - Copied now means byte-identical to a returned result, whatever
//     wrote it. A write_file with `from` is classified by the bytes it
//     wrote: identical is copied, anything else is a copy of something
//     else (scored with unknown). A shell write is copied only when
//     identical; a superset is derived. Derived and regenerated are
//     reported beside it.
//   - R4: the route fires when at least 8 of 10 valid runs in each cell
//     are copied. Derived does not count — ADR-0105 leaves derivation
//     unchanged, so counting it could pass without the route firing. A
//     valid run with no evidence file counts as not copied.
//   - Below the threshold the wording of the note and the write_file
//     description is revised once and the cells are measured once more;
//     if still below, the operator decides what comes next.
//   - Cost, estimated before the runs: s1 $0.218 per run (the pilot), s3
//     about $0.35 (scaled by the lagent s3 prompt tokens against the s1
//     pilot, about 1.6x); about $6 for both cells, about $12 if every
//     run is replaced up to 2n. Launched only after the operator approves
//     the figure.
//
// Neither decides the design; they decide whether the premise of the fix
// holds. n = 10 valid runs per runtime and scenario. A run that fails
// before the model answers (API error, timeout) is recorded and replaced,
// up to 2n attempts. Which runs those are is read from how the turn
// ended in the transcript, a closed set of record kinds (run.go,
// turnEnd): a completion the server ended with finish reason "error", a
// lost connection, a request with no model turn recorded after it, or
// the timeout. An answer, a turn the runtime stopped on the model's own
// behaviour (the loop guard, a full window) and a model that kept
// returning nothing are the model's outcomes and are scored. Until
// 2026-11-05 a run counted as answered when any model text existed, so a
// server failing after the model's first words was scored as a run with
// no evidence. A run is also replaced when CONTAMINATED: a tool
// call names a path under -fence other than the run's own directory,
// names the probe or its records, or climbs out by "..". The
// contamination rate and a sensitivity line scoring contaminated runs by
// their evidence are reported with every cell (knowledge,
// llm-integration.md: probes are built on the premise that the agent
// investigates the test itself).
//
// Cost. lagent runs on a local server and costs nothing. gem-agent runs
// call the paid model, and the isolation keeps their spend out of
// gem-usage-lens; estimate from one smoke run before launching, cap a
// cell with -max-prompt-tokens, and ingest the run roots afterwards.
//
// Usage:
//
//	evidenceprobe serve        (spawned by the runtime via mcp.json; BQ_LOG, BQ_WIDE)
//	evidenceprobe run -runtime gem|lagent -bin B -scenario s1|s2|s3 -model M [-risk-model R -summary-model S] -n 10 -out DIR -work DIR -fence DIR [-jobs N]
//	evidenceprobe score -out DIR
//	evidenceprobe rescore -out DIR   (re-apply the scorer to records on disk)
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: evidenceprobe serve | run | score")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Stdin, os.Stdout, os.Getenv("BQ_WIDE") == "1", os.Getenv("BQ_LOG"))
	case "run":
		err = runCmd(os.Args[2:])
	case "score":
		err = scoreCmd(os.Args[2:])
	case "rescore":
		err = rescoreCmd(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "evidenceprobe:", err)
		os.Exit(1)
	}
}
