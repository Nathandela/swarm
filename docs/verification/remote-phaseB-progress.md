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

28 of 140 shipped, 112 remaining (9 of 27 slices). The completed slices were deliberately the
blockers and the security-critical machine-side work -- dependency surgery, gateway durability
in both directions, the transport, the reconciliation frame -- because they gate everything
downstream. The remaining 113 are weighted toward the Android app and end-to-end verification.

## Slice status

| Slice | Requirements | State |
|---|---|---|
| S1 dependency-edge surgery | PB-BIND-0 | **SHIPPED** (`0024595`) — closure 52 -> 18 non-stdlib, zero forbidden |
| S2 gateway inbound durability | PB-GW-1, 3, 4 | **SHIPPED** (`f98b9a9`) -- inbound high-water + cursor now durable and identity-bound |
| S0 ADR amendment | PB-DOC-1 | **SHIPPED** (`6cdc164`) -- 14 Phase B decisions recorded |
| S5 design tokens | PB-TOK-1/2/3 | **SHIPPED** (`638b61b`) — Substrate pinned, drift-guarded |
| S3 QR renderer + payload | PB-PAIR-1, PB-PAIR-7 | **SHIPPED** (`20be9b2`) -- real symbol + relay URL; 39-char URL ceiling enforced; manual scan still owed |
| S0, S2, S2b, S4 | ADR decisions, gateway durability, supervision | **next** -- all parallel roots, startable immediately |
| S1b protocol additions | PB-SYNC-7 | **SHIPPED** (`689c8e8`) -- reconcile frame, lease confirmation, reply correlation |
| S6 transport resilience | PB-NET-2,3,4,6,7 | **SHIPPED** (`078ac63`) -- cleartext-via-redirect hole found by review and closed |
| S2b gateway outbound durability | PB-GW-7, PB-GW-8 | **SHIPPED** (`5aaacef`) -- live tail was refusing 81% of appends; coalescing + outbox |
| S7 durable phone state | PB-STATE-*, PB-GW-6 | **SHIPPED** (`0ac4fb9`) -- the phone now survives a process kill; was the most severe committee finding |
| S8 gomobile facade | PB-BIND-*, PB-SAS-1/2 | in progress (RED) -- the contract the Android app is built on |
| S6b low-latency input path | PB-NET-5 | not started (split out of S6) |
| S7..S21 | see §11 of the spec | not started |

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
- **Known pre-existing flake**: `TestRemotePeek_LargeGridClippedUnderMaxFrame` (i/o timeout
  under full-suite load; passes isolated). Predates Phase B.
- The final full-committee audit against all 139 requirements is still owed, per the goal.
