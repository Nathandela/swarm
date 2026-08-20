# GREEN evidence: Wave R7 -- Codex as a token-live native backend (Mirror M4.1-M4.5)

- **Date:** 2026-08-20 (UTC; per-command timestamps in `r7-green.txt`)
- **Role:** GREEN (implementer), round 1. RED is `docs/verification/r7-red/`.
- **Bead:** `agents-tracker-hggx.8`
- **Design:** ADR-013 Amendment 2026-08-20 §R7.1-§R7.10 (revision 2), ADR-010 Amendment
  2026-08-20 (revision 2)

## What a reader CAN and CANNOT conclude

**CAN:** every R7 contract test in the RED inventory is green, and the wiring is REAL rather
than test-only -- the B94 reachability ledger passes with NO new allowlist entries, which is
only possible because `adapter.ResolveBackend`, `adapter.AsBackendSource`,
`adapter.CheckBackendPlan` and the whole `internal/appserver` client are reachable from a
production entry point. Ten named mutation fences were exercised; eight fire, and the two that
do not are named below with the reason.

**CANNOT:** *nothing here shows a working Codex backend against the real CLI.* No test in this
wave has talked to a real `codex app-server`. The two `//go:build realcli` measurements
(rollout-to-resume, and whether `thread/read{includeTurns}` backfills losslessly) are written
and were **NOT RUN**; they remain open in `r1-codex-fixtures/r7-open-questions.md`. No Android
work is in this slice, so the phone's own rendering of a Codex session is unverified end to end
on a device. The terminal-TUI-and-phone-drive-one-thread exit criterion is **not** demonstrated
here.

## Wired vs parked

**WIRED (production-reachable):**

- the Codex adapter's `BackendSource` and `InteractionSource`, and the two new SignalSource rows
- `adapter.ResolveBackend` (obligations 8/9a/9c) via `skeleton.planSessionBackend`, which is
  `daemon.Config.BackendPlanner`, called from `daemon.spawnShim`
- the shim's backend child: own process group, own `Wait`, `backend.json`, the `backend_attach`
  go-ahead, `exit.json.backend_exit`, and the die-first edge
- `daemon.SessionBackend` / `backendAliveAt` / `reapOrphanBackend` (the reaper is called from
  `reconcile`'s orphan path)
- `internal/appserver`: dialed from `skeleton.joinSessionBackend`, which runs
  `initialize`/`initialized`/`thread/start`, sends the go-ahead, registers the connection and
  watches it for the lost edge
- the producer-edge pump: `ingestBackendFrame` is the client's `OnNotify`/`OnRequest`
- `Engine.ApplyTypedEvent`, driven by the pump
- the composer/interrupt/approve sink resolution, and the capability derivation

**PARKED, and named as such:**

- **`thread/resume` is never called.** `joinSessionBackend` always starts a FRESH thread. Whether
  the TUI can be pointed at a daemon-created thread is `r7-open-questions.md` Q2 and is not
  recorded, so `AgentArgs` carries `--remote unix://SOCK` and nothing else; a daemon that
  restarts under a live shim starts a second thread rather than rejoining the first. This is
  the honest state of §R7.2e's "which `agent_args`" question, not a design decision.
- **The reconcile-adoption path does NOT join a backend.** `daemon.Open` runs reconcile
  synchronously and fires `OnSessionStart` (`registerSession`) BEFORE `Serve` has assigned
  `d.core` -- so both the single-writer token suppression and `connectSessionBackend` skip for a
  session adopted after a daemon restart. This was found the hard way: the first version
  dereferenced the nil core and killed the replacement daemon, breaking
  `cmd/swarm`'s `TestAttachDialer_AttachesAfterAutoUpgrade` (green at HEAD, red with R7 -- see
  `r7-green.txt`). It is now guarded and disclosed rather than hidden behind a nil-tolerant
  lookup that would silently do the wrong thing. **The launch path is unaffected** (the core is
  assigned by then).
- **`noteBackendRejoined` has no production caller.** Case 2 of §R7.7 is implemented and fenced
  but the restart path currently goes through `joinSessionBackend`, which treats a rejoin as a
  fresh join. It does not degrade or gap (which is the property that matters), but the
  distinction is not yet drawn in production.
- **The two realcli measurements were not taken** (see above).
- **No Android work.** The phone reads `structured_gap` off the transcript; that side is
  untouched.

## Per-M-row summary

| Row | Production file:line | Fenced by |
|---|---|---|
| M4.1 shim topology | `internal/adapter/backend.go:26` (BackendSpec/Plan/Source, `ResolveBackend:130`), `internal/adapter/codex/backend.go:33`, `internal/shimwire/shimwire.go:44` (`TypeBackendAttach`), `internal/shim/backend.go:41` (`BackendFile`), `internal/shim/shim.go:196` (the branch), `internal/shim/server.go:367` (`killGroups`), `internal/daemon/backend.go:40`, `internal/daemon/launch.go:130` (wire names), `internal/skeleton/backendconnect.go:76` (`planSessionBackend`) | `internal/adapter/r7_backendsource_test.go`, `internal/adapter/codex/r7_backend_test.go`, `internal/shimwire/r7_backendattach_test.go`, `internal/shim/r7_backend_test.go`, `internal/daemon/r7_backendlaunch_test.go` |
| M4.2 InteractionSource + batching | `internal/adapter/codex/interaction.go:113` (`Interactions`), `internal/skeleton/backend.go:229` (`ingestBackendFrame`), `internal/appserver/appserver.go:141` (`Dial`), `internal/remotegw/itemadmission.go:26` (`DefaultItemWindow`) | `internal/adapter/codex/r7_interaction_test.go`, `internal/skeleton/r7_pump_test.go`, `internal/appserver/r7_wsupgrade_test.go`, `internal/remotegw/r7_itemwindow_test.go` |
| M4.3 native approvals | `internal/skeleton/approval.go:558` (`applyNativeDecision`), `internal/skeleton/backend.go:430` (`retireResolvedRequest`), `internal/adapter/codex/interaction.go:329` (`Decision`) | `internal/skeleton/r7_approval_native_test.go`, `internal/skeleton/r7_backend_e2e_test.go` |
| M4.4 composer/interrupt dispatch | `internal/skeleton/chat.go:186` (`resolveMessageSink`), `chat.go:236` (`deliverComposerText`), `chat.go:470` (the interrupt branch) | `internal/skeleton/r7_composersink_test.go`, `internal/skeleton/r7_backend_e2e_test.go` |
| M4.5 typed status | `internal/engine/typedevent.go:43` (`ApplyTypedEvent`), `internal/adapter/codex/codex.go:56` (the two new rows) | `internal/engine/r7_typedevent_test.go`, `internal/adapter/codex/r7_signalsources_test.go` |

## Mutation verdicts

| # | Mutation | Test that must fail | Verdict |
|---|---|---|---|
| 1 | `finishEscalation` no longer kills `-backendPgid` | `TestR7ShimBackend_ATermIgnoringBackendIsDEADAfterRunReturns` | **FIRES** |
| 2 | delete `SysProcAttr{Setpgid:true}` on the backend | `TestR7ShimBackend_TheBackendLeadsItsOwnProcessGroup` | **FIRES** (as a Run timeout, not the pgid assertion -- the ungrouped backend survives the group KILL and parks the join) |
| 3 | join `backend.dead` BEFORE `finishEscalation` | `TestR7ShimBackend_TheJoinDoesNotBlockRun` | **FIRES** (with the test's own message verbatim) |
| 4 | backend liveness becomes `dial() == nil` | `TestR7BackendLiveness_ADialThatSUCCEEDSIsNotLiveness` | **FIRES** (and the recycled-pid sibling too) |
| 5 | delete the 9c containment check | `TestR7BackendSource_ObligationNineC_...` | **FIRES** (all four malicious fixtures) |
| 6 | `DefaultItemWindow` back to 125 ms | (named) `TestR7ItemWindow_AtThreeStreamingSessions...` | **VACUOUS as named** -- see below. Fires on `..._TheItemFloorIsTwoHundredFiftyMilliseconds...` and `..._ItemAdmissionDefaultsToTheItemFloorAndNotTheAppendFloor` |
| 7 | mint a hook token for a backend session | (named) `TestR7ApplyTypedEvent_RequiresNoTokenAndNoDurableSequence` | **VACUOUS as named** -- see below. Fires on the same test when `ApplyTypedEvent` is given a token requirement |
| 8 | reach the keystroke path on a Codex composer send | `TestR7ComposerSink_NoBackendAndNoKeystrokeSeamREFUSESHavingTypedNothing` | **FIRES** |
| 9 | degrade + gap on a successful post-restart rejoin | `TestR7Lifecycle_ADaemonRestartWithASuccessfulRejoinNeitherGapsNorDegrades` | **FIRES** (both assertions) |
| 10 | derive `structured_chat`/`interrupt` from the adapter TYPE | `TestR7Capabilities_StructuredChatIsSeamANDLiveBackendPerSessionInstance` | **FIRES** (and the Interrupt sibling) |

### The two vacuous fences, stated plainly

**#6.** `TestR7ItemWindow_AtThreeStreamingSessionsTheTerminalPlaneSTILLGetsSlots` drives the
queue with `Offer` calls only, at 200 ms intervals per session. `ItemAdmission` releases on an
`Offer` (or a `Flush`), so with no release ticker in the rig the release rate is bounded by the
OFFER instants -- 5/s at a 125 ms floor and 3/s at 250 ms. Both are below `CoalescingSink`'s
8 slots/s, so the terminal plane gets slots either way and the assertion cannot distinguish the
two windows. The arithmetic §R7.4 corrects is real; this particular test does not exercise it.
Making it bind needs the release ticker (`releaseInteractions`, which calls `Flush` at
`DefaultAppendWindow`) modelled in the rig. **Filed as a residual for round 2; the constant and
the wiring ARE fenced by the two sibling tests.**

**#7.** `TestR7ApplyTypedEvent_RequiresNoTokenAndNoDurableSequence` calls
`HandleCallback` with a `Callback` whose `Token` is empty, so the refusal it asserts comes from
the engine's own empty-token check and holds no matter what token the SESSION was registered
with. Minting one therefore changes nothing the test can see. Even weakening the engine's check
leaves the test green, because the callback's `Sequence: 1` is then rejected by the
per-dimension high-water `ApplyTypedEvent` already advanced -- which is itself the shared-
namespace hazard §R7.3 describes. The mutation that DOES fire is giving `ApplyTypedEvent` the
hook path's token requirement, which is the property the first half of the test actually pins.
Separately, **the single-writer property is now enforced in production**
(`internal/skeleton/serve.go:461`: a session with a backend is registered with NO hook token),
which is what §R7.3 asks for; it is simply not what that test measures.

## Test-rig defects found and corrected

None of these weakened an assertion; every one was a rig that could not run, or a rig that was
racy. They are listed because a reader comparing RED to GREEN will see the diffs.

1. `internal/appserver/r7_wsupgrade_test.go` and `internal/shim/r7_backend_test.go` and
   `internal/daemon/r7_backendlaunch_test.go` bound UDS paths under `t.TempDir()`. On macOS that
   exceeds the 104-byte `sun_path` limit and every bind failed with EINVAL. Replaced with the
   short-`/tmp` prefix this repo already uses for exactly this (`internal/hookclient`,
   `internal/remotegw`, `internal/skeleton`).
2. `internal/shim/r7_backend_test.go` called `dialShim` without `c.startReader()`; every other
   test in that package starts it explicitly, and without it no reply frame is ever recorded.
3. `internal/daemon/r7_backendlaunch_test.go`'s squatter was only reaped in `t.Cleanup`, so a
   correctly killed backend lingered as a ZOMBIE and `kill(pid, 0)` still succeeded -- the
   reaper fence could not have passed for any implementation. Now reaped in the background.
4. The skeleton rigs launched the fake agent with `"sleep 60"`, which is not a directive the
   fake-agent script grammar has: the agent exited on a parse error before the session was
   usable, and no `ask` step meant `assertPTYUntouched` could never observe stdin at all.
   Replaced with a script of `ask` steps (`r7StdinScript`).
5. `r7RecordingAdapter` (pump test) had no mutex. The pump has two emitters -- the connection's
   read loop and the fold's 200 ms timer -- so the unsynchronized `append` silently LOST
   entries, which is how the losslessness assertion first failed. It also tripped `-race`.
6. `r7CodexAdapter.Interactions` returned a canned list, so
   `TestR7ComposerSink_TheBackendBranchCorrelatesTheEchoEXACTLYAndNeverByText` -- which drives
   two frames differing only in `clientId` -- could only ever have journalled nothing. It now
   delegates to the REAL codex adapter when no canned list is set, which is strictly stronger.

## Pre-existing tests updated, with the reason

- `internal/skeleton/capability_test.go` --
  `TestDeriveSessionCapabilities_TracksAsInteractionSourceExactly` now passes `liveBackend=true`.
  Its claim (the seam is NECESSARY) is unchanged; §R7.7 narrows sufficiency for an adapter that
  also proves a `BackendSource`, and the per-session-instance behavior is the R7 test's.
- `internal/skeleton/interaction_cap_test.go`, `r6r3_chat_test.go` -- the pinned-clock advance
  moved from `remotegw.DefaultAppendWindow` to `remotegw.DefaultItemWindow`. The two floors were
  split by §R7.4 and this queue enforces the item one.
- `internal/skeleton/r6_interruptapply_test.go`, `capability_test.go`,
  `r6fix_chat_test.go` -- signature updates only (`deriveSessionCapabilities` gained
  `liveBackend`, `stampComposerEchoLocked` gained `clientRef`).

## A refactor this wave forced, named because it moved platform code

`processStartTime` moved out of `internal/daemon/identity_{darwin,linux}.go` into a new
`internal/procstart` package, and the daemon now delegates to it in one line. The shim must
produce the IDENTICAL value for its `backend.json` (the daemon's liveness check compares them)
and `internal/shim` cannot import `internal/daemon` -- the dependency runs the other way. Two
copies of the platform read would be a fact the two sides could silently disagree about, and
the symptom of disagreement is a daemon that reaps a healthy app-server or adopts a stranger's.
