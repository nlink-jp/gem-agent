#!/usr/bin/env python3
"""Move every `[file.go:NNN](path)` citation in docs/*/adr/*.md to where its line went.

    scripts/remap-adr-citations.py [BASE]        # BASE defaults to HEAD

The ADRs cite code by line, and TestADRFileCitationsResolve fails when an edit
shifts a cited line. Fixing the reported citation by hand is the wrong repair:
the test only notices a citation that landed on a line WITHOUT one of its
paragraph's identifiers, so a shifted citation that happens to land on another
line naming the same thing passes while pointing at the wrong place. This maps
every citation into every file changed since BASE, by diffing BASE's text
against the working tree.

It is safe to run twice. The number to map is read from BASE's copy of the ADR,
never from the working copy — the first version read the working copy, and a
second run moved citations it had already moved. The citations must have been
right at BASE. A cited line that was itself edited has no mapping, and an ADR
whose citations were added or removed since BASE cannot be paired up; both are
reported for a person, and the exit status is 1.
"""
import difflib, glob, os, pathlib, re, subprocess, sys

repo = str(pathlib.Path(__file__).resolve().parent.parent)
base = sys.argv[1] if len(sys.argv) > 1 else "HEAD"
cite = re.compile(r'\[([A-Za-z0-9_.-]+\.(?:go|mod|md)):(\d+)\]\(([^)]+)\)')


def git_show(path):
    out = subprocess.run(["git", "-C", repo, "show", f"{base}:{path}"], capture_output=True, text=True)
    return out.stdout if out.returncode == 0 else None


changed = subprocess.run(["git", "-C", repo, "diff", "--name-only", base],
                         capture_output=True, text=True, check=True).stdout.split()
maps = {}
for f in changed:
    if not f.endswith((".go", ".mod")):
        continue
    old = git_show(f)
    if old is None:
        continue
    a, b = old.split("\n"), pathlib.Path(repo, f).read_text().split("\n")
    m = {}
    for op, i1, i2, j1, j2 in difflib.SequenceMatcher(None, a, b, autojunk=False).get_opcodes():
        if op == "equal":
            for k in range(i2 - i1):
                m[i1 + k + 1] = j1 + k + 1
    maps[os.path.normpath(os.path.join(repo, f))] = m

moved = problems = 0
for md in sorted(glob.glob(repo + "/docs/*/adr/*.md")):
    p = pathlib.Path(md)
    work = p.read_text()
    then = git_show(os.path.relpath(md, repo))
    if then is None:
        continue  # a new ADR: its citations were written against the working tree
    before = [(mo.group(1), int(mo.group(2)), mo.group(3)) for mo in cite.finditer(then)]
    now = list(cite.finditer(work))
    if [(b[0], b[2]) for b in before] != [(mo.group(1), mo.group(3)) for mo in now]:
        if any(os.path.normpath(os.path.join(os.path.dirname(md), b[2].split("#")[0])) in maps for b in before):
            problems += 1
            print("  CANNOT PAIR (citations added or removed since", base + "):", os.path.basename(md))
        continue
    out, last = [], 0
    for (name, n, link), mo in zip(before, now):
        m = maps.get(os.path.normpath(os.path.join(os.path.dirname(md), link.split("#")[0])))
        target = n if m is None else m.get(n)
        if target is None:
            problems += 1
            print("  NO MAPPING (the cited line itself was edited):", os.path.basename(md), f"{name}:{n}")
            target = int(mo.group(2))
        if target != int(mo.group(2)):
            moved += 1
            print("  ", os.path.basename(md), f"{name}:{mo.group(2)} -> {target}")
        out.append(work[last:mo.start()] + f"[{name}:{target}]({link})")
        last = mo.end()
    out.append(work[last:])
    if "".join(out) != work:
        p.write_text("".join(out))
print(f"{moved} moved, {problems} needing a person")
sys.exit(1 if problems else 0)
