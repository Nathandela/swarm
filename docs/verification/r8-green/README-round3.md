# Wave R8 — GREEN round 3 (2026-08-20)

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


Bead `agents-tracker-hggx.9`. This document supersedes `README-round2.md` **wherever the two
disagree**, and says so at each point rather than quietly rewriting it. Rounds 1 and 2 stay on
disk unedited, because "what was claimed and then found false" is part of the evidence.

Captured on `darwin 25.5.0 / arm64`, Go `go1.26.5` from `$HOME/go/bin`,
`golangci-lint v2.12.2`, JDK 21, Gradle 9.5.1 wrapper.

---

## 0. THE CLAIMS IN ROUND 2 THAT WERE FALSE

Round 2 opened with a section of this shape because round 1 broke hard rule 8. It is here again
for the same reason, and the fact that it is needed a second time is itself part of the record.

| Round-2 claim | Truth at the time | Now |
|---|---|---|
| §1 "CANNOT … a user cannot type into a fallback session from the shipped UI; anything speaking the protocol can, under the now-correct walls" | **FALSE for anything speaking the protocol through the relay and the gateway — i.e. the product.** `Gateway.ForwardCommand` dials a fresh daemon connection per command and closes it on the reply; the generation lived on `clientConn.termGen`. Begin minted on conn A; the next `terminal_input` arrived on conn B with `cc.termGen == nil`. Measured on the assembled remote-tier server: `op="error" code="stale_generation"`, **bytes at the PTY = 0**. Only a direct `remote.sock` client holding ONE persistent connection could ever type. Every round-2 control test was such a client. | TRUE, and driven through the product's own `remotegw.Gateway` by `TestR8R3_TheGatewayForwardsAControlGenerationAcrossItsPerCommandConnections` |
| §2 / §5 "the capability record … validated at all three seams" | **FALSE. There were TWO.** `internal/skeleton/instance.go` (author) and `internal/phonecore/journal.go` (phone decode). `grep -rn 'Validate()' internal/ mobile/` returned no gateway call, and the gateway never touched the record at all — while the amendment T2-b this wave itself authored names the gateway decode explicitly as one of the three. | TRUE: `namespaceRecord` validates at the gateway's journal egress |
| §5 WIRED "Session instance: minted per incarnation" | **FALSE. It was per session ID.** `recordSessionInstance` had one production caller, `adoptOrMintSessionInstance`, which mints only when the session has none; `spawnShim` has one caller, `Launch`, which always mints a fresh session id. No production path minted on replacement, and `instance.go`'s own comment described two call sites that did not exist. Round 2's replacement arm reached the property by calling `d2.recordSessionInstance(...)` **from the test** — the helper exercised, the production call site unfenced. | TRUE: the instance is bound to the shim's pid, so a daemon restart adopts and a new shim re-mints, through the production resolver |
| §2 row for D-NIL / S2 | The S2 slice required "a launched opencode session's record reaches `App.Session` over the real gateway". What existed was `TestR8Publication_EverySessionCreationPathAuthorsARecord`, a **regex over four filenames** for the literal `authorSessionCapabilities(`. No test anywhere launched a real session and asserted a record on the wire. | TRUE: `TestR8R3_ALaunchedSessionsRecordReachesTheGatewaySinkAndRoutesToTheFallback` launches a real session on the assembled daemon, bridges it with the real gateway, and asserts the **destination the phone's own router computes** |

---

## 1. WHAT A USER CAN AND CANNOT DO — re-stated after round 3

**CAN, end to end, over the real assembled path:**

- Launch OpenCode or AGY and **monitor it read-only** from the phone. Unchanged from round 2,
  and now additionally pinned from the daemon's own launch through to the phone's router by
  `TestR8R3_ALaunchedSessionsRecordReachesTheGatewaySinkAndRoutesToTheFallback`.
- **Type into a fallback session over the protocol the product actually speaks** — a signed
  `terminal_control_begin` on one connection, unsigned `terminal_input` and
  `terminal_control_keepalive` on whatever connections the gateway opens next, a signed
  `terminal_control_end` on another. This is what round 2 claimed and could not do.
- Keep the chat composer on a healthy Claude or Codex session.
- Read a machine-sanitized screen in which **no rune that renders invisibly on a phone
  survives** — stated now as a property over `unicode.Cf`, `Zl`, `Zp` and
  `Other_Default_Ignorable_Code_Point` rather than as a list, so the escapees the review
  measured (U+206A-U+206F, U+1D173-U+1D17A, the four Hangul fillers) and the ones nobody has
  found yet are one assertion.

**CANNOT — each a ruling, not a gap:**

- Reach the fallback from a healthy Claude or Codex session: not in the app, not over the new
  verb, not over the legacy `terminal_subscribe`, not with an absent, inconsistent or
  instance-less record — and, from this round, **not with a valid record the gateway would
  have to forward past its own validation**, because it is dropped at the machine boundary.
- **Be handed `terminalControl = true` for a session the router sent to the status card.** A
  valid opencode record on a machine whose profile carries `terminal_view_version == 0` —
  every machine deployed before this wave — did exactly that, with no attacker and no
  malformed record involved.
- **Hold a control generation across a kill switch that flips off and back on, including one
  minted in the window the sever was sweeping.** Round 2 closed the sever; round 3 closed the
  race the sever's own sibling documents.
- **Carry a session instance across a shim replacement.** A new shim re-mints, so a generation
  bound to the old PTY is refused against its replacement with `stale_instance`.
- **See a reaped watch rendered as a live terminal.** An expiry or a transport loss now blanks
  the phone's copy, and the live foreground screen renews on its own tick so an idle screen is
  not reaped while the user is looking at it.

**CANNOT YET — a reader must not conclude otherwise:**

- `TerminalViewV1` is still **not on the wire**. The phone reads the legacy `TerminalSnapshot`,
  which carries no `view_epoch`, `revision`, `reset`, `session_instance` or `rendered_at`. The
  phone still cannot distinguish a re-seed from a session REPLACEMENT, and the snapshot-AGE
  half of the staleness indicator is still inert (`ageMs` is 0). Unchanged from round 2.
- The **control plane is still not composed into the fallback screen.** Every seam beneath it
  is real, driven and — as of this round — reachable over the product's own transport, but
  `PhoneSurface` draws the fallback read-only and opens no generation. Round 3 makes that
  statement **auditable** rather than merely written down: the four binding wrappers that
  would reach it now carry ledger rows, and the ledger check follows the receiver, so the row
  fails the day one acquires a caller.
- T3's version-skew row stays deferred (`agents-tracker-hggx.2.1`), so an unrecognised Claude
  or Codex build still opens as structured chat rather than as a labelled read-only terminal.
- The **Kotlin renewal tick is fenced by source shape, not by a Robolectric run.** The gate
  asserts it exists, is armed where a watch is taken, is torn down on the way out, renews
  rather than watches, and emits nothing on the control plane. It does not assert that a
  `Handler` actually fires at 20 s under a shadow looper. Stated rather than implied.

---

## 2. PER-OBLIGATION SUMMARY

| # | Finding | Production fix | file:line | Pinning test | Mutation |
|---|---|---|---|---|---|
| BLOCKER 1 | the control half is unreachable over the real composition | generations moved to a **server-wide registry keyed by generation id**; `terminal_control_end` releases by (session, signing device); the device moved onto the generation | `internal/protocol/server.go:251-266` (registry + `termSeverGen`), `internal/protocol/remote_terminal.go:142-181` (type + why), `:196-263` (registry ops), `:305-322` (begin publishes), `:337-358` (end), `:374-382` (per-frame lookup), `:487-521` (sever/expire) | `internal/protocol/r8r3_generationregistry_test.go` (6 tests), `internal/remotegw/r8r3_realcomposition_test.go:120` (**the real `Gateway.ForwardCommand`**) | **M1 CAUGHT**, and targeted: round 2's single-connection suite stays green under it |
| BLOCKER 2a | "validated at all three seams" — there were two | `namespaceRecord` validates at the gateway's journal egress and drops an inconsistent record whole | `internal/remotegw/gateway.go:502-522` | `internal/remotegw/r8r3_recorddecode_test.go` (5 tests) | **M2 CAUGHT** |
| BLOCKER 2b | the instance was per session ID, and the replacement arm was test-driven | the instance binds the **shim's pid**; the side-file records it; an unknown pid adopts | `internal/skeleton/instance.go:65-97` (record), `:99-123` (encode/decode), `:165-195` (adopt-or-mint), `internal/skeleton/capability.go:84-90` (cache) | `internal/skeleton/r8r3_incarnation_test.go` (4 tests); `TestR8Instance_SurvivesADaemonRestartButNotAReplacement` **strengthened** to reach replacement through the production resolver | **M3 CAUGHT** |
| BLOCKER 2c | no test drove a real launched session's record onto the wire | — (the wiring existed; the evidence did not) | — | `internal/skeleton/r8r3_recordonwire_test.go` (2 tests): assembled daemon → real `remotegw.Gateway.RunJournal` → sink → `phonecore.RouteSession` | **M4 CAUGHT** (roster stops stamping → no record reaches the sink) |
| MAJOR 3 | the drop class was a hand enumeration and leaked | drop = `Cf ∪ Zl ∪ Zp ∪ (Default_Ignorable ∖ Letter)`; **replace** = the default-ignorable Letters, which occupy a terminal cell and render as nothing | `internal/vt/render.go:435-455` (`zeroWidthFormatRune`), `:457-461` (`invisibleCellRune`), `:379-392` (the switch) | `internal/vt/r8r3_sanitizeproperty_test.go` (5 tests, incl. two whole-plane property sweeps and a per-rune emulator measurement) | **M5 CAUGHT** (enumeration restored), **M6 CAUGHT** (replace class emptied) |
| MAJOR 4 | a raw record field reached Kotlin, guarded by a line that survived its own deletion | `phonecore.TerminalControlAvailable` (route first, then the record's accessor); the facade uses it | `internal/phonecore/terminalroute.go:99-122`, `mobile/app.go:1016` | `internal/phonecore/r8r3_controlpredicate_test.go` (4 tests incl. a 9-row table and a cross-product invariant), `mobile/r8r3_controlfield_test.go` (3 tests) | **M7 CAUGHT** (raw field restored), **M8 CAUGHT** (phone decode `Validate()` → `true`) |
| MODERATE 5 | the watch horizon can reap a screen the user is looking at, and the screen calls it fresh | machine: `Reap`/`UnwatchAll` blank the phone's copy. phone: a composition-bound renewal tick | `internal/remotegw/gateway.go:136-156` (`BlankTerminal`), `terminal_watcher.go:39-55` (`terminalBlanker`), `:252-289` (`Reap` + `blank`), `:311-332` (`UnwatchAll`); `PhoneSurface.kt:2591-2598` (the clock), `:2650-2668` (`scheduleWatchRenewal`, `renewHeldWatch`) | `internal/remotegw/r8r3_reapblank_test.go` (5 tests), `android/gate/r8r3_fallback_ui_test.go:45` and `:135` | **M9 CAUGHT**, **M11 CAUGHT**, **M12 CAUGHT** |
| MODERATE 6 | the unbound-verb ledger is satisfied by a wrapper nothing calls | a **third ledger dimension** over `TerminalFallbackBinding`, matched by RECEIVER; four honest rows | `android/unbound-verbs.tsv:58-61`, `android/gate/r8r3_fallback_ui_test.go:160` (forward), `:207` (rot), `:246-274` (`r8r3BindingCall`), `boundverbledger_test.go:449` (rot check taught the new dimension) | `TestR8R3Gate_TheFallbackBindingsVerbsAreReachedOrLedgered`, `...TheLedgerCannotExcuseAReachedBindingMethod` | **M13 CAUGHT**, **M14 CAUGHT**, **M15 CAUGHT** (bare-name matcher restored → the `releaseControl` row reads as stale) |
| MINOR 7 | `severTerminalControl` lacked the race fence its own sibling documents | `termSeverGen` bumped before the sweep; the begin re-checks it under `termMu` when it publishes | `internal/protocol/remote_terminal.go:196-221`, `:487-503` | `internal/protocol/r8r3_severrace_test.go` | **M10 CAUGHT** — after the fence was rewritten; see §3.1 |
| MINOR 8 | comments true before the wave, now misleading in the opposite direction | three paragraphs repaired, each naming what it used to say | `internal/skeleton/capability.go:249-268`, `:356-370`, `internal/skeleton/chat.go:304-318` | — (prose; the repair is the deliverable) | — |
| MINOR 8 (drive-by) | `profile.go`'s stale ADR-016 comment | **already landed in an earlier round** — `profile.go:21-23` reads "its three fields ARE declared below". Confirmed, not re-fixed. | `internal/protocol/schema/profile.go:21` | `internal/protocol/schema/r8_terminalview_test.go:196` | — |

---

## 3. MUTATION VERDICTS — 15 obligations, 15 caught, 0 standing escapes

Full transcript: `docs/verification/r8-red/round3-mutations.txt`. Every mutation targets a line
in **production** code (M14 targets the checked-in ledger, M15 the gate's own matcher — both
are the artifact under test, not a helper).

| # | Mutation | Test that failed |
|---|---|---|
| M1 | the generation is bound to the minting connection again | `TestR8R3_AGenerationOutlivesTheConnectionThatMintedIt` +3, and `...TheGatewayForwardsAControlGenerationAcrossItsPerCommandConnections` |
| M2 | the gateway forwards a record it never validated | `TestR8R3_TheGatewayStripsAnInconsistentCapabilityRecord` +2 |
| M3 | the instance ignores the incarnation | `TestR8R3_AReplacementShimMintsANewInstanceThroughTheProductionPath` +2 |
| M4 | the roster stops stamping the capability record | `TestR8R3_ALaunchedSessionsRecordReachesTheGatewaySinkAndRoutesToTheFallback` +1 |
| M5 | the drop class reverts to the hand enumeration | `TestR8R3_NoInvisibleRuneSurvivesSanitization` +2 (13 leaked runes named) |
| M6 | the replace class is emptied | `TestR8R3_ADroppedRuneCostTheTerminalNoColumnAndAReplacedOneDid` (4 fillers named) |
| M7 | the facade re-reads the raw `terminal_control` field | `TestR8R3Facade_AZeroProfileHandsKotlinNoTerminalControl` +2 |
| M8 | the phone decode seam stops validating | `TestR8R3_ThePhoneDecodeSeamRejectsAnInconsistentRecord` (3 subtests) |
| M9 | a reaped watch stops blanking | `TestR8R3_AReapedWatchBlanksThePhonesCopy` |
| M10 | the sever counter stops being bumped | `TestR8R3_ABeginRacingASeverFailsClosed` |
| M11 | the renewal tick is never armed | `TestR8R3Gate_AnIdleFallbackScreenKeepsItsWatchAcrossAHorizon` |
| M12 | the watch tick also emits a control keepalive | `TestR8R3Gate_TheRenewalTickIsNotAKeepaliveEmitter` |
| M13 | a second Kotlin file reaches a ledgered wrapper | `TestR8R3Gate_TheLedgerCannotExcuseAReachedBindingMethod` |
| M14 | a ledger row is deleted | `TestR8R3Gate_TheFallbackBindingsVerbsAreReachedOrLedgered` |
| M15 | the binding dimension matches the bare name again | `TestR8R3Gate_TheLedgerCannotExcuseAReachedBindingMethod` (the `releaseControl` row) |

### 3.1 ONE FENCE SURVIVED ITS OWN MUTATION AND WAS REWRITTEN

Recorded rather than quietly repaired, because this is the wave's own named defect class and
round 2 shipped three of them.

**M10 escaped on the first pass.** `TestR8R3_ABeginRacingASeverFailsClosed` sampled
`termSeverGen.Load()-1` and asserted the publish was refused. With the counter's bump removed
entirely, `Load()` is 0 and `0-1` is a large `uint64` that still differs from 0 — so the
publish failed for the wrong reason and the test passed over a plane with no race fence at
all. Rewritten to drive the race **in the order it happens** — sample, sever, publish — plus a
fourth arm asserting the counter does not wedge every subsequent begin. M10 then caught it.

### 3.2 M1 IS TARGETED, WHICH IS THE POINT

Under M1 the round-2 control suite (`TestR8Control_*`, one connection per test) stays **fully
green** while every round-3 cross-connection test and the real-gateway test fail. That is the
shape of the defect: round 2's tests could not see it, and the mutation reproduces the
blindness rather than breaking the plane.

---

## 4. GATE RECORD

### Go lane (`PATH=$HOME/go/bin`)

| Gate | Command | Start (UTC) | End (UTC) | Exit |
|---|---|---|---|---|
| build | `go build ./...` | 18:33:10 | 18:33:13 | **0** |
| vet | `go vet ./...` | 18:33:13 | 18:33:15 | **0** |
| lint | `golangci-lint run` (v2.12.2) | 18:33:15 | 18:33:18 | **0** — `0 issues` |
| race | `go test -race -count=1` over `internal/protocol/...`, `internal/phonecore`, `internal/remotegw`, `internal/skeleton`, `internal/daemon`, `internal/vt`, `mobile`, `android/gate`, `cmd/swarm-remote`, `internal/verify` | 18:33:25 | 18:40:29 | **0** |

Per package: `protocol 29.4s`, `protocol/schema 2.1s`, `phonecore 39.4s`, `remotegw 34.7s`,
`skeleton 415.9s`, `daemon 65.0s`, `vt 17.3s`, `mobile 43.3s`, `android/gate 90.3s`,
`cmd/swarm-remote 38.7s`, `verify 29.2s` — all `ok`.

> **One flake seen and not reproduced.** An earlier non-`-race` run of `internal/daemon` failed
> `TestStopRunningDaemon_PIDReuseSafety` with `daemon: another instance is already running`. It
> passed on two isolated re-runs and in the `-race` sweep above. It touches nothing this round
> changed; recorded because a reader who sees it once should know it has been seen.

**B94 delta.** `TestB94_EveryExportedSymbolIsReachableFromProduction` passed before and after;
the *test* delta is zero. As in round 2 that is itself the finding — B94 asks about
reachability from some exported path, not about the shipped composition, so it did not catch
BLOCKER 1, MAJOR 4 or MODERATE 5. The measured delta is in production call sites
(`grep -v _test` over `internal/ mobile/ cmd/`):

| Symbol | Before | After |
|---|---|---|
| `phonecore.TerminalControlAvailable` (new) | — | 2 |
| `Gateway.BlankTerminal` (new) | — | 3 |
| `terminalBlanker` (new interface) | — | 2 |
| `Server.publishTerminalGenerationIfCurrent` (new) | — | 2 |
| `Server.terminalGenerationByID` (new) | — | 2 |
| `Server.renewTerminalKeepalive` (new) | — | 2 |
| `Server.dropTerminalGenerationsFor` (new) | — | 2 |
| `Server.severTerminalGenerations` (new) | — | 1 |
| `Server.expireTerminalGenerations` (new) | — | 2 |
| `vt.invisibleCellRune` (new) | — | 4 |
| `skeleton.encodeSessionInstance` / `decodeSessionInstance` (new) | — | 2 / 3 |
| `clientConn.severTerminalGeneration` / `terminalGenerationDevice` / `expireTerminalGeneration` | 3 / 2 / 1 | **removed** (the per-connection plane is gone) |

**GG-7 delta: ZERO.** This round added no wire field and no op. The session-instance side-file
gained a second token, which is on-disk daemon state, not a wire shape.
`TestProtocolMDBidi_FieldSetMatchesStructs` passes; `docs/specifications/protocol.md` is
**unchanged by round 3** (its +32 lines in the working tree are rounds 1–2's), so no Meaning
cell was written and the literal phrase "JSON key" was not introduced — the 10 occurrences are
the pre-existing header cells rounds 1 and 2 already recorded.

### Android lane (script file only — `/tmp/r8_build_aar.sh`, `/tmp/r8_gradle.sh`)

`JAVA_HOME=/usr/local/opt/openjdk@21/libexec/openjdk.jdk/Contents/Home`,
`ANDROID_HOME=/usr/local/share/android-commandlinetools`,
`./gradlew --no-daemon --rerun-tasks --no-build-cache :app:testDebugUnitTest`. Never from a
bare command line: a bare invocation self-matches `pgrep -f gradle-wrapper.jar`.

| Artifact | Before this round | After |
|---|---|---|
| `android/app/libs/swarm.aar` | `2026-08-20T18:39:16Z` (round 2's) | `2026-08-20T18:42:25Z` — rebuilt for `App.session`'s new predicate |
| `app/build/test-results/testDebugUnitTest/*.xml` | 174 files, round-2 mtimes | **174 files, every one at `2026-08-20T21:04:56` local**, i.e. after the AAR above |

`:app:testDebugUnitTest` ran TWICE, and the second run is the one this record reports.

1. `2026-08-20T18:42:34Z → 18:46:38Z`, **BUILD SUCCESSFUL in 4m 4s**, 1395/0.
2. `2026-08-20T19:01:18Z → 19:04:57Z`, **BUILD SUCCESSFUL in 3m 37s**, **1395 tests, 0
   failures, 0 errors, 0 skipped**, zero XMLs carrying `failures="1"` or higher.

The second run exists because mutations M11–M14 edit `PhoneSurface.kt`, `FacadeBridge.kt` and
`android/unbound-verbs.tsv`, and they ran AFTER the first gradle invocation. Each reverts by a
byte-for-byte copy, but "the gate ran over the source that ships" is a claim that must be true
of the FINAL tree, not of a tree that was later restored to match it. So the lane was re-run
over the post-mutation source and is the record above.

> The gradle script's own `find app/build/test-results -name '*.xml' | wc -l` prints **334**,
> not 174: it walks `testReleaseUnitTest` too, whose XMLs are stale artifacts of an older
> release run this round did not execute. The number that matters — the debug unit-test XMLs
> this run rewrote — is 174, and every one of them is newer than the AAR.
>
> The Kotlin test count is unchanged at 1395 because round 3 added **no new Kotlin test**. The
> Kotlin change is composition wiring, and it is fenced from the Go side by
> `android/gate/r8r3_fallback_ui_test.go`, whose four mutations (M11–M14) are the evidence.
> Stated plainly so a reader does not read an unchanged count as an untested change.

---

## 5. WIRED VS PARKED — after round 3

### WIRED (production-reachable; §4's call-site counts are the evidence)

- Everything round 2 listed **except** the four rows corrected in §0.
- The terminal control plane **over the transport the product uses**: a generation minted on
  one connection is usable, renewable and releasable on any later one, and is severed, swept,
  instance-checked and capability-checked from a single server-wide registry.
- The sever race fence on the terminal plane, matching the lease plane's.
- The capability record's **gateway** validation seam — T2-b's second of three.
- The session instance bound to the shim incarnation, with the pre-R8 side-file adopted.
- `phonecore.TerminalControlAvailable`, resolved through the router, read by the facade.
- Reap/transport-loss blanking of the phone's copy, and the foreground watch-renewal tick.
- The unbound-verb ledger's third dimension, matched by receiver.
- The widened sanitizer, as a property over Unicode categories rather than a rune list.

### PARKED, unchanged in kind from round 2

1. **`TerminalViewV1` is not on the wire.** Consequence: the phone cannot tell a re-seed from a
   session replacement, and the snapshot-age half of the staleness indicator is inert.
2. **The control plane is not composed into the fallback screen.** Every seam beneath it is now
   driven end to end AND reachable over the product's transport; what is missing is the screen.
   Now ledgered rather than only written down (§2, MODERATE 6).
3. **T3's version-skew row** stays deferred to `agents-tracker-hggx.2.1`.
4. `RenderTerminal` still passes an EMPTY session instance on the legacy body, which has no
   field for it. Deliberate, commented at the call site; it stops being deliberate when item 1
   lands.

---

## 6. HANDED TO ROUND 4

1. **The generation is now a bearer token over an authenticated channel.** That is what
   ADR-017 T6 always described ("what authorises the frame is the E2EE seal's own
   authenticated sender and sequence PLUS the confirmed generation"), and the connection
   binding it replaced was an implementation accident, not a stated control. It is nonetheless
   a change in what an attacker who can read a `terminal_control_begin` **reply** can do, and
   the reply crosses the gateway. The gateway is already a trusted component on this path (it
   forwards the input frames themselves), so this adds no new trusted party — but the sentence
   belongs in the ADR, not only here, and round 4 should carry it into T6 as an explicit
   ruling with the threat model spelled out.
2. `TestRemotePeek_TerminatesOnKillSwitchFlip` still has the vacuous shape round 1 repaired in
   its own §3.1. Pre-existing, untouched, still known.
3. The M8-class limitation from round 2 (`Service.Run`'s exit-path fence is a source fence with
   a stated limit) is unchanged.
4. `internal/protocol/r5_sessionlaunch_test.go` is not `gofmt`-clean (pre-existing, one
   alignment line, `golangci-lint` does not flag it).
5. The Kotlin renewal tick has no Robolectric test (§1, last CANNOT-YET). If a future slice
   gives `PhoneSurface` an injectable scheduler, that fence should become behavioural.
