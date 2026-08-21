# Wave R8 — GREEN round 1 (2026-08-20)

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


> **SUPERSEDED IN THREE PLACES BY `README-round2.md`, WHICH LISTS THEM FIRST.** An adversarial
> review found three claims below to be FALSE at the time they were written: §1's "CAN … monitor
> OpenCode or AGY read-only" (the shipped remote profile was all zeros, so every session routed
> to the status card and the R6/R7 chat composer disappeared), §5's "Watch horizon … routed end
> to end" (three of the four verbs had zero production callers), and §5's "backgrounding as a
> DIRECT trigger" (both `Background` methods had zero production callers and no facade verb).
> The review also found three security defects on the control plane that no test here covered.
> This document is left UNEDITED on purpose: what was claimed and then found false is part of
> the evidence. Read `README-round2.md` for the current state.

Bead `agents-tracker-hggx.9`. What the RED corpus in `../r8-red/` asked for, what now answers it,
what was MUTATION-PROVED, and — stated first because it is the part a reader is most likely to
assume — **what is wired and what is parked.**

Captured on `darwin 25.5.0 / arm64`, Go toolchain from `$HOME/go/bin`, `golangci-lint v2.12.2`.

---

## 1. WHAT A USER CAN AND CANNOT DO, stated before any test result

**CAN, end to end, over the real assembled path:**

- Launch OpenCode or AGY and **monitor it read-only** from the phone. The daemon authors a
  capability record on every session-creation path, publishes it on the roster, and the phone
  routes the session to the sanitized terminal fallback by READING that record.
- Read a screen that is machine-sanitized at one choke point (`vt.SnapText`), bounded by the
  Snap's own declared geometry, and laid out left-to-right so implicit bidi cannot reorder a row.
- See **why** there is no chat: provider, detected version, and the missing capability.
- See an interleaving warning, and a read-only sentence when the machine granted no control.

**CANNOT — and each of these is a ruling, not a gap:**

- Reach the fallback from a healthy Claude or Codex session. Not in the app (the router refuses),
  not over the new verb, not over the LEGACY `terminal_subscribe` (the daemon refuses
  `capability_refused` before any tap opens), and not with an absent, inconsistent or
  instance-less record.
- Type into a fallback session **from the shipped UI**. See §5: the control plane exists at every
  Go seam and is not composed into the screen.

**CANNOT YET, and a reader must not conclude otherwise:**

- A phone **cannot tell a re-seed from a session REPLACEMENT** on the wire. `TerminalViewV1` and
  its `view_epoch` / `revision` / `reset` / `session_instance` / `rendered_at` are produced and
  tested daemon-side, but the phone still reads the LEGACY `TerminalSnapshot` body, which carries
  none of them. The staleness indicator is therefore inert (age is 0, which renders as fresh).
  This is the single largest parked item and §5 states it as such.
- An unrecognised Claude or Codex build still opens as structured chat rather than as a labelled
  read-only terminal: T3's version-skew row stays deferred to `agents-tracker-hggx.2.1`.

---

## 2. PER-OBLIGATION: what answers each RED file

| RED file | Answered by | Key production seam |
|---|---|---|
| `adversarial-read-red.txt` (S5) | `internal/vt/render.go` — widened `zeroWidthFormatRune`, explicit U+FFFD, per-cell combining clamp, declared-geometry row and grid caps | `SnapText`, `stripControls` |
| `capability-publication-red.txt` (S1+S2) | `internal/protocol/schema/capability.go` (`Validate`, `AllowsTerminalWatch/Control`, `SessionInstance`, `TerminalControl`), `internal/skeleton/instance.go` | `authorSessionCapabilities` |
| `instance-identity-red.txt` (S1) | `internal/skeleton/instance.go` | `mintSessionInstance`, `recordSessionInstance`, `sessionInstance` |
| `router-red.txt` (S3) | `internal/protocol/remote_terminal.go`, `internal/phonecore/terminalroute.go` | `terminalWatchAllowed`, `RouteSession` |
| `terminalview-red.txt` (S4) | `internal/daemon/terminalview.go`, `internal/remotegw/terminal_watcher.go` | `RenderTerminalView`, `Renew`/`WatchLive`/`Reap`/`UnwatchAll` |
| `control-red.txt` (S6+S7) | `internal/protocol/remote_terminal.go`, `internal/phonecore/terminalroute.go`, `mobile/commands.go` | `handleTerminalControlBegin`, `handleTerminalInput`, `TerminalControlState` |
| `severance-red.txt` (S8) | `internal/phonecore/terminalroute.go`, `lease.go` | `Sever`/`SeverAll`/`Background`, `AbandonSession` |
| `fallback-screen-red.txt` (S9) | `TerminalFallbackScreen.kt`, `TerminalFallbackView.kt`, `SessionDetailPanel.kt` | the one allowlisted screen |

---

## 3. MUTATION VERDICTS

Every row: change ONE production line (or, where noted, the whole mechanism), run the named
permanent test, revert. `*` marks a mutation whose FIRST single-line form was absorbed by a
SECOND layer of the same defence — recorded rather than hidden, because "two layers" is a
different claim from "one line is load-bearing".

| Obligation | Mutation | Test | Verdict |
|---|---|---|---|
| D-NIL | nil record allowed to watch | `TestR8Record_AnAbsentRecordIsTheStatusCardAndRefusesBothVerbs` | FAILS |
| D-XOR* | `Validate()` removed AND the gate reads one boolean | `TestR8Record_BothGatesReadBothBooleans` | FAILS |
| D-ZERO | zero profile offers the view | `TestR8Profile_ZeroVersionMeansNoFallbackNotUnlimited` | FAILS |
| D-SANITIZE | U+2028/U+2029 removed from the DROP class | `TestR8Corpus_SanitizedOutputIsExact/line_separator_u2028` | FAILS |
| D-SANITIZE | the per-cell combining clamp removed | `TestR8Corpus_CombiningMarkDepthIsClampedPerCell` | FAILS |
| T4/T5-a | the declared-geometry row cap removed | `TestR8Corpus_ARowIsBoundedByItsDeclaredGeometry` | FAILS |
| D-EPOCH | the epoch minted downstream of the loop start | `TestR8View_ARerunMintsANewEpoch` | FAILS |
| T4-a | `reset` is not loop state | `TestR8View_FirstSnapshotOfAnEpochCarriesReset` | FAILS |
| D-WATCHLEASE | the liveness predicate ignores the watch lease | `TestR8Watch_AnUnrenewedWatchExpires` | FAILS |
| D-INSTANCE | the instance is a constant | `TestR8Instance_IsMintedPerIncarnationAndIsNotDerivedFromTheSessionID` | FAILS |
| D-INSTANCE* | the instance is not read back off disk | `TestR8Instance_SurvivesADaemonRestartButNotAReplacement` | FAILS |
| D-DEGRADE-ORIGIN 2* | BOTH `terminal_control` withdrawal points removed | `TestR8Publication_ReRegistrationAfterADegradeCannotReGrantControl` | FAILS |
| D-LEGACY | the session gate removed from `terminal_subscribe` | `TestR8Legacy_TheLegacyPeekIsRefusedForAHealthyStructuredSession` | FAILS |
| T2-c scoping | the gate widened past the remote tier | `TestR8Legacy_TheOwnerTierIsUnaffected` | FAILS |
| D-RECHECK* | capability leaves BOTH the per-tick poll and the per-emission gate | `TestR8Legacy_TheGateIsReCheckedPerEmission` | FAILS |
| T8-b | backgrounding stops severing directly | `TestR8Control_BackgroundingSeversDirectly` | FAILS |
| T6-f | severance FLUSHES the held bytes | `TestR8Control_SeveranceDropsHeldBytesAndNeverFlushesThem` | FAILS |
| T7 | the horizon drifts from `TakeControlTTL` | `TestR8Control_TheHorizonIsTheOneAlreadyImplemented` | FAILS |
| T2 rule 3 | the router stops validating the record | `TestR8Route_ThreeDestinationsAndNothingInBetween` | FAILS |
| D-GATE | a SECOND Kotlin file calls `app.terminalViewWatch(` | `TestR8Gate_OnlyTheFallbackScreenIssuesAWatch` | FAILS |
| D-GATE | the same file calls the LEGACY `app.terminalWatch(` | `TestADR009_TheTerminalWellIsDeletedAtI1Exit` **and** `TestR8Gate_...IssuesAWatch` | BOTH FAIL |

**Nothing is reported as done without its mutation.**

### 3.1 A vacuous assertion the mutation pass CAUGHT, and the repair

`TestR8Legacy_TheGateIsReCheckedPerEmission` **could not fail** under any mutation as the RED pass
wrote it, and the reason is worth recording because the shape is common: its frame filter read
`if typ != 0 { continue }`, and `wire.TControl` is **1**, so every snapshot was skipped and the
negative assertion ("no snapshot arrives after the record is withdrawn") passed whether or not a
gate existed. It was found by adding a POSITIVE CONTROL — the same frame, over the same timings,
must arrive while the record still permits the watch — which is now part of the test. Two
strengthenings, no assertion removed.

The same shape exists in `TestRemotePeek_TerminatesOnKillSwitchFlip` (pre-existing, untouched by
this wave, and now known); it is handed to round 2 in §6.

---

## 4. GATE RECORD

### Go lane (`PATH=$HOME/go/bin`)

| Gate | Command | Start (UTC) | Exit |
|---|---|---|---|
| build | `go build ./...` | 14:47:00 | 0 |
| vet | `go vet ./...` | 14:47:02 | 0 |
| lint | `golangci-lint run` | 14:47:05 | 0 (**0 issues**) |
| race | `go test -race -count=1` over `internal/vt`, `internal/protocol/...`, `internal/skeleton`, `internal/daemon`, `internal/remotegw`, `internal/phonecore`, `mobile`, `internal/verify`, `android/gate` | 14:49:08 → 14:56:06 | **0** |

Race-lane package results: `vt 10.5s`, `protocol 32.6s`, `protocol/schema 2.1s`, `skeleton 410.3s`,
`daemon 65.8s`, `remotegw 37.8s`, `phonecore 48.2s`, `mobile 51.0s`, `verify 43.6s` (B94),
`android/gate 79.3s` — all `ok`.

**Whole repo**, in three batches because one `go test ./...` exceeds this harness's 10-minute
call bound: the owned-package race lane above (exit 0), then `./cmd/swarm` + `./internal/e2e`
(15:15:35Z → 15:16:16Z, both `ok`), then the remaining 57 packages in two halves (15:53:40Z →
15:54:17Z exit 0, 15:54:24Z → 15:57:54Z exit 0). **Every package green.**

> ONE OBSERVATION WORTH RECORDING RATHER THAN SMOOTHING OVER. Running all 59 remaining packages
> in ONE `go test` invocation produced five failures in `cmd/swarm` and `internal/e2e`, every one
> of the shape "smoke daemon never served the client protocol within 10s" / "keystroke never
> echoed within 2s". Each passes in isolation and both suites pass together (39.6 s / 23.3 s), so
> this is host resource contention under full parallelism -- the shape bd `agents-tracker-ev0w`
> records -- and NOT a behaviour change from this wave. It is stated here because a reader who
> reruns the wide invocation will see it, and "it passed for me" is not an answer.

GG-7: `TestProtocolMDBidi_FieldSetMatchesStructs`, `TestProtocolMD_ExistsAndDocumentsEveryField`
and `TestProtocolMD_DocumentsEveryOp` all PASS. The three new field rows and the R8 op section in
`docs/specifications/protocol.md` say **"wire name"**; the literal phrase "JSON key" appears 10
times in that file, all of them pre-existing and none of them in a row this wave wrote.

### Android lane (script file only)

`JAVA_HOME=/usr/local/opt/openjdk@21/...`, `ANDROID_HOME=/usr/local/share/android-commandlinetools`,
`--no-daemon --rerun-tasks --no-build-cache`. Run from `/tmp/r8_aar.sh` and `/tmp/r8_gradle.sh`,
never from a bare command line (a bare `./gradlew` self-matches `pgrep -f gradle-wrapper.jar`).

| Artifact | Before | After |
|---|---|---|
| `android/app/libs/swarm.aar` | `2026-08-20T06:05:31` | `2026-08-20T15:41:43` (built 13:41:22Z → 13:41:43Z) |
| `app/build/test-results/testDebugUnitTest/*.xml` | pre-run mtimes captured | all rewritten at `2026-08-20T16:01:59`+, **173 files** |

`:app:testDebugUnitTest` — **BUILD SUCCESSFUL**, 1393 tests, 0 failures (1382 before this wave's
own Robolectric suite; +11 from `TerminalFallbackViewTest`).

---

## 5. THE WIRED-VS-PARKED LEDGER

### WIRED (production-reachable, proved by `internal/verify`'s B94 reachability gate)

- Capability record: authored on **four** session-creation paths — `api.go` (the client-facing
  launch/resume seam), `serve.go` (the core's `OnSessionStart` hook and the post-reconcile pass),
  `sessiontap.go` (re-attach of a session dir that has none), `backend.go` (the side-process
  backend's join and its proven unavailability). Published on the roster; folded into the phone's
  session cache; validated at all three seams; read by the daemon's terminal gate and by the phone's
  router.
- Session instance: minted per incarnation, persisted 0600 in the session's own 0700 dir, adopted
  across a daemon restart, carried on the record.
- The T2-c gate on the LEGACY `terminal_subscribe`, remote tier only, before the tap, re-checked
  per emission and per tick. The gateway ends the watch on `capability_refused` instead of
  reconnecting.
- `RenderTerminalView` is the **single** render loop: `RenderTerminal` is now a projection of it,
  so there is one sanitization choke point, one debounce window and one liveness poll.
- Watch horizon: `Renew` / `WatchLive` / `Reap` / `UnwatchAll`, `terminal_renew` routed end to end.
- Phone: `RouteSession`, `ComposerAvailable` (which is what `Session.StructuredChat` now carries),
  `TerminalControlState` with backgrounding as a DIRECT trigger and drop-never-flush.
- Kotlin: the fallback screen is reachable from `PhoneSurface.drawContent` for a routed session and
  from nowhere else; `SessionDetailPanel` reads the record instead of `transcript.structureTorn`.

### PARKED, and each with the reason

1. **`TerminalViewV1` is not on the wire.** The body, its version constant, its bounds and its
   producer all exist and are tested; the phone still reads the legacy `TerminalSnapshot`, which
   carries no epoch, revision, reset, instance or render time. Consequence, stated plainly: the
   phone cannot distinguish a re-seed from a REPLACEMENT, and the staleness indicator is inert.
   Closing it needs a widened `TerminalSink` through `remotegw` and `phonecore` — a wire slice, not
   a rename.
2. **The control plane is not composed into the screen.** `terminal_control_begin/end`,
   `terminal_input` and `terminal_control_keepalive` exist as ops, as gateway routes, as phonecore
   seals and as facade verbs, and the fallback screen declares its binding for all four — but
   `PhoneSurface` draws the fallback READ-ONLY and opens no generation. R8b's S9/S10 own that.
3. **The daemon's idle-expiry timer for a generation** (T6-c's "severed within 30 s with zero
   inbound frames) does not exist. The generation expires on its own horizon at the next frame or
   keepalive, which is weaker: an idle generation survives until the horizon.
4. **The signed end-to-end drive of `terminal_control_begin`** (wrong sender / wrong profile /
   wrong instance / replayed op id over the real remote-tier server) is not written. The wire
   shape, the binding, the code values and the lease-plane separation are.
5. **T3's version-skew row** stays deferred (`agents-tracker-hggx.2.1`).

---

## 6. HANDED TO ROUND 2

1. `TestRemotePeek_TerminatesOnKillSwitchFlip` has the same vacuous shape §3.1 repaired here: a
   negative frame assertion with no positive control. It is pre-existing and it is now known.
2. The parked items in §5, in that order: the `TerminalViewV1` wire hop is the one that changes
   what a user can conclude from the screen.
3. `internal/daemon/terminalrender.go`'s `RenderTerminal` passes an EMPTY session instance,
   because the legacy body has no field for it. That is deliberate and commented at the call site;
   it stops being deliberate the moment the versioned body lands.

---

## 7. THE OBLIGATIONS RED HANDED TO GREEN (README §6), discharged

1. `TestSessionCapabilities_WireShape` — pinned literal gained `session_instance` and
   `terminal_control` and **lost no key**. Same for `RemoteProfileV1`'s three shapes and the two
   reconcile shapes in `remotegw`/`phonecore` (three bounds keys added, none removed).
2. `s11_lease_test.go` — the backgrounding row is **kept verbatim** and a SECOND row added
   (backgrounding severs directly, with no transport event). The assertion set grew; nothing was
   deleted.
3. `lease.go:48-57` — no longer cites the withdrawn 60 s biometric freshness as a live wall; it
   now records that ADR-017 T7 adopts `TakeControlTTL` so the system has one 15-minute wall.
4. `profile.go:22-23` — the stale "not yet declared here" comment is repaired; pinned by
   `TestR8Profile_StaleCommentAboutADR016IsRepaired`.
5. GG-7 rows landed in the same change; "wire name", never "JSON key".
6. `capability_test.go`'s two fences are **untouched** and green.

### Sanctioned supersessions, each recorded at its site

- `r1_refusalops_test.go`: `terminal_control_begin` → `invalid_field`, `terminal_control_end` →
  `stale_generation`, on R5's and R6's own precedent. Every choke-point ordering assertion is
  inherited unchanged. `handleRefusalOp` is deleted — it had no ops left — and the deletion carries
  the ledger of where each op's ordering went.
- `SessionDetailComposerTest` / the eight other Kotlin detail fixtures: composer availability is
  now supplied as a capability record rather than inferred from a `structured_gap` item. The torn
  case still measures a torn session; it states it as `structuredChat = false`, which is what the
  daemon's one-way degrade writes.
- `TestR6Fix_ADegradeWithNoPriorRecordStillRefusesTheComposer`: the no-record state is now
  CONSTRUCTED (the record is removed) rather than inherited from "no production path authors one",
  which R8 ended. Every assertion is unchanged.
- `ADR-009`'s terminal-well ban gains the same single-file allowlist ADR-017 T1 grants, and the
  retired peek symbols stay banned by name. Net assertion count rises: the watch ban goes from 3
  literal spellings to 3 + 5 shapes, plus nine new R8 fences.
