# Audit 002 — Implementation goals (Draft 1)

**Date**: 2026-07-16
**Artifact**: docs/specifications/implementation-goals.md, Draft 1
**Committee**: GPT-5.6 sol (codex, xhigh) · Sonnet · Opus. Gemini (agy) unavailable — individual quota exhausted (second consecutive audit); proceeded with three members.
**Verdict**: **REVISE** — unanimous. No architectural contradiction; the defects are omissions and unverifiable wording that would let a defective system pass every gate. All fixes applied in Draft 2 (same file).

## Consensus (2+ members — treated as real)

1. **D-1 daemon auto-start has no exit criterion anywhere** (Sonnet CRITICAL, Opus HIGH, codex coverage list). The product's first-run promise fell in the crack between Epic 5 (daemon side) and Epic 7 (stub-only client). → new E5.9.
2. **Security baseline only partially gated** (codex CRITICAL, Opus HIGH, Sonnet MEDIUM). ADR-004 requires 0600 on sockets, meta, transcripts; Draft 1 asserted only state-dir 0700 + transcript 0600. → E5.7 rewritten: full permission sweep under permissive umask across all seven artifact classes.
3. **Performance budgets recorded, not enforced** (Opus HIGH, codex MEDIUM, Sonnet MEDIUM). "Measured in a test" cannot fail. → E7.6/E8.5/E14.4 now assert failing thresholds with defined workloads.
4. **Status-engine trigger paths missing** (codex HIGH, Opus MEDIUM, Sonnet coverage). Grid-heuristic evaluation on output events + low-frequency fallback poll (T-3) and inconclusive→unknown (T-4) had no criterion. → new E10.8.
5. **FULL-scope contracts deferred to Epic 14 / paperwork pass** (codex HIGH, Sonnet MEDIUM, Opus MEDIUM). E14.6 required naming tests, not satisfying declared IV scope; compat matrix, soak, attach-under-failure only surfaced 13 epics after the responsible code. → E14.6 rewritten (evidence manifest, skipped/flag-disabled required tests fail the epic); minimal compat smoke pulled into E5.10; attach-under-kill into E8.6; soak cycle count stated in E14.3.
6. **S-2 full persisted-field round-trip never asserted** (codex, Sonnet). → new E1.4b.
7. **S-6 environment provenance has no differential test** (codex, Sonnet). Fallback to `os.Environ()` at spawn would pass Draft 1. → E4.1 differential test (daemon env A vs client env B).
8. **macOS CPU sampling as one-time manual run** (Sonnet HIGH, Opus MEDIUM, codex verifiability). → E10.6: macOS CI runner.
9. **Orchestration protocol blind spots** (all three): reviewers check criteria but never re-derive coverage from the spec (Opus); no cross-model review until Epic 14 (Sonnet); "stop, don't push through" safeguard dropped (Sonnet); no shared-package interface freeze before parallel fleets (Opus); no evidence artifact or closure rule (codex). → protocol section rewritten with all five.
10. **V-4/V-5/L-2/L-3 UI content under-specified** (codex HIGH, Opus/Sonnet LOW). → E7.2/E7.5 enumerate required row fields, both banner triggers, version-range greying, invalid-cwd refusal.
11. **T-5 proof via commit archaeology is brittle** (codex MEDIUM, Opus LOW). → E9.5/E11.8 reworded to package-boundary checks.

## Divergence

- **E8.2 SIGKILL termios restoration** — codex alone flagged it CRITICAL, and it is simply correct: no process runs cleanup after SIGKILL. The other two missed a physically impossible test gate. Fixed (restore claimed for detach/panic/SIGINT/SIGTERM/SIGHUP only).
- **"Zero loss" ambiguity in GG-1 vs deliberate frame-dropping in E4.6** — codex alone; accepted, "loss" now defined (process, grid, meta, transcript-per-policy — not transient DATA_OUT frames).
- **Frame-codec resource exhaustion (max size before allocation)** — codex alone; accepted into E6.1 (local DoS risk).
- **F-2 mis-citation** (UDS-neutral schemas "tested" by a handshake test) — Sonnet alone; accepted, split into its own recorded check.
- **Over-engineering**: Sonnet found none; codex/Opus found only minor excesses (E2.3's implied Restore API, fleet mandate for docs-only epics, triple T-5 proof). All trimmed. The document's failure mode is under-specification, not gold-plating — unanimous.

## Blind spots (questions nobody asked)

- No human-acceptance checkpoint: golden files verify the TUI against itself; nobody asks when Nathan actually looks at the running product (added: walking-skeleton demo at Epic 8 close is flagged for human review).
- The fake-agent representativeness assumption (Epic 4) is inherited from the plan unexamined — real-CLI PTY quirks (bracketed paste floods, terminal queries expecting responses) won't surface until Epic 11.
- Nobody asked what happens to the goals document itself when an epic legitimately discovers the spec is wrong mid-build (added to protocol: spec changes go through an ADR + goals amendment, never silent criterion drift).

## Per-member signal

- **codex**: the two physically-grounded catches (SIGKILL impossibility, frame-size DoS) plus the sharpest wording replacements; deepest EARS-by-EARS partial-coverage table.
- **Sonnet**: the cleanest coverage hole (D-1, CRITICAL) and the two process findings with teeth (cross-model review timing, dropped stop-safeguard).
- **Opus**: the structural process insight (per-epic reviewers can never see a *missing* criterion — coverage re-check must be incremental) and the L1 budget-splitting arithmetic error.
- **agy**: no signal (quota).

## Fix list → Draft 2

All 29 accepted fixes applied to implementation-goals.md in the same commit as this report; the document header records the audit. Rejected: none outright; two codex suggestions weakened rather than adopted verbatim (evidence manifests are per-epic markdown, not machine-readable JSON — proportionality; specialist reviewer *assignments* folded into the cross-model rule rather than a separate roster).
