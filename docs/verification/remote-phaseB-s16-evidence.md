# S16 evidence — the phone app

**Requirements**: PB-APP-1..10, PB-PAIR-2/-3/-4/-5/-6, PB-SAS-3, PB-TOK-1, plus the two inherited
residuals (PB-INPUT-1's ledger, PB-APP-8's clock verdict) and the S14 Android-wiring blocker.
**Decisions**: ADR-007 B8 (single inbound key crossing), B16/B18 (custody), **B21** (the QR-scanner
choice, written this slice). Requirement amendments landed mid-slice: **PB-PAIR-5** (`7d9ffae`),
**PB-APP-10** (`e1ab559`).

## What this proves

That the phone app exists as a product rather than as a facade with no caller: the Android side now
constructs the phone, keeps its sealing key across a process death, and renders every state the Go
core can report. Sixteen error classes reach sixteen declared Kotlin screens; Stop interrupts a
running agent; a revoked handset stops redialling and says why; the push toggles reach the machine
and suppress delivery at the sender.

## What it does NOT prove

**PB-E2E-5 remains deferred and nothing here changes that.** No test covers a real biometric, a real
camera, real FCM delivery, real Doze behaviour or hardware Keystore attestation. The Kotlin tests
model POLICY — which state is rendered for which input — and `android/gate/s16_ui_test.go`'s
`TestS16_NoUnitTestClaimsAPhysicalHandsetProperty` fences that they keep doing only that.

> **AMENDED 2026-07-31 (ADR-007 B133).** "A real biometric" has left PB-E2E-5's scope, because the
> feature left the product — removal by feature deletion, not reclassification by fiat. The other
> four items stay deferred and stay in `docs/operations/physical-handset-gate.md`. The fence named
> above is unaffected and still fences the same property.
>
> `android/gate/s16_ui_test.go` also now carries
> `TestB133_TheAppImportsNothingFromAndroidxBiometric`, so the deleted gate cannot return by
> accident while the dependency is still declared in `build.gradle.kts`.

It also proves nothing about the scanner: **PB-PAIR-3 is a recorded decision, not an implementation.**
ADR-007 B21 names the library; no scanner dependency is declared and no camera code exists. The
pairing screen models the permission POLICY only.

## Failing-first evidence (GG-5)

Captured by checking out `HEAD` into a scratch worktree, copying the untracked RED files in, and
running them against **pre-S16 production code**. This is a real run, not a reconstruction from
memory.

**Source-level (`./mobile`)** — 5 failures, each naming its own absence:

    TestPBAPP9_EveryErrorClassOnTheBoundSurfaceHasARenderedState
      the pinned surface declares no ErrClass* constant, so there is no error taxonomy at all
    TestPBAPP9_TheThreeRemediesAreNeverCollapsed
      requires a checked-in error taxonomy at mobile/error_taxonomy.tsv: no such file
    TestPBAPP9_NoFacadeErrorIsConstructedWithoutNamingItsClass
      36 error(s) are constructed in the facade without naming an ErrClass* constant
    TestPBAPP9_TheClassifierIsReachableFromKotlin
      the facade exports no error classifier
    TestS16_EveryNewScreenElementIsTracedToAFacadeMethod
      7 elements with no row in screen_coverage.tsv

**Runtime (`./mobile/conformance`)** — 17 failures. Representative reasons, each a defect rather
than a missing symbol:

    s16_appverbs_test.go:62    PB-APP-3: Stop sent no 0x03 to the machine.
    s16_errorstates_test.go:246 PB-APP-10: a revoked phone reports "reconnecting".
    s16_staleness_test.go:224  PB-APP-8: *swarmmobile.App has no method ClockVerdict.
    s16_undelivered_test.go:58 PB-INPUT-1: the undelivered ledger holds 4000 entries after 4000
                               refused keystrokes and is therefore unbounded.
    s16_undelivered_test.go:70 PB-INPUT-1: *swarmmobile.UndeliveredList has no method Dropped.

**`./android/gate`** — 8 failures, including all four wiring tests (`TheSealedStoreSurvivesA
ProcessDeath`, `AProductionKekProviderExists`, `TheAppConstructsThePhone`, `TheKeystoreProviderLives
BesideItsPolicy`) and both PB-TOK-1 value tests.

**Kotlin** — `:app:compileDebugUnitTestKotlin FAILED`, unresolved references across all six
`dev.swarm.phone.ui` test files. A whole package did not exist.

**Two tests did NOT fail at baseline, and that corroborates the RED rather than weakening it.**
`TestPBSAS3_TheSASIsComparedAndNeverTyped` and `TestPBAPP4_ThePeekRendersTheDaemonsBytesAndNothing
Else` are both labelled by their author "PASSES TODAY, AND THAT IS NOT COVERAGE"; the baseline run
confirms the label. They are regression fences, and no evidence line counts them as earned.

**One honest gap in this record.** The conformance package could not be compiled at baseline as it
stands, because the committed `s10_bootstrap_test.go` calls `s16PassOriginGate`, whose body calls
`ConfirmOrigin`, which pre-S16 does not have. The baseline above was produced by stripping that one
call in the scratch tree. The RED author's own files are unaffected: they resolve every S16 verb
**by reflection** precisely so a missing verb fails one test rather than the directory.

## What shipped

### The error taxonomy (PB-APP-9, PB-APP-10)

Sixteen exported `ErrClass*` constants, `mobile/error_taxonomy.tsv` as the join, and
`App.ErrorClass(string) (string, error)`. The class rides the error MESSAGE as a prefix, because
gomobile leaves nothing else of a Go error at the JNI boundary. Exhaustiveness is claimed in three
directions and no direction trusts the table: class set equality against the golden, `rendered_state`
set equality against the Kotlin `ErrorState` enum, and an adversarial runtime sweep in which no
facade error may land in the reserved unknown class.

Two class tokens are deliberately the strings PB-KEY-6 already shipped
(`swarm-custody/auth-required`, `swarm-custody/key-invalidated`). They are re-declared as literals
rather than aliased because the fence reads each class's value from SOURCE as a plain string literal
— a const referring to another const would be unreadable to it, and reading from source is what
stops a drifted table lying about what crosses.

`stampErrorClass` runs inside `barrier`, which every entry point already installs (PB-BIND-5), so
totality is structural: a verb cannot forget to classify and a verb added later inherits it. Its
residual default (`ErrClassInternal`) applies only to a foreign error identity none of its arms
recognise, which is correctly a bug — defaulting at a CONSTRUCTION site is what the syntax fence
forbids, and it still does.

### Stop is a keystroke (PB-APP-3)

`App.Interrupt` holds the lease and sends `0x03`. No signed action was minted: one would change what
`requireRemoteAuthz` accepts and need its own authz tuple, biometric tier and replay story, and until
every hop learned it the daemon's closed capability switch would refuse it one hop short while
sealing no reply — so Stop would hang forever while looking, to the app, like a command that was
delivered. Three consequences are the requirement rather than side effects: it is gated on a
confirmed lease, it is live-only (never queued, never replayed), and an undeliverable one lands on
the PB-INPUT-1 ledger.

### `revoked` and the three keyless answers (PB-APP-10)

`relay.ErrRevoked` gets a seventh connection state and a TERMINAL arm of the dial switch; before it,
that error fell through a bare `continue` and the phone redialled every 250 ms behind a spinner for
the life of the process.

The amendment added a **fourth** answer. Both grant-loss and first-launch are keyless and only one is
terminal, so `resolveSend` splits on whether the grant channel is MARKED: marked is `errGrantLost`
(wrapping `phonecore.ErrGrantLost`, remedy `machine_regrant`), unmarked is `ErrClassAwaitingKey`
(remedy `wait_for_machine`). The waiting state is therefore unreachable by the grant-loss detector by
construction rather than by a rule beside it, and it clears when the grant arrives because the same
transaction that installs the key clears the mark.

**S16 added no detection rule for grant loss, deliberately.** An earlier draft did — "connected +
keyless + mailbox drained to an empty tail" — and it was deleted. The phone cannot measure "drained,
no grant, retention cap passed": it holds no pairing timestamp, and the retention cap is relay
configuration asserted by the party this design treats as hostile. S10's existing two-condition rule
is proof rather than inference and it stands; what S16 owed was the JOIN.

### Pairing (PB-PAIR-4/-5/-6, PB-SAS-3)

`BeginPairing` decodes and stops. It used to dial on its second statement, so a QR naming an
attacker's relay had the handset's connection — its IP, the fact that it holds a pairing secret, and
the timing — before the user had seen the URL. `ConfirmOrigin` is now the only thing that dials, and
it carries the displayed origin BACK, which is what makes a swap after display impossible rather than
merely unlikely.

The fifth terminal state is `different_machine`, decided MID-HANDSHAKE from
`pairing.DeviceOutcome.MachineStatic`, because PB-PAIR-7 decided not to carry the machine static in
the QR and nothing before Noise msg2 knows which machine a QR belongs to. The comparison is the Noise
static and **nothing else**: the relay-auth and grant-signing keys are coordinates a machine may
legitimately rotate under the same identity, so comparing either would refuse a same-machine re-pair —
the flow `pin()` exists to serve.

`RejectSAS` is a distinct verb from `Cancel`, and neither ingests a SAS. Pairing state is persisted,
and the handshake declares a deadline no later than the relay's.

### Push preferences (PB-APP-7)

`SetPushPreference` seals a real signed `push_prefs`. Alerts carries NEEDS_INPUT and Mentions carries
FINISHED — a bijection, because two switches wired to one category leaves one dead and swapping them
silences what the user asked for while delivering what they refused. The version is a free-standing
durable counter (schema **v5 -> v6**), never derived from an epoch-scoped one: `SendSeq` restarts at
1 after a revoke rotates the epoch while the machine's stored version does not reset, so a derived
version would regress across a rotation and every later update would be refused for the life of the
install.

### Android (the S14 blocker)

`SealedStore` persists to injected file I/O under `noBackupFilesDir`; a Keystore-backed `KekProvider`
that never caches the KEK, so the content tier's gate is the unwrap refusing rather than a flag beside
it; `KeystoreProvisioner.generate`; and `PhoneRuntime` constructing `swarmmobile.NewApp` lazily and
failably. A KEK that is genuinely gone surfaces as `KeyPermanentlyInvalidated` (re-pair), never as a
silent failure to start — a first launch with no key is generation, and a launch where the alias
existed and the material is unusable is permanent invalidation.

> **AMENDED 2026-07-31 (ADR-007 B133) — one clause above is now false, and it is a clause, not the
> sentence.** *"The content tier's gate is the unwrap refusing rather than a flag beside it"* had a
> producer only while the content KEK was auth-gated. `Provisioning.kt:421` now requests
> `setUserAuthenticationRequired(false)`, so **the unwrap does not refuse and the content tier has
> no user-authentication gate at all** — B133 accepts that, and records that code discipline is
> what is left of the phone-side tier boundary.
>
> **Everything else in the paragraph is untouched and is explicitly KEPT by B133**: `SealedStore`
> under `noBackupFilesDir`, sealing at rest, a `KekProvider` that never caches the KEK, and the
> generation-versus-permanent-invalidation distinction. Non-exportability defends an attacker
> holding the app's data directory and not the handset, which is not the holder and which nothing
> in the new boundary trusts.

### PB-TOK-1

`android/design-tokens.tsv` joins `internal/design/tokens.json` to `colors.xml`, the three colour
values now actually agree, and `SwarmTheme.EXPECTED_DARK_COLORS` is recorded from the ORIGIN. That
constant's instinct was right and pointed at the wrong number: recorded from `colors.xml` it certified
that the app renders whatever `colors.xml` says, which is what it would do if `colors.xml` were wrong.
It was.

## Gates

`go build ./...` and `go vet ./...` clean. `gofmt` clean on every changed file. `golangci-lint run
./mobile/...` clean; on `./internal/phonecore/...` only the four pre-existing errcheck findings in
journal tests that the S15 evidence already records, in files this slice did not touch.

Kotlin: **191 tests, 0 failures, 0 errors** across 29 classes, on a forced `--rerun-tasks`.

**`-race` over every S16 conformance test: not one race has a single stack frame in `swarm/mobile` or
`swarm/internal/phonecore` production code.** Stated precisely because a bare "races reported" reads
as a defect: the seven reported races all sit between the RED scaffolding's machine goroutine and the
test goroutine (`s10Machine.deviceOutcome`), and one of them has since been fixed in the scaffolding
(`84d4443`).

## Accepted residuals

- **PB-PAIR-4's pairing state is a facade-owned file** (`<stateDir>/pairing-attempt`), not a
  `phonecore.State` field. This is a **decision, not a constraint**. It was written as a State field
  first, at a moment when a pinned-fixture guard made a new top-level field unaddable; that guard has
  since been made version-aware, so the obstacle is gone and a new field is addable again. The file
  stays because the coordinate carries no user content and no key material — PB-STATE-9 assigns it no
  tier, exactly as it assigns none to the staleness marks — and moving it now is churn. It is
  nonetheless a second durability mechanism in a codebase deliberately strict about PB-STATE-1's one
  enumerated durable schema, and is recorded as such rather than left unremarked.
- **A push preference the machine never received is never retried, and no facade surface reports it.**
  `SetPushPreference` saves and advances the durable version BEFORE sealing, so a send that fails on a
  disconnected handset — which ADR-007 B16 makes the normal case — burns a version the machine never
  stored. That is safe, and it was measured rather than reasoned about
  (`TestPBAPP7_APreferenceBurnedByAFailedSendDoesNotPoisonTheNextOne`): it works *only* because
  `filePushPrefs.SavePrefs` refuses anything not STRICTLY exceeding what it holds — a gap is fine, a
  repeat is not. If that rule ever became "consecutive", the first backgrounded toggle would deafen
  the machine to every later one. What remains is that the user sees their choice on screen while the
  machine keeps the old one until they happen to toggle again. The Kotlin `SettingsScreen` already
  models `pendingSync`/`acknowledged()` and the facade gives it nothing to drive them from. No retry
  was built: no requirement asks for one, and inventing an offline queue for a preference is the shape
  ADR-007 D7 refuses elsewhere.
- **`TestPBPAIR5_.../different_machine` is load-dependent.** Its precondition waits on
  `sum.EpochID != 0`, which `pin()` satisfies from the machine's payload before the bootstrap grant
  lands, so under suite load a later `TerminalWatch` can refuse for want of a content key. Measured:
  with a settle inserted, both halves hold. An epoch is not an epoch key, and that is not a usable
  proxy anywhere in the suite. Fix owned by the RED author.
- **The Kotlin halves of S16 and S17 cannot be verified independently.** Kotlin compiles the whole
  test source set at once, so an unimplemented RED in one package blocks every Kotlin test in the
  module. The 191-test run above was produced with a throwaway Gradle init script, outside the
  repository, excluding S17's `WakeNotificationTest.kt`. Nothing in the tree was changed to obtain it.
- **The AAR was rebuilt this slice** (arm64-v8a, x86_64, androidapi 21) so the Android side compiles
  against the new surface. Anything added to the facade after this point needs another rebuild before
  Kotlin can call it.
- **PB-PAIR-3 is a decision only.** No scanner dependency, no camera code, no decode path.

## Changes to shipped tests, disclosed in full

- `mobile/coverage_test.go`: S16's seven elements added to S8's `requiredScreenElements`. Additive;
  forced by a bidirectional check whose own failure message names "or the requirement list is stale".
- `mobile/conformance/verbs_test.go`: `Interrupt` and `SetPushPreference` removed from
  `TestS8_SurfacesWithNoWireVerbFailVisiblyAndLeakNoPendingOps`. Their premise was "this verb is not
  wired", which S16 makes false. **The second property those cases carried — that the call leaks no
  pending op — was owed back and has been**, in `s16_pushprefs_pending_test.go`. `Interrupt` is exempt
  because it creates no op at all.
- `mobile/conformance/conformance_test.go`: `TakeControl` + `AwaitLease` before `Interrupt` in the
  facade walk, because Stop is now a keystroke and PB-INPUT-2 gates it.
- Four shipped pairing call sites gained one `s16PassOriginGate(t, p)` line — the mechanical
  consequence of PB-PAIR-6 inserting a step. No assertion touched.
- `internal/phonecore/state_test.go`: the v6 fixture the schema bump requires; the test demands it.
- `internal/phonecore/s15_statetier_test.go`: one line of gofmt realignment picked up by a blanket
  format. Whitespace only.

Two further adaptations were made and then **reverted** once the PB-PAIR-5 amendment removed the need
(`TestPBSAS2` and `TestPBPAIR5_CancelIsATerminalStateNotAHang` briefly paired a fresh app). Neither is
modified now.

## Independent review (2026-07-25) — SHIP, with four corrections to THIS file

Twelve mutations were applied and reverted; every fence fired with a message naming the real defect.
Two results are **stronger** than this file claimed, and four claims here are **weaker** than written.

### Stronger than claimed

- **The Kotlin half verifies clean as committed.** The reviewer built the AAR and ran
  `./gradlew :app:lint :app:test` to BUILD SUCCESSFUL with **no init script and no workaround**.
  S17's test file is not in this commit, so the recorded "the Kotlin halves of S16 and S17 cannot be
  verified independently" residual **does not apply at this commit**. It applies once S17's RED
  lands, not before.
- **The four keyless states are disjoint BY CONSTRUCTION, not incidentally.** Verified structurally
  rather than by the passing test: `streamsOf()` never returns the grant stream, so the commit path
  cannot mark it; the sole writer of that mark is the grant-loss detector, gated on keyless AND a
  replay refusal; the sole clearer is the grant install's success path.

### Corrections

1. **The different-machine guard's narrow comparison is UNFENCED in the direction that matters.**
   Extending it to also compare the relay-auth key leaves the whole pairing suite green. The
   same-machine subtest re-pairs against a machine supplying **byte-identical** coordinates, so it
   cannot distinguish "compares the static only" from "compares everything" — while the code comment
   cites key rotation as the entire justification for the narrow comparison. **Fence owed.**
2. **The error-taxonomy syntax direction is convention-bound, not absolute.** An aliased `fmt` import
   evades the AST scan, as do a custom error struct literal and a join. **The keystone claim
   survives** — the barrier's residual default still classifies, so nothing reaches a user
   *unclassified* — but what can slip past is a **misroute** (a not-found rendering as internal with
   a report-a-bug remedy), not an unmapped class. This file should say totality is carried
   structurally by the barrier, and the syntax scan is a convention check on top.
3. **"The Android side constructs the phone" should read CAN construct.** The app declares **no
   Activity**, nothing calls the runtime's phone accessor, and the discard-invalidated-custody
   remedy has no caller. The wiring gate passes on a **string match** inside a method with no caller,
   and its own failure message stays literally true after the fix — a fence reporting a property it
   does not establish. Defensible as phase scope, since screens are policy models and the handset
   gate is deferred, but it is a residual rather than a delivery.
4. **Both PB-TOK-1 assertions written this slice are membership-based, not positional.** Permuting
   the recorded palette passes both. The property holds only because a **pre-existing** Robolectric
   test compares lists order-sensitively. Fence owed, and the credit belongs to a neighbour.

### The one defect, folded into S18b rather than filed here

`PhoneRuntime.routed()` collapses four custody verdicts onto re-pair. A platform downgrade then
gives re-pair -> re-provision -> downgrade -> the same screen (**the failure loop PB-APP-10 forbids,
reached through the remedy**), and a transient full disk at construction tells the user their key was
destroyed. Unfenced — there is no runtime test at all, and the custody test accepts the two
recoveries as a pair so it cannot see them merged one layer up. It is the same brick class as
PB-STATE-10, so it is being fixed there.

## Per-requirement evidence (PB-E2E-3)

Added in S19. The traceability table cites this file for **PB-APP-1..10, PB-PAIR-2/-3/-4/-5/-6,
PB-SAS-3 and PB-TOK-1**. The header's range form `PB-APP-1..10` is not a mention: four shipped
rows — PB-APP-2, PB-APP-4, PB-APP-5, PB-APP-6 — cited a document in which their identifiers do
not appear, so an auditor holding the row had no path to the proof. Reconstructed from the tests.

**Two of the four turned out to be partly unearned, and that is recorded here rather than
smoothed over.** S19's exit demonstration found that PB-APP-4's take-control half and PB-APP-6's
launch were both UNREACHABLE in production while their rows read `shipped`. Each section below
says what S16 established and what it did not.

### PB-APP-2 — the triage inbox

*Criterion: "UI test covering all four Groups and the empty state."*

`android/app/src/test/kotlin/dev/swarm/phone/ui/TriageInboxTest.kt`, six cases:
`every group is its own section in triage order` (all four Groups, in order),
`an empty group is still a section and says so`, `an inbox with no sessions at all reports the
empty state` (the criterion's second half — the two empty states are distinct, because a phone
that shows nothing is otherwise indistinguishable from a dead one), `the need summary is one line
and comes from the wire`, `a roster built over a stale journal is never presented as live`, and
`an absent session is marked absent and not stale`.

The facade side is `screen_coverage.tsv` row `sessions_with_group` (`App.Session`,
`Session.Group`), whose rule is that Group is VERBATIM from the wire and never derived on-device;
`mobile/conformance` exercises the roster against a real relay and a real gateway opener.

*Scope*: these are JVM unit tests over the presentation layer, not instrumented device tests.
`android/gate/s16_ui_test.go`'s `TestS16_NoUnitTestClaimsAPhysicalHandsetProperty` fences that
they keep claiming only policy.

### PB-APP-4 — terminal peek and take-control

*Criterion: "UI + integration test; asserts only sanitized text is rendered."*

**The peek half is established.** `mobile/conformance/s16_appverbs_test.go`'s
`TestPBAPP4_ThePeekRendersTheDaemonsBytesAndNothingElse` asserts the phone renders the daemon's
own lines joined verbatim, that the grid arrives at the geometry the daemon rendered, and that a
terminal snapshot never reaches the journal read model. `android/gate/s16_ui_test.go` fences the
negative that ADR-007 D2 actually requires — that no Android source contains terminal-escape
handling, i.e. there is no second renderer on the device — and
`android/app/src/test/kotlin/dev/swarm/phone/ui/SessionScreensTest.kt`'s `TerminalPeekTest` covers
`the grid is the daemon-rendered text verbatim`, `the lease is acquired and released explicitly`,
`the keyboard is enabled only while the lease is held`, and `a stale grid is banner-marked and the
keyboard stays available`.

**The take-control half was NOT established by S16, and was not reachable in production.**
`App.TakeControl` minted no gate token, so it sealed through the ordinary command path with a nil
content hash; the daemon's `handleTakeControl` refuses an empty `GateToken` before authorization,
so a real handset could never acquire a lease and therefore never type. Every S16-era fence sat on
a path that does not reach that check: this suite's harness GRANTS ITSELF the lease
(`harness.Drain` answers each take_control with a locally-sealed `OpLease`), and the Kotlin tests
model the lease as a state flag. Closed in S19 —
`mobile/conformance/s19_gatetoken_test.go` for the token and its binding,
`internal/skeleton/s19_e2e_test.go` for the whole chain against a real daemon.

### PB-APP-5 — the machine pane

*Criterion: "UI test; revoke + kill switch gated per PB-SEC-2."*

> **AMENDED 2026-07-31 (ADR-007 B133) — this criterion has NARROWED, and the paragraph below
> asserts three tests that no longer exist.** PB-SEC-2 is VOID: the trust boundary is the wire,
> and there is no local authentication on this handset for a freshness table to describe. The
> criterion loses its "gated per PB-SEC-2" clause entirely.
>
> **The three named tests were DELETED, not rewritten**, and what each fenced is recorded at
> `android/app/src/test/kotlin/dev/swarm/phone/ui/MachineAndLaunchTest.kt:12-28` so nobody goes
> looking: `destructive actions demand a per-use authentication and typing does not` transcribed
> section 6.0's freshness table; `an authentication for one action never authorises another` drove
> PB-SEC-2's last clause; `backgrounding or a screen lock invalidates every outstanding grant`
> drove three events that no longer occur. **None of this can be re-demonstrated, and no
> substitute is offered — the gate is gone.**
>
> **What survives is the half that was never about the holder**, and it is everything else in the
> paragraph below: the four display elements, and the kill switch being displayed and never
> settable from the phone. That is a daemon-side refusal (`handleRemoteSetControl` refuses the
> remote tier before consulting its backend) plus a bound surface that exports no setter, so it is
> unaffected by anything removed from the phone. **It also matters more now**, because
> `swarm remote off` and revoke from the computer are the only surviving mitigation for a lost
> handset.

`android/app/src/test/kotlin/dev/swarm/phone/ui/MachineAndLaunchTest.kt`, `MachinePaneTest`:
`the pane shows presence, the paired device and the activity log` covers the four display
elements; `the kill switch is displayed and can never be set from the phone` is the read-only
half, which the facade enforces structurally (`android/gate` and `mobile/coverage_test.go`'s
`TestPBBIND3_FacadeCannotEnableTheKillSwitch` — the bound surface exports no setter at all, so
the screen cannot regress into one). The PB-SEC-2 gating is `destructive actions demand a per-use
authentication and typing does not`, `an authentication for one action never authorises another`,
and `backgrounding or a screen lock invalidates every outstanding grant`.

The activity log is served by `App.ReadJournal`, exercised over the real relay in
`mobile/conformance/s16_staleness_test.go`, which also pins that a page over a stale journal is
never presented as live.

### PB-APP-6 — launch

*Criterion: "UI + façade test."*

`android/app/src/test/kotlin/dev/swarm/phone/ui/MachineAndLaunchTest.kt`, `LaunchScreenTest`:
`a submitted spec becomes one launch operation`, `a policy rejection is rendered with its reason
and is not retried` (the criterion's "policy rejection surfaced"), `a rate limited refusal is
distinguishable from a policy one`, and `an unanswered launch stays pending rather than being
guessed at`. The façade half is `App.Launch` and `screen_coverage.tsv` row `launch`, whose rule is
that the content hash must come from the canonical `schema.LaunchContentHash`.

**Two production defects survived all of it, and S19 found both.** (1) `swarmmobile.LaunchSpec`
carried no terminal geometry, so `App.Launch` built a `LaunchReq` with `Cols=0, Rows=0` and the
daemon refused every remote launch with `launch: cols/rows out of range`. (2) The daemon's
SUCCESSFUL launch reply carried no `OperationID`, so a launch that actually ran was
unattributable: `phonecore.foldContent` drops an untagged reply rather than mis-key it, and the op
stayed pending for the life of the phone process. Note what that means for the fourth Kotlin case
above — `an unanswered launch stays pending` was, in production, the behaviour of a launch that
SUCCEEDED. Both closed in S19: `mobile/conformance/s19_launchgeometry_test.go` and
`internal/protocol/s19_launchreply_test.go`, with the chain in `internal/skeleton/s19_e2e_test.go`.

### The pattern across the four

Three of these rows (PB-APP-2, -4, -5) are carried by JVM UI tests plus facade-level conformance
tests, and that is what their criteria ask for. What no S16 fence could do is reach the machine:
this suite's harness answers the phone itself, so every requirement whose real gate lives in the
daemon — the lease gate, the launch validator, the reply tagging — was satisfiable while the
product did not work. That is defect class (v) in the S19 brief, and PB-E2E-1 exists because of it.

---

## PB-APP-11 — staleness by silence (added and implemented 2026-07-31, ADR-007 B121)

**The requirement is new and the mechanism it names was specified in v2.** §6.0 has carried
*"cached-state freshness before it is shown as stale — 5 min without a successful poll"* since
the second round, `grep` returned **zero hits for it outside the requirements file**, and the
staleness decision it governs (`App.StreamState` -> `streamStale` -> `Core.StreamStale` -> the
persisted per-channel flags) had **no clock input at any layer**. It was not a row falsely
marked met; it was a binding number nobody implemented, and nothing in the project could see
that — which is why `internal/verify/phaseb_budget_test.go` now checks §6.0's table for an
owner and a fence per row.

**What it defends against.** Every staleness mechanism here keys on a GAP, and a gap is
observable only when a LATER seq arrives. So the declared adversary's cheapest move is not a
forgery: it withholds the newest frames and keeps answering polls with an empty page. No gap
forms, so nothing is stale; the poll SUCCEEDS, so no connection-state machinery fires; and
`Presence()` asks **the withholding party** whether the machine is alive.

**RED, evidenced** (`mobile/conformance/pbapp11_silence_test.go`, before any implementation —
genuine assertion failures against the real relay, not a compile error):

```
--- FAIL: TestPBAPP11_SilenceIsNotLive (0.33s)
    the machine's newest authenticated word is 6 minutes old and StreamState("journal") = "live", want "stale"
    ... same for "terminal", "reply", "grant"
    SessionList.Stale() = false (err <nil>), want true
    JournalPage.Stale() = false (err <nil>), want true
--- FAIL: TestPBAPP11_TheVerdictSurvivesARestart (0.25s)
    after a restart StreamState("journal") = "live", want "stale"
    ... same for "terminal", "reply", "grant"
```

**The test moves the CONNECTION, not a constant** (ADR-007 B113): the machine's `RelaySink`
seals six minutes in the past, which is byte-for-byte indistinguishable from a relay that held
the frame for six minutes — `IssuedAt` is AAD-covered, so a relay can only make a frame look
OLDER. Six minutes is inside PB-TIME-2's ten-minute window, so every frame is ACCEPTED: this is
not the age-refusal path. The suite asserts the attack's PREMISE too — `ConnectionState` still
reads `online` while the polls keep succeeding — so a run that passed because the transport
noticed something would fail instead.

**Mutation-proved, each applied and reverted in one command with the file checksummed after:**

| mutation of the production connection | result |
|---|---|
| `heardAt` returns the ARRIVAL instant instead of the machine's stamp | all silence assertions fail, including *"a coordinate this fresh is the phone's ARRIVAL time, which is the one clock in this exchange the relay controls"* |
| the coordinate is OVERWRITTEN rather than kept monotonic | `ALateFrameDoesNotMoveTheCoordinateBackwards` fails: a retained frame becomes the relay's switch for the phone's warning state |
| never-heard reads as live | `APhoneThatHasNeverHeardIsNotLive` fails |
| the coordinate is live-only (dropped from `persistState`) | `TheVerdictSurvivesARestart` fails: `1785456077553 -> 0` |
| `MachinePane` drops the freshness parameter / DEFAULTS it / keeps the boolean and drops the stamp | the three `android/gate/pbapp11_freshness_test.go` assertions fail in turn |

**Where it lives**: `internal/phonecore/freshness.go` (the budget, the clamp and the verdict),
the durable `State.LastHeardAt` committed inside the existing receive transaction (schema
7 -> 8, v8 fixture pinned), `mobile/app.go`'s `streamStale` — the ONE function all four read
models resolve to — and `App.MachineFreshness` for the explicit *"not heard from your machine
since HH:MM"* state, traced in `screen_coverage.tsv` under `stale_state`.

**The Go/Kotlin seam is fenced** because ADR-007 B121/S-1 is the same failure one requirement
earlier: a repair channel complete in Go with no production Kotlin caller, surviving because
each side's tests pass over the other's absence. `android/gate/pbapp11_freshness_test.go` reads
BOTH sources and requires the pane that renders relay presence to carry the phone's own
evidence as a REQUIRED parameter.

**Recorded residual, not invented away**: nothing on this wire is a liveness beacon, so an idle
machine and a withheld one are indistinguishable from the handset. The state is therefore
worded as what the phone knows ("not heard from") and never as a claim that the machine is
down. A beacon needs an interval in §6.0 that nobody has decided.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** to emit the traceability
table's DERIVATION column, and `internal/verify` fences that it does. One row per requirement, the
verdict token `DERIVED` or `NOT DERIVED`, and -- for `DERIVED` -- **the mutation that was made to
fail, in the same row**. An empty mutation cell is refused and counted NOT DERIVED.

**Reading a fence is not deriving it.** Every mutation below moved a PRODUCTION connection
(ADR-007 B113) -- a branch, a field assignment, a call site, a Kotlin constructor parameter --
never a constant a test transcribes. Each was applied, run, and reverted; `git status` is clean.

**Scope limit, stated before the table.** No JDK is available in this environment
(`/usr/libexec/java_home` reports none; `./gradlew` cannot start), so **no JVM/Robolectric test was
run or mutated**. Every Kotlin-side derivation below is a mutation of Kotlin PRODUCTION SOURCE
checked by a Go gate in `./android/gate`, which reads checked-in source and needs no toolchain.
Where a row's only remaining evidence is a Robolectric UI test, that half is called out in the row
rather than claimed. Nothing here touches PB-E2E-5's deferred set.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-PAIR-6 | DERIVED | `Pairing.ConfirmOrigin`'s `if origin != want` guard disabled at the CALL SITE, so the phone joins whatever origin it is handed rather than the one the QR named -> `TestPBPAIR6_AnOriginSwappedAfterDisplayIsRejected` fails. Reverted; `mobile/pairing.go` sha256 `6c23e0c9767e...` identical. Note the `mobile` unit package passes (13.8s) -- like PB-NET-1, this row is fenced at the conformance layer only, which is the right layer for it. |
| PB-SAS-3 | DERIVED | Re-run without truncating the output, after the previous attempt was piped through `head -4` and could not say whether this test fired. Same call-site mutation -- the phone's SAS display halved, `strings.Join(sas[:], " ")` -> `sas[:3]` -> `TestPBSAS3_TheSASIsComparedAndNeverTyped` fails on **both** its arms: *"the SAS is `🍌 🍌 🥕`, want six space-separated emoji as ONE display string"* and *"the two screens show different strings (phone `🍌 🍌 🥕`, machine `🍌 🍌 🥕 🐭 🍎 🐳`)"*. The second arm is the one that matters for a compare-both-screens ceremony: it fails because the screens DISAGREE, not merely because the string is short. Reverted; sha256 `6c23e0c9767e...` identical. |
| PB-PAIR-2 | DERIVED | `PermissionGate.degradedCapability` mutated so a **DENIED** camera permission names no degraded capability (the `PERMANENTLY_DENIED` arm left intact, so only one of the two denial paths breaks) -> **two Kotlin tests fail**: `PermissionGateTest > every_non_granted_state_names_a_degraded_capability` and `PairingPermissionTest > both denial paths offer manual entry`. Reverted; `PermissionGate.kt` sha256 `220e27a4b185...` identical. **The second failure is the row's own clause** — the manual-entry fallback is fenced against *both* denial paths, not just the permanent one, which is the distinction a user who taps "deny" once rather than "never ask again" actually lives in. |
| PB-TOK-1 | DERIVED | A single colour in `android/app/src/main/res/values/colors.xml` diverged from the token source -- `#FF0809` -> `#DEADBE` -> `TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens` fails. Reverted; `colors.xml` sha256 `8c20184e7a44...` identical. The fence compares the shipped Android resource against `internal/design/tokens.json`, so one value drifting apart is caught rather than only a wholesale replacement. |
| PB-PAIR-5 | NOT DERIVED -- and the cancelled arm is UNFENCED | Two mutations. **(1) At the constant:** `pairCancelled` collapsed onto `pairFailed`'s string -> both packages pass. Inconclusive by B113 -- moving a constant an assertion reads compares it to itself. **(2) At the CALL SITE**, which is the mutation B113 says to make: `p.setState(pairCancelled)` on the cancel path changed to `p.setState(pairFailed)`, so a user who cancels is told the pairing FAILED -> `mobile` **passes** (13.6s) and `mobile/conformance` **passes** (182.9s). Nothing catches it. Reverted; `mobile/pairing.go` sha256 `6c23e0c9767e...` identical. **The row's other arms ARE fenced** -- an earlier round's mutation fired `TestPBPAIR5_EveryTerminalStateIsExplicitAndDistinct/different_machine` -- so this is a fence covering some terminal states and not this one, with `cancelled` appearing in that test only as a comment. |
| PB-APP-1 | DERIVED | `Pairing.ConfirmOrigin`'s `if origin != want` guard disabled, so the phone joins a destination other than the one it displayed -> `TestPBPAIR6_AnOriginSwappedAfterDisplayIsRejected` fails: *"The user said yes to one URL and the phone joined another"*. **A second mutation SURVIVED -- see finding (1)** |
| PB-APP-2 | DERIVED | `App.Session` derives the roster Group on-device (`Group: "active"`) instead of copying `cs.Group` from the wire -> the fence fires with *"Session.Group = \"active\", want \"needs_you\" taken VERBATIM from the wire"* -- but from `TestPBBIND3_EveryFacadeMethodWorksAgainstARealBackend`, a test named for a different requirement. See finding (4). The four-Groups-and-empty-state half is `TriageInboxTest.kt` and was NOT exercised (no JDK) |
| PB-APP-3 | DERIVED | `interruptByte` changed from `0x03` to `0x1b` at its single use in `App.Interrupt`, so Stop sends ESC -> `TestPBAPP3_StopIsTheInterruptKeystrokeOnTheLiveLease` fails: *"PB-APP-3: Stop sent no 0x03 to the machine"* |
| PB-APP-4 | DERIVED | `App.Peek` re-processes the daemon's bytes -- `strings.ReplaceAll(strings.Join(s.Lines, "\n"), "\t", "    ")`, a plausible on-device re-sanitize -> `TestPBAPP4_ThePeekRendersTheDaemonsBytesAndNothingElse` fails. **Two further mutations SURVIVED -- see finding (2)** |
| PB-APP-5 | DERIVED | a `func (a *App) SetKillSwitch(on bool) error` added to the bound surface -> `TestPBBIND3_FacadeCannotEnableTheKillSwitch` fails (*"which would let a stolen phone re-enable remote control"*), with `TestPBBIND3_NoUntracedEntryPoint` and `TestPBBIND7_ExportedSurfaceMatchesTheGolden` beside it. The read-only half is structural. Presence, paired device and activity log are `MachinePaneTest.kt` and were NOT exercised (no JDK) |
| PB-APP-6 | DERIVED | (a) `App.Launch`'s geometry default removed, so a spec with no size seals `cols=0 rows=0` -> `TestS19_ARemoteLaunchCarriesATerminalGeometryTheMachineAccepts` fails: *"the daemon refuses ... PB-APP-6's launch never reaches a PTY"*. (b) the sole production Kotlin call `app.launch(specOf(draft))` removed from `PhoneSurface.kt` -> `TestPBAPP6_TheAppCanStartASessionAndTheLedgerAgrees` and `TestBoundVerbs_EveryBoundVerbIsCalledFromProductionKotlinOrLedgered` fail |
| PB-APP-7 | DERIVED | both toggles wired to one category (`NeedsInput: pref.Alerts, Finished: pref.Alerts`) -> `TestPBAPP7_TheTwoTogglesAreIndependentAndNotInverted` fails: *"A switch that gates both categories leaves the other switch dead"*. **The INVERSION mutation SURVIVED -- see finding (3)**. **AMENDED 2026-07-31 (ADR-007 B133) — NARROWED, and this row's subject is on the KEPT side of the narrowing.** The settings screen's biometric-gate toggle is deleted and the requirement no longer names it; the **two push toggles are explicitly kept**, and they are the whole of what this mutation touches. `TestPBAPP7_TheTwoTogglesAreIndependentAndNotInverted` is live in `mobile/conformance/s16_pushprefs_test.go`, and finding (3) below is unaffected and still open. Verdict carried forward, not re-earned |
| PB-APP-8 | DERIVED | (a) `streamStale` stops consulting the per-channel flag (`return !reconciled - a.core.MachineSilentAt(now)` only) -> `TestPBAPP8_EveryJournalDerivedReadModelCarriesItsStreamsStaleness`, `_AResyncInFlightIsVisibleAndIsNotAThirdValueOfStreamState` and four `TestS10_*` resync tests fail. (b) `SessionList.stale` repointed from the journal channel to the REPLY bucket -> `TestPBAPP8_EveryJournalDerivedReadModelCarriesItsStreamsStaleness` fails, so the bucket distinction is real. A journal->terminal repoint survives, correctly: PB-SYNC-1 requires a shared-bucket gap to stale BOTH, so the two flags are indistinguishable by construction |
| PB-APP-9 | DERIVED | `stampErrorClass` stops stamping (early return after the message check), so facade errors leave unclassified -> `TestPBAPP9_EveryErrorTheFacadeReturnsCarriesAKnownClass` fails on `App.Interrupt`, `App.Paste` and `App.Resize`: *"classified as \"swarm/unknown\" ... the user is told nothing they can act on"*. Totality is carried structurally by the barrier, as this file's own review correction (2) states. **A MISROUTE still survives -- see finding (5)** |
| PB-APP-10 | DERIVED | (a) the `relay.ErrRevoked` arm of the dial switch made unreachable -> `TestPBAPP10_ARevokedDeviceIsToldToRePairInsteadOfLoopingForever` fails (*"a revoked phone reports \"reconnecting\" ... it redials every 250 ms forever behind a spinner"*) and `TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace` with it. (b) the keyless split collapsed so both answers return `errGrantLost` -> `TestPBAPP10_APairedKeylessPhoneIsToldToWaitRatherThanToActOnTheMachine` fails on both the class and the collapse |
| PB-APP-11 | DERIVED | five mutations, all caught. (a) `heardAt` returns the phone's ARRIVAL instant instead of the machine's stamp -> `TestPBAPP11_SilenceIsNotLive` fails on all four `StreamState` channels, `SessionList.Stale`, `JournalPage.Stale`, `MachineFreshness().Silent` and the stamp itself (*"A coordinate this fresh is the phone's ARRIVAL time, which is the one clock in this exchange the relay controls"*), and `_TheVerdictSurvivesARestart` fails too. (b) `Core` overwrites `st.LastHeardAt` instead of keeping it monotonic -> `_ALateFrameDoesNotMoveTheCoordinateBackwards` fails, naming the millisecond regression. (c) `Snapshot.Stale` hardcoded false -> caught, but only by `TestPBAPP8_*` (finding 4). (d) `LastHeardAt` dropped from the persisted file form -> `_TheVerdictSurvivesARestart` fails `1785466717639 -> 0`. (e) `MachinePane.freshness` given a default value in `MachineAndLaunch.kt` -> `TestPBAPP11_TheMachinePaneCannotRenderPresenceWithoutTheFreshnessVerdict` fails |

### Findings from this derivation

1. **The different-machine guard's narrow comparison is STILL unfenced.** This file's own
   independent review (2026-07-25, correction 1) recorded *"Fence owed"* for exactly this, and it
   was not delivered. Widening `App.differentMachine` to also compare the relay-auth key --
   `!bytes.Equal(st.MachineRelayAuthPub, out.Machine.MachineRelayAuthPub)` -- leaves **`./mobile`
   and `./mobile/conformance` green**. The code's own comment says that widening would refuse a
   same-machine re-pair after a key rotation, i.e. break S8's revoke-then-re-pair, which is the
   flow `pin()` exists to serve. The same-machine subtest re-pairs against byte-identical
   coordinates, so it cannot distinguish the narrow comparison from a total one.
2. **PB-APP-4's fixture does not discriminate in two directions.** (a) `strings.TrimSpace` around
   the joined peek text is a no-op on the fixture -- three lines with inner tabs and inner double
   spaces but no leading or trailing whitespace and no blank lines -- so it PASSES, while a real
   daemon-rendered grid whose bottom rows are blank would have them silently stripped and the two
   ends would disagree about what the user saw, which is what the test's own message forbids.
   (b) the geometry is pushed as **80x24**, the universal default, so hardcoding `Cols: 80, Rows:
   24` in `App.Peek` also PASSES. A non-default fixture (say 132x43) and one trailing blank line
   close both.
3. **PB-APP-7's inversion test does not detect inversion.** Swapping the mapping to `NeedsInput:
   pref.Mentions, Finished: pref.Alerts` leaves `TestPBAPP7_TheTwoTogglesAreIndependentAndNotInverted`
   **green**: its two assertions are that each toggle gates exactly one category and that they gate
   different ones, both of which a swap preserves. The requirement fixes the assignment (Alerts
   carries NEEDS_INPUT, Mentions carries FINISHED) and production's own comment names the cost --
   *"swapping them silences the notifications the user asked for while delivering the ones they
   refused"*. The test needs to name which category each toggle delivered, not count them.
4. **Two properties are held INCIDENTALLY by tests named for other requirements.** PB-APP-2's
   "Group is verbatim from the wire" is caught only by `TestPBBIND3_EveryFacadeMethodWorksAgainstARealBackend`,
   and PB-APP-11's `Snapshot.Stale` -- enumerated by name in that row's own criterion (b) -- is
   caught only by `TestPBAPP8_EveryJournalDerivedReadModelCarriesItsStreamsStaleness`.
   `pbapp11_silence_test.go` asserts `StreamState`, `SessionList.Stale` and `JournalPage.Stale` and
   never `Snapshot.Stale`, which is the terminal grid -- the read model the withholding attack
   renders as live. Both properties hold; neither is guarded by a test whose title would send a
   maintainer to it.
5. **A MISROUTE in the error taxonomy is still invisible.** Reclassifying `relay.ErrQuotaExceeded`
   from `ErrClassRateLimited` to `ErrClassInternal` -- a user with a quota refusal told to file a
   bug -- leaves `./mobile` and `./mobile/conformance` green, including
   `TestPBAPP9_TheThreeRemediesAreNeverCollapsed`. This is the residual this file's review
   correction (2) already recorded ("what can slip past is a misroute ... not an unmapped class"),
   re-measured and still open. It is outside the row's literal criterion, which maps class to
   rendered state rather than sentinel to class, so it is recorded rather than counted against the
   verdict.
