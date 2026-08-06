#!/usr/bin/env python3
"""PB-DOC-7: enforce the Phase B requirement-ownership manifest.

Rounds 2, 3 and 4 of the audit committee each found requirements that were written
into the spec but never wired into a slice (homeless PB-KEY-2, then PB-STATE-10 and
PB-SAS-2, then PB-GW-7/8, PB-KEY-8, PB-PUSH-10). Ownership lived in prose, so the
error was only ever caught by a human reading carefully. This makes it mechanical.

Fails if a requirement defined in the spec is unowned, owned more than once, or if
the manifest names an id the spec does not define; if the slice DAG declares a slice
twice or has a dangling edge, a cycle, or an orphan; or if section 11's readable
table CONTRADICTS either authoritative file, including by using a wildcard.

Usage:
    check-phaseb-manifest.py [ROOT] [--strict-section11]

ROOT defaults to the repository this script lives in. Taking it as an argument is
what makes negative controls possible: internal/verify/phaseb_manifest_test.go
stages mutated copies of the three inputs and requires this script to reject each
one. Before that, "verified against a negative control" meant a human had deleted a
row once, and the checker read the repository unconditionally -- so every mutation
test that could have been written would have passed vacuously.

SECTION 11 IS CHECKED FOR CONTRADICTION BY DEFAULT, COMPLETENESS ONLY UNDER --strict.
Section 11 declares its own contract ("The slice table above is the readable view;
the manifest is the source of truth"), so a row that omits an owned requirement is
drift while a row that assigns one to the WRONG slice is the readable table lying
about the authoritative one. The default rejects the second. --strict-section11 also
rejects the first, and is how an operator amending section 11 knows when they are
done; it does not pass today, and `section 11 drift` in the S20 evidence file says
exactly which rows are behind.
"""
import re, sys, pathlib

argv = [a for a in sys.argv[1:]]
STRICT = "--strict-section11" in argv
argv = [a for a in argv if not a.startswith("--")]
ROOT = pathlib.Path(argv[0]).resolve() if argv else pathlib.Path(__file__).resolve().parents[1]

SPEC = ROOT / "docs/specifications/remote-phaseB-requirements.md"
MANIFEST = ROOT / "docs/specifications/remote-phaseB-manifest.tsv"
SLICES = ROOT / "docs/specifications/remote-phaseB-slices.tsv"

# NOTE: [A-Z0-9]+ not [A-Z]+ -- the PB-E2E-* family contains a digit, and a narrower
# pattern silently drops the entire family (which is how it was first missed).
ID = re.compile(r"^\|\s*(~~)?\*{0,2}(PB-[A-Z0-9]+-\d+)\*{0,2}(~~)?\s*\|")

spec_text = SPEC.read_text()
defined, withdrawn = set(), set()
for line in spec_text.splitlines():
    m = ID.match(line)
    if m:
        (withdrawn if m.group(1) else defined).add(m.group(2))
defined -= withdrawn

owned, errors_early = {}, []
for raw in MANIFEST.read_text().splitlines():
    if not raw.strip() or raw.startswith("#"):
        continue
    parts = raw.split("\t")
    if len(parts) != 2:
        errors_early.append(f"PARSE     manifest line is not '<id>\\t<slice>': {raw!r}")
        continue
    rid, slice_ = parts
    owned.setdefault(rid, []).append(slice_)

errors  = list(errors_early)
errors += [f"UNOWNED   {r}" for r in sorted(defined) if r not in owned]
errors += [f"MULTIOWN  {r} -> {v}" for r, v in sorted(owned.items()) if len(v) > 1]
errors += [f"PHANTOM   {r} (in manifest, not defined in the spec)" for r in sorted(set(owned) - defined)]
errors += [f"WITHDRAWN {r} (withdrawn in the spec but still owned)" for r in sorted(set(owned) & withdrawn)]

# --- slice DAG: acyclic, and no orphan slice ---------------------------------
deps, terminal = {}, set()
for raw in SLICES.read_text().splitlines():
    if raw.startswith("#terminal:"):
        terminal = {t.strip() for t in raw.split(":", 1)[1].split(",")}
        continue
    if not raw.strip() or raw.startswith("#"):
        continue
    name, d = raw.split("\t")
    # A slice declared twice leaves the DAG decided by line order, and drops one of the two
    # dependency sets unseen -- which is how the orphan negative control went stale: it
    # injected a second row for S22 and the later row silently won.
    if name in deps:
        errors.append(f"DUPSLICE  {name} is declared twice; one dependency set would be "
                      "discarded by line order")
        continue
    deps[name] = [] if d.strip() == "-" else [x.strip() for x in d.split(",")]

for rid, owners in owned.items():
    for o in owners:
        if o not in deps:
            errors.append(f"BADSLICE  {rid} owned by unknown slice {o}")
for name, ds in deps.items():
    for d in ds:
        if d not in deps:
            errors.append(f"DANGLING  {name} depends on unknown slice {d}")

WHITE, GREY, BLACK = 0, 1, 2
mark = {n: WHITE for n in deps}
def visit(n, path):
    if mark[n] == GREY:
        errors.append(f"CYCLE     {' -> '.join(path + [n])}"); return
    if mark[n] == BLACK:
        return
    mark[n] = GREY
    for d in deps.get(n, []):
        if d in deps:
            visit(d, path + [n])
    mark[n] = BLACK
for n in list(deps):
    visit(n, [])

# Every slice must be reachable from the exit demonstration, else its work can be
# skipped while the exit gates still pass -- the round-5 orphan defect.
reach, stack = set(), ["S19"]
while stack:
    n = stack.pop()
    if n in reach:
        continue
    reach.add(n)
    stack += deps.get(n, [])
for n in deps:
    if n not in reach and n not in terminal:
        errors.append(f"ORPHAN    {n} is not reachable from S19; its requirements could be skipped")

# --- section 11: the readable table must not contradict the authoritative files ---
#
# PB-DOC-7 names section 11 explicitly ("A test parses section 11 and the requirement
# tables"), and the wildcard clause exists because of a defect that lived ONLY there:
# v3 gave both S19 and S20 the dependency "all", which read literally made each depend
# on the other. Nothing that reads only the TSVs can see that.

ITALIC_NOTE = re.compile(r"\*\([^()]*\)\*")
PAREN = re.compile(r"\([^()]*\)")
FAMILY_WILDCARD = re.compile(r"(PB-[A-Z0-9]+)-\*")
SLICE_TOKEN = re.compile(r"^S\d+[a-z]?$")
SLICE_LEAD = re.compile(r"^\s*(S\d+[a-z]?)\b")

def clean_cell(cell):
    """Strip markdown emphasis and explanatory notes, preserving family wildcards.

    Parentheticals are removed FIRST and completely, because several of them name
    requirements they explicitly disclaim ("PB-DOC-6 withdrawn", "PB-DOC-1 is owned
    by S0"); a parser that read them would invent ownership out of a footnote.
    Family wildcards are rewritten before emphasis stripping so that `PB-TOK-*` does
    not decay into `PB-TOK-` when the asterisks go.
    """
    cell = FAMILY_WILDCARD.sub(r"\1-ALLIDS", cell)
    cell = ITALIC_NOTE.sub(" ", cell)
    prev = None
    while prev != cell:
        prev = cell
        cell = PAREN.sub(" ", cell)
    return cell.replace("**", " ").replace("`", " ").replace("~~", " ")

def section11_rows(text):
    lines = text.splitlines()
    starts = [i for i, l in enumerate(lines) if l.startswith("## 11.")]
    if not starts:
        return None
    start = starts[0]
    ends = [i for i, l in enumerate(lines) if i > start and l.startswith("**Ownership is machine-checked")]
    end = ends[0] if ends else len(lines)
    out = []
    for l in lines[start:end]:
        if not l.startswith("| ") or l.startswith("|---") or "| Requirements |" in l:
            continue
        out.append(l)
    return out

rows = section11_rows(spec_text)
s11_req, s11_dep, parsed_rows = {}, {}, 0
if rows is None:
    errors.append("S11MISSING section 11 (## 11.) is not in the spec; its readable slice table "
                  "cannot be cross-checked")
else:
    for row in rows:
        cells = row.strip().strip("|").split("|")
        if len(cells) < 4:
            errors.append(f"S11PARSE  section 11 row has {len(cells)} cells, want 4: {row[:70]!r}")
            continue
        lead = SLICE_LEAD.match(clean_cell(cells[0]).strip())
        if not lead:
            errors.append(f"S11PARSE  section 11 row does not start with a slice id: {row[:70]!r}")
            continue
        name = lead.group(1)
        parsed_rows += 1
        reqs, family = set(), None
        for tok in re.split(r"[,;]", clean_cell(cells[1])):
            tok = tok.strip()
            if not tok:
                continue
            if tok.lower() in ("all", "*", "any"):
                errors.append(f"WILDCARD  {name} owns {tok!r} in section 11; ownership must be enumerated")
                continue
            m = re.match(r"^(PB-[A-Z0-9]+)-(.+)$", tok)
            if m:
                family, rest = m.group(1), m.group(2)
            else:
                rest = tok
            if family is None:
                errors.append(f"S11PARSE  {name}: {tok!r} names no requirement family")
                continue
            if rest == "ALLIDS":
                reqs |= {r for r in defined if r.startswith(family + "-")}
                continue
            for piece in rest.split("/"):
                rng = re.match(r"^(\d+)\.\.(\d+)$", piece)
                if rng:
                    reqs |= {f"{family}-{n}" for n in range(int(rng.group(1)), int(rng.group(2)) + 1)}
                elif re.match(r"^\d+$", piece):
                    reqs.add(f"{family}-{piece}")
                else:
                    errors.append(f"S11PARSE  {name}: cannot read requirement token {tok!r}")
        ds = set()
        for tok in re.split(r"[,;]", clean_cell(cells[3])):
            tok = tok.strip()
            if not tok or tok in ("-", "—", "–"):
                continue
            if tok.lower() in ("all", "*", "any"):
                errors.append(f"WILDCARD  {name} depends on {tok!r} in section 11; every edge must "
                              "be enumerated (v3 gave S19 and S20 'all', making each depend on the other)")
                continue
            if not SLICE_TOKEN.match(tok):
                errors.append(f"S11PARSE  {name}: cannot read dependency token {tok!r}")
                continue
            ds.add(tok)
        s11_req.setdefault(name, set()).update(reqs)
        s11_dep.setdefault(name, set()).update(ds)

    if parsed_rows < 20:
        errors.append(f"S11PARSE  only {parsed_rows} section 11 rows parsed; the table's shape has "
                      "changed and this cross-check is measuring nothing")

    for name in sorted(s11_req):
        if name not in deps:
            errors.append(f"S11SLICE  {name} has a section 11 row but is not in the slice DAG")
            continue
        for rid in sorted(s11_req[name]):
            real = owned.get(rid)
            if real is None:
                errors.append(f"S11REQ    {name} claims {rid}, which the manifest does not own")
            elif name not in real:
                errors.append(f"S11REQ    {name} claims {rid}, which the manifest gives to {'/'.join(real)}")
        for d in sorted(s11_dep[name] - set(deps[name])):
            errors.append(f"S11DEP    {name} claims a dependency on {d} that the slice DAG does not have")

    if STRICT:
        for name in sorted(deps):
            if name not in s11_req:
                errors.append(f"S11MISS   {name} has no section 11 row at all")
                continue
            gap = {r for r, v in owned.items() if name in v} - s11_req[name]
            if gap:
                errors.append(f"S11MISS   {name} omits {', '.join(sorted(gap))}")
            gap = set(deps[name]) - s11_dep[name]
            if gap:
                errors.append(f"S11MISS   {name} omits the dependency edge(s) {', '.join(sorted(gap))}")

print(f"slices: {len(deps)} ({len(reach)} on the S19 exit path, terminal: {sorted(terminal)})")
print(f"spec: {len(defined)} active requirements ({len(withdrawn)} withdrawn) | manifest: {len(owned)} owned")
print(f"section 11: {parsed_rows} readable rows cross-checked"
      f"{' (STRICT: completeness required)' if STRICT else ' (contradictions only; --strict-section11 also requires completeness)'}")
if errors:
    print("\n".join(errors)); sys.exit(1)
print("manifest OK: every requirement owned exactly once")
