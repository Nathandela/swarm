# Phase B progress and handoff

**Branch**: `worktree-remote-control-research`. **Spec**: `docs/specifications/remote-phaseB-requirements.md` (v3.5.1, 139 requirements).
**Gates**: `python3 scripts/check-phaseb-manifest.py` (ownership + DAG), `go build/vet/test -race ./...`.

## Requirements phase: COMPLETE

Five adversarial audit-committee rounds (codex/GPT-5.6 sol, opus, fable), all findings
verified in source before acting. Converged at v3.5.1: opus `requirements-complete`, fable
"nothing blocking", codex's single remaining blocker fixed and independently re-verified by
both. Full record in §14 of the spec.

Ownership and slice reachability are machine-enforced (`remote-phaseB-manifest.tsv`,
`remote-phaseB-slices.tsv`), each verified with negative controls, because homeless
requirements recurred in three consecutive rounds and an orphan slice in a fourth.

## Slice status

| Slice | Requirements | State |
|---|---|---|
| S1 dependency-edge surgery | PB-BIND-0 | **SHIPPED** (`0024595`) — closure 52 -> 18 non-stdlib, zero forbidden |
| S5 design tokens | PB-TOK-1/2/3 | **SHIPPED** (`638b61b`) — Substrate pinned, drift-guarded |
| S3 QR renderer + payload | PB-PAIR-1, PB-PAIR-7 | **SHIPPED** (`20be9b2`) -- real symbol + relay URL; 39-char URL ceiling enforced; manual scan still owed |
| S0, S2, S2b, S4 | ADR decisions, gateway durability, supervision | **next** -- all parallel roots, startable immediately |
| S1b protocol additions | PB-SYNC-7 | reconcile frame + lease confirmation + reply correlation implemented, in fix-then-close |
| S6 transport resilience | PB-NET-2,3,4,6,7 | implemented, security fix in flight (cleartext-via-redirect) |
| S6b low-latency input path | PB-NET-5 | not started (split out of S6) |
| S7..S21 | see §11 of the spec | not started |

## Working agreement that is producing the results

Four independent agents per slice, no shared context: test author (RED, evidenced failure)
-> implementer -> independent reviewer -> fix agent. The reviewer has caught a real defect in
every slice so far, including ones the implementer and test author both missed.

## CROSS-SLICE BRICK RISK -- wire both halves or neither

PB-SYNC-7 (S1b) ships the reconcile record and the phone-side gate, but production wiring is
deliberately NOT in that slice: `remotegw/service.go` still constructs `RelaySink` with nil
`Authorities`/`Machine`, so the bootstrap is inert and the record is never published. The
phone-side seams (`RequireReconciled`, `Reconciled`, `TakeFor`, `SeedFrom`) have zero
production callers today.

**The failure mode**: S7 wires the phone-side `RequireReconciled()` gate, nobody wires
`RelayConfig.Authorities` + `RelayConfig.Machine`, and the phone refuses every mutating op
FOREVER while nothing in the tree fails. That is precisely the permanent brick PB-SYNC-7
exists to prevent, re-created at the slice seam.

Both halves, in the same slice:
- gateway: `RelayConfig.Authorities` (a real `ReconcileSource`) + `RelayConfig.Machine` in
  `internal/remotegw/service.go`. `InboundHighWater()` is
  `inbound.Load().Highest[InboundStream{Sender: [8]byte{}, Epoch: cfg.EpochID}]` (sender-zero,
  because phone->machine seals never set `SenderKeyID`); `ReplyCeiling()` is the reply
  `SeqSource.Issued()`.
- phone: the calls to `RequireReconciled` / `SeedFrom` / `SeedHighWater` / `NewGrantReceiverAt`.

## Open items carried forward

- **PB-PAIR-1 needs an evidenced manual scan** under `docs/verification/` — a real phone
  camera reading the symbol off a real terminal. No test can supply it. Lower risk after the
  row-budget fix (quiet zone 3 at 24 rows, 4 at 25+), but still the check that matters. The
  encoder always uses mask 0 and every pairing mints fresh random material, so the reviewer
  recommends re-running the out-of-band decode over ~1000 random payloads, not one.
- **The relay URL ceiling is enforced at WRITE time only.** A `relay.json` written by hand or
  before this change is loaded as-is; `swarm remote pair` then degrades with the now-accurate
  "shorten the relay URL" message. Refusing at load would brick an existing config on upgrade.
- **S8 must NOT reimplement `LaunchContentHash`** in the facade. It stayed in
  `internal/protocol` (Go has no function aliases). Reimplementing its canonical encoding
  would produce silent signature failures with no compile error. Options are: move it then,
  or expose it through the facade. See `remote-phaseB-s1-evidence.md`.
- **Known pre-existing flake**: `TestRemotePeek_LargeGridClippedUnderMaxFrame` (i/o timeout
  under full-suite load; passes isolated). Predates Phase B.
- The final full-committee audit against all 139 requirements is still owed, per the goal.
