# ADR-0122: a place is taken once

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-18) |
| Date | 2026-11-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Three rounds of review in one day each found a place the fixes before had not looked at. Operator: this is patching; list every such place and design from the list |
| Relates to | ADR-0121 (the sandbox checks' probes), ADR-0120 (opens that cannot wait), ADR-0073 (the lanes), ADR-0099 §12 (how an added directory is taken) |

## Context

A lane writes the project, the session's work directory with its scratch and cache, an added directory and the temporary directories. What a lane writes it can replace: remove the directory and put a link under its name.

The runtime kept names for these places and used them again later. The fixes of ADR-0120 and ADR-0121 each closed one such use where a review had found it, and each review found another. So every call in the runtime that names a file by path or starts a process was listed, mechanically, and given a class: 159 calls. Eighteen name a place a lane writes; the rest are in the runtime's own state directory, read only, or right already. The eighteen have one cause.

Measured on both runtimes, under the real profiles:

| What the write lane did | What the next build of the lanes gave it |
|---|---|
| removed its work directory and put a link to a directory beside the project under its name | write access to that directory — a file there was written and another overwritten — with the startup checks passing and the session reported confined. The read lane wrote a `scratch` directory there |
| the same with the project directory, in a project it could empty | the same |
| the same in a project holding its git configuration | nothing: the lane is refused the removal of that file, and a directory that is not empty cannot be replaced |

The lanes are built at startup and again in a session — by `/clear`, and by the granting of a directory — and each build resolved the project's and the work directory's names anew and anchored the profiles on what it found. A result saved into a work directory that had become a link was made where the link led (measured). Removing a work directory that had become a link removed the link and nothing else (measured).

An added directory was already handled the other way, since the reviews of ADR-0099: opened once, its real path read back from what was opened, and that path used from then on.

## Decision

1. **A place is taken once** (`internal/place`). Taken means opened as a directory and held for the life of the process, with its real path as it was then. The project is taken at the first build of the lanes, at startup. A work directory, its cache and its scratch are taken when the runtime makes them, from the directory above — which is in the state tree or is itself a place — by one name and not through a link.
2. **Whoever needs a place asks for it by the name it was taken under, and gets the place held or an error.** The name is looked at first: it must still lead, the last step through no link, to the directory held — what the name holds now and what the descriptor holds now are the same directory. A name that leads elsewhere is `ErrReplaced`. A name nothing was taken under is `ErrNotTaken`, a fault of the runtime: a place taken late would be whatever a lane had put there.
3. **The profiles are anchored on the real paths of the places, and no name is resolved again.** That alone keeps a lane where it was: a link under a place's name leads out of every anchor. The look of 2 is what tells the session.
4. **A build that finds the project or the work directory replaced builds nothing.** The session is told which place, every shell command asks from there on, and the lanes that were built before stay in the way. A scratch or a cache that was replaced is not given to a lane: the read lane goes without its scratch, and asks.
5. **What the runtime itself makes, writes or removes in a place goes through the directory held, by one name**: the scratch directory, the directories of the checks, and a result saved into the work directory — a temporary file made exclusively and renamed over the result's name, both names of that one directory.
6. **The file tools open a place through it**: the root they get is opened by the place's path and handed over only when it is the directory held. The file-read child is a process of its own and takes nothing; it resolves the paths it is given, inside its cage.
7. **The list is a test.** `TestEveryPathCallHasItsClass` gives every function that names a file by path, or starts a process, one of six classes — an entrance, read only, the operator's input, a file the operator named, the system's, the runtime's own state — and fails on a call in a function the table does not name. There is no class for making, writing or removing something by path in a place a lane writes. `TestProfilesAreNotAnchoredOnANameResolvedAgain` holds the functions that resolve a name to the ones that take or judge a place.

## Consequences

- An operator whose session meets a replaced place reads `sandbox unverified: <path> is no longer the directory this session took`, and is asked before every shell command until a new session is started. Nothing is lost: the lanes built before still confine what runs.
- The places stay open for the life of the process: a few descriptors a session.
- The table of places is the process's, not a session's: a process is one session, and a place taken under a name answers to that name wherever it is asked for.
- Not changed, and recorded: the programs the operator configured to run outside the sandbox — hooks, MCP servers — are given the work directory by its name, and what they do with a name is theirs.
- Not changed: an added directory is taken as before, by the grant, and the checks open it by its path (ADR-0121's residual stands for it).
- A project replaced before a session starts is another project: what is read from it is compared and asked about as any project's is.

## Tests the design requires

- The write lane, under the real profile, replaces its work directory by a link; the next build is refused as a replaced place, the lanes built before do not write where the link leads, and nothing is made there. The same for a project it can empty, with the lanes built as `/clear` builds them; a project holding its git configuration cannot be replaced.
- `/clear` and a grant that find a place replaced leave the session asking before every shell command, and say which place.
- A scratch directory replaced by a link is not given to a lane.
- A result is saved through the work directory held: nothing is made through a replaced work directory, and a link, a FIFO or other bytes under the result's name are replaced by the result without a wait.
- The file tools and the checks refuse a replaced place.
- A place: every way its name can come to lead elsewhere; a sub-place through a link; a name never taken.
- Each property put back is seen by a test.

## Alternatives considered

- **Hand the places to every function that needs one** — not taken: the same rule with every signature changed, and nothing that makes a function ask. One entrance that looks before it answers does both.
- **Take a place the first time it is asked for** — not taken: asked for late, it is whatever a lane has put under the name by then.
- **End the session when a place is found replaced** — not taken: the lanes built before still hold, and the operator's work is in the session.
- **Close the profile's anchors and leave the rest** — not taken: it is the fourth fix of one cause, and the three before it are why the list was made.
