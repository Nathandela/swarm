# BRIEF — Phase B slice S20: closure, runbooks, and the certificate pin (8 requirements)

cwd = `/Users/Nathan/Code/swarm/.claude/worktrees/remote-control-research`. Work only there.

You are the TEST AUTHOR (RED) where a requirement admits a test, and the AUTHOR where it does not —
several of these are documentation obligations whose acceptance is "executed once during
verification". Say clearly which is which. A separate agent implements; a third reviews.

Requirements: **PB-OPS-1, 2, 3, 5** and **PB-DOC-2, 3, 4, 7**.

## PB-OPS-5 is the only code defect in this slice, and it is a product-breaking one

I verified it. `internal/remote/relay/security.go:118-135` pins with `bytes.Equal(raw, pinned)` — a
**full leaf DER** comparison. On Android the trust-root source is **pinning-only**, so there is no
fallback path. **Every certificate renewal therefore breaks the handset**, and Let's Encrypt renews
every 60-90 days. A phone remote-control that stops working every two months is not a product.

The requirement offers two ways out: pin the **SPKI hash** instead, or document and accept the
operational consequence. **Recommend the SPKI pin** — but there is a nuance the requirement glosses
and you should not repeat it uncritically:

> "Pinning the SPKI hash rather than the full leaf DER survives renewal at the same security level."

That is true **only if the key is reused across renewal**. Most ACME clients generate a fresh
keypair on each renewal by default, and a fresh key means a fresh SPKI, which breaks an SPKI pin
exactly as it breaks a DER pin. So an SPKI pin is a *necessary* half; the runbook must also require
key reuse, and the fence should make the pairing of the two explicit rather than leaving an operator
to discover it at the first renewal. **If you conclude the requirement is inaccurate as written, say
so — that is a finding, and four requirements in this phase have already been amended on exactly
that kind of evidence.**

## Four of these are closure obligations, and two are already partly satisfied

Check what exists before writing anything:

- **PB-DOC-7** (machine-checked slice-ownership manifest: every id owned exactly once, no wildcard
  ownership, dependency edges enumerated, acyclicity validated) — `scripts/check-phaseb-manifest.py`
  exists and runs green today. Verify it actually enforces every clause, including the **negative
  controls** (a duplicate owner, a wildcard, a cycle, an orphan slice). If any clause is unenforced,
  that is the finding.
- **PB-DOC-2** (a verification file mapping every PB-* id to evidence) —
  `docs/verification/remote-phaseB-traceability.md` exists and is **generated** by
  `scripts/phaseb-traceability.py`. Check it covers what the requirement asks. Note it has already
  caught one requirement reported as shipped whose owning slice had shipped while the requirement
  itself was unmet.
- **PB-DOC-3** (residuals in the Phase A closure style: what, why, adversary-reachable or not) — a
  great many residuals are recorded across `remote-phaseB-progress.md` and the per-slice evidence
  files, but they are **not** consolidated and most do not state adversary-reachability explicitly.
  That consolidation is real work and it is what the final audit will read.
- **PB-DOC-4** — a roadmap line saying implementers are "never fable/haiku" must be **amended rather
  than silently contradicted** by the model assignment actually used. Check what §11 assigns and what
  was actually done, and report the discrepancy rather than resolving it in prose.

## PB-OPS-1, -2, -3

- **PB-OPS-1**: a local/TLS relay runbook sufficient for the handset demonstration. Production
  deployment and VPS provisioning are explicitly **Phase C** — do not scope-creep into them.
- **PB-OPS-2**: an operator runbook for install, pair, revoke, kill switch, device loss, push
  configuration. Acceptance is that **each step is executed once during verification**, so write it
  as something executable, not as prose.
- **PB-OPS-3**: honest metadata disclosure covering the relay operator **and the push provider**,
  consistent with the payload-disclosure decision. ADR-007 B20 already records what the push
  provider observes, and a separate finding records that a **persisted push token is a new durable
  device identifier at rest in the untrusted relay's store**, correlatable with the mailbox by
  routing id. Both belong in the disclosure.

## The standard this slice is held to

The phase has recorded several criteria that are **easy to over-read from a green suite**: a latency
headline behind an environment-variable skip, an artifact gate behind an unset toolchain variable,
and two acceptance criteria that literally could not fail. **A documentation requirement is the
easiest place in the whole phase to write something that reads as done and is not.** PB-DOC-2's
criterion is "full ID coverage" — coverage of ids, not of prose.

Standing defect classes: (i) a guard that cannot fail; (ii) a plausible-but-wrong value hiding a
brick; (iii) a test passing because its subject became unreachable; (iv) a requirement satisfiable
while the defect ships; (v) a fence guarding a path production does not take.

## Do NOT

- **`internal/remote/crypto` is FROZEN.** The pin lives in `internal/remote/relay/security.go`, which
  is not frozen — but changing the pin format is a security-relevant change and wants an ADR note;
  propose it and I will write the entry.
- Do not edit `docs/specifications/`. Report and I will amend.
- Do not edit `docs/verification/remote-phaseB-traceability.md` — it is generated.
- **Do not commit and do not stage anything.** Leave your work unstaged; I stage it explicitly.

## Environment

- `golangci-lint` at `/Users/Nathan/go/bin/golangci-lint`. Android SDK at
  `/usr/local/share/android-commandlinetools`.
- Host is an Apple M1 but `/usr/local/bin/go` is x86_64. The reliable check is `go env GOHOSTARCH`
  (= amd64), an **in-process** probe.
- Other slices have uncommitted work in the tree. Scope your runs, leave anything dirty outside your
  scope alone, never `git checkout` what you did not write.

## Deliverable

1. Files, uncommitted and unstaged, with a clear split between what is tested and what is executed.
2. The verbatim failing-first run for anything testable.
3. **Your PB-OPS-5 recommendation**, including whether the requirement's own claim about SPKI
  pinning is accurate as written.
4. Which PB-DOC clauses are already satisfied by existing artifacts, verified rather than assumed,
   and which are not.
5. Anything unimplementable or already unmet as written.

Report via SendMessage to "main" — plain text output is NOT visible.
