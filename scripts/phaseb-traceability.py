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
    "PB-NET-7": "THE CRITICAL IS FIXED; THE REQUIREMENT IS STILL NOT MET, and those are "
                "different things (23d1dc1, RED at c2b7eb5). The wedge is closed: every "
                "exchange is bounded per CALL and a reached deadline surfaces as "
                "ErrClassOffline, so a silent relay no longer parks the outbound plane and "
                "ConnectionState stops reporting online. TWO CLAUSES REMAIN. (1) The "
                "budget table binds a NON-WAIT REQUEST TIMEOUT OF 10 s to this "
                "requirement and says changing a value needs committee agreement, not "
                "implementer discretion; the fix chose 5 s on latency grounds without "
                "reconciling. The 10 s exists ONLY in internal/remote/transport -- the "
                "dead package -- exactly as PB-NET-4's backoff numbers do. (2) The row's "
                "own evidence column asks for a GOROUTINE-LEAK ASSERTION over repeated "
                "Start/Stop, and there is no NumGoroutine or goleak anywhere in relay, "
                "mobile or remotegw. Caught while attempting to mark this row met "
                "(ADR-007 B99)",
    "PB-NET-4": "marked met by MY OWN adjudication (B90), which asserted the resilience "
                "half is \"implemented and fenced\". Section 6.0's backoff numbers -- "
                "initial 500ms, factor 2, ceiling 30s, jitter +/-20% -- exist ONLY in "
                "internal/remote/transport, which has zero production callers. Shipped "
                "reconnects are fixed-delay with no growth, no ceiling and no jitter; "
                "setting the shipped delay to 3h leaves every fence passing (ADR-007 B94)",
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
    "PB-NET-3": "UNFENCED, NOT DISPROVEN -- and the distinction is the point. Every fence "
                "for it lives in internal/remote/transport, which has no production "
                "caller: opaque_test.go taps the wire of the DEAD Session, and the "
                "structural arm reflects over transport's own types, saying nothing about "
                "relay.Client or mobile. The property itself (a sealed payload's plaintext "
                "never reaches the wire) appears TRUE of the shipped phone -- sendInputFrame "
                "seals before it appends and there is no raw-append path -- but that is read, "
                "not measured. The nearest live candidate fences transport POLICY (ws vs "
                "wss), not payload plaintext. Needs a wire tap over the SHIPPED path "
                "(ADR-007 B98)",
    "PB-NET-6": "DECOMPOSES, and one clause has no live subject at all. Replay-refused-"
                "across-restart and durable-cursor-survives-restart do have live equivalents "
                "in phonecore (ErrStaleSeq, RelayCursor). But HOSTILE PAGINATION TERMINATES "
                "is fenced only by ErrStuckPage, which exists nowhere but the dead package; "
                "the shipped App.drain substitutes a weaker progress-conditioned throttle "
                "that is fenced as no termination property anywhere. Deleting the dead code "
                "without writing that fence converts a misaimed fence into no fence "
                "(ADR-007 B98)",
    "PB-PAIR-4": "the acknowledgement means \"the acceptance frame arrived\", NOT \"the "
                 "phone durably committed\". The device sends the ack in "
                 "remote/pairing/pairing.go and only later does mobile/pairing.go call "
                 "app.pin; the machine enrolls on the ack. Process death or a pin failure "
                 "(full disk, read-only dir, Keystore refusal) in that window leaves the "
                 "MACHINE ENROLLED and remote control live while the phone holds no "
                 "durable pin. The send site's own comment enumerates the OPPOSITE "
                 "residual (phone pins, machine claims nothing) and calls it harmless -- "
                 "it never considers this orientation. Mutation: forcing App.pin to always "
                 "error leaves every PBPAIR4-named test passing (ADR-007 B96)",
    "PB-PUSH-3": "the fence asserts SIZE; the requirement asks for a SCHEMA. The presence "
                 "sweep emits 78 RANDOM bytes, and the relay holds no key it could seal a "
                 "real envelope with -- by two-tier design. A provider that PARSES rather "
                 "than measures still separates a sweep from a wake, because a genuine "
                 "wake's envelope header is cleartext. The project's OWN disclosure "
                 "document says so in as many words -- 'Still open: the sweep is separable "
                 "by SHAPE, just not by SIZE' -- while this row read shipped. Mutation: a "
                 "plaintext payload leaves every PB-PUSH-3 test passing (ADR-007 B96)",
    "PB-SEC-2": "the per-prompt identity fix landed on PerUseGate and NOT on the timed "
                "tier, so the class was never closed. PhoneSurface.reauthorizeTimedTier "
                "shows confirmForContent with NO ticket registered; the ledger entry is "
                "created inside the callback by grantTimedTier. An invalidation that "
                "clears the ledger therefore has nothing to clear for a prompt that is "
                "ON SCREEN, and a queued late success mints a fresh authorization AFTER "
                "invalidation. promptForContent has the same shape. Mutation: replacing "
                "the freshness decision with `if (true)` leaves both the Go gate and the "
                "Android unit suite green (ADR-007 B96)",
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
