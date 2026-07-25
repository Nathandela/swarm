# Phase B progress and handoff

**Branch**: `worktree-remote-control-research`. **Spec**: `docs/specifications/remote-phaseB-requirements.md` (v3.5.1, 139 requirements).
**Gates**: `python3 scripts/check-phaseb-manifest.py` (ownership + DAG), `go build/vet/test -race ./...`.

**Checkpoint after 7 slices (`ea38ed0`)**: full `go test -race ./...` **green -- 47 packages ok,
zero failures, exit 0**, with `go build` and `go vet` clean across the whole tree. That covers
surgery on committee-validated Phase A code (the protocol split, the gateway inbound guard,
the reply-correlation change) with no regressions and no data races.

## Build environment (VERIFIED 2026-07-25 -- read this before any Android or timing work)

The Android toolchain is **fully present but in a non-standard location**. Nothing is on `PATH`
and no `ANDROID_*` variable is exported by default, so a naive probe (`which adb`,
`ls ~/Library/Android/sdk`, `java -version`) reports *no Android toolchain at all* and
`/usr/libexec/java_home` reports no JVM. That conclusion is WRONG. Every Android slice
(S13-S17) and the `gomobile bind` gate must export:

```sh
export ANDROID_HOME=/usr/local/share/android-commandlinetools
export ANDROID_SDK_ROOT=$ANDROID_HOME
export ANDROID_NDK_HOME=$ANDROID_HOME/ndk/<version>     # ls to pick
export JAVA_HOME=/usr/local/opt/openjdk@17              # brew-installed, NOT symlinked
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$PATH"
```

Verified available: SDK platform `android-35`, build-tools `35.0.0`, NDK, `platform-tools/adb`,
`emulator`, system images `android-35/google_apis/{arm64-v8a,x86_64}`, JDK 17.0.20,
Gradle 9.6.1 (Kotlin 2.3.21), and `gomobile`/`gobind` in `$(go env GOPATH)/bin`.
**An AVD named `swarmtest` already exists** (Android 15, `google_apis/arm64-v8a`), so
instrumented/emulator tests are genuinely runnable here -- the PB-APP-* "UI test" acceptance
criteria are NOT blocked by tooling. Only PB-E2E-5 (real camera, real biometrics, real FCM,
Doze, reboot, hardware Keystore attestation) needs physical hardware, and it is already an
explicitly deferred gate under §13.

### The host runs Go under Rosetta -- this taints every timing number

```
uname -m                  -> arm64          (Apple M1)   <-- ONLY from a NATIVE shell
file $(which go)          -> Mach-O 64-bit executable x86_64
go env GOARCH/GOHOSTARCH  -> amd64 / amd64
```

**CORRECTION — do NOT detect this with `uname`.** An earlier version of this note implied a
`uname -m == arm64 && GOARCH == amd64` probe. That is wrong, and wrong in the direction that
defeats the purpose. A translated process inherits the translated personality, so from **inside**
a Rosetta'd Go binary the kernel lies:

```
GOARCH=amd64 uname="x86_64" hw.machine="x86_64" proc_translated="1" brand="Apple M1"
```

`uname -m` and `hw.machine` both report `x86_64`; only **`sysctl.proc_translated`** reports the
truth (`1`). The shell-level `uname -m` above returned `arm64` only because the *shell* is native.
A uname-based probe therefore reports "native" on a Rosetta host — precisely the misleading
record this section exists to prevent. Verified by running the probe from a translated process
(S6b RED author's finding, independently reproduced). Harnesses MUST use `sysctl.proc_translated`
(plus `hw.optional.arm64` / `machdep.cpu.brand_string` for the host's real identity).

**AND THE PROBE MUST RUN IN-PROCESS.** *Where* you run it matters as much as which sysctl you
read, and this has now caught two reviewers in opposite directions. Measured on this host:

```
$ sysctl -n sysctl.proc_translated                 # from the SHELL
0                                                  # the shell is NATIVE -> reports "not translated"

$ /usr/local/bin/go run probe.go                   # from inside a process the Go toolchain spawned
GOARCH=amd64 proc_translated="1"                   # the truth

$ file /usr/local/bin/go
/usr/local/bin/go: Mach-O 64-bit executable x86_64  # -> Cellar/go/1.26.1
```

One reviewer ran it from bash, got `0`, and reported the toolchain as native arm64. That is the
same class of error as the `uname` trap, inverted: the shell is native, the Go process is not.
A latency harness must read `proc_translated` **from within the test binary** (e.g.
`unix.SysctlUint32("sysctl.proc_translated")` in-process), never by shelling out from a
native parent. Anything else records the parent's personality, not the measurement's.

The machine is Apple Silicon but `/usr/local/bin/go` is an **x86_64 binary running under
Rosetta 2**. Consequences, in order of how easy they are to get wrong:

1. Every benchmark recorded in this project was taken through binary translation -- including
   the "13-15 ms per-keystroke fsync on an M1/APFS host" figure that §6.0 cites to justify the
   file-backed `InboundState` harness rule.
2. Measurements are therefore **pessimistic** versus a native `arm64` build (Rosetta typically
   costs 20-30% on compute-bound Go; much less on syscall/IO-bound paths). A §6.0 budget that
   PASSES here passes natively with margin. **Never loosen a bound to "correct for" Rosetta.**
3. The real hazard is a **spurious FAIL** pushing an implementer into over-engineering a
   mechanism that was already fast enough. Any budget landing within ~25% of its limit must be
   flagged, not silently encoded.
4. §6.0 says the latency harness "records the environment". Recording "M1" alone would be
   actively misleading. The harness must record `GOARCH`/`GOHOSTARCH`/`runtime.GOARCH` and the
   translation status, or runs on native arm64 and on CI's x86 Linux are silently incomparable.
5. This is the strongest argument for splitting **structural** tests (an append and an
   outstanding wait cannot contend -- architecture-independent, cannot flake) from **budget**
   tests (environment-bound). The always-run gate should lean on the structural ones.

The AAR cross-compile (`android/arm64` from an amd64 host toolchain) is unaffected and works:
S8 produced a 5.4 MB AAR, independently confirmed on disk at the reported size.

## Requirements phase: COMPLETE

Five adversarial audit-committee rounds (codex/GPT-5.6 sol, opus, fable), all findings
verified in source before acting. Converged at v3.5.1: opus `requirements-complete`, fable
"nothing blocking", codex's single remaining blocker fixed and independently re-verified by
both. Full record in §14 of the spec.

Ownership and slice reachability are machine-enforced (`remote-phaseB-manifest.tsv`,
`remote-phaseB-slices.tsv`), each verified with negative controls, because homeless
requirements recurred in three consecutive rounds and an orphan slice in a fourth.

## Requirements coverage (measured, not estimated)

**60 of 142 shipped, 82 remaining (15 of 29 slices).** The completed slices were deliberately the
blockers and the security-critical machine-side work -- dependency surgery, gateway durability
in both directions, the transport, the reconciliation frame, the bound facade -- because they
gate everything downstream. The remaining 96 are weighted toward the Android app and end-to-end
verification.

The counts have risen twice mid-implementation, both times because a reviewer or test author found
a hole that would otherwise have shipped:
- **PB-LIFE-7 / S4b** -- an S4 re-reviewer restated an "accepted residual" that turned out to be
  exit-criterion-fatal on the default install (the phone pairs, then silence, forever).
- **PB-KEY-9 / S14a** -- the S14 RED author found that ADR-007 B14 (make `crypto.KeyStore`
  failable, in the Go core) was **decided and never implemented**, so PB-KEY-6's criterion was
  unreachable from Android; and that key material at rest is not sealed at all, which collapses
  PB-KEY-2's tier split independently of anything the Android side does.

That is the process working, not scope creep.

## Slice status

| Slice | Requirements | State |
|---|---|---|
| S0 ADR amendment | PB-DOC-1 | **SHIPPED** (`6cdc164`) -- 14 Phase B decisions recorded |
| S1 dependency-edge surgery | PB-BIND-0 | **SHIPPED** (`0024595`) -- closure 52 -> 18 non-stdlib, zero forbidden |
| S1b protocol additions | PB-SYNC-7 | **SHIPPED** (`689c8e8`) -- reconcile frame, lease confirmation, reply correlation |
| S2 gateway inbound durability | PB-GW-1, 3, 4 | **SHIPPED** (`f98b9a9`) -- inbound high-water + cursor now durable and identity-bound |
| S2b gateway outbound durability | PB-GW-7, PB-GW-8 | **SHIPPED** (`5aaacef`) -- live tail was refusing 81% of appends; coalescing + outbox |
| S3 QR renderer + payload | PB-PAIR-1, PB-PAIR-7 | **SHIPPED** (`20be9b2`) -- real symbol + relay URL; 39-char URL ceiling enforced; manual scan still owed |
| S4 gateway supervision | PB-LIFE-1..6, PB-OPS-3 | **SHIPPED** (`75674c1`) -- cask never linked `swarm-remote`; `Ensure` classified launchd failures by parsing prose. Re-reviewed: SHIP |
| S5 design tokens | PB-TOK-1/2/3 | **SHIPPED** (`638b61b`) -- Substrate pinned, drift-guarded |
| S6 transport resilience | PB-NET-2,3,4,6,7 | **SHIPPED** (`078ac63`) -- cleartext-via-redirect hole found by review and closed |
| S7 durable phone state | PB-STATE-*, PB-GW-6 | **SHIPPED** (`0ac4fb9`) -- the phone now survives a process kill; was the most severe committee finding |
| S7b gateway bounded-age | PB-GW-2 | **SHIPPED** (`a0bd09d`) -- age backstop for the window the seq guard cannot see. Reviewed: SHIP, seven mutations all fired |
| S8 gomobile facade | PB-BIND-*, PB-SAS-1/2 | **SHIPPED** (`8293915`) -- `ReleaseControl` sealed `delete` and would have DESTROYED the session; one undecodable frame spun the drain at 1455 reads/s |
| S4b remote-socket contract | PB-LIFE-7 | **SHIPPED** (`d971525`) -- stock install paired then went silent forever; the supervisor respawned a doomed gateway every 10s |
| S6b low-latency input path | PB-NET-5 | **SHIPPED** (`c22eb36`) -- phone->PTY p50 **486 ms -> 31 ms**, 21% of budget. Review found a race + permanent wait-slot leak from a legal frame sequence |
| S13 Android skeleton | PB-RUN-*, PB-TOOL-*, PB-TOK-4 | **SHIPPED** (`3fbaa50`) -- first Android code; AAR + APK + Gradle gate + CI lane all green |
| S11 input/lease semantics | PB-INPUT-*, PB-TIME-* | **SHIPPED** (`582676e`), re-audited through **four rounds** (`c85c210`, `3d84d53`). Round 3 found the daemon-restart lease brick and an ordering lock that covered only part of a shared stream; round 4 measured **2 of 80 commands vanishing**. Wants one clean review to close |
| **S14a Go custody seam** | **PB-KEY-9** (half) | **SHIPPED** (`582676e`), re-audited through **three rounds** (`3dfbab7`, `010876a`, `47809f0`). Round 2 found an **unauthenticated key-adoption path** that bypassed the binding round 1 added. Round-3 review in flight -- **S14 is blocked on it** |
| S14 Android key custody | PB-KEY-*, PB-SEC-1/2 | RED complete (77 `@Test` + 563 lines of scaffolding, `67a9116`); **blocked until S14a closes**. Owes PB-KEY-9's closing half, the sentinel->Kotlin mapping, and the dial-refusal behaviour |
| S12 push transport | PB-PUSH-0/1/2/3/5/6/7/8/10 | **SHIPPED** (`2cb9b13`) -- 65 tests. ADR-007 B19/B20 decided the wake-key crossing and the payload disclosure. Found `internal/remote/push` had **zero production callers**: a fully-tested sender no binary installed |
| S9 | PB-NET-1 | **in flight** -- one integration requirement, and it gates S10. It is also the slice that closes the phonesim-vs-facade gap end-to-end |
| S10, S15..S21 | see §11 of the spec | not started |

**Review rounds are still earning their keep, which is why they continue.** Every round so far has
found at least one real defect, including in work already signed off twice: S14a's round-2 re-review
found an unauthenticated ingress that two prior reviews and my own inspection all missed. When a
round comes back clean with its failed attacks listed, that is when a slice closes.

**S14a is shipped but NOT closed.** The cross-model review (mandated by ADR-007 B14 for any widening
of the frozen crypto package) confirmed the widening is exactly what B14 and B18 authorise and
nothing more -- verified commit-to-commit and by recomputing all six KAT vectors against the parent
commit -- but found the B18(a) branch at `internal/remote/relay/client.go:402-406` **unfalsifiable
by any test**: both `ClientAuth.Sign` fixtures in the tree return nil errors, so the refusal path
can be deleted and the whole suite still passes. Standing defect class (i), a guard that cannot
fail. Remediation in flight.

## Working agreement that is producing the results

Four independent agents per slice, no shared context: test author (RED, evidenced failure)
-> implementer -> independent reviewer -> fix agent. The reviewer has caught a real defect in
every slice so far, including ones the implementer and test author both missed.

## CROSS-SLICE BRICK RISK -- wire both halves or neither

PB-SYNC-7 (S1b) ships the reconcile record and the phone-side gate, but production wiring is
deliberately NOT in that slice: `remotegw/service.go` still constructs `RelaySink` with nil
`Authorities`/`Machine`, so the bootstrap is inert and the record is never published. The
phone-side seams (`RequireReconciled`, `Reconciled`, `TakeFor`, `SeedFrom`) have zero
production callers today.

**The failure mode**: S7 wires the phone-side `RequireReconciled()` gate, nobody wires
`RelayConfig.Authorities` + `RelayConfig.Machine`, and the phone refuses every mutating op
FOREVER while nothing in the tree fails. That is precisely the permanent brick PB-SYNC-7
exists to prevent, re-created at the slice seam.

Both halves, in the same slice:
- gateway: `RelayConfig.Authorities` (a real `ReconcileSource`) + `RelayConfig.Machine` in
  `internal/remotegw/service.go`. `InboundHighWater()` is
  `inbound.Load().Highest[InboundStream{Sender: [8]byte{}, Epoch: cfg.EpochID}]` (sender-zero,
  because phone->machine seals never set `SenderKeyID`); `ReplyCeiling()` is the reply
  `SeqSource.Issued()`.
- phone: the calls to `RequireReconciled` / `SeedFrom` / `SeedHighWater` / `NewGrantReceiverAt`.

## Standing review guidance (earned, not theoretical)

Two defect classes have now recurred often enough to be worth asking about on every slice:

1. **"What if there is more than one?"** Single-instance tests pass while the multi-instance
   case is broken. S2 needed the replay high-water keyed per `(sender, epoch)` -- a scalar
   would brick the phone on every revoke, since a rotated epoch legitimately restarts at seq 1.
   S2b needed the coalescing stash keyed per session -- a single slot lets one session's frame
   discard another's stashed final grid, stranding a quiescent peek on a stale grid forever.
   Both were found by a reviewer asking about N, not by the tests.

2. **"Does the fix make a self-healing failure permanent?"** Three of the defects found so far
   were regressions introduced BY hardening work. S2's durability turned two previously
   self-healing conditions (a regenerated machine identity, a reset relay mailbox) into silent
   permanent bricks. Ask what used to recover on restart and no longer does.

### Six orchestration errors caught by agents pushing back

Not defects in the code -- defects in the INSTRUCTIONS the orchestrator gave. Each was refused
with proof rather than implemented:

1. A reservation-style seq seam inbound -- would have silently censored the phone's next 64
   legitimate frames, take_control and kill included (reservation is a sender-side technique;
   inbound the phone owns the seq space).
2. Enabling the bounded-age check before the phone stamps IssuedAt -- would have computed an
   age of ~56 years and rejected every legitimate keystroke.
3. "A failed append never consumes a seq" -- unsafe, because the relay commits before replying,
   so the same seq would carry two different sealed envelopes and the phone would silently drop
   one.
4. A rollback anchor naming three authorities -- no inbound frame could carry any of them, so
   "fail closed until reconciled" would have been permanent.
5. Excluding PB-NET-5 from the slice the manifest assigned it to -- the requirement would have
   gone unimplemented while its evidence file never mentioned it.
6. Sourcing the machine id from the identity module -- it exposes only a hostname and routing
   id, neither of which is the endpoint id the phone sees; a plausible-but-wrong value HIDES
   the brick rather than exposing it.

A compliant swarm implements all six. The independence between roles is what caught them.

Also standing: an agent that says "the test is wrong" is right about half the time here, and
has been right every time it PROVED it (the QR no-op substitution, the identical httptest
certificates, the unsatisfiable timing assertion, the plist regex). Verify the proof, never the
claim.

## Three wire-verb gaps found by the facade traceability table (S8)

The facade owns the SURFACE; another slice owns the VERB. Same split pattern section 11 already
uses for the push preference.

1. **`interrupt` has no verb at all.** The signed action set defines launch/kill/delete/approve/
   device_revoke/take_control/terminal_watch/terminal_unwatch -- no interrupt -- and the gateway's
   action map covers only kill/delete/launch. **PB-APP-3's persistent Stop is half-unimplementable**
   until a verb exists.

   **RESOLUTION (recorded 2026-07-25): no new verb is needed.** An interrupt IS a keystroke --
   Ctrl-C is byte `0x03`, and a PTY in its default `ISIG` mode turns it into SIGINT for the
   foreground process group. That is precisely how a human stops a running agent, and the phone
   already has the machinery: `take_control` -> `data_in`. So PB-APP-3's Stop resolves to
   **acquire lease (if not held) -> send 0x03**, with `kill` remaining the escalation for a
   session that ignores SIGINT. This is strictly better than minting an `interrupt` action:
   a new signed action would change the signed action set (ADR + a change to what
   `requireRemoteAuthz` accepts) to duplicate a capability the input plane already delivers,
   and it would need its own authz tuple, its own biometric tier, and its own replay story.
   Two consequences the S16 implementer must honor: (i) Stop requires the lease, so an observer
   must be shown the take-control step rather than a Stop button that silently does nothing;
   (ii) 0x03 rides the LIVE-only path (ADR-007 D7), so an offline Stop must resolve to
   "delivery unknown / not sent" and must NOT be queued for replay -- a Stop that arrives ten
   minutes later, after the user gave up and did something else, is a genuine hazard.
   S8's facade correctly refuses to invent a verb; it records a durable local refusal instead,
   which stays correct until S16 wires Stop to the input plane.
2. **`revoke` is broken at the gateway.** `ActionDeviceRevoke` IS in the signed set and the daemon
   serves it through `requireRemoteAuthz`, but `remotegw.opForAction` does not map it, so a
   phone-sealed `device_revoke` is refused "unsupported command action". One line, but it is a
   reviewed edit in `internal/remotegw`.
3. **The kill switch is READ-ONLY by design and must stay that way.** The daemon refuses the
   remote tier *before* consulting the backend: "a remote device must never re-enable a switch
   its owner turned off". The facade exposes only a getter and a test bans any setter -- a stolen
   phone re-enabling remote control would be a surface-level bypass of a daemon gate (PB-SEC-6).

## A fifth standing defect class, found in S11: the fence guards a path production does not take

The four already recorded are: a guard that cannot fail; a plausible-but-wrong value that hides a
brick; a test that passes because its subject became unreachable; and a requirement satisfiable
while the defect ships. S11's review added a fifth, and it is the hardest to see because
**everything looks correct**: the invariant is real, the test is real, the test passes, and the
production code path is not the one under test.

The instance: ADR-007 D7 says input is live-only, never queued or replayed. `transport.SendLive`
enforces it and `TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing` fences it. But
**`mobile.App.SendInput` never calls `SendLive`** — it calls `relay.Client.MailboxAppend` directly,
through `sendContext` -> `awaitConn`, which polls up to **5 seconds** for a reconnection and then
appends. Probed: a keystroke typed with no live connection blocked 864 ms, returned `nil`, and
**arrived on the reconnected link**. The invariant was enforced in a package the phone does not use
for this, and the fence had been green all along.

What to do about it, for every remaining slice: when a requirement names a structural invariant,
**trace the production call path from the outermost caller inward** and confirm the fence sits on
*that* path, not on a sibling that implements the same idea. Grep for the enforcing function and
check who actually calls it. A second instance in the same review: `RetryFor` has zero production
callers, so PB-INPUT-4's "never blind resend" is enforced nowhere while its table is fully tested.

## IN FLIGHT AND UNCOMMITTED (as of the last handoff) -- read before touching the tree

Two slices have complete work sitting **uncommitted in the worktree**, and they share files, so
they must be committed together or not at all:

- **S11** (PB-INPUT-*, PB-TIME-*) -- implementation plus a full remediation round closing four
  blocking review findings. Its own gates were green (`go test` and `-race` across phonecore,
  remotegw, transport, mobile, conformance) **immediately before S14a's change landed**; those
  numbers are the real state of S11.
- **S14a** (PB-KEY-9) -- mid-migration. The failable `crypto.KeyStore` signatures (ADR-007 B18)
  have landed and the `phonecore`/`mobile` call sites are partway through, so the tree is
  transiently broken:
  ```
  internal/phonecore/command.go:42: assignment mismatch: 1 variable but ks.SignCommand returns 2
  internal/phonecore/core.go:83:   too many arguments in call to OpenStore
  ```
  This is the expected shape of the change, not a defect.

**They cannot be separated**: `internal/phonecore/core.go` now carries edits from both. Committing
S11 alone would sweep S14a's partial migration. Finish the migration, verify the tree builds, then
commit S11 first and S14a second.

**Both still owe an independent review pass**: S11's remediation (four blockers, including a
behaviour change -- the clock-skew refusal was removed from the phone in favour of "phone explains,
machine enforces") and S14a in full. ADR-007 **B14 additionally requires S14a be re-reviewed
CROSS-MODEL after GREEN**, as the 2026-07-23 SAS widening was, because it widens the frozen crypto
package.

## THE SHIPPED APP STILL WRITES THE CONTENT KEY IN THE CLEAR -- S14 owes the closing half

**PB-KEY-9 is NOT delivered until S14 lands a facade verb.** S14a sealed key material at rest and
the acceptance gate (`android/gate/keycustody_test.go`, which reads the raw bytes on disk) went
RED -> GREEN. But that gate injects real sealers from Go. **The Android app cannot**: gomobile
cannot set a Go struct field, and the facade is golden-pinned (`mobile/testdata/exported_surface.golden`,
enforced by `mobile/contract_test.go`), so `mobile.NewApp` has no way to supply one. It therefore
passes `phonecore.InsecureCleartextSealer()` -- identity Seal/Open -- and on a real handset today
the epoch content key is still recoverable from `phone-state.json`.

This is ADR-007 B18's own forecast made concrete, and it is the fifth standing defect class in its
purest form: **the acceptance test is green and the product is not**. It is recorded here rather
than left in a commit message because a later reader could otherwise take PB-SEC-1's green gate as
proof the property holds on device.

Fail-closed at `NewApp` was considered and rejected as the interim: it would take the Android app
and the whole `mobile/conformance` suite down until S14 lands, with no security gain in the
meantime (nothing ships to a handset before S14 either way). The safety property B18(c) actually
demands is preserved -- **omission** yields `ErrNoSealer`, and cleartext requires typing
`Insecure...` at a call site, so production cannot reach it by forgetting a field. Two FILES carry
that word -- `mobile/app.go` and `mobile/conformance/harness_test.go` -- holding four call
expressions between them (one sealer per tier, twice).

**This is now fenced, not merely conventional.**
`TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites` requires the set of files calling
`InsecureCleartextSealer` to equal exactly those two, and fails in BOTH directions: a third file
means unsealed custody spread somewhere uninventoried; **fewer** means S14 landed the facade verb and
**this very section is stale**. Its failure message says so and names this section, so whoever trips
it is pointed at the stale record rather than left to wonder why a count moved. The fence walks
`_test.go` files too, because one of the two sites is a test file.

**S14's obligation, precisely**: add a facade verb that accepts an Android-Keystore-backed KEK,
regenerate the golden, and delete both `InsecureCleartextSealer` call sites -- at which point the
fence above goes red on purpose and this section must be retired with it.

## S14 ALSO OWES THE PHONE'S CUSTODY-REFUSAL BEHAVIOUR -- the dial error is discarded today

Found by main while verifying the S14a cross-model review finding. **This is a defect that would
ship WITH S14**, not before it, which is exactly why it is written down here.

`mobile/relay.go:140-142` discards the dial error outright:

```go
cl, err := a.dial(ctx)
if err != nil {
	continue
}
```

`mobile/relay.go:165-167` wires `Sign: ks.SignRelayAuth`, and `cmd/swarm-remote/config.go:143`
records that the machine identity never refuses -- so **the phone is the only production caller of
`relay.ClientAuth.Sign` that can fail**. Once custody is hardware-backed, that bare `continue`
means:

- `crypto.ErrKeyAuthRequired` (RECOVERABLE, wants a biometric) -> an endless "reconnecting" loop
  with no re-prompt. The user has no way to learn that authenticating would fix it.
- `crypto.ErrKeyInvalidated` (PERMANENT, the device must re-pair) -> the same silent loop, with no
  terminal state and no re-pair prompt. It retries forever against a key that is gone.

This is ADR-007 B18(a)'s own stated failure mode -- "swallowing that refusal ... would re-create one
layer up exactly the errorless interface B14 removed" -- reproduced **one layer further up than the
ADR's comment anticipated**. The sentinels are handled carefully at `internal/phonecore/keycustody.go:179`
and `internal/phonecore/state.go:444`, and then dropped on the floor at the transport edge.

**Not reachable today**, which is the only reason it is not a stop-ship: the shipped app still uses
the software keystore (see the section above), so `SignRelayAuth` cannot error. It goes live at the
exact moment S14 lands the hardware-backed KEK.

**S14's obligation**: a test drives `ErrKeyAuthRequired` through the phone dial path and asserts a
user-visible **re-prompt** state (not "reconnecting"); a second drives `ErrKeyInvalidated` and
asserts a **terminal, non-retrying** re-pair state. Both must fail against the current `continue`.
Requirement anchor is PB-KEY-6 -- "every signing path" includes this one. S14 already owns mapping
the two sentinels to typed Kotlin exceptions, which is the natural home for the behaviour.

The narrower sibling finding -- that `relay/client.go:402-406` handles the refusal correctly but is
unfalsifiable by any test -- is being closed under S14a and is NOT this item.

## A FRESH INSTALL NEVER LEARNS ITS MACHINE ID -- the most severe defect found in Phase B

Found by S9's RED author on the FIRST run of the first test that pairs a fresh install and then uses
it. Fix in flight; recorded here immediately because of what it costs if it ships.

**The chain** (every link verified independently before dispatching a fix):

- `mobile/app.go:115` passes `cfg.MachineID` into `phonecore.Config.Machine`.
- `phonecore/core.go:83` forwards it to `OpenStore`, where it is **only a load-time filter** --
  `state.go:497`: `if machineID != "" && f.Machine != machineID { return nil }`. Never an initialiser.
- On a fresh dir the file does not exist, so `load()` returns early with the state zero.
- `mobile/pairing.go:167-169` `pin()` sets `MachineStatic`, `MachineSignPub` and
  `MachineRelayAuthPub` -- **never `st.Machine`**.

So `State.Machine` stays `""` for the life of the install.

**Consequence A**: `crypto.Command.Canonical()` (`devicesig.go:47`) refuses an empty `Machine`, so
`TakeControl`, `Kill`, `Launch` and `ReleaseControl` -- every mutating verb -- fail on a first-launch
pair-then-use.

**Consequence B, and this is why it is the worst one**: `persistState` writes `Machine: ""`
(`state.go:610`), so the NEXT process start compares it against the configured id and **discards the
entire durable blob** -- pairing, epoch, content key, relay cursor, durable send-seq ceilings. On
Android a process death is routine. A product that fails every command on first launch is noticed in
five minutes; a product that then silently forgets a working pairing on the first restart presents as
"it randomly loses my phone".

**Why nothing caught it, and why this vindicates the slice**: every conformance fixture seeds
`Machine: h.Machine` (`harness_test.go:163`), and `phonesim` never authors a signed command through
the facade at all. It is reachable ONLY by pairing a fresh install and then using it -- the exact
composition PB-NET-1 asks for. Standing class (v) in its purest form: **a whole fixture family
seeded past the defect.** S9 exists to close that gap and closed it on its first run.

**The edge the obvious fix misses**: `state.go:497`'s different-machine early return. Initialise only
the not-exist branch and the "re-pair self-heals" path lands straight back in the broken state.

## PB-NET-5's LATENCY HARNESS DOES NOT MEASURE THE SHIPPED INPUT PATH

Found by the S11 round-3 implementer while I was asking it to prove its lock had not blown the
latency budget. **It could not have, and neither can the harness see a regression there at all.**

`internal/phonesim` `Phone.Type` seals with `phonecore.SealInputData(p.content, ..., p.seq.Next(),
...)` and calls `relay.MailboxAppend` **directly**. It never enters `mobile/commands.go`. So S6b's
PB-NET-5 measurement covers phonecore's seal, the relay, the gateway, the daemon and the PTY — but
**not the gomobile facade**, which is where the coalescer, the lease gate, `sendInputFrame` and now
the input ordering lock all live. Those are on the real app's path and on nothing the harness times.

The numbers are real for what they measure (p50 29.9 ms against a 150 ms budget, 20%), and the
facade's added cost was measured separately and is negligible (~1.9 us per keystroke frame, ~13.4 us
per 4 KiB paste). **The gap is coverage, not a known regression.** But it is standing defect class
(v) applied to a performance budget: the fence guards a path production does not take, which is
precisely the shape that let the S11 B2 defect ship in the first place.

**Owner: S19**, whose exit demonstration is the natural place to time the real facade path
end-to-end. Until then PB-NET-5's evidence should be read as "phonecore -> PTY", not "phone -> PTY",
and no one should conclude from it that a facade-layer regression would have been caught.

## Carried from the S11 round-2 review (NOT in any remediation brief -- deliberately deferred)

The three blockers (B-1 daemon-restart lease brick, B-2 input seq inversion, B-3 the spelling-fence)
are in remediation. These are the recorded residuals, each with an owner, so none of them is
rediscovered later as if new:

- **The clock verdict never clears and has no pull surface.** `mobile/relay.go:340`
  (`if !changed || msg == "" { return }`) emits nothing on the transition back to healthy, and the
  golden has no clock verb. A screen opened after the event, or after the user fixes the clock,
  cannot learn the current verdict. This is the same latch the round-1 B4 fix removed from the
  command path, re-created one layer up in the UI -- and it is inconsistent with this round's own
  `UndeliveredInputs()`, added expressly as "the matching pull surface for a screen that opens
  afterwards". **Owner: S16** (it needs a facade verb, so it travels with the UI slice).
- **A phone more than 10 minutes FAST goes silently deaf to the entire machine->phone plane.**
  `snapshot.go:472` is one-sided against `InboundMaxAge` on the PHONE's clock, so every reply,
  journal record and snapshot returns `ErrStaleAge`; `mobile/relay.go:220-223` swallows it with no
  stale mark, no event, and `ConnectionState()` still reads "online". The skew feature cannot fire
  because it needs an OPENED reply. Not a regression -- it follows from PB-TIME-2/S7b's deliberate
  one-sidedness. Note this is the **third** clock wall (30 s surfaced, 60 s refused opaquely,
  10 min deaf) and the only one that was undocumented. **Blocked on** the PB-TIME-2 reply-seal gap
  already recorded below; the two must land together.
- **`transport.RetryFor` AND `transport.SendLive` both have zero production callers** -- the facade
  appends through `relay.Client.MailboxAppend` directly. Which means
  `TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing`
  (`transport/s6b_input_test.go:374`) -- the fence whose blindness let the round-1 B2 defect ship --
  is **still a fence on a path production does not take**, and this round neither retired nor
  re-pointed it. Standing defect class (v), now named with an owner. Not blocking: "never blind
  resend" is trivially satisfied because nothing resends, and D7 is structurally enforced by
  `sendCoalesced` never re-buffering a failed frame. The hazard is the next slice adding a resend
  without consulting the table. **Owner: whoever next touches the transport send path -- delete the
  fence or re-point it at the live path.**
- **`replyMu` is held across a relay append with a 10 s ceiling** (`lease_confirm.go:82-97`). A
  wedged relay lets one severance notice head-of-line-block every lease confirmation and command
  reply for up to 10 s. Correct given the ordering the reply-seq fix requires, and not on the
  keystroke path, but new this round. Watch it if reply latency ever matters.
- **The undelivered-input ledger is unbounded.** `coalesce.go:56/170/180` append forever,
  `Undelivered()` is a read and not a drain, and the facade has no clear verb. A minute of
  autorepeat against a dead lease retains roughly 1800 entries, and `UndeliveredInputs()` copies the
  whole slice per call. **Owner: S16**, with the pull surface above.

## EVIDENCE FILES ARE MISSING FOR SIX SLICES -- close this BEFORE the final audit

Measured, not estimated: `docs/verification/remote-phaseB-s<slice>-evidence.md` exists for S1, S1b,
S2, S2b, S3, S4, S4b, S6, S7 and S14a, and is **missing for S5, S6b, S7b, S8, S11 and S13**. (S0 is
the ADR amendment itself, so ADR-007 is its evidence.)

This is not a gate failure today — the project convention ties evidence to the EPIC's exit criteria,
and Phase B's exit demonstration is S19 — but the final audit committee validates against all 142
requirements, and six slices whose only record is a commit message will be the slowest part of that
audit. The narrative for each is in the commit messages and in this file; what is missing is the
per-requirement traceability an auditor needs.

**Do not backfill these from memory.** Reconstruct each from its commit and its tests, the way
`remote-phaseB-s14a-evidence.md` was written, or the evidence file becomes a plausible-sounding
record of what was intended rather than what shipped — standing class (ii), applied to documentation.

## Open items carried forward

- **PB-PAIR-1 needs an evidenced manual scan** under `docs/verification/` — a real phone
  camera reading the symbol off a real terminal. No test can supply it. Lower risk after the
  row-budget fix (quiet zone 3 at 24 rows, 4 at 25+), but still the check that matters. The
  encoder always uses mask 0 and every pairing mints fresh random material, so the reviewer
  recommends re-running the out-of-band decode over ~1000 random payloads, not one.
- **The relay URL ceiling is enforced at WRITE time only.** A `relay.json` written by hand or
  before this change is loaded as-is; `swarm remote pair` then degrades with the now-accurate
  "shorten the relay URL" message. Refusing at load would brick an existing config on upgrade.
- **S8 must NOT reimplement `LaunchContentHash`** in the facade. It stayed in
  `internal/protocol` (Go has no function aliases). Reimplementing its canonical encoding
  would produce silent signature failures with no compile error. Options are: move it then,
  or expose it through the facade. See `remote-phaseB-s1-evidence.md`.
- **The offline op queue is safe only by accident of design, and nothing pins it** (found by the
  S7b implementer, recorded before it can bite). Now that PB-GW-2 enforces a 10-minute inbound
  age bound, a phone backgrounded past ten minutes would have **every queued mutating op refused
  as stale** if the queue held *sealed* envelopes. It does not: `OpQueue` stores unsealed
  `QueuedOp` (`internal/phonecore/opqueue.go:20-25`) and seals at replay time, so `IssuedAt` is
  stamped **on send, not on enqueue**. That ordering is what makes offline replay work at all
  under the bound -- and no test asserts it. A future refactor that sealed at enqueue would
  silently brick offline replay, and the failure would look exactly like the PB-GW-6 brick this
  slice was created to avoid. Wants a test pinning seal-at-send, against S7/PB-STATE.
- **PB-GW-2 is inbound-only; the phone's receiver still runs `maxAge == 0`.** A real asymmetry,
  deliberately out of S7b's scope. Closing it is blocked on the PB-TIME-2 reply-seal gap above
  (`SealControlReply` stamps no `IssuedAt`), so the two must be done together or the phone
  bricks on its own command replies.
- **Pre-existing `gofmt` drift, NOT gated and NOT introduced by Phase B**: 10 files are listed by
  `gofmt -l`, including two production files (`internal/remotegw/command_in.go`,
  `internal/remotegw/terminal_watcher.go`); the rest are older test files under
  `internal/protocol`, `internal/remote/device`, `internal/remote/pairing`, `internal/skeleton`.
  All are unmodified since HEAD. GG-4's gate is build/vet/test/`golangci-lint`, and the `gofmt`
  linter is not enabled in the golangci config, so this fails no gate today — `golangci-lint run
  ./...` is clean apart from slice S6b's declared build-RED. Recorded so the final audit does not
  mistake it for Phase B breakage; cleaning it is unrelated hygiene and deliberately out of scope.
- **PHASE A, found by the S6b reviewer: re-authenticating an ALREADY-AUTHENTICATED relay socket
  under a different key is a legal frame sequence.** Neither `handleAuthInit`
  (`internal/remote/relay/server.go:615`) nor `handleAuthResp` (`:658`) checks `sc.authed`, so a
  client may authenticate as A and then re-authenticate as B on the same connection.
  `registerSession` (`:689`) rewrites `sc.rid` in place. Phase B's exposure through this
  (S6b's wait-slot leak, where `s.waits[rid]` was keyed on the *current* rid and could never be
  reclaimed) is fixed in S6b by capturing the rid at registration. The remaining Phase A surface
  is `s.sessions`, which the reviewer verified **does** self-heal because `registerSession`
  overwrites the key -- so this is recorded, not urgent. Worth deciding deliberately whether
  re-auth on an authed connection should be refused outright: it is state a client can rewrite
  after passing the gate, and the next feature keyed on `sc.rid` inherits the same trap. NOT in
  Phase B's scope; do not fix it inside a Phase B slice without an ADR.
- **The daemon's `OpLease` grant carries no `ExpiresAt`** (found by the S11 RED author).
  `internal/protocol/server.go:620-626` encodes Op/EndpointID/SessionID/Generation/SnapshotLen,
  while the lease deadline the daemon actually enforces is the earliest of three bounds
  (`:1500-1533`). So the phone **cannot observe the authoritative expiry**. S11 takes the
  machine's value when a confirmation carries one and otherwise falls back to the horizon the
  phone signed -- an upper bound on the truth, so the phone can only ever believe the lease ends
  *later* than it does, never earlier, and the severance notice (not a countdown) stays the
  authority. Changing a daemon reply was outside S11's fence, so the precedence is pinned by test
  now (`Lease_TheMachinesExpiryWinsOverThePhonesSignedHorizon`) to stop a later slice wiring the
  real value through and having it silently ignored. Wants a follow-up.
- **The load-sensitive test family.** On a QUIET machine the full suite is green at HEAD — verified
  in a pristine clone. These fail only under concurrent agent load, which the orchestration itself
  creates, and pass in isolation. **Do not dismiss a real regression as one of these: re-run in
  isolation before concluding.**
  - `internal/tui TestRemotePeek_LargeGridClippedUnderMaxFrame` (predates Phase B)
  - `internal/remotegw TestS6B_GatewayInputLatencyIsNotPollGated`
  - `cmd/swarm TestTUI_OpensAndRestoresOverPTY`
  - `mobile/conformance TestPBBIND6_SlowCallbackDoesNotStallTheCore` (confirmed failing identically
    at the parent commit)
  - `internal/tui TestFirstPaintGate_RealDaemon_FiftySessions_P95`
  - `internal/skeleton` and `internal/protocol TestFix_SupersedeReAttachRealShim` — the latter
    spawns real shim processes, so it is the most load-sensitive thing in the repo
  - `relay TestPresence_TransitionsAndSilentPush` under `-race -count=2` — the SAME unsynchronised
    shape as the sweep-loop test fixed in `3dfbab7`: it closes the client and immediately advances
    the fake clock, with nothing waiting for the server's `removeConn` to set `connected = false`.
    Attributed both directions (fails at the same rate with the push-delivery split reverted), so it
    is pre-existing and exposed by load, not caused by it. **Fixable the same way** — wait for the
    server to observe the disconnect — and it will keep biting `-count=N` runs until someone does.
  - `internal/shim TestSocket_AttachDeliversSnapshot` — attributed with a dependency proof rather
    than assumed: `go list -deps ./internal/shim` has **zero** references to `phonecore` or
    `swarm/mobile`, so the phone work cannot reach it
  - `mobile/conformance TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT` — a **cleanup** failure
    (`TempDir RemoveAll: directory not empty`) from harness goroutines writing during teardown; the
    test body passes. Attributed properly rather than assumed: reproduced identically in a
    `git archive` of the parent commit, 1 of 5 loaded runs, 3/3 clean quiet on both sides.
- **`relay TestRelay_SweepLoopPresenceSilentPushNoManualCall` was NOT a flake and is FIXED**
  (`3dfbab7`). It raced its own setup: `removeConn` stamps `disconnectedAt` on the server goroutine
  after `Close` returns client-side, so advancing the fake clock first wrote the already-advanced
  time into it, and since the clock never moves again the elapsed comparison stayed 0 **forever**. A
  permanent wedge, not a slow machine — which is why it passed whole-package runs and failed under
  `-run` filtering, a trap for anyone bisecting. It now waits for the server to observe the
  disconnect. No assertion changed.
- The final full-committee audit against all **142** requirements is still owed, per the goal.
