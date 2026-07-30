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
    "PB-NET-7": "THE LIVE DEFECT IS FIXED (c4cc8b8); the ENUMERATION residual returns. "
                "B112's critical -- MailboxWait parked 70s against a silent relay because "
                "its only bound was a ceiling the DECLARED ADVERSARY enforces -- is closed "
                "at the caller, in the gateway loop, WITHOUT touching "
                "TestCallDeadline_TheLongPollIsNotBoundedByIt, whose contract that the long "
                "poll ends on the CALLER's deadline is deliberate. Every named clause is now "
                "fenced and mutation-proven: the 10s budget pinned from section 6.0's table "
                "rather than the constant, cancellation on request AND dial, the "
                "goroutine-leak assertion over 12 cycles, typed refusal after close, and "
                "Close idempotency. WHAT REMAINS is what B109 first named and then discharged "
                "wrongly: the row says 'timeouts EVERYWHERE' and nothing ENUMERATES the call "
                "paths, so a NEW unbounded call site would be caught by nothing. Fixing the "
                "one instance does not close a quantifier -- that is residual 4.23's shape, "
                "and this round has now found the same gap at four separate rows. REFERRED "
                "to the round-7 committee rather than adjudicated by me (ADR-007 B112, B115)",
    "PB-NET-4": "REASON REPLACED 2026-07-31; the pre-fix text described a defect now closed, "
                "which is fossil evidence in the audit's own artifact. Section 6.0's backoff "
                "IS implemented and fenced over a REAL FLAPPING RELAY: dial-gap growth in "
                "non-overlapping bands, a second auth_init, and the connection EVENT sequence, "
                "each mutation-proven by reverting the CALL SITE rather than a constant. "
                "Re-auth and no-replay independently mutation-verified. WHAT REMAINS: "
                "production jitter can be disabled and every assertion still passes -- a frac "
                "source returning 0 sits inside the +/-20% band on every sample, and the "
                "flapping test accepts the exact unjittered 500ms/1s/2s bases. B113 one level "
                "in: the RANGE is fenced, the connection to actual randomness is not. Nothing "
                "observes production reaching the 30s ceiling (ADR-007 B118)",
    "PB-NET-5": "DECOMPOSES, and I refuted it wrongly once already (B98) by checking the "
                "numeric clause and not the quantifier. The criterion clause (p50 150ms "
                "phone Type -> PTY write) and the drop-the-gateway-poll clause ARE fenced "
                "on live code. But the requirement says BOTH HOPS, and the PHONE hop's "
                "fence is transport/s6b_input_test.go -- all six tests driving the dead "
                "Session.Follow, which is the only phone-side MailboxWait caller in the "
                "tree and has zero production callers. The shipped phone does not follow, "
                "it POLLS: mobile/app.go pollInterval = 500ms. So what shipped is a "
                "GATEWAY-SIDE-ONLY fix -- the exact mirror of the phone-side-only fix this "
                "requirement's own text warns would fake the criterion. UNFENCED, not "
                "disproven: the shipped poll plausibly avoids head-of-line blocking by a "
                "cruder mechanism, but nothing measures it, and the echo direction it "
                "gates is outside the numeric criterion by construction (ADR-007 B100)",
    "PB-NET-6": "THE REPLACEMENT FENCE CANNOT FAIL ON THE DEFECT IT IS NAMED FOR. "
                "mobile/pbnet6_drainreaders_test.go pins CALL SITES by AST scan, not "
                "concurrency: mutating App.run to launch two genuinely concurrent drains "
                "on one connection -- verbatim the defect the fence's own error message "
                "describes -- leaves it PASSING. And concurrent-drain is not a clause this "
                "requirement names; it names seq gating, replay/reorder/dup rejection, the "
                "mailbox cap and hostile-pagination termination. THE ROW IS CLOSER THAN "
                "THIS REASON SUGGESTS: all four named clauses have live subjects "
                "(relay/abuse_test.go for the cap, skeleton's adversarial replay test, "
                "phonecore/processdeath_test.go for restart, and conformance/drain_test.go's "
                "non-advancing-page test which IS hostile-pagination termination in its "
                "shipped form, currently attributed to PB-SYNC-6). Nobody has assembled "
                "that union under this row, and none of those four has been mutated "
                "(ADR-007 B112)",
    "PB-PAIR-4": "REASON REPLACED 2026-07-31; the half-pair defect is CLOSED and the pre-fix "
                 "text is fossil. The phone commits durably BEFORE it acknowledges, the "
                 "machine enrols only on that acknowledgement, and the persisted write fsyncs "
                 "the file, renames, and fsyncs the directory. Two fences by two agents 23 "
                 "minutes apart both go RED on the reverted ordering, one by SIGKILLing a "
                 "genuine second process. WHAT REMAINS: the row requires process death AT "
                 "EVERY named transition, and the enumeration declares Noise msg2 "
                 "'unreachable' -- the kill test logs that and RETURNS WITHOUT KILLING "
                 "ANYTHING. The enumeration passes, proving only that the no-op row is named. "
                 "Either kill in that window or amend the criterion to state the "
                 "bounding-state equivalence explicitly (ADR-007 B117)",
    "PB-PUSH-3": "the fence asserts SIZE; the requirement asks for a SCHEMA. The presence "
                 "sweep emits 78 RANDOM bytes, and the relay holds no key it could seal a "
                 "real envelope with -- by two-tier design. A provider that PARSES rather "
                 "than measures still separates a sweep from a wake, because a genuine "
                 "wake's envelope header is cleartext. The project's OWN disclosure "
                 "document says so in as many words -- 'Still open: the sweep is separable "
                 "by SHAPE, just not by SIZE' -- while this row read shipped. Mutation: a "
                 "plaintext payload leaves every PB-PUSH-3 test passing (ADR-007 B96)",
    "PB-SEC-2": "REASON REPLACED 2026-07-31; the lifecycle defect is CLOSED and the pre-fix "
                "text is fossil. The timed tier has the per-use tier's prompt lifecycle, and "
                "two live BYPASSES found by mutating the production connection are fenced: a "
                "button that stops calling its gate, and observers never installed. WHAT "
                "REMAINS is the row's FIRST clause, untouched by round 7: 'cryptographically "
                "enforced, not cosmetic ... Keystore-enforced unwrap/sign authorization rather "
                "than a UI boolean'. The timed tier reads an IN-MEMORY LEDGER TIMESTAMP and "
                "invokes the action when fresh; SendInput accepts no proof; resolveSend reuses "
                "an already-resident content key without consulting Keystore. The authorized "
                "biometric operation is not cryptographically bound to the Go action -- the "
                "clause the requirement wrote 'rather than a UI boolean' to forbid "
                "(ADR-007 B117)",
    "PB-E2E-3": "DEFINED DOWN by my own restatement (B93). It claimed RED-first is "
                "evidenced by a committed failing state, and its three cited exemplars "
                "contain ZERO lines of actual failing output -- verified, grep returns 0 "
                "on all three. They carry PROSE NARRATING failures, which is exactly what "
                "the restatement claimed to replace. And 26 slices landed implementation "
                "and tests in one commit, not the 4 the row names (ADR-007 B94)",
}


def evidence_path(slice_id):
    """The per-slice evidence file, if one exists. S0 is the ADR itself."""
    if slice_id == "S0":
        return "docs/adr/ADR-007-remote-access.md"
    rel = "docs/verification/remote-phaseB-%s-evidence.md" % slice_id.lower()
    return rel if os.path.exists(os.path.join(ROOT, rel)) else None


# ADR-007 B67(1). EXISTENCE IS NOT CURRENCY, and this report used to conflate them.
#
# `evidence_path` answers "is there a file", which the header sold as MEASURED and a reader
# reasonably trusted. But an evidence file is a claim about the commit that produced it, and a
# check that tests only for existence silently re-dates every claim inside it to now. Two files
# were fossils before anyone noticed: S18's described the screenshot sink B65 deleted, and is
# cited for all TEN of S18's requirements; S17's predated both the defect that made PB-PUSH-9
# unmet and the fix that restored it.
#
# So a superseding banner is now MACHINE-VISIBLE rather than decoration. Marking a file stale
# costs one line in the file and shows up here, which is the only way a reader of this table
# learns that "evidenced" meant "evidenced against a different version".
#
# It is deliberately a marker scan and not a freshness heuristic. Comparing mtimes or commit
# dates against the slice's code would flag every evidence file on every unrelated commit, and a
# signal that fires constantly is one nobody reads -- the failure mode this project already has
# ten of.
SUPERSEDED_MARKERS = (
    "SUPERSEDED IN PART", "PARTLY SUPERSEDED", "SUPERSEDED --",
    # ADR-007 B79. The first three were the banners this orchestrator happened to write.
    # A round-5 reviewer found three evidence files carrying HONEST inline corrections in
    # other words -- "CORRECTION <date>", "THIS FINDING IS CLOSED", "FALSIFIED by ..." --
    # none of which matched, so the flag had a recall gap in exactly the direction that
    # matters: it under-reports. The scan window was 4000 bytes for the same reason, and a
    # correction written where the defect is discussed sits well past it.
    "CORRECTION 20", "THIS FINDING IS CLOSED", "FALSIFIED BY", "FALSIFIED by",
    "AMENDED 20", "WITHDRAWN 20",
)


def evidence_superseded(rel):
    """True when the evidence file carries a dated correction, amendment or withdrawal.

    ADR-007 B79. This flag was first written to catch the two files whose bodies had been
    OVERTAKEN by a later decision, matching three banner phrases this orchestrator happened
    to have used. A round-5 reviewer found three more files carrying honest inline
    corrections in other words, none of which matched -- a recall gap in the direction that
    matters, since the flag under-reported.

    Widening it changes what it MEANS, and the section is renamed to match: it no longer
    claims the file is stale, only that it contains something dated that a reader must see
    before citing it. Most of these corrections are the record working -- a finding
    falsified, a fix closing an earlier note -- and reading one as "this evidence is
    untrustworthy" would be the opposite of the truth.

    S0 is excluded: it is the ADR, which is nothing BUT dated amendments, so flagging it
    carries no information.
    """
    if rel is None or rel.endswith("ADR-007-remote-access.md"):
        return False
    try:
        with open(os.path.join(ROOT, rel), encoding="utf-8") as fh:
            head = fh.read()   # whole file: a correction sits where the defect is discussed, not at the top
    except OSError:
        return False
    return any(m in head for m in SUPERSEDED_MARKERS)


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
    out("**AND *EVIDENCED* MEANS THE FILE EXISTS, NOT THAT IT IS CURRENT** (ADR-007 B67). An\n")
    out("evidence file is a claim about the commit that produced it. Files that declare themselves\n")
    out("partly superseded are listed below and their claims must be read against that notice, not\n")
    out("against HEAD.\n\n")
    out("| | count |\n|---|---|\n")
    out("| Requirements | %d |\n" % len(rows))
    out("| Shipped (asserted by hand) | %d |\n" % n_shipped)
    out("| Evidenced (measured on disk) | %d |\n" % (n_shipped - n_no_evidence))
    out("| **NOT MET (slice shipped, requirement invalidated later)** | **%d** |\n" % n_not_met)
    out("| Remaining | %d |\n" % (len(rows) - n_shipped - n_not_met))
    out("| **Shipped with NO evidence file** | **%d** |\n\n" % n_no_evidence)

    stale = sorted(
        {sl for _, sl in rows if sl in shipped and evidence_superseded(evidence_path(sl))},
        key=lambda x: (len(x), x),
    )
    if stale:
        out("## Evidence files carrying a dated correction, amendment or withdrawal\n\n")
        out("These count as *evidenced* above and mostly ARE — the flag says the file contains a\n")
        out("dated correction, not that it is untrustworthy. **Read the correction before citing\n")
        out("the passage it touches.** Two of these (S17, S18) were genuinely overtaken by a later\n")
        out("decision and carry a superseding banner; the rest carry honest inline corrections,\n")
        out("which is the record working rather than failing. See ADR-007 B67(1) and B79.\n\n")
        for sl in stale:
            n = len(by_slice[sl])
            out("- **%s** — cited for %d requirement%s: %s\n"
                % (sl, n, "" if n == 1 else "s", ", ".join(sorted(by_slice[sl]))))
        out("\n")

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
            status, ev = "**NOT MET**", NOT_MET[req]
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
