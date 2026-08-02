# Phase B slice S2 — durable gateway inbound state (PB-GW-1, PB-GW-3, PB-GW-4)

## The defect (§4.6)

The gateway's inbound replay guard did not survive a restart. `NewCommandBridge` built a fresh
`crypto.NewMailboxReceiver()` every start with a cursor of 0; its own doc said a caller
"resuming across a restart should seed it via SetCursor from durable state" and **`SetCursor`
was never called from production startup**. `cmd/swarm-remote` persisted only
`outbound-journal.seq` and `outbound-reply.seq` — **no inbound state at all**. In
`crypto.MailboxReceiver.Accept` the staleness test is `if seen && Seq <= hi`, so on a fresh
receiver `seen == false` and the check was **skipped entirely**.

**Scope, carefully.** The full keystroke-injection exploit was investigated and **disproved**
for the shipped tree (no production phone client imports `phonecore`; a restart gives an empty
`LeaseManager` which drops input with no lease; a replayed `take_control` is refused by a
single-use `operation_id` in the durable idempotency store). What is true is narrower: the
guard rests on incidental mechanisms rather than on itself, and with a **seq-regressed phone**
— the state Phase B creates before PB-STATE lands — a legitimate `take_control` at seq 1 opens
a lease, a retained input at seq 60 is gap-dropped but advances the high-water to 60, and seq
61 is then contiguous and routes to the PTY. The Phase A closure is NOT amended to assert an
exploit.

## RED (failing first, GG-5)

A compile failure cannot demonstrate per-test RED, so the test author temporarily declared the
seam **inert** (types present, never loaded, never saved — today's behaviour), ran the suite,
and deleted the scaffold. All 11 failed on their assertions:

```
--- FAIL: TestCommandBridge_InboundHighWaterSeededAcrossRestart
    post-restart processed=3 forwarded=3, want 0/0
--- FAIL: TestCommandBridge_MailboxCursorSeededOnStart
    post-restart Cursor() = 0 before any poll, want 3
--- FAIL: TestCommandBridge_InboundHighWaterKeyedPerEpoch
--- FAIL: TestReplay_SeqRegressedPhoneRetainedInputNeverReachesLease
    1 retained keystroke(s) reached the lease plane after the restart, want 0
--- FAIL: TestReplay_RetainedFrameClassesRefusedAfterRestart/{take_control,take_control_end,
           idempotent_mutation_kill,terminal_watch,terminal_unwatch}
--- FAIL: TestCrashMatrix_* (6 tests)
```

Tests that would pass against unfixed code: **none**. The reviewer independently rebuilt the
unfixed code and confirmed all 13 tests/subtests fail for their stated reasons — including two
that only fail at run 3, which are exactly the assertions a weakened test would have dropped.

## The central engineering judgement: no reservation inbound

The implementation brief suggested a reservation-style optimistic high-water (mirroring
`seqstore.go`) to avoid an fsync per keystroke. **The implementer refused, and was right.**

Reservation is a *sender-side* technique. Outbound the gateway allocates the seq space, so
skipping a block's unused tail produces a `Gap` at the phone — a resync signal, never a drop.
Inbound **the phone allocates**: seeding `lastSeen + 64` would make `Accept` return
`ErrStaleSeq` for the phone's next 64 *legitimate* frames, and the phone gets no feedback
because `Run` discards `PollOnce`'s error. Commands and input share one sequencer, so the
burned window swallows `take_control` and `kill` too. Durability would become silent
censorship of future traffic. Per-frame persist shipped instead.

## Per-class ordering (PB-GW-3)

A local transaction cannot atomically span the persisted high-water, the cursor, an external
PTY/daemon side effect and the relay ack, so each class differs:
- **input**: persist consumption **before** the PTY write; a persist failure drops the
  keystroke (loss allowed, duplication forbidden — ADR-007 D7 makes input live-only).
- **mutations**: forward **before** persisting; exactly one bounded re-forward, deduped by the
  daemon's durable idempotency.
- **watch/unwatch**: dispatch then persist; converge by re-dispatch, never synthesise the
  opposite transition.

## Format

Versioned JSON, `{schema_version, machine identity, cursor, streams[{sender, epoch, seq}]}`,
sorted for byte-stability, fail-closed on malformed/unversioned/bad-sender, monotonic merge,
in-memory advanced only after the write lands, temp+fsync+rename+dir-fsync. JSON rather than
the packed uint64 of `outbound-*.seq` because a checkpoint is a variable-length map under a
compound key written at human rate, not a per-journal-record hot path.

## Review findings applied

**B1 (blocking, a regression S2 itself introduced)**: the checkpoint was bound to no identity.
`swarm remote init` regenerates `machine.key` without touching its siblings, so a stale
`inbound-state.json` carried an epoch-1 high-water of N and stale-dropped a freshly paired
phone's first N frames including `take_control`; and a reset relay mailbox left the gateway
deaf forever. **Both self-healed before S2** because the in-memory guard reset on restart. Now
the file is stamped with the machine identity and a mismatch yields an empty checkpoint.

**N3**: the fail-closed input drop was completely silent (`Run` discards `PollOnce`'s error), so
a full or read-only state dir would drop every keystroke forever with no signal. Surfaced.

**N6**: a production comment claimed a retaining relay "can replay every frame ... keystrokes
included" — true at the guard, but the PTY half is the claim §4.6 withdrew. Qualified.

## Accepted residuals

1. **A scalar high-water per stream cannot serve both classes — accepted deviation from
   PB-GW-3's "loss forbidden" for mutations.** A `kill` at seq 1 whose forward fails, followed
   by a keystroke at seq 2 in the same batch, persists `Highest=2`; on restart the retained
   kill is refused and never re-forwarded. Reachable **with no crash at all**. The reviewer
   tested the obvious fix (hold the high-water at the contiguous consumed prefix) and it makes
   the inputs above the hole replayable, violating the input class's forbidden-duplication
   rule; stopping the batch at the hole reintroduces the poisoned-envelope wedge `PollOnce`
   exists to prevent. A gap set would be needed. **Softening**: against an honest (purging)
   relay the mutation is already lost regardless, because `PollOnce` acks `maxCursor`
   unconditionally past per-item failures — so this is a requirements-level gap that predates
   S2, not an S2 regression. Recorded as accepted, not fixed.
2. **Per-keystroke fsync is on the input critical path**, measured at 13-15 ms on this
   M1/APFS host (200 iterations, warm), so a batch of 8 costs ~120 ms — about 10% of §6.0's
   p50 <= 150 ms budget on fast local storage, worse on a network-mounted state dir.
   **Consequence for PB-NET-5: its latency harness must run with a real file-backed
   `InboundState`, not the in-memory default, or the budget is measured against a fiction.**
   An invariant-preserving optimisation exists (persist once per maximal run of consecutive
   input frames in a batch, never past an undispatched command frame) — recorded, not taken.
3. **Retired-epoch entries accumulate**, one small record per revoke. Harmless and prunable:
   the gateway holds one epoch content key at a time, so a retired epoch's retained frames fail
   the AEAD regardless of the high-water.
4. **`golangci-lint` has 9 pre-existing findings in this package**, byte-identical between HEAD
   and the S2 tree. S2 introduces none. Separate issue, but CLAUDE.md makes lint a closure gate.

## Gates

```
go test -race ./internal/remotegw/ ./cmd/swarm-remote/ ./internal/skeleton/ -count=1   PASS
go build ./... && go vet ./internal/remotegw/ ./cmd/swarm-remote/                      clean
```
Run on a throwaway copy with slice S1b's in-flight RED files removed there only — the package
does not compile in-tree while two slices are live in it.

## Per-requirement evidence (PB-E2E-3)

Added in S19. The traceability table cites this file for **PB-GW-1, PB-GW-3, PB-GW-4, PB-GW-5 and
PB-DOC-5**; its title named only the first three, so two shipped rows cited a document that never
mentioned them. The two below are the DOCUMENTATION rows of this slice, and they are the pair the
requirements deliberately split.

### PB-GW-5 — the closure records only what was reproduced

The requirement is a prohibition as much as an instruction: the Phase A closure "must not be
amended to assert an exploit that was disproved". What discharges it is the "Scope, carefully"
section above, which is this slice's own record of the split — the full keystroke-injection
exploit was investigated and **disproved** for the shipped tree (three independent blocking
mechanisms traced: no production phone client imports `phonecore`, a restart yields an empty
`LeaseManager` that drops input naming no lease, and a replayed `take_control` is refused by a
single-use `operation_id` in the durable idempotency store), while the narrower two facts — no
durable inbound high-water, a disabled bounded-age check — were reproduced and are what S2 fixes.

The verification that the prohibition HELD is now mechanical: the note landed in
`docs/verification/remote-phaseA-committee-closure.md` states in its own first paragraph that the
closure's no-hole finding stands and that no reader should resurrect the exploit claim from it.

### PB-DOC-5 — the scoped note on the Phase A closure

**This row could not be substantiated when S19 audited it, and the reason is worth stating.**
PB-DOC-5's acceptance criterion is a document change — "Closure amended with the reproduced
finding only; a note distinguishing the two claims" — and S2 performed the *analysis* (above)
without ever amending `remote-phaseA-committee-closure.md`. That file contained no mention of
§4.6, of the disproved exploit, or of the single-gateway-run scope, so the requirement's
deliverable did not exist while its row read `shipped`. The traceability table could not see it,
because a row is measured by whether its evidence FILE exists.

The note now exists: `docs/verification/remote-phaseA-committee-closure.md`, section
"SCOPED NOTE — the Phase B §4.6 finding (PB-DOC-5, added 2026-07-26)". It records the two
reproduced facts and nothing else, states that the closure's no-hole claim stands, states the
scope correction (verified within a single gateway run; across a restart the property rested on
incidental downstream mechanisms), and keeps the still-valid conditional Phase-B trace separate
from the disproved shipped-Phase-A one.

It is dated to S19 rather than backdated to S2, because that is when it was written.

## Derivation

**MACHINE-READABLE** (ADR-007 B129). `scripts/phaseb-traceability.py` reads this section for the
traceability table's DERIVATION column and `internal/verify/phaseb_derivation_test.go` fences that
it does. `DERIVED` means somebody made this row's fence FAIL ON PURPOSE and restored it — not that
a test exists, not that the slice shipped. A `DERIVED` row naming no mutation is malformed and
counted NOT DERIVED.

Every mutation below is to the CONNECTION in production code (B113), never to a constant a test
transcribes. All were reverted; the package is green at HEAD.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-GW-1 | DERIVED | three, all caught. (a) `NewCommandBridge` no longer calls `recv.SeedHighWater` -> 16 tests fail across 4 files, including the §4.6 trace; (b) `b.cursor = ck.Cursor` deleted -> `TestCommandBridge_MailboxCursorSeededOnStart` fails ("re-reads from 0 and re-consumes every item still in the relay's store"); (c) `consume` stops calling `saveCheckpoint` -> all 16 fail. Production reachability confirmed at `cmd/swarm-remote/config.go:174` (`OpenInboundState`) and `:213` (`Inbound:`) |
| PB-GW-3 | DERIVED | each per-class ordering broken SEPARATELY, so neither rides the other's test. Input persist moved AFTER `routeInput` -> only `TestCrashMatrix_InputLostNotDuplicatedWhenCrashBeforePTYWrite` fails; mutation persist moved BEFORE `routeCommand` -> only `TestCrashMatrix_MutationDuplicateBoundedToOneRedelivery` and `..._WatchConvergesAndDuplicateWindowCloses` fail. Both halves are independently fenced |
| PB-GW-4 | DERIVED | class-selective: `consume` returns early for every frame that is not `FrameInput`, so the four non-input classes are refused by nothing at the guard -> all five subtests of `TestReplay_RetainedFrameClassesRefusedAfterRestart` fail independently, plus the four mutation/watch crash-matrix tests. The input class's fixture DISCRIMINATES: run 2 re-seals `take_control` at seq 1 (a genuinely seq-regressed phone) and the retaining relay re-serves at FRESH storage cursors 100/101/102, so the read-cursor half cannot stand in for the high-water half |
| PB-GW-5 | NOT DERIVED | **FINDING E, open — there is no fence over this row's subject to break.** PB-GW-5 is a prohibition on the CONTENT of `docs/verification/remote-phaseA-committee-closure.md`, and nothing in the tree reads that file: `grep -rn 'committee-closure' --include=*.go --include=*.py` returns nothing. Mutation attempted anyway: PB-DOC-5's entire deliverable, the `## SCOPED NOTE` section, was DELETED from the closure and `go test ./internal/verify/` stayed GREEN. Deleting the note is the strongest available mutation and it survives; adding the disproved exploit claim back — the thing the row actually forbids — has no fence at all. The row's shipped status rests on the evidence FILE existing, which is the comfortable fiction B67(1) already names |
