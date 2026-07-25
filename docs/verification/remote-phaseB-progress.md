# Phase B progress and handoff

**Branch**: `worktree-remote-control-research`. **Spec**: `docs/specifications/remote-phaseB-requirements.md` (v3.5.1, **143 requirements** -- the count has risen four times mid-implementation, each time because someone found a hole that would otherwise have shipped; see below).
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

### `--` IS ILLEGAL INSIDE ANDROID RESOURCE XML COMMENTS, AND THIS PROJECT'S PROSE STYLE USES IT

A double hyphen may not appear **anywhere** inside an XML comment, per the XML spec, and AAPT2
rejects the entire file rather than warning. One occurrence in a `res/values/*.xml` comment fails
`:app:mergeDebugResources`, which blocks **every** Gradle task for the module — including
`:app:testDebugUnitTest`, so no Kotlin test in the module can run at all.

This is a live hazard here rather than a curiosity: the house style uses `--` heavily in Go comments,
commit messages and these documents, where it is perfectly fine, so the habit transfers straight into
a total build break the first time someone writes a thorough comment in a resource file. It happened
exactly that way during S16.

Colons or parentheses read the same. If a Gradle task fails somewhere that looks unrelated to the
change, parse the resource XML before believing anything else:

```sh
for f in $(find android -name '*.xml' -not -path '*/build/*'); do
  python3 -c "import xml.dom.minidom,sys; xml.dom.minidom.parse('$f')" || echo "BROKEN: $f"
done
```

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

**117 of 143 shipped, 26 remaining (23 of 29 slices).** Counted from the manifest, not estimated.
The completed slices were deliberately the blockers and the security-critical machine-side work --
dependency surgery, gateway durability in both directions, the transport, the reconciliation frame,
the bound facade, the custody seam, the push transport -- because they gate everything downstream.
The remaining work is weighted toward the Android app (S14-S17), the resync machinery (S10), and
end-to-end verification (S18-S21).

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
| **S14a Go custody seam** | **PB-KEY-9** (half) | **SHIPPED** (`582676e`), re-audited through **four rounds** (`3dfbab7`, `010876a`, `47809f0`, `4d8a37d`). Round 2 found an **unauthenticated key-adoption path** that bypassed the binding round 1 added; round 3 found three fences that could not fail. **Closed**; S14 unblocked and now delivered |
| **S14 Android key custody** | PB-KEY-1/2/5/6/7/8, PB-SEC-1/2, **PB-KEY-9** (closing half) | **GREEN, awaiting review** -- 77 `@Test` green; the facade `KeyCustody` verb landed, the golden was regenerated, **both `InsecureCleartextSealer` call sites are gone** and the fence + this document were retired with them. Also closes the sentinel->Kotlin mapping and the dial-refusal behaviour |
| S12 push transport | PB-PUSH-0/1/2/3/5/6/7/8/10 | **SHIPPED** (`2cb9b13`) -- 65 tests. ADR-007 B19/B20 decided the wake-key crossing and the payload disclosure. Found `internal/remote/push` had **zero production callers**: a fully-tested sender no binary installed |
| S9 façade drives the real client | PB-NET-1 | **SHIPPED** (`b31c56b`) -- the first test pairing a FRESH install found that a fresh phone never learned its machine id, so every mutating verb failed and the first process death discarded the whole durable blob |
| S10 staleness, repair, grant delivery | PB-SYNC-*, PB-KEY-3/4/10 | **SHIPPED** (`fcd7d7a`), re-audited (`03f87f0`). PB-KEY-10 was a hole in the REQUIREMENTS: the phone could not obtain an epoch key at all. Round 2 found a locked handset acking its own key so the relay deleted it |
| S15 state tier split | PB-STATE-9, PB-STATE-6, PB-SEC-10 | **SHIPPED** (`82eb7e6`) -- decrypted session content and the typed command line were on disk in the clear. PB-STATE-9 was unimplementable three ways; the content tier needs TWO containers, since a locked Save must preserve what the purge must destroy |
| **S16 the phone app** | PB-APP-1..10, PB-PAIR-2..6, PB-SAS-3, PB-TOK-1 | **SHIPPED** (`3a6d1cd`), reviewed **SHIP**. Largest slice in the phase. A revoked handset previously redialled forever behind a spinner; the Android key store was an in-memory map, so the key vanished on process death and both sealed files became permanently unopenable |
| S17 phone push client | PB-PUSH-4, PB-PUSH-9 | **GREEN in flight**. Its RED found three shipped defects in the token lifecycle and a vacuous acceptance criterion in PB-PUSH-9 itself |
| S18b unbrick recovery | PB-STATE-10 | **GREEN in flight**. Its RED found revoke and re-pair MUTUALLY EXCLUSIVE -- the relay ban is never cleared and the phone's id is per-install, so revoking to unstick a phone bricked it harder. Decided in ADR-007 B22 |
| S18, S19, S20, S21 | see §11 of the spec | not started -- briefs written, `docs/verification/slice-briefs/` |

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

## THE DIFFERENT-MACHINE GUARD NEEDS AN ABSENCE CASE -- carry this into S16's evidence

Contributed by the S16 RED author and NOT in the amended requirement row, which would have been
wrong without it: **you can only detect "different" against something you pinned.** A phone with no
pinned `MachineStatic` must therefore PROCEED, not refuse.

That matters concretely because `harness.seedState` never sets that field, so every seeded-harness
phone in the suite has nothing to compare against. A guard keyed on "is this phone paired at all"
would refuse them, which is the same mistake as the retired fail-fast one layer down. It is also a
second, independent reason the revoke-then-re-pair flow survives the guard.

Two more properties from the same author, both load-bearing:

- **Assert non-re-pinning by where the next command LANDS**, not by reading a state field. The send
  target derives from `MachineRelayAuthPub`, so whose mailbox the next command reaches *is* the
  question, and "abandons machine A" is the actual defect. A state assertion proves less.
- **The check lands at Noise msg2, before a channel binding exists**, so a refusing phone never
  displays a SAS. A driver built on "wait for the SAS" would report the refusal as "the phone never
  derived a SAS" — the exact misreporting shape of the latent pairing race recorded above.

## "HAS AN EPOCH" IS NOT A PROXY FOR "HAS AN EPOCH KEY" -- it was wrong in two tests, one of them green

`pin()` sets `State.EpochID` from the machine's **payload**, the instant the handshake completes
(`mobile/pairing.go:591`). The epoch key arrives later, in a signed grant on the mailbox. So a test
that waits on `EpochID != 0` as a proxy for "the phone is ready" is satisfied by the **pairing**, not
by the grant, and its probe then runs against a phone with an epoch and no key.

It was wrong in two S16 tests. One failed visibly; **the other was passing by luck**, because the
flow that followed purged the keys anyway, so the wrong precondition never surfaced.

**What actually proves a grant was committed is the relay cursor moving** — a coordinate the grant
advances, rather than one the pin advances.

This generalises past those two tests: any wait that approximates a durable outcome with a
coordinate written earlier in the same flow is a precondition that can be satisfied by the wrong
event. Prefer asserting the fact the test needs — here, that the phone can actually reach the
machine — over approximating it.

## THREE GUARDS THAT COULD NOT FAIL, IN ONE FENCE FILE -- and the third was the security one

The clearest instance of this project's most-repeated defect, found in S17's gate file, and worth
keeping because all three shared ONE root cause that reads as correct.

**gobind lowercases the first letter when it emits the Java binding.** The generated
`swarmmobile/App.java` declares `registerPushToken`, `handlePushWake`, `deletePushToken`;
`Swarmmobile.java` declares `newApp`. So **no correct Kotlin call site can contain the Go-cased
name** -- and a fence matching `RegisterPushToken(` is unsatisfiable by any correct implementation.

1. **Five assertions were unsatisfiable.** The proof was that `TestS17_TheAppTheServiceUsesIsTheProductionOne`
   failed against S16's **shipped and correct** wiring. S16's own gate had already met this and
   accepts both spellings; the precedent existed and was not carried across.
2. **A body scanner was off by one.** Its declaration regexp ends with `\(`, so the offset was
   already inside the parameter list, while the depth counter started at zero -- the outer close
   drove it to -1 and the break could never fire. It returned **no body** for six functions and a
   **wrong** body (short by the parameter list) for the rest, so three more assertions reported
   "nothing to walk" regardless of the implementation.
3. **The security guard could not fire at all**, and this is the one that matters. The
   forbidden-verb map in the no-content-verb check matched Go casing too. That check is not a wiring
   convention -- it carries PB-PUSH-4's actual property, that **a wake callback never fetches
   content while no user is present**. It would have passed forever.

The third was not reported by the implementer; it was found while fixing the other two. **After
fixing it I mutated the Kotlin to call a content verb from the wake callback, watched it report the
violation, and reverted** -- because a guard just restored from "cannot fail" must be shown to fail.

**A second lesson, in the implementer's own words, and it is the sharper one.** It *did* name the
forbidden-verb map -- but filed it as a **consequence** of the casing bug rather than as the finding,
so it arrived third in a list rather than first. Its correction:

> "Rank a broken guard by what it was guarding, not by which root cause it shares."

Two of those three assertions were wiring conventions; one carried the requirement's security
property. Sharing a root cause made them look like one finding of equal parts, and a reader
triaging by root cause would have fixed all three and never noticed that one of them mattered
differently from the other two.

**The generalisable lesson**: when a fence matches a NAME across a language boundary, the boundary
may rewrite the name. A cross-language fence should assert on both spellings or on the resolved
symbol, never on the source-language spelling alone -- and one instance of that mistake in a file
implies the file has more.

## KOTLIN COMPILES THE WHOLE TEST SOURCE SET, SO ONE SLICE'S RED BLOCKS EVERY OTHER SLICE'S TESTS

Found during S16. `:app:testDebugUnitTest` cannot run while **any** test file in the module fails to
compile, because Kotlin compiles the entire test source set as a unit. So three unimplemented RED
files in `keys/`, `push/` and `theme/` blocked all 52 finished tests in `ui/` — a different package,
fully green, with zero errors of its own.

This is not the Go behaviour and it changes how Android slices can be sequenced. It means:

- **The Kotlin halves of S16 and S17 cannot be verified independently**, on top of the two-way
  coupling already recorded below. A Kotlin RED authored by one slice blocks the other's GREEN from
  being demonstrated at all.
- **A green Gradle run is not available as a per-slice signal** while any sibling RED is outstanding,
  so "the module's tests pass" is a statement about the last slice to land, not about each one.

The workaround that produced a real result here was to compile and run the finished package on a
standalone JVM (`kotlinc` + `android.jar` + the AAR's `classes.jar` + JUnit) — which is worth
knowing, but is a verification path outside the build system and should be reported as such rather
than quoted as a Gradle result.

## S16 AND S17 MUST BE VERIFIED TOGETHER, NOT SEQUENTIALLY

Raised by the S17 RED author and it changes how these two close. They are coupled in **both**
directions:

- **S17 depends on S16's Android wiring.** Nothing in production Kotlin constructs the bound app, so
  a push service has no phone to call into. `TestS17_TheAppTheServiceUsesIsTheProductionOne` cannot
  go green until S16 lands it — and a slice that shipped a messaging service talking to an app nobody
  builds would satisfy every other test in its file while push did not exist on a handset.
- **S16 depends on S17 for one of its own states.** PB-APP-10's new waiting state is *rendered* by
  S16 but is most reachable through S17's wake path: a phone paired minutes ago and backgrounded
  before its first grant landed has no key, no user present and nothing on screen. From inside the
  wake handler that is byte-for-byte indistinguishable from permanent grant loss, and the two have
  **opposite remedies** — this one self-heals because the gateway re-appends its sidecar every
  session, while the terminal one's remedy is re-pairing, which the user cannot perform at all
  because pairing fail-fasts while the device is still registered.

So neither may be declared green alone, and a sequential verification would report each as passing on
the strength of a stub the other owes.

**A product distinction worth keeping from the same author**: a content tier refusing DURING A WAKE
is not an error at all — it is a flag on the returned alert. A locked phone is the **normal** case
for a push wake, not a failure, and classing it as an error would put a failure state on screen for
every single wake the product delivers.

## WHY THE ROLES ARE INDEPENDENT -- the one time it slipped, and what it cost

In S16 the test author and the implementer settled a disagreement about a RED test **directly**, and
the author edited it. On the merits both were right: the test asserted something no correct
implementation could satisfy, because it demanded S16 invent a detection rule for PB-KEY-3, which
**S10 owns**. I verified that independently and ruled the edit stands.

It should still have come to me, and the author's own account of why is better than mine:

> "S16-green is not the party who can authorise a change to the fence they are being measured
> against, and 'is this a correction or a softening?' is precisely the question the independence
> exists to stop the test's author answering alone."

**A process that only works when the author judges correctly is not a process.** Good faith was
present on both sides and the merits were on their side, and the rule still holds — because the
route through the orchestrator is what makes a third outcome available. Twice this session an agent
pushed back on a test, routed it to me, and the result was that **the requirement was amended**: the
test was right and the requirement was wrong. That outcome does not exist inside a two-agent
agreement.

It produced a third amendment here too, and only because the author raised the leftover as a
question rather than restoring it as a fixture: correcting the setup left a real state — paired,
keyless, **not** terminal — covered by nothing, so PB-APP-10 gained a transient waiting state
distinct from permanent loss. Without it, the first-pairing window and a lost key are
indistinguishable on screen.

**Standing rule, restated**: when an implementer believes a RED test is wrong, they report the claim
and their verification and keep working around it; the author changes nothing until the orchestrator
rules. "This test is wrong" and "this requirement is incomplete" are different findings and only one
of them is settled by editing a test.

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

## PB-KEY-9 IS DELIVERED -- the shipped app no longer writes key material in the clear

**RETIRED BY S14, and the retirement was forced rather than remembered.** This section used to
read "THE SHIPPED APP STILL WRITES THE CONTENT KEY IN THE CLEAR". It was held by a fence that
named it:
`TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites` required the set of files calling
`phonecore.InsecureCleartextSealer` to equal exactly `{mobile/app.go,
mobile/conformance/harness_test.go}`, and its failure message said that a SHORTER list meant S14
had landed the facade verb and that "this very section is stale". S14 deleted both call sites,
the fence went red on purpose, and this is the reckoning it demanded. The fence was retargeted to
a floor of ZERO and renamed `TestS14A_TheCleartextSealerHasNoCallSitesLeft`; it was not deleted
and its list was not widened.

**What closed it.** `swarmmobile.NewApp` now takes a second argument, `KeyCustody` -- a
REVERSE-BOUND interface the Android app implements over the Android Keystore, with one method per
PB-KEY-2 tier returning that tier's transient data key. `NewApp` derives one `custodySealer` per
tier from it (AES-256-GCM, key fetched per operation and zeroized immediately), and there is no
second constructor that omits it: a nil `KeyCustody` is a refusal, so cleartext custody is not
reachable by forgetting anything. The golden was regenerated as a reviewed change --
`func NewApp(*Config, KeyCustody)`, `type KeyCustody interface`, two `ifacemethod` lines and two
`const` verdict tokens.

**ADR-007 B8 still holds, and the direction is the subtle part.** On a reverse-bound interface Go
is the CALLER, so a result travels Java -> Go (inbound -- B8's single permitted crossing) and a
parameter travels Go -> Java (outbound). `KeyCustody` therefore returns `[]byte` and accepts none.
The shape that looks natural and is wrong is a reverse-bound `Seal`/`Open` pair: sealing needs the
PLAINTEXT device scalars, so `Seal(plaintext []byte)` would hand Java the three content-tier
private keys. `TestS14_TheCustodySeamIsInboundOnly` fences that -- PB-BIND-4's own guard cannot,
because its `entryPoints()` covers funcs and methods only and an `ifacemethod` is invisible to it.

**What now carries the property, and what does not.** The `android/gate` PB-SEC-1 pair still
drives `phonecore.Resume` directly with sealers injected from Go: a path the Android app cannot
take. `TestS14_TheShippedFacadeSealsBothTiersUnderTheInjectedKEK` is the addition that goes
through `swarmmobile.NewApp` and `App.InstallWakeKey`/`InstallContentKey` -- the constructor the
app uses and B8's single inbound crossing -- and then reads the bytes of `device.key` and
`phone-state.json`. It fails against a facade put back on the cleartext sealer, and it names the
defect directly (a blob no longer than its plaintext carries neither nonce nor tag).

**DO NOT read a green byte search as "no cleartext key material".** S14a's round-3 correction
stands and is repeated in the new test's own comment: base64 encodes three bytes at a time, so a
32-byte needle's encoding appears inside a longer field's encoding only when it happens to be
3-byte aligned and terminal. Under a leak of all four device privates as one base64 field the
search catches exactly one of them. What carries the property is the POSITIVE half -- that the
material went through the injected sealer and comes back only under it.

## THE PHONE'S CUSTODY-REFUSAL BEHAVIOUR -- CLOSED by S14

Found by main while verifying the S14a cross-model review finding, and recorded as "a defect that
would ship WITH S14". It did not: S14 closed it in the same change that made it reachable.

`mobile/relay.go` discarded the dial error outright:

```go
cl, err := a.dial(ctx)
if err != nil {
	continue
}
```

`mobile/relay.go` wires `Sign: ks.SignRelayAuth`, and `cmd/swarm-remote/config.go` records that
the machine identity never refuses -- so the phone is the ONLY production caller of
`relay.ClientAuth.Sign` that can fail. That was unreachable while the app ran on the software
keystore, and went live the moment PB-KEY-9's Keystore-backed KEK landed.

**What it does now**, in `App.ConnectionState` terms:

- `crypto.ErrKeyAuthRequired` (RECOVERABLE) -> `reauth_required`, and the state PERSISTS across
  retries rather than being overwritten by `reconnecting` at the top of the next iteration. The
  loop keeps dialing, because the biometric may be satisfied at any moment and the retry is what
  notices; the first successful dial clears it by setting `online`.
- `crypto.ErrKeyInvalidated` (PERMANENT) -> `repair_required`, and the loop RETURNS. Returning
  rather than breaking is deliberate: `break` falls through to `setConn("offline")` and erases
  the one state that tells the user to pair again. Retrying a destroyed key is a websocket
  handshake every 250 ms, forever, on a battery, against the relay's per-source ops budget.

**Both tests fail against the old `continue`**, verbatim: `left the phone reporting
"reconnecting"`. They inject the refusal at the KEK rather than at the signature, so the
assertion covers the whole chain -- `KeyCustody` -> `custodySealer` -> `SignRelayAuth` ->
`relay.ClientAuth.Sign` -> `relay.Dial` -> the transport loop -- over a real relay and a real
handshake. The terminal one observes the loop STOPPING by counting wake-tier unwraps across two
windows, which is the half the old code could never satisfy.

**Known consequence, stated rather than discovered later**: after `repair_required` the run
goroutine has returned but `a.sess` is still set, so `Start` is a no-op until `Stop` is called.
That is the correct end state for a device whose key is gone -- recovery is a re-pair, which
builds a new App -- but it is a behaviour, not an accident.

The narrower sibling finding -- `relay/client.go` handling the refusal correctly but
unfalsifiably -- was closed under S14a and is not this item.

## THE PHONE CAN NEVER OBTAIN AN EPOCH KEY -- and no requirement owned it until now

**This is the most severe finding of Phase B, and it is a hole in MY requirements set**, not merely
in the code. 142 requirements, validated by a full audit committee, and none of them owned the step
without which nothing else can work. Now **PB-KEY-10**, owned by S10 (143 requirements).

Found by a read-only fixture-seeding audit I commissioned after the machine-id defect, on the theory
that its cause would generalise. It did, and worse than expected.

**The chain, verified independently before amending anything:**

- `State.Keys` is written ONLY by `InstallWakeKey`/`InstallContentKey` (`mobile/app.go:369,386`) --
  inbound verbs called from Kotlin. **Nothing supplies those bytes.**
- The machine delivers the epoch key as a sealed `crypto.EpochGrant` in a tagged bootstrap frame
  appended to the phone's mailbox (`cmd/swarm-remote/deliver.go:29-40`).
- `phonecore.AcceptGrant` has exactly **one** production caller: `internal/phonesim` -- the test
  simulator. `mobile/` never calls it.
- `MailboxRouter.TakeGrant` has **zero** production callers. Its own comment says "route+expose
  only" and defers consumption to a work item ("C5") that was never done. `Core.Grants()` has zero
  callers anywhere.
- Kotlin cannot supply the bytes either: the custody surface is inbound-only by design (B8) and the
  golden-pinned facade has **no verb that ingests a grant**.

**On a real handset**: `resolveSend` returns `errNoContentKey` for every send, every inbound frame
fails to open, the relay cursor never advances, and the drain polls the same page forever. The
product does not function at all.

**Why the entire suite is blind, and this is the part worth internalising:**

- No test in `mobile/` calls `App.BeginPairing`.
- The conformance harness says out loud that it seeds durable state "rather than running a pairing
  handshake" (`harness_test.go:88-90`).
- **Even the PB-NET-1 real-wire test does not catch it.** It generates the epoch keys in-test and
  hands the content key to `InstallContentKey`. The "no fakes" test performs BY HAND the exact step
  the facade is missing -- and it was written by the agent that had just found the machine-id
  defect, in the slice created to close this very class.

That is the lesson: a test can be built entirely from real components and still paper over a missing
production path, because supplying the missing input is the most natural thing in the world when you
are setting up a test. **Standing class (v) survives even a no-fakes integration test unless someone
asks where each input comes from in production.**

### What phonesim skips, precisely -- the list of properties with NO end-to-end coverage

From the same audit, and it should shape every remaining brief and especially **S19's exit
demonstration**. `internal/phonesim` is the phone stand-in that nearly every integration test drives,
and **it never constructs a `phonecore.Core` at all** — so everything Core owns is skipped by
construction, not by omission:

- **Durable state entirely**: no `Store`, no `State`, no `Save`.
- **Durable send-seq reservation (PB-STATE-3)**: `p.seq` is a bare `Sequencer` driven by `Next()`,
  the NON-durable allocator — not `NextCommand`/`NextInput`, which the facade uses. The burned-block
  and gap-absorption rules are never exercised end-to-end.
- **Durable receive transaction**: core-less `Accept`, not `AcceptCommit` — no `commitReceive`, no
  durable high-water, no relay cursor, no persisted stale flags.
- **The lease gate (PB-INPUT-2)**: no `LeaseState` at all. `Type`/`Resize` seal and append with no
  `Leases().Require`, so keystrokes go out with no confirmed lease.
- **Input coalescing (PB-INPUT-5)**: no `InputCoalescer`; one append per call.
- **The reconcile fail-closed gate (PB-SYNC-7)**: no `RequireReconciled` before mutating ops.
- **Skew (PB-TIME-3), the op queue, and the undelivered ledger (PB-INPUT-1)**.
- **The machine id**: taken from phonesim's own `Config.Machine`, never from `State` — which is
  precisely why it could not have caught the fresh-install defect.

Every one of those belongs to a slice that has already SHIPPED. They have unit coverage; they have
no end-to-end coverage anywhere. **That is what S19 must actually demonstrate**, and it is why an
exit demonstration driven through phonesim would be worth very little.

## TWO CRITERIA THAT ARE EASY TO OVER-READ FROM A GREEN GATE

Both found by the evidence backfill, both disclosed in-file rather than hidden, both worth knowing
before the final audit reads a green suite as proof.

1. **PB-NET-5's headline latency number comes from a test that SKIPS by default.**
   `TestS6B_InputLatencyPhoneTypeToPTYWrite` is gated on `SWARM_S6B_LATENCY=1`, so a green
   `go test ./...` says nothing about the budget. The gating is deliberate and well-argued (an env
   var rather than a build tag, so the four untagged gates still compile, vet and lint it). Combined
   with the separately recorded fact that the harness measures phonecore-to-PTY rather than
   phone-to-PTY, **this is the requirement easiest to over-read in the whole phase.** Re-run at HEAD:
   PASS, p50 36.4 ms against a 150 ms budget, with the Rosetta status correctly recorded in-process.
2. **PB-BIND-1's literal criterion skips in the default gate.**
   `TestPBBIND1_GomobileBindProducesAnAAR` skips when `ANDROID_HOME` is unset, which is the normal
   state for a plain `go test ./...` — only the Android lane sources the pin. The property IS
   covered, by S13's one-command-produces-an-AAR test, which was rebuilt and passed at HEAD. But an
   auditor reading S8's green gate alone would over-read it.

## A TEST BUILT TO AVOID SELF-CERTIFICATION THAT SELF-CERTIFIED ONE LEVEL UP

Found by the S16 RED author while fencing PB-TOK-1, and it explains why that requirement survived a
slice boundary: **the palette is stated a THIRD time.** `SwarmTheme.EXPECTED_DARK_COLORS` holds the
same literals, and its own doc says they exist so the theme test "compares the resolved theme against
a recorded number rather than against itself".

That instinct is exactly right and it was pointed at the wrong number. The constant was recorded
**from `colors.xml`**, so it certifies that the app renders what `colors.xml` says — which is what it
would do if `colors.xml` were wrong. Two files disagreeing with a third file that agrees with one of
them reads, from any single test, as consistency.

The fix is a checked-in join table both the Go gate and the Kotlin test read, enforced in **both**
directions (a row naming a missing token fails; a colour with no row fails — which is how "single
origin" decays into "origin plus a few extras"), plus a requirement that every colour literal in the
theme source be a mapped token's value: derive it from the origin or delete it.

**And the "must fail when they diverge" half is executed rather than asserted in prose**: a control
mutates the comparator by one hex digit each run and requires the difference to be reported. A
converter that over-normalised — dropping alpha, folding case, accepting short hex — passes every
value assertion and fails only that one.

## PB-TOK-1 WAS MARKED SHIPPED AND IS NOT MET -- reassigned S5 -> S16

Found by the evidence backfill, verified by me. The requirement is that one JSON token source is the
**single origin for the Android theme**. There is no join in either direction — nothing outside
`internal/design/` references `tokens.json` at all — and **the values disagree**:
`--p-bg` `#08090a` vs `swarm_background` `#FF101114`, `--p-ink` `#f7f8f8` vs `swarm_text_primary`
`#FFE6E8EB`.

S5 delivered the JSON and drift-guarded it against the design source. That is real, and it is half
the requirement. The other half is only satisfiable where the Android theme lives, so PB-TOK-1 now
belongs to **S16**, which owns the screens and has not started.

It was **disclosed, not hidden** — `colors.xml`'s own comment calls its values "placeholders for the
skeleton", and S13 said it shipped a skeleton. But nobody held the join, and the traceability index
reported the requirement as shipped because its owning slice had shipped. **That is the failure mode
worth remembering: slice-level status silently answers a requirement-level question.**

## PB-PUSH-3's REPLAY WINDOW IS A FIELD, NOT A MECHANISM -- a gap in SHIPPED work

Found by the S15 RED author while measuring the at-rest inventory, verified independently:
**`State.WakeReplay` has no producer.** Outside `state.go`'s own persist/merge/load plumbing, nothing
in `internal/` or `mobile/` ever writes it.

Section 6.0 requires a "Push envelope TTL / replay window | 10 min, with the replay coordinate
persisted per PB-STATE-1" against PB-PUSH-3, which slice **S12 shipped**. The coordinate is
persisted; nothing advances it. So a wake envelope replayed by the relay -- the declared adversary,
and the party that necessarily handles every wake -- is not detected.

**Bounded, not severe**: the wake payload is content-free and a constant 78 bytes (ADR-007 B20), so a
replay costs a spurious reconnect and battery, not disclosure. But PB-PUSH-3 names the replay window
explicitly and the field exists to serve it, so this is an unmet requirement rather than a design
choice.

**Owner: S17**, which owns the phone's push client -- the receiver is where the check belongs. The
field is already sealed under the wake tier by S15, correctly, since the wake path must read it with
no user present.

## A DEAD OFFLINE OP QUEUE THAT READS AS A FEATURE -- decision needed

Same audit. `State.PendingOps` is persisted, restored, cloned and asserted to survive a restart --
and **`OpQueue.Enqueue` has zero production callers.** Nothing ever appends to it. An offline
mutating op is not queued: `sealSignedCommand` -> `sendContext` -> `awaitConn` waits 5 s and returns
an error, and the op is dropped. `StateSummary.PendingOps` reports `len(a.inflight)`
(`mobile/app.go:342,349`), never the durable queue, so the gap is invisible from the facade.

PB-NET-4 says only that idempotent ops **may** queue, so no requirement is violated -- which is
exactly why this needs a decision rather than a fix. Three options: require queuing (a new
requirement), delete the dead machinery, or record it as deliberate. **Leaving durable machinery
that production never fills is the worst of the three**, because `state_test.go` proves the blob
round-trips a queue nobody writes, and that reads as coverage.

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

## AFTER ANY CHERRY-PICK, RUN THE BUILD -- a clean textual merge is not a clean semantic one

S10 and S14 were implemented in parallel and cherry-picked with **zero textual conflicts**, because
they touched different files. The build then failed: S14 changed `swarmmobile.NewApp` to take a
second argument, and S10 had added a new test calling the old signature. Git cannot see that.

The generalisable form, in the S14 implementer's own words: it reported the conflict it could **see**
(the one file both slices edited) rather than the class of conflict it had **created**. **An arity or
signature change makes every caller a merge hazard regardless of which file it lives in**, including
callers added after the surveying grep was run.

So: `go build ./...` after every cherry-pick is **mandatory, not advisory**, and an agent whose change
alters a shared signature should say so as a class rather than enumerating today's call sites. This
cost one commit to fix (`c7acd7b`) and would have cost far more had the parallel slices been larger.

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
- **A LATENT PAIRING RACE that reports itself as the wrong failure.** Found by the S9 implementer
  while debugging its own scaffolding. It is NOT a load flake -- it is a real ordering bug that
  discards its own cause. `relay.handleRendezvousClaim` (`server.go:1200`) refuses a rendezvous id it
  has never seen, and `pairing.RunDevice` does **not retry** its claim. So a `BeginPairing` that
  beats the machine goroutine's `Create` fails the handshake **terminally** -- and the waiting test
  reports it five seconds later as "the phone never derived a SAS", with the actual cause thrown
  away. Reproduced 2 runs in 5 under concurrent agent load.
  **`mobile/conformance/s9_pbnet1_test.go` and `conformance_test.go`'s `runMachinePairing` both still
  have it.** The S9 implementer gated its own new test on the machine's `Create` having returned and
  made the SAS wait fail fast on a terminal pairing state; the other two were deliberately left
  alone. Anyone who sees "never derived a SAS" should suspect this before anything else.
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
  - `internal/remote/transport TestS6B_KeystrokeCompletesWhileFollowing` and `internal/remotegw
    TestS6B_GatewayInputLatencyIsNotPollGated` — **every latency-BUDGET test in this repo belongs
    here.** They are the ones measuring a wall-clock median, so a loaded box moves them by 30-50%%
    and a full-suite run is exactly that load. Both passed in isolation immediately after failing a
    `go test ./...` sweep.
  - `internal/skeleton` and `internal/protocol TestFix_SupersedeReAttachRealShim` — the latter
    spawns real shim processes, so it is the most load-sensitive thing in the repo
  - `relay TestPresence_TransitionsAndSilentPush` under `-race -count=2` — the SAME unsynchronised
    shape as the sweep-loop test fixed in `3dfbab7`: it closes the client and immediately advances
    the fake clock, with nothing waiting for the server's `removeConn` to set `connected = false`.
    Attributed both directions (fails at the same rate with the push-delivery split reverted), so it
    is pre-existing and exposed by load, not caused by it. **Fixable the same way** — wait for the
    server to observe the disconnect — and it will keep biting `-count=N` runs until someone does.
  - `mobile/conformance TestPBNET1_AFreshInstallsPairingSurvivesTheNextProcessStart` — one of the
    two tests still carrying the latent pairing race recorded above, so a full-suite failure here
    should be read as that race before anything else
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
