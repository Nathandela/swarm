# Remote Phase 1 — Daemon-Foundation Slice (9242559) — Audit Committee Review

**Slice under review**: `internal/journal`, `internal/idempotency`, `internal/protocol`
(journal ops / ErrorCode / ServeRemote), and the `internal/daemon` wiring (commit
`9242559`, GREEN in isolation). Reviews R-JRN, R-IDP, R-PROT.3/.7 against ADR-007 D6 and
amendments A3/A7/A11. Author: Opus 4.8 (a plan-compliant model).
**Review date**: 2026-07-20. **Beads**: epic `agents-tracker-qnx`, task `agents-tracker-ibh`.
**Verdict**: **REVISE**. The low-level durability primitives are sound and correctly
wired into the EXISTING `saveMetaLocked` choke point; but the NEW capability the slice
exists to enable — a phone reliably resuming/retrying across a daemon crash — is broken
in exactly the scenarios that matter, and none of the breakage is visible from the green
suite because every test exercises a component in isolation or through a stub.

## Committee composition

- **Opus subagent** — deep crash-window analysis (the A3 crash table below). Delivered.
- **Sonnet subagent** — integration/wiring + test-adequacy pass. Delivered.
- **Independent lead read** — idempotency/journal/daemon-journal/launch. Delivered.
- **codex (GPT-5.5)** — attempted twice: first rate-limited, then `401 Unauthorized`
  (stale CLI auth token — needs re-login). No output. The cross-model requirement is
  still open; RE-RUN when codex auth is restored. (Per user direction, proceeding on the
  opus+sonnet+lead panel, which converged strongly.)

## The A3 launch crash table (centerpiece)

Launch path with `OperationID != ""` (`launch.go:120-243`); the idempotency `Prepare`
fsync is SEPARATE from and BEFORE the reserve `saveMeta` fsync. Kill -9 at each point:

| # | Crash position | On-disk after kill | Replay (same opID) | Verdict |
|---|---|---|---|---|
| W0 | after in-mem reserve, before `Prepare` | nothing | fresh launch | **CLEAN** |
| W1 | after `Prepare` fsync (L144), before `saveMeta` (L190) | idem `prepared{opID->id}`; no meta/journal/shim | `existed`; `Get(id)` not found -> hard error "cached session gone" (L154) | **POISON (permanent)** |
| W1b | W1 + PreLaunch worktree created | as W1 + orphan worktree | same error; PreDelete never runs | **POISON + leak** |
| W2 | after journal `launched` fsync, before `store.Save` | idem `prepared`; journal `launched(N)`; no meta | same hard error | **POISON + dangling journal record** |
| W3 | after reserve `saveMeta` (L190), before spawn; meta `ShimPID=0` | idem `prepared`; journal; meta `Running,ShimPID=0` | reconcile marks LOST (`reconcile.go:69`); replay `Get(id)`=LOST returned as **success** (L152) | **SILENT CORPSE** |
| W4 | after `cmd.Start` (L208), before identity `saveMeta` (L234) | idem `prepared`; meta `Running,ShimPID=0`; **a real shim+agent running** | reconcile can't match identity -> LOST; live shim never signalled (S3) -> orphan; replay returns LOST as **success** | **SILENT CORPSE + LIVE ORPHAN AGENT** |
| W5 | after identity `saveMeta` (L234) | idem `prepared`; full meta; journal | reconcile reconnects Running; replay returns live session | **CLEAN — exactly-once** |

**Only W5 delivers the ADR D-5 "exactly-once survives crash" guarantee.** W1/W2 burn the
`operation_id` permanently; W3/W4 return a dead or orphaned session as a SUCCESSFUL launch
(no error) — more dangerous than the loud W1 error, and squarely against a threat model
where the op spawns code-editing agents.

**Adjudication: REJECT the deviation as shipped.** The evidence doc's claim that the
separate-fsync form is equivalent to the literal "same fsync" form is FALSE — the
deviation strictly regresses W1/W2. Note the ordering (opID durable before meta) is
CORRECT for no-double-spawn; reversing it would let W1/W2 spawn a second session. The fix
needs all three: (a) atomic opID+meta durability (literal form, or persist opID inside
`meta.json` in the same `saveMeta`); (b) **phase-aware replay** — a cached record still
`prepared`/`executing` must NOT be returned as a completed success; (c) an **Open-time
resolver** for stale `prepared`/`executing` launch records so the opID is re-drivable, not
burned.

## Consensus findings

### CRITICAL

**DCR-1. D-5 exactly-once-survives-crash is not delivered for launch** (the crash table).
`launch.go:143-156,190,234`; `daemon.go:443-451`; `reconcile.go:58-72`. Idempotent replay
works only in W5; W1/W2 poison, W3/W4 return a corpse (W4 also orphans a live agent). The
one property the slice exists to provide is absent in 4 of 5 windows. (Opus CRITICAL-1;
lead #1 for the poison half; the silent-corpse W3/W4 variant is Opus's addition.)

**DCR-2. The poison is PERMANENT — no TTL, `Compact` never called.** `daemon.go:196`
opens the store with `Options{}` (TTL=0, MaxEntries=0); `grep` finds no `idem.Compact()`
caller anywhere. So the "until Compact TTL expiry" mitigation does not exist — one
mid-launch crash burns that `operation_id` for the life of the state dir; the phone's
by-design key reuse fails forever. (Sonnet C3; Opus HIGH-1; sharpens lead #1.)

### HIGH

**DHI-1. `journal_read`/`journal_subscribe` are UNREACHABLE in the assembled daemon.**
`protocol/remote.go:50-53` requires both `JournalReadFrom` AND `JournalSubscribe` for the
`JournalBackend` assertion (`server.go:1056`). `*daemon.Daemon` never implements
`JournalSubscribe`, and the real production adapter `coreAPI` (`skeleton/api.go:39-66`)
forwards NEITHER journal method — so every `journal_read`/`journal_subscribe` on the real
binary returns "journal not supported by this daemon" (`server.go:1058`). Invisible to
tests: daemon tests call `d.JournalReadFrom` directly; protocol tests use `journalStub`;
no test constructs the real assembled path. The evidence doc claims the protocol pieces
"deliver" these ops without disclosing the assembled daemon cannot serve either. (Sonnet
C1.) *Scope note*: the gateway/assembly wiring may be a deliberately later slice — but
then it is a disclosure gap, and the slice's "delivers journal_read/subscribe" claim is
overstated. *Fix*: add `JournalReadFrom`/`JournalSubscribe` forwarders to `coreAPI`.

**DHI-2. `Resume.Roster` — the snapshot half of R-JRN.4 — is missing and structurally
unimplementable on the wire.** `journal.ReadFrom` never sets `Resume.Roster`
(`journal.go:228`); worse, `protocol.JournalResume` (`remote.go:40-44`) and
`protocol.Control` carry NO roster field, so even a populated `Resume.Roster` could not be
sent. A fresh phone (`from=0`) or one told `FullResync=true` has nothing to resync FROM,
and `List()` is cursor-less so calling it separately races the journal cursor. Opus adds:
a reconcile-reconnected Running session emits NO journal record (`registerRunning`->`putMem`,
no `saveMeta`), so it is invisible in the event stream too — the phone cannot enumerate the
current roster by any path. (Sonnet C2; Opus HIGH-2; lead #3.) *Fix*: build Roster from the
meta scan atomically under the journal lock and add a wire field, OR formally amend R-JRN.4.

**DHI-3. The two-phase state machine is a defect as wired — only `Prepare` is used.**
`Begin`/`Complete`/`Fail`/`ResolveOutcomeUnknown` have ZERO daemon callers (grep-confirmed;
`store_test.go` only). Every launch record is created `prepared` and never advances, so
the replay path reads no phase and assumes success (feeds DCR-1's W3/W4). Further,
`ServeRemote` requires `operation_id` on `kill`/`delete` (`server.go:1246`,
`requireOperationID`) but those handlers never touch `d.idem` — presence-checked then
discarded: a false-advertised idempotency contract. `interrupt` (the one op that is NOT
naturally idempotent and needs `outcome_unknown`) has no daemon wiring at all. The store's
`TestIdempotency_CrashBetweenExecuteAndCommitNoDoubleExecute` exercises `Begin`->crash — a
path the daemon never takes — so the tested guarantee is unreachable in production.
(Sonnet H1; Opus in the adjudication; lead #2.) *Fix*: wire `Begin`/`Complete`/`Fail`
around the spawn and into kill/delete/interrupt, OR stop gating those ops on
`operation_id` and scope interrupt/`ResolveOutcomeUnknown` explicitly as unimplemented.

### MEDIUM

**DME-1. Retention / `FullResync` machinery is dead in production; the journal grows
unbounded.** `daemon.go:190` opens the journal with `Options{}` (`MaxBytes=0,MaxFiles=0`),
so `enforceRetentionLocked` no-ops and `floorLocked` never rises -> `FullResync` is
unreachable on the real daemon (unit-tested only), while the on-disk journal grows one
fsync'd line per transition forever. `daemon.Config` exposes no bound fields. (Sonnet H3;
Opus MED-2.) *Fix*: thread real `MaxBytes`/`MaxFiles`/idempotency-TTL from config; add a
daemon-level (not bare-package) FullResync + Compact test.

**DME-2. `journal_subscribe` has no atomic read+subscribe and returns no cursor.**
`handleJournalSubscribe` (`server.go:1026-1046`) registers then `replyOK("")` with no
cursor; it is a separate op from `journal_read`. The natural client order
`journal_read(from)` then `journal_subscribe` silently DROPS every event appended between
the read completing and the subscriber registering — and the ack carries no cursor to
anchor a backfill. (Opus MED-1 — missed by lead and Sonnet.) *Fix*: register first, return
the current cursor in the subscribe ack, backfill+dedupe up to it.

**DME-3. `journal.Append` fsync serializes the write path (CORRECTION to lead #4).** The
append runs under `d.writeMu`, NOT `d.mu` (`d.mu` is released at `daemon.go:439` before the
append), so `List`/`Get` are NOT blocked — the lead's "all daemon operations serialize"
was mis-scoped. Real cost: every meta write now pays a SECOND sequential fsync (persist's
own + the journal's) under the single-writer lock -> doubled per-write fsync latency under
status flapping. (Sonnet M1; Opus MED-4.) *Fix*: batch/group-commit or move the fsync out
of the write-serialization section.

**DME-4. Dangling journal-without-meta records are not reconciled on replay.** WAL ordering
(journal before meta) is correctly honored, so a crash leaves the TOLERABLE direction — but
nothing reconciles an orphan `launched`/transition against the meta scan; with Roster
unpopulated (DHI-2) a phantom event on the stream is never corrected. Folds into DHI-2.
(Opus MED-3.)

### LOW

- **DLO-1. No parent-directory fsync** after segment/log create+rename
  (`journal.go:272`, `store.go:248`, and also `persist.go` meta.json rename) — a crash just
  after can lose the directory entry on some filesystems despite the file fsync. (lead #5;
  Sonnet M2; Opus LOW-1.)
- **DLO-2. `idempotency.Store` has no `Close()`** — fd released on GC only; benign given
  fsync-per-write. (Self-flagged; lead #6; Sonnet L1; Opus LOW-2.)
- **DLO-3. Wedged journal-subscriber eviction is event-driven, never time-driven** — a
  wedged-but-quiescent subscriber (queue full, no further events; production journal events
  are sparse) is never evicted, holding a goroutine+queue+socket. (Opus LOW-3 — missed by
  lead and Sonnet.) *Fix*: periodic liveness sweep.
- **DLO-4. No concurrency test for `idem.Prepare` racing on identical `operation_id`** —
  the mutex design looks correct but the exact flaky-mobile-retry race is untested. (Sonnet
  M3.)

## The flaky test is NOT a masked defect

Opus confirmed the S9 bounded-eviction PROPERTY is real (the writer bounds in-flight to
`1+eventQueueCap` before overflow, given the bounded `SO_SNDBUF` at `server.go:1035`);
`TestProtocol_JournalSubscribeOrderedAndEvictsWedged` flakes purely from CPU-starvation of
the fan-out goroutine under parallel load, as its own comments admit — not a hidden bug.
The genuine gap is the quiescent-wedge case (DLO-3), which the test does not cover.

## Confirmed-correct (adversarially checked, no defect)

WAL ordering "journal before meta" honored at EVERY choke-point path including `Delete`
(`lifecycle.go:90-93`) and the tombstone short-circuit; cursor monotonicity across restart
(retention never deletes the active segment, `Open` recovers the high-water cursor); the
`from+1 < floor` FullResync arithmetic (no off-by-one); the retention record-drop
`j.records[old.count:]` alignment (count == decodable records, both under `j.mu`);
`journalRecordFor` transition derivation. The lead's open questions #7 (retention
robustness) resolved UNFOUNDED.

## Verdict

**REVISE.** The primitives are solid and the existing-path wiring is correct, but the
slice is not yet a trustworthy crash-safety foundation: the launch idempotency delivers
D-5 in only 1 of 5 crash windows (silently returning dead/orphaned sessions in two of
them), the journal resume contract ships without its roster half (and cannot carry it on
the wire), and the journal ops are unreachable in the assembled daemon. Fix DCR-1/DCR-2
and DHI-1..DHI-3 before building on it, and RE-RUN with codex once its auth is restored.
