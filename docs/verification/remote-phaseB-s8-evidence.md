# S8 evidence — the gomobile facade (PB-BIND-1..7, PB-SAS-1, PB-SAS-2)

**Commit**: `8293915` — one commit, 27 files, +6064/-91, including the review remediation.
**Requirements**: 9. **Decisions**: ADR-007 B8 (the single permitted key crossing), ADR-008.
**Related**: `remote-phaseB-s1-evidence.md` (PB-BIND-0, the dependency allowlist this slice inherits).

> **RECONSTRUCTED**, 2026-07-25, from the commit, the diff and the tests. Every result below was
> re-run at HEAD; where a later slice changed something S8 shipped, that is said explicitly.

## Why this slice exists

Requirements §4.1 found that `internal/phonecore`'s "gomobile-ready" doc comment was **false and
unenforced** — `crypto.ContentKey [32]byte` in nine exported signatures, unsigned ints throughout,
`AcceptGrant` returning four values, `(T, bool)` returns, `crypto.SAS` returning `[6]string`. §4.2
found that binding `phonecore` would ship the PTY, the VT emulator and the whole daemon to a
handset an adversary may hold, and that gobind's generated wrapper lives outside the module's
`internal/` boundary so an internal package cannot be bound directly.

A facade was therefore **mandatory** — a new layer, not a retrofit. S8 is that layer: the bound
surface the entire Android app is built on.

## What shipped

37 `App` methods (counted from the golden at `8293915`; 39 at HEAD after S14) plus the pairing,
roster, journal, snapshot, input, lease and custody types. Every entry point returns `error` last,
collections cross as handles rather than slices, and every entry point opens with a deferred
recover so no Go panic can reach the JVM.

## Per requirement: what proves it

| Requirement | What proves it |
|---|---|
| PB-BIND-1 (one facade, non-internal, the only bound surface) | `TestPBBIND1_FacadeIsTheOnlyBoundSurface` — the facade is not under `internal/`, the core still **is**, and exactly one non-internal non-`cmd` package imports `phonecore`. AAR leg: see the residual below |
| PB-BIND-2 (only gomobile-legal types) | `TestPBBIND2_NoExportedSymbolIsBindIllegal` runs **real gobind** over the facade and fails on either detection route — a hard refusal *and* a silent skip. It emits the counts: at HEAD, **129 exported elements, 129 bind-legal, 0 bind-illegal** |
| — its negative control | `TestPBBIND2_GuardDetectsAKnownIllegalSurface` runs the same detector over `internal/phonecore` and requires it to be reported illegal. At HEAD: **gobind hard-refuses `internal/phonecore`** — §4.1's blocker reproduced mechanically rather than asserted |
| PB-BIND-3 (covers every v1 screen capability) | `mobile/screen_coverage.tsv` (50 rows) joined **in both directions**: `_EveryScreenElementHasAFacadeMethod`, `_EveryTracedMethodExistsOnTheFacade`, `_NoUntracedEntryPoint`. Behaviour: `TestPBBIND3_EveryFacadeMethodWorksAgainstARealBackend` drives them against a real in-process relay + gateway |
| PB-BIND-4 (no unnecessary secret crosses JNI) | `TestPBBIND4_TheOnlySecretCrossingIsNamedAndInbound` — **no** entry point may RETURN `[]byte`; `[]byte` parameters are allowed only for `InstallWakeKey`, `InstallContentKey` (B8's crossing) and `SendInput`; and the crossing must be justified in the package doc by name (`PB-KEY-1`, `Keystore`, `zeroize`). Plus `_ExportedSurfaceCarriesOnlyFacadeTypes` |
| PB-BIND-5 (no panic crosses) | `TestPBBIND5_EveryEntryPointHasAPanicBarrier` (source-level, every exported func/method) + `TestPBBIND5_NoEntryPointPanicsAcrossTheBoundary` (behavioural: each entry point invoked on a **nil** `*App`, one subtest per method) + `_APanickingListenerDoesNotKillTheCore` |
| PB-BIND-6 (threading/lifecycle contract, stated bound and overflow) | Contract in `mobile/doc.go:26-37`, pinned by `TestPBBIND6_ThreadingAndLifecycleContractIsDocumented`; behaviour by `_ConcurrentCallsAndRepeatedStartStop` (`-race`) and `_SlowCallbackDoesNotStallTheCoreAndOverflowIsObservable`. The bound is a **stated constant**: `CallbackQueueSize = 256`, **drop-oldest**, and the drop is never silent — the listener receives an `Event{Kind:"overflow", Dropped:n}` |
| PB-BIND-7 (surface pinned) | `TestPBBIND7_ExportedSurfaceMatchesTheGolden` against `mobile/testdata/exported_surface.golden` (114 lines at `8293915`, 129 at HEAD) |
| PB-SAS-1 (SAS from the Go core, never a Kotlin table) | `TestPBSAS1_SASIsADisplayStringFromTheGoCoreAndNeverATableInKotlin` — `Pairing.SAS` must be exactly `() (string, error)` (a `[6]string` is bind-illegal and a per-index accessor *invites* a Kotlin table), and a repo-wide scan for the 64-entry wordlist. **It writes a synthetic Kotlin table first and requires its own scanner to trip on it** |
| PB-SAS-2 (KAT pins channel binding -> six emoji) | `TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT` — three KAT vectors as literals against the frozen `crypto.SAS`, **and** a live half: a real pairing over the real relay rendezvous, asserting the phone's rendered SAS equals the machine's |

## The two defects an independent review found, and neither suite caught

These are the slice's real content. Both are in `mobile/conformance/verbs_test.go` and
`drain_test.go`, whose headers say *"FAILING-FIRST (TDD RED, GG-5)"* in as many words.

### 1. `ReleaseControl` sealed `ActionDelete` — it would have DESTROYED the session

`take_control_end` routes on the **lease** plane and is never forwarded to the daemon. `delete`
*is* forwarded — `opForAction -> protocol.OpDelete -> handleDelete -> Daemon.Delete` — and
`internal/skeleton/deviceauth.go` classes delete as `ActionControl`, **the tier a phone that can
take control at all already holds**. So the sequence *take control, type, release* was fully
authorised and would have deleted the agent session the user was merely stepping away from,
appending a tombstone.

The fence is `TestS8_ReleaseControlEndsTheLeaseAndNeverDeletesTheSession`, and the negative half is
the point: it is not enough that `take_control_end` arrives — **no `delete` may ever be sealed by
this path**. The commit records the cause plainly: the take-control assertion ten lines above it
existed and the release-control one did not. *That absence is why the defect shipped.*

### 2. One undecodable frame spun the drain loop at full speed, forever

`phonecore` advances `RelayCursor` only inside `commitReceive` — only on a frame it **successfully
opened**. So an unopenable item at the mailbox **tail** is never passed, every subsequent read
returns it, and a drain loop whose only exit condition is "the page was empty" re-reads it at full
speed. Measured at the time: **4366 `mailbox_read` ops in 3 s against §6.0's 3/s budget**, flapping
the phone offline through the relay's own quota refusals.

Two things make this a security property rather than a performance one, and both are recorded in
the test's header: the relay is the **declared adversary** and the drain loop is the only thing it
can speak to unprompted, so this is a **one-frame battery drain** available to it; and it is
reachable *benignly* by any frame arriving before `InstallContentKey` — a phone that is merely
locked when the machine publishes is enough.

`TestPBSYNC6_AnUndecodableTailFrameDoesNotSpinTheDrain` measures **the phone's own wire traffic**
through a verbatim websocket proxy in front of the real relay, rather than asking the relay how
often it was read. The fix makes the loop continue only on **cursor progress**, so a real backlog
still drains at full speed because it advances.

### And three surfaces that returned success for ops no hop can resolve

`SetPushPreference` and `RevokeThisDevice` returned success-shaped values for verbs that cannot be
delivered. Both alternatives to a refusal are worse, and the test says which:

- an op handed to `issue()` that no reply can ever resolve raises `PendingOpCount` for the life of
  the process, **making every real pending op invisible**;
- a sealed-and-appended `device_revoke` burns a durable send-seq and returns nil — so the phone's
  declared panic action (PB-SEC-7) is *a silent no-op dressed up as success*.

`Interrupt` already did the right thing (seal nothing, record a durable legible refusal), so it is
the model the other two were moved to. `TestS8_SurfacesWithNoWireVerbFailVisiblyAndLeakNoPendingOps`
asserts all three resolve, carry a message, seal no `device_revoke`, and leave `PendingOpCount`
unchanged across all three calls.

### The epoch-scoped state that survived an epoch change

`TestS8_RepairIntoANewEpochReArmsTheFailClosedGates`: `pin()` wrote the new epoch id and nothing
else, leaving the live `App`'s `reconciled` flag set and the **previous** epoch's content key in
durable state. The window is invisible precisely because it **fails closed on the next process
start** — and on Android that process can live for hours.

## Three wire-verb gaps this slice found, and refused to paper over

The facade owns the **surface**; another slice owns the **verb**. S8 recorded all three in
`screen_coverage.tsv` rather than inventing verbs:

1. **`interrupt` has no signed action at all.** Resolved later (2026-07-25) without a new verb: an
   interrupt *is* a keystroke — `0x03` through a PTY in default `ISIG` mode — so PB-APP-3's Stop is
   `take_control` then `SendInput(0x03)`. See the progress doc for the two consequences S16 must
   honour.
2. **`device_revoke` is signed and served by the daemon but unmapped by `remotegw.opForAction`**,
   so it is refused one hop short. One line, in a package outside this slice's fence.
3. **The kill switch is read-only by design and must stay that way.** `handleRemoteSetControl`
   refuses the remote tier *before* consulting the backend — "a remote device must never re-enable
   a switch its owner turned off". The facade exposes only a getter and
   `TestPBBIND3_FacadeCannotEnableTheKillSwitch` bans any setter: a stolen phone re-enabling remote
   control would be a surface-level bypass of a daemon gate (PB-SEC-6).

## The trap S1 predicted, and the test that holds it

`LaunchContentHash` stayed in `internal/protocol` (Go has no function aliases). Reimplementing its
length-prefixed canonical encoding in the facade would produce **silent signature failures with no
compile error**. Two tests hold it: `TestS8Trap_FacadeDoesNotReimplementLaunchContentHash`
(source-level) and `TestS8Trap_LaunchContentHashMatchesTheCanonicalEncoding` (a KAT through the
real backend). Both pass at HEAD.

## Gates, re-run at HEAD 2026-07-25

`go test ./mobile/ -count=1` — **every S8-owned test PASSES**: the four `PBBIND3_*` coverage tests,
`PBBIND0`, `PBBIND1_FacadeIsTheOnlyBoundSurface`, both `PBBIND2` tests, both `PBBIND4`, `PBBIND5`'s
source fence, `PBBIND6`'s contract, `PBBIND7`'s golden, `PBSAS1`, both `S7Residual` tests and
`S8Trap`. `ok github.com/Nathandela/swarm/mobile 17.3s` when filtered to those.

`go test ./mobile/conformance/ -count=1` filtered to S8's twelve behavioural tests —
**all PASS**, `ok ... 28.7s`, including `PBBIND6_SlowCallbackDoesNotStallTheCoreAndOverflowIsObservable`
(18.6 s) and `PBSYNC6_AnUndecodableTailFrameDoesNotSpinTheDrain` (4.1 s).

> **Both packages are RED as a whole at HEAD, and none of it is S8's.** The failures come from
> **uncommitted S16 RED files** in the working tree — `mobile/s16_taxonomy_test.go`,
> `mobile/conformance/s16_pushprefs_test.go`, `s16_staleness_test.go`, `s16_undelivered_test.go`,
> `s16_pairing_test.go`, `s16_errorstates_test.go`. They are S16's declared failing-first state.
> Anyone re-running these packages before S16 lands must filter, or they will read another slice's
> RED as an S8 regression.

## What later slices changed in S8's own files, checked rather than assumed

`git diff 8293915 HEAD -- mobile/` — **no S8 assertion was weakened.** `mobile/bind_test.go`,
`contract_test.go`, `coverage_test.go`, `golden_test.go`, `conformance/verbs_test.go`,
`conformance/drain_test.go` and `conformance/robustness_test.go`'s S8 tests are byte-identical or
strictly extended. The two edits to `conformance_test.go` are an added `h.AwaitLease(testSession)`
(S11 made a confirmed lease a precondition for typing) and the removal of a redundant `Cursor: 1`
from a pushed journal record. `screen_coverage.tsv` gained rows and detail; none was deleted.

The golden moved twice as **reviewed** changes: S12/S11 added input surfaces, S14 added
`func NewApp(*Config, KeyCustody)`, `type KeyCustody interface`, two `ifacemethod` lines and two
`const` verdict tokens.

## Accepted residuals

- **PB-BIND-1's literal criterion — "`gomobile bind` succeeds on the façade" — is SKIPPED in the
  default gate.** `TestPBBIND1_GomobileBindProducesAnAAR` skips under `-short` and when
  `ANDROID_HOME`/`ANDROID_SDK_ROOT` are unset, which is the normal state for `go test ./...`
  because only S13's `androidgate` lane sources the pin. Confirmed at HEAD: the run logs
  *"no ANDROID_HOME/ANDROID_SDK_ROOT; PB-BIND-1's AAR leg needs the SDK+NDK"*.
  **The property is covered — by S13's `TestPBTOOL2_OneCommandProducesAnAARWithEveryDeclaredABI`,
  which binds `./mobile` through the checked-in build command and inspects the artifact, and which
  passes at HEAD.** But an auditor reading S8's green gate alone would over-read it. Recorded in
  both files.
- **`TestPBBIND4`'s `entryPoints()` sees funcs and methods only, so an `ifacemethod` is invisible
  to it.** That matters because S14 later added a **reverse-bound** interface (`KeyCustody`), on
  which Go is the caller and the directions invert. PB-BIND-4's guard cannot see it;
  `TestS14_TheCustodySeamIsInboundOnly` is what covers that shape. Recorded in the S14a evidence
  and repeated here because the gap is in *this* slice's fence.
- **S8 built its AAR by hand**, which is why two of its defects survived review: the ABI set was
  whatever the invocation happened to name, and the bind ran without `-trimpath`, so the shipped
  `libgojni.so` carried **48 absolute builder paths** rooted at the developer's home directory.
  Both are flags, and flags belong in a checked-in command — which is what S13's `build-aar.sh`
  and `assertAARContents` are for. Closed by S13, recorded here because it is S8's finding.
- **`PB-SAS-2`'s emulator half is not delivered.** The requirement's criterion is "Go KAT; emulator
  evidence shows phone SAS == machine SAS". The Go KAT is here and the live half runs against a
  real relay rendezvous **in-process**; no emulator screenshot exists. PB-SAS-3 (the
  compare-both-screens UI presentation) is S16's and is not claimed here.
- **`mobile/conformance TestPBSAS2_...` is on the load-sensitive list** as a *cleanup* failure
  (`TempDir RemoveAll: directory not empty`) from harness goroutines writing during teardown; the
  test body passes. Attributed properly in the progress doc — reproduced identically in a
  `git archive` of the parent commit.
- **A latent pairing race lives in `runMachinePairing`**, which `TestPBSAS2_...` uses.
  `relay.handleRendezvousClaim` refuses a rendezvous id it has never seen and `pairing.RunDevice`
  does not retry, so a `BeginPairing` that beats the machine goroutine's `Create` fails
  **terminally** and reports itself five seconds later as "the phone never derived a SAS", with the
  real cause discarded. S9 gated its own new test; `conformance_test.go`'s `runMachinePairing` was
  deliberately left alone. Anyone who sees "never derived a SAS" should suspect this first.
- **`go.mod` stays at 1.25.0**; the revert was refused, see ADR-008.

---

## PB-SAS-4 — added 2026-07-30, met by the same change that closed PB-PAIR-4

**ADDED 2026-07-30 (ADR-007 B86) because it was MISSING.** Every count movement in five audit
rounds had been a row that was WRONG; this was the first that was ABSENT. Nothing in the
specification required the channel binding to attest the **accept/decline exchange** — so
PB-SAS-1, -2 and -3 could all be perfectly met (no emoji table in Kotlin; a known-answer test over
the binding; a compare-don't-type UI rule) while the operators' comparison said nothing whatever
about whether the two sides had **agreed**. *The family named after the defence did not contain the
requirement that would catch the defect.*

**Met by commit `96b41ef`, which closed PB-PAIR-4 in the same change** — the committee's own
finding that these were **one protocol change, not two**.

**The mechanism.** The ceremony gained two frames. The device pins the machine on the acceptance
and **acknowledges** it; the machine pins the device and records routing **only** on that
acknowledgement. **The acknowledgement is keyed on the Noise CHANNEL BINDING — the same value the
two operators compared.** So the machine now commits only on a frame that no party lacking that
binding can produce, which is what makes the emoji comparison finally attest something about the
accept/decline exchange rather than only about the handshake that preceded it.

**What it does not claim.** The binding adds no secrecy here, and this is not two-generals solved:
an acceptance that is sent and never acknowledged yields `ErrAcceptUnacknowledged` and **NO device
claimed**. The property is that **doubt resolves toward claiming nothing**, which is the direction
PB-PAIR-4 requires.

**Evidence:**
- `internal/remote/pairing/pbpair4_agreement_test.go` and
  `internal/skeleton/pbpair4_lockout_test.go` — written RED by an independent author, committed
  failing at `a8bdc31`, green at `96b41ef`.
- **Mutation-verified independently:** pinning regardless of the acknowledgement fails the skeleton
  lockout test while the pairing test still passes — correct isolation, each half fenced separately.
- One existing security assertion was **restated and made stricter**, with its reasoning recorded in
  `96b41ef`: it had demanded the machine "cleanly accept" a tampered decision while the device
  failed closed, which **was the deterministic half-pair asserted as expected behaviour.**

## Derivation

**Backfilled 2026-07-31 from ADR-007 B116**, which recorded the round-7 `PB-BIND` deep-derivation.
Those mutations were **run and their results recorded per row**, in prose rather than in this marker
format — so the generator read the family as underived while four rows had in fact been broken on
purpose. **Only rows whose mutation B116 names individually are backfilled here.** The rest stay
`NOT DERIVED`, including three that B116 recorded as findings and one it explicitly left unmutated.

| requirement | verdict | mutation made to fail |
|---|---|---|
| PB-BIND-1 | NOT DERIVED | B116 recorded the topology half as READ, not mutated, and the row's literal criterion — `gomobile bind` succeeds — **skips** on any host without an Android SDK. Not derived, and its own criterion does not execute in the normal gate. |
| PB-BIND-2 | DERIVED | Real `gobind` run, 167 elements / 167 bind-legal / 0 illegal, with a live negative control that hard-refuses `internal/phonecore`. Non-vacuous **by measurement** rather than by assertion; confirmed not silently skipping. |
| PB-BIND-3 | NOT DERIVED | Round-7 FINDING, unfixed: a facade method that never works — error-classed, traced, golden regenerated — **passed both full suites**. The test named `Every...` has no completeness check. |
| PB-BIND-4 | NOT DERIVED | Round-7 FINDING, unfixed: a second reverse-bound key-crossing interface passes both custody fences. They fence DIRECTION; nothing fences COUNT ACROSS TYPES. |
| PB-BIND-5 | DERIVED | `defer barrier(&err)` removed from `App.Start` -> the source half fails (*"the first statement of App.Start is not a deferred recover"*) while the runtime half passes, because `Start` on a zero receiver errors before it can panic. The two halves are complementary, and the **total** one is the source half. |
| PB-BIND-6 | NOT DERIVED | Round-7 FINDING, unfixed: inverting `drop-oldest` to drop-**newest** — same constant, same counter, same doc string — leaves **both** fences passing. One checks the constant and the prose, the other that *some* events dropped. Nothing checks WHICH. |
| PB-BIND-7 | DERIVED | Tripped by **every** surface-changing mutation in the round-7 pass, including a `Sas()`/`SAS()` Java-namespace collision. Sensitive; its limit is that it pins **Go** names, not the Java namespace the app compiles against. |
| PB-SAS-1 | NOT DERIVED -- and its own claim is UNFENCED | The row says the emoji table *"is never re-implemented in Kotlin"*. Probed 2026-07-31: a Kotlin file (`ui/SasEmojiProbe.kt`) re-implementing a six-element SAS table with a `render()` over it was added to the shipped source set -> **`android/gate` passes, `ok 3.491s`**. Nothing names it. Probe removed; tree clean. The subject that IS fenced is `crypto.SAS` in the FROZEN package, which no Kotlin re-implementation touches. `s16_ui_test.go:208` already states the shape in its own comment -- *"that collected six emoji and compared them locally would satisfy every Go fence"* -- so the gap was known, written down, and never closed. |
