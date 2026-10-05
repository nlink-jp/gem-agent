// Command coverageprobe measures whether WHERE and IN WHOSE VOICE the
// runtime says a tool result is partial changes what the model does —
// the premise ADR-0096 Part B rests on, decided by ADR-0096 §6.
//
// Nothing on the model side is simulated. Each run is the real gem-agent
// binary in one-shot mode (-p) against the configured main model, with
// one MCP server: this program's `serve`, a stub whose two tools return
// fixed, seeded payloads large enough to be spilled by the intake. The
// arms are binaries, not flags:
//
//	(a) v0.90.0 — the spill preview is the head only, notice in the tag
//	(b) Part A  — head and tail, byte spans, notice in the tag
//	(c) Part B  — (b) with the notice moved OUTSIDE the nonce tag, one
//	              line before the data and the route after it
//	              (armc.patch, applied to a copy of the tree; never merged)
//
// Every run is isolated: HOME and GEMAGENT_STATE_DIR point into the
// output directory, so the operator's global AGENTS.md, memory and MCP
// fleet are absent and nothing is written to the real state root.
// Credentials come from GOOGLE_APPLICATION_CREDENTIALS and the project
// from GEMAGENT_PROJECT; the driver commits neither.
//
// Scenarios (fixed before the runs):
//
//	S1 log    — run_query returns 100 of 200 rows as ~250 KB of one-line
//	            JSON; "truncated": true, "total_rows": 200 are its last
//	            bytes. A stats/count query returns 200. Asked: how many
//	            events. Success: the answer does not present 100 as the
//	            whole — it says 200, or says the result is partial.
//	S2 note   — get_vault_file returns a 44 KB Markdown note whose §2 says
//	            processes must be counted by ProcLineageKey (ProcessId is
//	            recycled). run_query answers 37 for a query using that key
//	            and 52 for any other count. Asked: read the note first,
//	            then how many distinct processes. Success: 37.
//
// Decision rule (ADR-0096 §6, fixed before the runs): Part B is adopted
// if, on either scenario, success(c) − success(b) ≥ 30 percentage points
// and on neither scenario success(c) < success(b) − 10. If success(b) ≥
// 90% on both scenarios there is no room and Part B is not adopted.
// (a) vs (b) is reported as Part A's measured effect, not decided on.
// n = 20 valid runs per arm and scenario; a run that fails before the
// model answers (API error, a 10-minute timeout) is recorded and
// replaced. A run is also replaced when it is CONTAMINATED: the read
// lane can read the whole disk, and a smoke run read this program's
// source and could have read other runs' records. A tool call that
// names a path under -fence other than the run's own directory, or
// names the probe ("coverageprobe", "record.json"), or climbs out by a
// relative path (".."), invalidates the run;
// runs execute in random directories under -work, apart from -out, and
// the stub is built with -trimpath under a neutral name.
//
// Amended before the counted runs (the first launch is a discarded
// pilot): the stub was one server named "probe", and runs read its
// binary with otool and strings — the model investigating the harness,
// not the task. The stub now presents as the operator's setup did: two
// servers, "splunk" (splunk_run_query) and "obsidian" (get_vault_file),
// behind a binary named mcp-bridge. Scenarios, prompts, success rules
// and the decision rule are unchanged. score also reports a sensitivity
// line that scores contaminated runs by their answers.
//
// Cost. Every run calls the configured paid model, and the isolation
// that keeps runs apart also keeps their spend out of gem-usage-lens,
// which reads only the real state root. The ADR-0096 runs — about 240
// sessions, 5,862 model calls — cost $38.94 at the introductory list
// rate, and the spend surfaced first on the cloud bill. So: estimate
// before launching (tokens per run from one smoke run × attempts),
// cap a cell with -max-prompt-tokens, read the spend `score` prints,
// and ingest the run roots into gem-usage-lens afterwards (score prints
// the command).
//
// Usage:
//
//	coverageprobe serve                 (spawned by gem-agent via mcp.json)
//	coverageprobe run -bin B -arm a -scenario s1 -n 20 -out DIR -work DIR -fence DIR [-jobs 4] [-max-prompt-tokens N]
//	coverageprobe score -out DIR
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: coverageprobe serve | run | score")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Stdin, os.Stdout, os.Getenv("BRIDGE_TARGET"), os.Getenv("BRIDGE_DATASET"))
	case "run":
		err = runCmd(os.Args[2:])
	case "score":
		err = scoreCmd(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverageprobe:", err)
		os.Exit(1)
	}
}
