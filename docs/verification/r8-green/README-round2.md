# Wave R8 — GREEN round 2 (2026-08-20)

> ## CORRECTION — 2026-08-20, Wave R8 CLOSING round
>
> **This document overstates the wave. Read `docs/verification/r8-green/README-closing2.md`
> first; where the two disagree, the closing document is authoritative.** (`README-closing.md`
> was authoritative from 2026-08-20 until closing round 2 on 2026-08-21; it now carries a
> banner of its own naming three of its claims as false, so read the two in that order.)
>
> The specific correction, in the words the closing review used: **R8 lands as the READ HALF
> ONLY.** Every claim in this file that a user, a phone, or "the product" CAN ENTER CONTROL of
> a terminal, or that the control plane is exercised end to end, is **STRUCK**.
> `protocol.TerminalInputSink` has no production implementation — `internal/skeleton.coreAPI`,
> which is what is passed as `srv.d`, has no `TerminalInput` method — so `handleTerminalInput`
> takes the `op_not_implemented` arm for every frame the product can produce, and there is no
> take-control affordance on any screen. The control-plane measurements in this file were taken
> at `r8Backend.written`, an IN-PROCESS FAKE standing in for a PTY, over a real
> `Gateway` + `ServeRemote`; the accurate statement of what they prove is **"bytes counted at
> the `TerminalInputSink` seam over the real gateway composition"**, which is a real and useful
> result and is not "bytes at the PTY".
>
> The positive corollary is also struck-through-into-place rather than left implied: **the
> raw-input attack surface in the shipped product is currently ZERO.**
>
> See ADR-017 amendment C0/C1 for the parking decision and its preconditions.


Bead `agents-tracker-hggx.9`. This document supersedes `README.md` (round 1) **wherever the two
disagree**, and it says so at each point rather than quietly rewriting it. Round 1's own record
stays on disk unedited, because "what was claimed and then found false" is part of the evidence.

Captured on `darwin 25.5.0 / arm64`, Go toolchain from `$HOME/go/bin`, `golangci-lint v2.12.2`,
JDK 21, Gradle 9.5.1 wrapper.

---

## 0. THE THREE CLAIMS IN ROUND 1 THAT WERE FALSE

Round 1's hard rule 8 is "state what a user CAN and CANNOT do", and these are the lines that
broke it. They are listed first because a reader who trusts the previous document is misled in
exactly these three places.

| Round-1 claim | Truth at the time | Now |
|---|---|---|
| §1 "CAN … Launch OpenCode or AGY and monitor it read-only from the phone" | **FALSE.** `cmd/swarm-remote/config.go` was the only production construction of `RemoteProfileV1` and it left `capability_record_version` and `terminal_view_version` at ZERO. `RouteSession` fails closed on `TrustsCapabilityRecord()` first, so **every** session — OpenCode included — routed to the status card. And because `ComposerAvailable` is the same predicate, R6's and R7's chat composer disappeared for **every** session: an R8 wiring defect silently regressed two preceding waves. | TRUE, and pinned through the real assembler by `TestR8Profile_TheShippedProfileRoutesBothDestinationsForReal` |
| §5 WIRED "Watch horizon: `Renew`/`WatchLive`/`Reap`/`UnwatchAll` … routed end to end" | **FALSE for three of four.** Only `Renew` had a production caller. Nothing called `Reap`, so an unrenewed watch never expired; nothing called `UnwatchAll`, so transport loss did not unwatch; and the emission path asked nothing about watch liveness. Round 1's mutation for this obligation mutated the **helper**, so it failed while the production property did not exist. | TRUE: `gw.bindWatchLiveness` in `NewService`, a reaper tick and a cancel-path `UnwatchAll` in `Service.Run` |
| §5 WIRED "`TerminalControlState` with backgrounding as a DIRECT trigger" | **FALSE.** `TerminalControlState.Background` and `LeaseState.Background` both existed with **zero** production callers, no facade verb reached either, and `PhoneActivity.onPause` documented that it "reaches no facade verb". Backgrounding still severed only by consequence of the disconnect — the answer T8-b was written to replace. | TRUE: `App.EnterBackground`, called from `PhoneSurface.release()`, which is `onPause`'s path |

---

## 1. WHAT A USER CAN AND CANNOT DO — re-stated after the repairs

**CAN, end to end, over the real assembled path:**

- Launch OpenCode or AGY and **monitor it read-only** from the phone. The shipped profile now
  declares the versions the fallback depends on, so the router reaches `terminal_fallback` for a
  session the machine routed there. Driven through `resolveGatewayParams` and `RouteSession`.
- Keep the chat composer on a healthy Claude or Codex session (the R6/R7 regression above).
- Read a machine-sanitized screen, bounded by the Snap's declared geometry **and, from this
  round, by the resolved `TerminalViewBounds` on the phone's own copy** — including when the Snap
  declares no geometry at all, which previously bypassed the row-count cap entirely.
- See the staleness state honestly: when the phone core reports the machine's terminal stream
  stale, the screen says so instead of rendering a silent machine as an idle terminal.

**CANNOT — each a ruling, not a gap:**

- Reach the fallback from a healthy Claude or Codex session: not in the app, not over the new
  verb, not over the legacy `terminal_subscribe`, not with an absent, inconsistent or
  instance-less record. Unchanged from round 1 and re-verified.
- **Hold raw-input authority past the signed 15-minute horizon by sending keepalives.** Round 1
  shipped exactly that: measured at t+4h40m through a 15-minute wall. The keepalive now renews
  only the 30-second missing-keepalive deadline; the horizon is stamped once at begin and nothing
  moves it.
- **Resume a control generation after the kill switch goes off and back on**, or after a device
  is revoked and re-paired. Round 1's per-frame checks only PAUSED the generation, and a pause
  reverses; both severances are now synchronous at the daemon.
- Hold a generation open from a background coroutine, a scheduled job or a service timer: the
  keepalive is bound to the one allowlisted screen, backgrounding severs directly, and the daemon
  sweeps an idle generation on its own clock with no inbound frame at all.
- Render the raw grid, or issue a watch, from any other Kotlin file **including the `ui/kit`
  directory**, which the round-1 gate exempted and which three measured mutations walked through.

**CANNOT YET — a reader must not conclude otherwise:**

- `TerminalViewV1` is still **not on the wire**. The phone reads the legacy `TerminalSnapshot`,
  which carries no `view_epoch`, `revision`, `reset`, `session_instance` or `rendered_at`. So the
  phone still **cannot distinguish a re-seed from a session REPLACEMENT**, and the snapshot-AGE
  half of the staleness indicator is still inert (`ageMs` is 0). The stream-staleness half is now
  live; the age half is not, and the screen does not pretend otherwise.
- The **control plane is still not composed into the fallback screen**: no "enter control"
  affordance, no keyboard, no banner drawn. Every seam beneath it is now real and driven —
  including the phone-side generation ingestion that round 1 left with no caller — but a user
  cannot type into a fallback session from the shipped UI. §5 carries this as parked, and §1's
  "CANNOT" above is about anything that speaks the protocol, not just the UI.
- T3's version-skew row stays deferred (`agents-tracker-hggx.2.1`), so an unrecognised Claude or
  Codex build still opens as structured chat rather than as a labelled read-only terminal.

---

## 2. PER-OBLIGATION SUMMARY (round-2 findings)

| # | Finding | Production fix | file:line | Pinning test | Mutation |
|---|---|---|---|---|---|
| BLOCKER 1 | keepalive extended the signed horizon | horizon and missing-keepalive deadline split into two fields; keepalive touches only the second | `internal/protocol/remote_terminal.go:126` (`TerminalKeepaliveTTL`), `:171-183` (`terminalGeneration`), `:283-287` (both walls per frame), `:370` (keepalive) | `TestR8Control_TheKeepaliveDoesNotExtendTheSignedHorizon` | M1 CAUGHT |
| BLOCKER 2 | kill switch only paused the generation | `Server.severTerminalControl` called from `SeverAllRemoteControl` | `internal/protocol/server.go:1747` (call), `:1683-1700` (impl), `remote_terminal.go:378-393` (`severTerminalGeneration`) | `TestR8Control_TheKillSwitchSeversRatherThanPauses` | M2 CAUGHT |
| BLOCKER 3 | shipped profile was all zeros ⇒ every session status card, composer gone | production profile populated | `cmd/swarm-remote/config.go:143-165`, `internal/protocol/schema/capability.go:24` (`CurrentCapabilityRecordVersion`), `schema/terminalview.go:60-70` (declared bounds) | `TestR8Profile_TheShippedProfileDeclaresTheVersionsTheFallbackDependsOn`, `TestR8Profile_TheShippedProfileRoutesBothDestinationsForReal` | M5 CAUGHT |
| MAJOR 4 | `Reap`/`WatchLive`/`UnwatchAll` had no production caller | predicate bound in `NewService`; reaper tick and cancel-path unwatch in `Run`; emission path consults it | `internal/remotegw/service.go:326` (bind), `:547-566` (reaper + unwatch), `gateway.go:106-133` (`watchStillLive`), `gateway.go:293-299` (per-emission), `terminal_watcher.go:199-214` (`reapInterval`) | `TestR8Wiring_*` (4 tests) | M6, M7, M8 CAUGHT |
| MAJOR 5 | watch re-issued per redraw, never closed, never renewed | `reconcileTerminalWatch` + redraw guard | `PhoneSurface.kt:2208` (reconcile call), `:2513-2521` (guard), `:2555-2593` (reconcile), `:1468` (unwatch on release) | `TestR8Gate_TheWatchIsOpenedOnceClosedAndRenewed` | M17 CAUGHT |
| MAJOR 6 | T8-b unreachable — no facade verb, no lifecycle call | `App.EnterBackground`, called from `PhoneSurface.release()` | `mobile/app.go:466-495`, `PhoneSurface.kt:1490-1503`, `PhoneActivity.kt:181-192` (comment repaired) | `TestR8Background_*` (3 tests) | M9 CAUGHT |
| MAJOR 7 | gate evaded by rename + by the `ui/kit` exemption | identifier-anywhere patterns, bare `.peek(` banned, unexempted scanner | `android/gate/r8_fallback_ui_test.go:41-63` (patterns), `:65-118` (`r8AllProductionKotlin`) | `TestR8Gate_OnlyTheFallbackScreenIssuesAWatch`, `...PrintsTheTerminalWell` | M10, M11, M12 CAUGHT |
| MAJOR 8 | device revocation only paused the generation | `severTerminalControl` from `severRevokedDeviceControl` | `internal/protocol/server.go:1678` | `TestR8Control_DeviceRevocationSeversRatherThanPauses` | M3 CAUGHT |
| MODERATE 9 | live staleness verdict discarded, screen asserted freshness | `snap.stale` carried through `TerminalGrid` to a distinct sentence | `TerminalFallbackScreen.kt:24-45` (`TerminalGrid`), `:196-217` (`STREAM_STALE`, `stalenessLine`), `:280-296` (`grid()`), `TerminalFallbackView.kt:89/119` | `TestR8Gate_TheStalenessSignalIsCarriedRatherThanDiscarded` + 2 Robolectric tests | M13 CAUGHT |
| MODERATE 10 | `TerminalViewBounds` had no production caller | applied in `App.Peek` to the copy the phone hands a view | `mobile/app.go:1030-1069` | `TestR8Bounds_*` (4 tests) | M14 CAUGHT |
| MODERATE 11 | grid read reachable with no capability read, outside the allowlist | peek moved into the allowlisted binding, behind the record read | `TerminalFallbackScreen.kt:280-296`, `FacadeBridge.kt:83-92` (delegates) | `TestR8Gate_TheGridReadIsGatedOnACapabilityRead` | M15 CAUGHT |
| MINOR 12 | undeclared row count bypassed the cap | `snapRowCountCap` + `snapDefaultRowCeiling` | `internal/vt/render.go:153`, `:172-193` | `TestR8RowCap_*` (4 tests) | M16 CAUGHT |
| PROCESS 13 | control half live on the wire with no behavioural test | 7 tests over the real assembled remote-tier server, each measuring bytes at the PTY sink | `internal/protocol/r8_controlbehaviour_test.go` | — | M1–M4 all run through it |
| EVIDENCE 14 | three false claims in the round-1 README | §0 of this document | — | — |
| (T6-c) | no daemon idle-expiry timer (round-1 parked item 3) | `terminalSweepLoop` started in `newServer`, so it exists under `NewServer` too and not only under `Serve` | `internal/protocol/server.go:349-355`, `:1702-1720` (`sweepTerminalControl`), `remote_terminal.go:415-430` (`expireTerminalGeneration`) | `TestR8Control_AnIdleGenerationIsSweptOnTheServersOwnClock` | M4 CAUGHT |
| (round-1 parked item 4) | signed `terminal_control_begin` never driven end to end | the whole of `r8_controlbehaviour_test.go` drives it, signed, over `ServeRemote` | — | `TestR8Control_ALiveGenerationTypesOntoThePTY` is the vacuity guard | — |

---

## 3. MUTATION VERDICTS — 17 obligations, 17 caught, 0 escapes

Full transcript: `docs/verification/r8-red/round2-red.txt`. Each row restores exactly one pre-fix
production line (or adds one attacker-shaped call site), runs the permanent test, records the
named failure, reverts. Every mutation targets a line in **production** code, never a helper.

| # | Mutation | Test that failed |
|---|---|---|
| M1 | keepalive also moves `termGen.horizon` | `TestR8Control_TheKeepaliveDoesNotExtendTheSignedHorizon` |
| M2 | `SeverAllRemoteControl` stops severing generations | `TestR8Control_TheKillSwitchSeversRatherThanPauses` |
| M3 | `severRevokedDeviceControl` stops severing generations | `TestR8Control_DeviceRevocationSeversRatherThanPauses` |
| M4 | `terminalSweepLoop` not started | `TestR8Control_AnIdleGenerationIsSweptOnTheServersOwnClock` |
| M5 | `CapabilityRecordVersion` dropped from the shipped profile | both `TestR8Profile_*` |
| M6 | `gw.bindWatchLiveness` removed | `TestR8Wiring_TheServiceBindsTheWatcherToTheGateway` |
| M7 | reaper tick stops calling `Reap` | `TestR8Wiring_RunReapsExpiredWatchesAndUnwatchesOnTransportLoss` |
| M8 | cancel path stops calling `UnwatchAll` | `TestR8Wiring_TheCancelPathUnwatchesRatherThanWaitingForRunToReturn` |
| M9 | `live.enterBackground()` commented out in `release()` | `TestR8Background_TheAndroidLifecycleReachesIt` |
| M10 | `app.startTerminalWatch(s)` added to `SessionDetailPanel.kt` | `TestR8Gate_OnlyTheFallbackScreenIssuesAWatch` |
| M11 | `monoWell(terminal = true)` added to a `ui/kit` file | `TestR8Gate_OnlyTheFallbackBodyPrintsTheTerminalWell` |
| M12 | `app.terminalViewWatch(` added to a `ui/kit` file | `TestR8Gate_OnlyTheFallbackScreenIssuesAWatch` |
| M13 | `streamStale = false` hardcoded again | `TestR8Gate_TheStalenessSignalIsCarriedRatherThanDiscarded` |
| M14 | `Peek` stops applying `clampSnapshotLines` | `TestR8Bounds_PeekResolvesAndAppliesTheBounds` |
| M15 | `terminalRows` peeks directly again | `TestR8Gate_TheGridReadIsGatedOnACapabilityRead` |
| M16 | `snapRowCountCap` returns `have` for an undeclared count | `TestR8RowCap_AnUndeclaredRowCountIsStillBounded` |
| M17 | `watch()` back inside `drawTerminalFallback` | `TestR8Gate_TheWatchIsOpenedOnceClosedAndRenewed` |

### 3.1 THREE FENCES THAT SURVIVED THEIR OWN MUTATION ON THE FIRST PASS

Recorded rather than quietly repaired, because "a fence that survives its own mutation" is this
wave's own named defect class and round 1 shipped five of them.

- **M1 escaped.** The first version of the keepalive test probed at t+16m after renewing only to
  t+14m — by which point the 30-second missing-keepalive deadline had ALSO lapsed, so the weaker
  clause refused the frame and masked the horizon entirely. Repaired by renewing continuously to
  the wall and adding a guard that **fails the test** if the fixture is ever re-tuned so the
  keepalive deadline lapses before the probe instant. The fix was right; the fence was measuring
  the wrong clock.
- **M8 escaped, and part of it cannot be fixed behaviourally.** `Service.Run`'s deferred
  `watchers.Close()` also ends every watch, so "no watch survives Run" is true with or without
  the explicit `UnwatchAll`. The real difference is WHEN: `UnwatchAll` fires at context cancel,
  `Close` only after `wg.Wait()` has joined the journal loop, the command bridge and the link
  watcher — and during that window the daemon is still rendering and the sink still sealing for a
  phone whose link is already dead. Reproducing that window in a unit rig means stalling one of
  those loops artificially, which is a test about the stall. So the fence is a **source fence
  scoped to `Service.Run`'s own body** (not satisfiable by `UnwatchAll`'s declaration in another
  file), and its limitation is stated in the test's own doc comment.
- **M9 escaped** because the fence grepped the raw Kotlin, so a commented-out call still matched.
  Repaired with a comment stripper — the same thing `kotlinCodeOnly` does on the gate side, which
  is why the gate fences caught their mutations and this one did not.

### 3.2 A WEAK MUTATION UPGRADED

M14's first form was caught by the **compiler** (`declared and not used: bounds`), which is a
weaker signal than an assertion. It was re-run with `_ = bounds` added so the package still
builds; the named assertion failure is in the transcript.

---

## 4. GATE RECORD

### Go lane (`PATH=$HOME/go/bin`)

| Gate | Command | Start (UTC) | End (UTC) | Exit |
|---|---|---|---|---|
| build | `go build ./...` | 17:05:55 | 17:05:58 | **0** |
| vet | `go vet ./...` | 17:05:58 | 17:06:00 | **0** |
| lint | `golangci-lint run` (v2.12.2) | 17:06:00 | 17:06:02 | **0** — `0 issues` |
| race | `go test -race -count=1` over `internal/protocol/...`, `internal/phonecore`, `internal/remotegw`, `internal/skeleton`, `internal/daemon`, `internal/vt`, `mobile`, `android/gate`, `cmd/swarm-remote`, `internal/verify` | 17:06:08 | 17:13:12 | **0** |

Per package: `protocol 31.7s`, `protocol/schema 2.9s`, `phonecore 39.2s`, `remotegw 36.9s`,
`skeleton 417.0s`, `daemon 71.6s`, `vt 13.7s`, `mobile 53.9s`, `android/gate 89.9s`,
`cmd/swarm-remote 38.2s`, `verify 41.4s` — all `ok`.

**B94 delta.** `TestB94_EveryExportedSymbolIsReachableFromProduction` passed before and after, so
the *test* delta is zero — which is itself the finding: B94 did not catch any of MAJOR 4,
MAJOR 6 or MODERATE 10, because the symbols involved are reachable from *some* exported path
even when nothing in the shipped composition calls them. The measured delta is in production
call sites, counted by `grep -v _test` over `internal/ mobile/ cmd/`:

| Symbol | Before | After |
|---|---|---|
| `TerminalWatcher.Reap` | 0 | 1 |
| `TerminalWatcher.WatchLive` | 0 | 6 |
| `TerminalWatcher.UnwatchAll` | 0 | 2 |
| `RemoteProfileV1.TerminalViewBounds` | 0 | 3 |
| `TerminalControlState.Background` | 0 | 1 |
| `LeaseState.Background` | 0 | 1 |
| `TerminalControlState.Apply` (new; makes `Begin` reachable) | — | 1 |
| `Server.severTerminalControl` (new) | — | 4 |
| `Server.sweepTerminalControl` (new) | — | 4 |

**GG-7 delta: ZERO.** This round added no wire field and no op. `TestProtocolMDBidi_
FieldSetMatchesStructs` passes; `docs/specifications/protocol.md` is unchanged by round 2, so no
Meaning cell was written and the literal phrase "JSON key" was not introduced anywhere (the 10
pre-existing occurrences are all header cells that predate this wave, as round 1 recorded).
`TerminalKeepaliveTTL` and `TerminalIdleSweep` are server-side durations, not wire fields.

### Android lane (script file only — `/tmp/r8_build_aar.sh`, `/tmp/r8_gradle.sh`)

`JAVA_HOME=/usr/local/opt/openjdk@21/libexec/openjdk.jdk/Contents/Home`,
`ANDROID_HOME=/usr/local/share/android-commandlinetools`,
`./gradlew --no-daemon --rerun-tasks --no-build-cache :app:testDebugUnitTest`.
Never from a bare command line: a bare invocation self-matches `pgrep -f gradle-wrapper.jar`.

| Artifact | Before this round | After |
|---|---|---|
| `android/app/libs/swarm.aar` | `2026-08-20T18:15:36` (round 1's) | `2026-08-20T18:39:16` — rebuilt for `App.EnterBackground` |
| `app/build/test-results/testDebugUnitTest/*.xml` | 173 files, round-1 mtimes | **174 files, every one at `2026-08-20T19:17:18` local**, i.e. after the AAR above |

`:app:testDebugUnitTest` — **BUILD SUCCESSFUL in 3m 55s**, `2026-08-20T17:13:21Z → 17:17:19Z`.
**1395 tests, 0 failures** (1393 in round 1; +2 from the two new `TerminalFallbackViewTest`
staleness cases). Zero result XMLs carry `failures="1"` or higher.

> A note the round-1 record needs. `TestPageSize_TheBuiltAARsNativeLibrariesAreLinkedFor16KBPages`
> and `TestPBBIND7_TheBuiltAARExportsThePinnedFacadeSurface` both FAILED against round 1's
> checked-out AAR at the start of this round, and both pass after the rebuild. The AAR is
> `.gitignore`d, so this is a stale local artifact rather than a code defect — but it means the
> round-1 gate record's Android lane was taken against an artifact that no longer matched the
> Go surface, and a reader should not assume otherwise.

---

## 5. WIRED VS PARKED — after round 2

### WIRED (production-reachable; the call-site counts in §4 are the evidence)

- Everything round 1 listed **except** the three rows corrected in §0.
- The signed control horizon: stamped once at begin, never moved by anything, both walls
  re-evaluated per frame, and an idle generation swept off the server's own ticker.
- Kill-switch and device-revocation severance of a control generation, synchronous at the daemon.
- The shipped remote profile: version, interaction-schema version, TerminalView version,
  capability-record version and all three bounds.
- The watch horizon, all four verbs: `Renew` (relay route), `WatchLive` (per-emission predicate
  on the gateway's snapshot path), `Reap` (reaper ticker in `Service.Run`), `UnwatchAll` (cancel
  path). Plus the phone half: opened once, renewed per redraw of the live screen, closed when the
  screen goes away or the app leaves the foreground.
- `TerminalViewBounds`, applied to the phone's copy in `App.Peek`.
- T8-b: `App.EnterBackground` from `PhoneSurface.release()`, severing both planes and dropping —
  never flushing — the held bytes.
- The phone's generation ingestion: `TerminalControlState.Apply` on the authenticated command
  reply, so `Begin` has a production caller and the instance comes from the session's own
  capability record rather than from the reply.
- The unexempted Kotlin gate scan, which now covers `ui/kit` and `theme`.

### PARKED, unchanged in kind from round 1 and re-stated because §0's corrections do not touch them

1. **`TerminalViewV1` is not on the wire.** Consequence: the phone cannot tell a re-seed from a
   session replacement, and the snapshot-age half of the staleness indicator is inert. Closing it
   needs a widened `TerminalSink` through `remotegw` and `phonecore`.
2. **The control plane is not composed into the fallback screen.** Every Go seam beneath it is
   now driven end to end over the assembled server, and the phone records the generation when the
   machine mints one — but `PhoneSurface` draws the fallback read-only, opens no generation, and
   the screen's `beginControl` / `releaseControl` / `type` / `keepAlive` bindings have no call
   site. R8b's S9/S10 own that, and the banner, the horizon countdown and the in-view release are
   built and unit-tested in `TerminalFallbackView` waiting for it.
3. **T3's version-skew row** stays deferred to `agents-tracker-hggx.2.1`.
4. `RenderTerminal` still passes an EMPTY session instance on the legacy body, which has no field
   for it. Deliberate, commented at the call site, and it stops being deliberate when item 1 lands.

---

## 6. HANDED TO ROUND 3

1. `TestRemotePeek_TerminatesOnKillSwitchFlip` still has the vacuous shape round 1 repaired in
   its own §3.1 (a negative frame assertion with no positive control). Pre-existing, untouched,
   still known.
2. Parked item 1 (`TerminalViewV1` on the wire) remains the one that changes what a user can
   conclude from the screen, and parked item 2 remains the one that changes what they can do.
3. The M8 fence is a source fence with a stated limitation (§3.1). If a future slice gives the
   gateway an injectable clock for its exit path, that fence should become behavioural.
4. `internal/protocol/r5_sessionlaunch_test.go` is not `gofmt`-clean (pre-existing, one
   alignment line, `golangci-lint` does not flag it).
