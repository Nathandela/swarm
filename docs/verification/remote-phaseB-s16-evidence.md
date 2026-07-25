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
