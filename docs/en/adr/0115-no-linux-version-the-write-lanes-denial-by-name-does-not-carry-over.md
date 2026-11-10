# ADR-0115: No Linux version — the write lane's denial by name does not carry over

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-11) |
| Date | 2026-11-11 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A user asked for a Linux version. Windows was to be served by running that version under WSL2, and the sandbox control was to be put behind an abstraction so a second mechanism could sit beside Seatbelt |
| Relates to | ADR-0001 (the mechanism; "macOS-only" as a design constraint), ADR-0073 (lanes; the kernel judges a shell command, its text does not), ADR-0072 and ADR-0074 (persistent files), ADR-0086 (the kernel reads the file) |

## Context

ADR-0001 made gem-agent macOS-only because the isolation is built on `sandbox-exec`. That was a statement about the mechanism chosen. Whether another kernel could carry the same guarantees had not been measured. This record measures it and decides.

What has to carry over is not "a sandbox" but the lanes of ADR-0073, and one property of the write lane in particular:

- A write to a persistent file — `AGENTS.md`, `CLAUDE.md`, `.mcp.json`, `.gem-agent.toml`, `.claude/`, `.git/hooks`, `.git/config` and the rest of `sandbox.PersistentFiles` — through `write_file` or `edit_file` always asks the operator. The model tier may not answer.
- A `shell_exec` call in the write lane is not judged by its text at all (ADR-0073: text is an unbounded domain). What keeps it from writing those files is the kernel: the write-lane profile denies them **by name, at any depth, whether or not the file exists yet**. Whoever approves a write-lane command — the operator, or under auto-approve the model tier — has not approved a new instruction file; that needs the operator lane, which only the operator answers and whose approval says so.

A model that is refused by `write_file` tries `shell_exec` next. On macOS that attempt fails in the kernel whatever the command's spelling. This is the property the product rests on: the difference between "may change the project" and "may change what later sessions trust" is enforced below the operator's ability to read a command line.

## Measurement

Two Linux machines, one run each, 2026-11-11, as an unprivileged user:

- a Fedora CoreOS 44 virtual machine, kernel 7.1, arm64 — Landlock ABI 9, SELinux;
- an Ubuntu 24.04 container on a Proxmox VE host, kernel 7.0-pve, amd64 — Landlock not enabled in that kernel (`landlock_create_ruleset` answers `EOPNOTSUPP`).

Two mechanisms were tried: Landlock alone (rules added through the three system calls, then `exec`), and namespaces (`unshare` with a user and a mount namespace, the protections arranged as bind mounts, then a nested user namespace as the operator's own uid so the command holds no capability). Every probe that should be refused was first run without the mechanism and had to succeed there; a refusal that also happens outside proves nothing (ADR-0073 §5). The questions, the reading of a result and a one-run stop rule were written into the script before the first run. One probe of the first round was malformed (its control unmounted where nothing was mounted) and was redone on its own, with the bare `umount2` call.

| What the macOS lanes guarantee | Landlock alone | Namespaces |
|---|---|---|
| No write outside the project | refused | not measured alone; refused together with Landlock |
| An existing `AGENTS.md` cannot be written, unlinked or renamed | **allowed** | refused |
| A new `AGENTS.md` cannot be created | **allowed** | **allowed** |
| A directory holding an `AGENTS.md` cannot be renamed | not measured | **allowed** |
| An existing `.env` cannot be read | **allowed** | refused (`/dev/null` bound over it) |
| A credential directory cannot be read | refused, when reads are an allowlist of trees | refused (an empty filesystem mounted over it) |
| No TCP connection | refused | refused |
| No signal to the parent | refused | refused |
| No connection to an abstract UNIX socket | refused | refused |
| No connection to a UNIX socket at a path | **allowed** | **allowed** |
| The command cannot remove the protection | — | refused (`EPERM`) |
| `ls`, `cat`, a pipe, `git init` and `git status` still run | ran | ran |

The namespace column was the same on both machines. The kernel documentation for Landlock (read 2026-11-11) agrees with the first column: a rule names a file hierarchy and the access it allows; there are no deny rules and no name patterns.

So: what exists can be protected, and what does not exist yet cannot. Neither mechanism can refuse the creation of a file because of its name inside a tree the command may otherwise write.

## Decision

1. **There is no Linux version.** gem-agent stays macOS-only. ADR-0001's consequence stands, and its reason is now measured rather than inherited from the mechanism: on Linux a write-lane command creates a new `.mcp.json`, a new `AGENTS.md` in a subdirectory, or a `.claude/` that was not there, and the only thing between the model and that file is a reading of the command line — the operator's, or under auto-approve the model tier's — the boundary ADR-0001 was written to not depend on.
2. **WSL2 is not offered as the Windows route**, for the same reason: it is the Linux version.
3. **The sandbox control is not put behind an abstraction.** An interface over one implementation, kept for a second one that is decided against, is cost without a caller. `internal/sandbox` stays as it is.
4. **This is reopened by a mechanism, not by demand.** The condition is an unprivileged kernel facility that refuses creation and rename by name pattern inside a writable tree, present on the distributions people run, and shown by the probes above — each with its control — to refuse a new persistent file. A weaker guarantee on one platform is not a version of this product.

## Consequences

- The Makefile keeps its single target, and `AGENTS.md` and `CLAUDE.md` keep "do not add linux/windows build targets", now pointing here.
- A Linux user is not served. That is the cost, and it was weighed against shipping the product's name on a guarantee it would not hold there.
- lagent shares `internal/sandbox` and the lanes. Nothing in this measurement is specific to gem-agent.

## Alternatives considered

- **Unconfined as the Linux default** (every shell call is the operator's, as under `--no-sandbox`) — the cheapest port, and no false claim: the runtime already says what it could not establish. Not taken: it makes operator approval the only boundary in the normal mode of use. `--no-sandbox` exists for debugging (ADR-0001), not as a product.
- **Reproduce the read lane; make every write-lane command ask** — the read lane's guarantees did carry over in the measurement. Not taken: approving a write-lane command would then also approve any new persistent file it creates, so the write and operator lanes are one lane for new names, and the approval text could not honestly say otherwise.
- **Detect instead of prevent** — compare the persistent-file snapshot after every write-lane command and tell the operator what appeared. Cheap, and the machinery exists (ADR-0074, ADR-0099 §10). Not taken as the basis of a platform: it reports after the write, and another tool that reads the same files may read them first.
- **Write into an overlay, then carry the result into the project through the exact name matcher** — judges the files that came to exist, not the command's text, so it is the one route that might hold the guarantee. Not measured and not taken: the merge (deletions, renames, directories), the cost for a build that writes a great deal, and writes that other processes do not see until the command ends are a second file-system layer to maintain for one request.
- **Intercepting system calls from a supervisor** — not examined.

## Not measured

- A real Ubuntu host, where AppArmor may restrict unprivileged user namespaces; the container measured runs on its host's kernel and says nothing about it.
- WSL2 itself. Only Microsoft's published kernel configuration (6.6.107.1, x86 and arm64) was read: Landlock and user, PID and network namespaces are enabled there.
- The controlling terminal, UDP, and Landlock's access right for UNIX sockets at a path.

None of these could change the decision: each bears on how much else would carry over, not on creation by name.

## References

- Landlock: unprivileged access control — <https://docs.kernel.org/userspace-api/landlock.html>
- ADR-0001, ADR-0073, ADR-0074
