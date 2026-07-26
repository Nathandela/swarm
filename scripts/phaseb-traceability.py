#!/usr/bin/env python3
"""Emit the Phase B requirement traceability index.

The final audit validates against every requirement, not every slice, so the
question it needs answered per ROW is: which slice owns this, has that slice
shipped, and where is the evidence. A gap here is invisible in a per-slice view --
it shows up as a run of requirements whose only record is a commit message. It has
already caught one requirement that was reported shipped because its owning SLICE
had shipped, while the requirement itself was not met (PB-TOK-1).

Regenerate with:  python3 scripts/phaseb-traceability.py > docs/verification/remote-phaseB-traceability.md
Checked by scripts/check-phaseb-manifest.py for ownership; this script does not
re-verify ownership, it reports status.
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFEST = os.path.join(ROOT, "docs/specifications/remote-phaseB-manifest.tsv")
VERIF = os.path.join(ROOT, "docs/verification")

# Slices whose implementation has landed on the branch. Kept explicit rather than
# inferred from git: a slice is shipped when its work is committed AND gated, and
# only the orchestrator knows the second half.
#
# THIS LIST IS AN ASSERTION, NOT A MEASUREMENT, and the report says so where it is
# read. Nothing here verifies that a named slice landed or that its gate was green --
# editing one string makes 5 requirements read as shipped. It is the one input to this
# report that cannot be checked by running it, so the report prints the MEASURED
# evidenced count beside it rather than letting a single number carry both meanings.
SHIPPED = [
    "S0", "S1", "S1b", "S2", "S2b", "S3", "S4", "S4b", "S5", "S6", "S6b",
    "S7", "S7b", "S8", "S9", "S10", "S11", "S12", "S13", "S14", "S14a",
    "S15", "S16", "S17", "S18", "S18b", "S19", "S20",
]


# Requirements whose OWNING SLICE shipped but which are NOT met, with the reason.
# This exists because slice-level shipping cannot express "the slice landed and this
# particular requirement was later invalidated". PB-KEY-7 is exactly that: S14 shipped,
# and PB-KEY-10's fix (a DIFFERENT requirement, correctly fixed) removed the mechanism
# PB-KEY-7's recovery path was architected on. Nothing re-derived it, and it read as
# shipped for the rest of the phase. See ADR-007 B35.
NOT_MET = {
    "PB-E2E-2": "the emulator smoke has never run -- its own evidence file disclaims it "
                "and no log or screenshot exists; counted shipped only because evidence "
                "is measured per SLICE, not per requirement (ADR-007 B38)",
}


def evidence_path(slice_id):
    """The per-slice evidence file, if one exists. S0 is the ADR itself."""
    if slice_id == "S0":
        return "docs/adr/ADR-007-remote-access.md"
    rel = "docs/verification/remote-phaseB-%s-evidence.md" % slice_id.lower()
    return rel if os.path.exists(os.path.join(ROOT, rel)) else None


def main():
    rows = []
    with open(MANIFEST, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != 2:
                continue
            rows.append((parts[0], parts[1]))

    shipped = set(SHIPPED)
    by_slice = {}
    for req, sl in rows:
        by_slice.setdefault(sl, []).append(req)

    n_shipped = sum(1 for r, sl in rows if sl in shipped and r not in NOT_MET)
    n_not_met = sum(1 for r, _ in rows if r in NOT_MET)
    no_evidence = sorted(
        {sl for _, sl in rows if sl in shipped and evidence_path(sl) is None},
        key=lambda s: (len(s), s),
    )
    n_no_evidence = sum(
        1 for _, sl in rows if sl in shipped and evidence_path(sl) is None
    )

    out = sys.stdout.write
    out("# Phase B requirement traceability\n\n")
    out("**GENERATED — do not edit by hand.** Regenerate with\n")
    out("`python3 scripts/phaseb-traceability.py > docs/verification/remote-phaseB-traceability.md`.\n\n")
    out("The final audit validates against every REQUIREMENT, not every slice. This is the per-row\n")
    out("view: owner, whether that owner has shipped, and where the evidence is.\n\n")
    out("**READ THE TWO COUNTS BELOW DIFFERENTLY — they have different provenance.** *Shipped* is\n")
    out("the orchestrator ASSERTING that a slice landed and gated; it is maintained by hand in\n")
    out("`scripts/phaseb-traceability.py` and no code checks it, so it is exactly as reliable as\n")
    out("that bookkeeping. *Evidenced* is MEASURED: the evidence file is on disk. A requirement\n")
    out("counted as shipped but not evidenced has no durable record an auditor can read, and the\n")
    out("gap between the two numbers is the honest size of what is asserted rather than shown.\n\n")
    out("| | count |\n|---|---|\n")
    out("| Requirements | %d |\n" % len(rows))
    out("| Shipped (asserted by hand) | %d |\n" % n_shipped)
    out("| Evidenced (measured on disk) | %d |\n" % (n_shipped - n_no_evidence))
    out("| **NOT MET (slice shipped, requirement invalidated later)** | **%d** |\n" % n_not_met)
    out("| Remaining | %d |\n" % (len(rows) - n_shipped - n_not_met))
    out("| **Shipped with NO evidence file** | **%d** |\n\n" % n_no_evidence)

    if no_evidence:
        out("## Shipped slices with no evidence file\n\n")
        out("These %d slices are implemented and gated, but their only durable record is a commit\n"
            % len(no_evidence))
        out("message. That is not sufficient for a per-requirement audit. **Reconstruct each from its\n")
        out("commit and its tests, never from memory** — an evidence file written from recollection is a\n")
        out("plausible account of what was intended rather than of what shipped.\n\n")
        for sl in no_evidence:
            n = len(by_slice[sl])
            out("- **%s** — %d requirement%s: %s\n"
                % (sl, n, "" if n == 1 else "s", ", ".join(sorted(by_slice[sl]))))
        out("\n")

    out("## Every requirement\n\n")
    out("| Requirement | Slice | Status | Evidence |\n|---|---|---|---|\n")

    def sort_key(row):
        req, sl = row
        m = re.match(r"^(PB-[A-Z0-9]+)-(\d+)$", req)
        return (m.group(1), int(m.group(2))) if m else (req, 0)

    for req, sl in sorted(rows, key=sort_key):
        if req in NOT_MET:
            status, ev = "**NOT MET**", "ADR-007 B35 — " + NOT_MET[req]
        elif sl not in shipped:
            status, ev = "pending", "—"
        else:
            status = "shipped"
            path = evidence_path(sl)
            ev = "`%s`" % path if path else "**none — commit message only**"
        out("| %s | %s | %s | %s |\n" % (req, sl, status, ev))

    return 0


if __name__ == "__main__":
    sys.exit(main())
