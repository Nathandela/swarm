# GREEN evidence: Wave R7 round 4 -- the two BLOCKING rulings, MEDIUM 3, MEDIUM 4 and LOW 5

- **Date:** 2026-08-20 (UTC; per-command timestamps and exit codes in `r7-green-round4.txt`)
- **Role:** CLOSING FIX (implementer), round 4. Rounds 1-3 are `README.md`, `README-round2.md`
  and `README-round3.md` beside this file; RED is `docs/verification/r7-red/r7-red-round4.txt`
- **Bead:** `agents-tracker-hggx.8`
- **Design:** ADR-013 Amendment 2026-08-20 **revision 4** (this round wrote it: the root cause,
  the three rulings, §R7.2e's struck measurement sentence and §R7.7's fourth case and non-case)

## What a reader CAN and CANNOT conclude

**CAN.** The ordinary fresh Codex launch now works, and it is proved by a test that drives the
real `joinSessionBackend` against a real WebSocket app-server over a real UDS: no
`structured_gap`, no durable degrade, a live message sink before any turn exists, and a phone
`composer_send` that arrives at the app-server as `turn/start`. **Thirteen of thirteen mutations
fire** -- every fix below had its production line changed, the named test failed, the failure
message is printed in `r7-green-round4.txt`, and the change was reverted.

**CANNOT.** *The wave's exit criterion is still not demonstrated end to end against the real
CLI.* No test has driven a real Codex TUI and the phone against one live thread; the probes that
would are behind `//go:build realcli` + `SWARM_REALCLI=1` and were not run this round (hard rule
6 -- the real binary is bound to the owner's own ChatGPT account). The app-server in the new
tests is a faithful stand-in: it performs the recorded WebSocket upgrade, announces
`thread/started` when a client completes `initialized`, and -- the detail that makes these tests
probe the real deadlock rather than a convenient one -- **it refuses `thread/resume` with the
recorded `no rollout found for thread id` until a `turn/start` has created the rollout.**

**And ~~one residual~~ residuals a reader must NOT over-read** (corrected in place 2026-08-20 —
two more were disclosed pre-commit, see items 4 and 5). The phone still treats ANY `structured_gap`
as "no message sink" and hides the composer; a successful REJOIN silently bridges the
daemon-downtime window; and Ruling 2's pre-subscription window can dispatch a second concurrent
turn. See "Disclosed, not fixed".

## The root cause, in one paragraph

The R1 gate RECORDED (`r1-codex-gate.md:112-115`) that `thread/resume` answers `no rollout found
for thread id` until a thread's FIRST TURN starts. The design joins the thread at session
LAUNCH, before any turn exists. Round 3 then compounded that three ways: a retry set `late=true`
and the join emitted `structured_gap` reason `backend_joined_late` **on the success path**; the
phone reads any gap as "no message sink" (`TranscriptPanel.kt:331` -> `SessionDetailPanel.kt:770-772`
-> `Composer.kt:89-91`), so the composer disappeared; and `registerBackend` ran only AFTER the
resume succeeded, so until then `resolveMessageSink` found no backend and refused
`structured_unsupported` -- the phone could not start the very turn that would create the
rollout. On an ordinary fresh launch the wave's exit criterion was structurally unreachable, and
an owner who thought for 45 s got a PERMANENTLY degraded session while the app-server was
perfectly healthy.

## The findings and their fixes

| # | Finding | Fix | Production file:line |
|---|---|---|---|
| R1 | a join that missed NOTHING emitted a gap; the honest case emitted none | the FIRST resume attempt is the only one carrying information: a rollout that ALREADY exists proves prior unread turns -> `backend_prior_history`; a rollout that does not exist proves no turn has happened -> silence | `internal/skeleton/backendconnect.go:214-234`, `internal/skeleton/backend.go:615-634` |
| R2 | the message sink was the SUBSCRIPTION, so sending depended on having read history | `registerBackend` runs as soon as the connection is initialized and usable for `turn/start`; the subscription is a separate, possibly-pending concern that gates no operation | `internal/skeleton/backendconnect.go:227`, `internal/skeleton/backend.go:98-112` |
| R3 | a healthy backend was PERMANENTLY degraded after 45 s of the owner thinking | `subscribeSessionThread` retries for the life of the session, bounded by the session's own backend registration, backoff 100 ms doubling to a 5 s ceiling; only a NON-rollout failure tears | `internal/skeleton/backendconnect.go:334-386` |
| M3 | the 200 ms fold's headline number was measured against a stream that never occurs | a frame that can shape NO interaction cannot reorder a transcript, so it no longer flushes the fold; the test feeds the RECORDED mix | `internal/skeleton/backend.go:381-414` |
| M4 | `joinSessionBackend` and `watchSessionBackend` had no behavioural test of any arm | every arm of both, driven through the real function against a real app-server | `internal/skeleton/r7r4_join_test.go` (9 tests) |
| M4b | the Serve catch-up grep ran from an anchor to END OF FILE | scoped to `Serve`'s own body | `internal/skeleton/r7fix_test.go:415-443` |
| LOW 5 | `deriveSessionCapabilities`' `liveBackend` argument has no production caller | DISCLOSED, not wired -- see below and the KDoc | `internal/skeleton/capability.go:334-345` |

## The mutation record

Every row: the production line was changed, the named test was run, it FAILED with the message
quoted, and the change was reverted. Full output in `r7-green-round4.txt`.

| # | Mutation | Test that failed |
|---|---|---|
| M1 | register the backend only `if subscribed` (round 3's order) | `TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists` -- "the session has NO message sink after 20s" |
| M2 | emit `gapBackendPriorHistory` on the not-yet-subscribed path | same test -- "1 structured_gap(s) were journalled: [backend_prior_history]" |
| M3 | delete the `gapBackendPriorHistory` arm | `TestR7R4_AThreadThatHadALREADYRunTurnsBeforeTheJoinIsAnHonestGap` |
| M4 | treat the recorded rollout race as a hard failure (`if true`) | `TestR7R4_AUserWhoThinksLongerThanTheJoinDeadlineIsNEVERPermanentlyDegraded` -- "thinking for longer than the join deadline PERMANENTLY degraded a healthy session" |
| M5 | delete the `thread/status/changed` row from `backendFoldPassthrough` | `TestR7Pump_AFrameThatCanShapeNOItemDoesNotFlushTheFold/thread/status/changed` |
| M5b | make every frame a boundary again (round 3's behaviour) | `TestR7Pump_TheRECORDEDFrameMixStaysInsideTheAppendCeiling` -- **10** folded records in 1.10 s, against a machine budget of 8/s |
| M6 | delete `noteBackendUnavailable` from the dial-failure arm | `TestR7R4_ADialThatNeverSucceedsGapsDegradesAndRELEASESTheShimAnyway` |
| M7 | delete `conn.Close()` from the no-thread-announced arm | `TestR7R4_AThreadNeverAnnouncedGapsDegradesAndCLOSESTheConnection` |
| M8 | accept any resume error at the join (`if false`) | `TestR7R4_AHardResumeFailureAtTheJoinRefusesToRegisterADeafBackend` |
| M9 | make the subscription loop's hard-failure arm silent | `TestR7R4_ASubscriptionThatFailsHARDAfterRegistrationTearsHonestly` |
| M10 | delete `go d.watchSessionBackend(id, conn)` from the join | `TestR7R4_TheWatcherGapsTheTailWhenTheAppServerDiesMidSession` |
| M11 | delete the already-ended guard from `watchSessionBackend` | `TestR7R4_TheWatcherIsSILENTForASessionThatAlreadyEnded` |
| M12 | move `connectBackendsForRunning()` out of `Serve` into `registerSession` (BELOW it in the file) | `TestR7Fix_ServeCatchesUpTheBackendsOfSESSIONSItAdoptedAtReconcile` |

**M10 is worth reading twice.** Its first form SURVIVED: while the subscription is still
retrying, its own hard-failure arm notices the dead connection too, so the test passed with no
watcher at all. The test now lands the subscription first, which is what makes it a fence on the
WATCHER rather than on whichever goroutine wins. **M12 is the decisive one for M4b**: the round-3
grep, which searched from its anchor to end of file, passes under exactly this mutation.

## Ruling 4's trace: does any SUCCESS path still emit a gap?

Every `structured_gap` producer reachable from the backend, traced:

| site | when | success path? |
|---|---|---|
| `backendconnect.go:187` | the dial never succeeded | no -- failure |
| `backendconnect.go:201` | no `thread/started` within the deadline | no -- failure |
| `backendconnect.go:218` | `thread/resume` failed for a NON-rollout reason | no -- failure |
| `backendconnect.go:232` | the rollout ALREADY existed: prior unread turns | **YES** -- see below |
| `backendconnect.go:262/269/276` | rejoin: dial, discovery or resume failed | no -- failure |
| `backendconnect.go:374` | the subscription can never be established | no -- failure |
| `backendconnect.go:482` | the app-server connection closed mid-session | no -- failure |
| `hookdrain.go:603` | Claude's hook-channel proven gap (untouched) | no -- failure |

**On the ORDINARY FRESH LAUNCH no success path emits a gap**, and that is asserted, not argued:
`TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists` fails if any gap
reaches the journal, and `TestR7R4_TheWatcherGapsTheTailWhenTheAppServerDiesMidSession` repeats
the check as a control before it kills the server.

**The one exception is real and is stated plainly.** A join to a thread that had ALREADY run
turns (a `codex resume`-shaped session) emits `backend_prior_history`, because the transcript
genuinely begins mid-conversation and ADR-017 forbids bridging that silently. On the phone, that
gap will hide the composer for that session -- which is wrong, and which is the residual below.
It does NOT degrade the session durably: the tear is in the history, the channel is healthy.

## Ruling 2's ordering hazard, and exactly how it is covered

**Scope, stated 2026-08-20: this section is about item CAPTURE only.** The same window has a second
consequence for turn DISPATCH which it does not cover — see residual 5 under "Disclosed, not fixed".

A turn can now be started before the subscription is live. That turn's items are still captured,
by three facts and one test:

1. `thread/resume` on a RUNNING thread is a rejoin that delivers that thread's live stream --
   RECORDED (`r1-codex-gate.md:93-119`): the R1 observer joined a turn already in flight and
   received 97 frames of it.
2. The daemon's own `turn/start` is what creates the rollout, so the very next retry (<= one
   backoff interval, 100 ms at first) succeeds. The daemon reconstructs nothing.
3. Any item whose opening deltas fell inside that window is completed by `item/completed`, which
   carries the item's FULL text (RECORDED: `frame-samples.json`, and the shaper reads it at
   `adapter/codex/interaction.go`'s `itemCompleted`) -- so a partially-missed message recovers
   its whole text rather than staying truncated.

Tested: `TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists` sends
BEFORE any turn exists, waits for the subscription to land (>= 2 `thread/resume` at the server),
pushes a recorded frame and asserts it reaches the journal.

## What bounds the retry loop

Not a timer: **the session**. `subscribeSessionThread` re-reads `sessionBackendFor(id)` on every
iteration and returns when the registration is gone -- which `forgetBackend` does from
`endSession`, from `noteBackendLost`, and from the loop's own hard-failure arm. Backoff starts at
`backendReadyInterval` (100 ms) and doubles to `backendSubscribeMaxBackoff` (5 s), so an owner
who thinks for a minute costs ~15 requests rather than 600, and a turn started at any moment is
joined within 5 s.

## Disclosed, not fixed

1. **The phone conflates every `structured_gap` with "no message sink".**
   `TranscriptPanel.kt:331` sets `structureTorn` on any gap, `SessionDetailPanel.kt:770-772`
   derives `structuredChat` from it, and `Composer.kt:89-91` renders ABSENT. That is CORRECT for
   a lost sink and WRONG in general. Rulings 1-3 remove every case that reaches it on the
   ordinary path; the `backend_prior_history` case still reaches it. **No Android change was made
   this round** -- the R6 Kotlin is reviewed, the blockers were fixable daemon-side, and the
   orchestrator files the conflation as a follow-up bead.
2. **`deriveSessionCapabilities` is dead code (LOW 5).** Its `liveBackend` argument has no
   production caller, and neither does its own caller `registerSessionCapabilities`. R7 landed
   the per-session-instance correction rather than leave a known-wrong derivation for a later
   slice to inherit, but **no live session has a capability record at all today**, and the only
   production-reachable capability fact is the durable degrade marker. A reader must not conclude
   that a Codex session's capability record says any of this. The KDoc now says so in as many
   words; wiring it is `agents-tracker-hggx.2.1`'s slice.
3. **Round 3's residuals stand unchanged**: the terminal-wins approval mis-attribution window
   (ADR-013 §R7.10 item 6), and "rejoin is not distinguished from a fresh join" at reconcile.

*Items 4 and 5 were added 2026-08-20 as a pre-commit correction, before this wave was committed.
Neither changes behaviour; both name a loss this evidence file previously did not.*

4. **A successful REJOIN silently bridges the daemon-downtime window.** `rejoinSessionBackend`
   emits no `structured_gap` (§R7.7 case 2), and the comment justifying that asserted the earlier
   turns "were captured by the daemon that launched it and are in the journal already". **That is
   false.** Turns that ran while NO daemon was attached — the agent keeps working against the
   surviving shim — are in the app-server's rollout and NOT in the journal, because a client
   receives a thread's items only from the point it resumes. So a `swarm daemon restart` that spans
   any terminal activity produces a transcript that continues as if nothing were missing.
   **The behaviour is deliberately unchanged**: the phone reads any gap as "no message sink"
   (residual 1 above), so gapping honestly would remove the composer for the whole session on every
   restart — trading a history tear for a capability loss. **What would close it:** backfill via
   `thread/read {includeTurns:true}`, gapping only what the backfill cannot recover — ADR-013
   **Q4**, open because that call's practical `itemsView` is unrecorded and `summary` is lossy.
   Corrected in place: `internal/skeleton/backendconnect.go`'s `rejoinSessionBackend` comment and
   ADR-013 §R7.7 case 2.

5. **Ruling 2's own window can dispatch a SECOND concurrent turn.** "Ruling 2's ordering hazard"
   above covers item CAPTURE and is correct as far as it goes; it does not cover TURN DISPATCH.
   Between `registerBackend` (`backendconnect.go:227`) and the subscription landing, the daemon
   holds a live message sink and **no turn id**, so a phone `composer_send` carrying
   `expected_turn: ""` matches the precondition and takes the `turn/start` branch **even if the
   terminal already started a turn this daemon has not observed** — which by `chat.go:247`'s own
   rule queues a second turn, so the owner's question and the phone's become two conversations on
   one thread. **Ruling 3's backoff widens it**: retries double to a 5 s ceiling, so on a session
   whose owner has been thinking a terminal-started turn can go unobserved for up to about 5 s.
   **Observable:** start a turn at the terminal, send from the phone inside that window, and the
   agent answers the phone's message as a separate turn afterwards instead of steering the one in
   flight. The corpus does not record what the server does with `turn/start` on a busy thread
   (`errors-observed.json` holds three errors, none of them this), so queue-vs-refuse is unprobed;
   either way the phone is told the send succeeded. **Not narrowed:** `turn/started` is the only
   candidate signal, is recorded only AFTER `thread/resume`, and shapes no interaction by design
   (`adapter/codex/interaction.go:140-148`) — reading it would source a turn at the pump, which
   IS-ENV-1 reserves to the daemon and which would change `expected_turn` on a shipped wire. A
   send-time `thread/resume` proves only that a rollout exists, which is equally true of an idle
   thread. Reverting Ruling 2 reinstates the deadlock. This needs a probe, not a patch.

## Tests deleted or corrected this round, and why

Two round-3 assertions fenced behaviour that revision 4 rules FALSE. They were corrected before
the production change, not after it:

- `TestR7Fix_TheThreadJoinRetriesOnlyTheRecordedRolloutRaceAndGapsWhenItWasLate` asserted that
  the join emits a gap when the resume needed retries. That inference is backwards. Renamed to
  `TestR7Fix_TheThreadJoinRetriesOnlyTheRecordedRolloutRace`, keeping the retry-predicate fence
  and adding one on the loop's tear; the honest gap is fenced BEHAVIOURALLY in
  `TestR7R4_AThreadThatHadALREADYRunTurnsBeforeTheJoinIsAnHonestGap`.
- `TestR7R3_TheRecordedRolloutRaceIsRETRIEDAndReportedLATE` -> `...WithoutClaimingAnythingWasMissed`:
  the retry still has to happen and now must be SILENT.

**One further correction, 2026-08-20 (pre-commit).**
`TestR7Pump_TheRECORDEDFrameMixStaysInsideTheAppendCeiling` measured an `elapsed` duration and then
thresholded on a FIXED count (`offered > 6`), so a loaded runner that stretched its ~1.09 s loop
would fail a CORRECT implementation — a knowingly-flaky gate. It now asserts the RATE it already
measures, `offered <= 5*elapsed.Seconds()+1`, keeping the +1 flush slack and the same intent. The
M5b mutation was re-run against the corrected assertion and still fires: **10 folded records in
1.087 s — 9.2/s against a bound of 6.4** (the failure message names both). Teeth hold for any
elapsed below 1.8 s; above that the mutated fold genuinely is inside the 8 appends/s ceiling at
that feed rate, so passing there is correct rather than a lost fence.

## Gates

`go build ./...`, `go vet ./...`, `golangci-lint run` (v2.12.2), `go test -race -count=1` on the
twelve owned packages plus `internal/verify`, plus the GG-7 `protocol.md` bidi drift check.
Timestamps and exit codes: `r7-green-round4.txt`.

**B94 delta: none.** No exported symbol was added; every new function (`resumeThreadOnce`,
`subscribeSessionThread`, `markBackendSubscribed`, `backendSubscribed`) is unexported, as is
`backendFoldPassthrough`.

**GG-7 delta: none.** No wire field and no op changed. `backend_prior_history` is a value inside
an existing `structured_gap` record's free-text reason, replacing `backend_joined_late` in the
same position; it is not a new key.

## The authoritative gate run: the orchestrator's pre-commit audit (2026-08-20)

The gate records above were taken on a machine carrying up to 28 orphaned `swarm shim` and
fake-agent processes from earlier runs, which is the load that makes `internal/daemon`,
`internal/shim` and `internal/skeleton` fail with `device not configured` and `shim did not
confirm serving`. Every orphan was reaped first, so this run is the one the commit stands on.
It also served as a controlled measurement of the leak itself (bead `agents-tracker-ev0w`).

| Gate | Window (UTC) | Exit | Result |
|---|---|---|---|
| starting state | 10:58:47 | — | **`ORPHANS_BEFORE=0`**, 21 `/dev/ttys` entries — verified clean, not assumed |
| `go build ./...` | 10:58:47 → 10:58:49 | **0** | — |
| `go vet ./...` | 10:58:49 → 10:58:51 | **0** | — |
| `golangci-lint run` (v2.12.2, the version pinned in ci.yml) | 10:58:51 → 10:58:52 | **0** | **`0 issues.`** |
| `go test -race -count=1` over internal/skeleton, internal/daemon, internal/shim, internal/appserver, internal/adapter/..., internal/protocol/..., internal/remotegw, internal/phonecore, mobile/..., internal/verify, android/gate | 10:58:52 → 11:05:56 | **0** | every package `ok`; GG-7's bidi check inside protocol, `TestB94` inside verify |
| whole-repo `go test -count=1 ./...` (what CI runs) | 11:07:23 → 11:15:23 | **0** | zero `FAIL` |

A first attempt at the whole-repo pass was stopped externally one minute in, at 11:05:56Z; it is
recorded here because it happened, and it is superseded by the completed 11:07:23Z run rather
than quietly replaced by it.

**The leak reproduced under this run, on the orchestrator's own measurement.** R7's daemon
backend tests (`TestR7BackendLaunch_*`, `TestR7BackendReaper_*`, `TestR7BackendLifecycle_*`)
leave orphaned shims exactly as the pre-existing skeleton rigs do. What did NOT leak is the
thing this wave built: no `swarm-fake-codex` and no app-server child survived, so the backend
containment holds. The rig-cleanup defect is shared by old and new tests and is filed, not
fixed here. Four REAL `codex app-server` processes from earlier realcli probes were also found
running 3+ hours against the owner's account and reaped; note that the owner's VS Code ChatGPT
extension runs a process of the same shape, so reaping must be by explicit pid, never by a
pattern match on `codex` or `app-server`.
