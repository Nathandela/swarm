# Phase B requirements — Android handset (the v1 milestone)

**Status**: v3.5, after audit-committee rounds 1-5. Rounds 1-4 returned REVISE from all three
reviewers; round 5 returned **requirements-complete** from opus, with codex's single blocking
edge and opus's late round-4 blockers integrated. Rounds 1 and 2 returned REVISE from
all three reviewers. **Round 3 disproved v3's own claim** that §4.6 was a live Phase A security
hole; the claim is withdrawn and the finding is now correctly scoped as a durability /
defense-in-depth defect with no reproduced exploit (PB-GW-*). Recording that retraction
prominently is deliberate: an unnecessary amendment to a committee-signed closure is its own harm.
**Date**: 2026-07-25.
**AMENDED 2026-07-31 (ADR-007 B133)** — the trust boundary moved to **the wire**, and all phone-side
user authentication is removed. §3's threat model, §6.0's two freshness rows and the op-queue
rationale, **PB-SEC-2 (VOID)**, PB-KEY-2/-7/-8, PB-APP-5/-7, PB-PUSH-4, PB-SEC-12, PB-E2E-5, §10 and
§13 are amended in place; PB-KEY-5, PB-KEY-6, PB-KEY-9, PB-STATE-9 and PB-INPUT-3 carry a **FLAG**
recording a disposition B133 did not take. Nothing is deleted: a VOID requirement stays visible so
its id is never reused.
**Binds**: the Phase B implementation. Refines `docs/research/remote-v1-roadmap.md` §"Phase B"
into testable requirements.
**Predecessor**: Phase A is closed (`docs/verification/remote-phaseA-committee-closure.md`,
committee-validated single-device v1 after seven rounds).

Every claim about existing code carries a `file:line` verified against the tree at `a2b6397`.
Round-1 reviewers falsified seven of v1's claims; all are corrected here and listed in §12.

---

## 1. The binding exit criterion

From the roadmap, verbatim:

> **Phase B exit:** your Android phone pairs, observes, launches, and types into a real
> session over the untrusted relay.

| Verb | Means | Status (verified) |
|---|---|---|
| **pairs** | QR -> Noise XXpsk0 -> SAS six-emoji compare on both screens -> enrolled, grant delivered | device driver EXISTS (`internal/remote/pairing/pairing.go:362`), SAS (`internal/remote/crypto/sas.go:58`), QR *decoder* (`internal/remote/pairing/qr.go:86`). **No QR ENCODER exists anywhere (§4.4). No UI. No bound surface.** |
| **observes** | roster, sessions in the four Groups, journal events, terminal peek | core EXISTS (`internal/phonecore`), driven only by `phonesim` from Go tests. |
| **launches** | submit a launch spec (builder + policy; live execution is Phase 2 per ADR-007) | `phonesim.DriveLaunch` (`internal/phonesim/phonesim.go:359`). |
| **types** | `take_control` lease -> sealed, seq-gated keystrokes | `TakeControl` (:404), `Type` (:436). **Breaks permanently after one Android process death (§4.3).** |
| **over the untrusted relay** | a real relay client on the phone | WebSocket client EXISTS (`internal/remote/relay/client.go`) but is unreachable from Android, not resilient, and structurally cannot support low-latency input (§4.5). |

---

## 2. Environment ground truth (verified by building, not assumed)

| Component | Version | Location |
|---|---|---|
| JDK | OpenJDK 17.0.20 (Homebrew `openjdk@17`) | `$(brew --prefix openjdk@17)/libexec/openjdk.jdk/Contents/Home` |
| Android cmdline-tools | 14742923 | `/usr/local/share/android-commandlinetools` |
| Android platform | android-35 | `$ANDROID_HOME/platforms` |
| Build tools | 35.0.0 | `$ANDROID_HOME/build-tools` |
| NDK | 27.2.12479018 | `$ANDROID_HOME/ndk` |
| gomobile / gobind | installed 2026-07-24 | `~/go/bin` |
| Gradle | 9.5.1 wrapper, checked in and pinned by distribution SHA-256 (PB-TOOL-4, closed by S13). 9.6.1 is what the host has; 9.6.0 removed an internal API AGP 8.x needs, and AGP 9 rejects the Kotlin Android plugin PB-TOOL-6 requires. | `android/gradle/wrapper` |
| Android Gradle Plugin / Kotlin | AGP 8.13.2, Kotlin 2.3.21 | `android/build.gradle.kts` |
| Emulator + AVD | `swarmtest`, Android 15, `google_apis/arm64-v8a`; boots headless in ~30 s, adb attaches | `$ANDROID_HOME/emulator` |
| Host CPU | Apple M1 (arm64) | — |
| Go | 1.26.1 toolchain (module declares `go 1.25.0`, `go.mod:3`) | system |
| | *Was `go 1.24.2` when this table was written. Raised as a consequence of the `golang.org/x/mobile` tool directive this same section mandates: `x/mobile`, `x/mod`, `x/sync`, `x/sys` and `x/tools` all declare 1.25.0, and a hand-reverted directive is not a fixpoint under any module command. Decision and CI/doc corrections recorded in **ADR-008**. Found stale by the S13 RED author, who noted it contradicts the ADR that exists so the next reader finds a decision rather than an accident.* | |

Proven by producing a real AAR containing `jni/arm64-v8a/libgojni.so`. Two toolchain facts the
build scripts must encode:

- `gomobile bind` requires `golang.org/x/mobile` **in the module dependency graph**
  (`go get -tool golang.org/x/mobile/cmd/gobind`); a Go 1.24+ tool directive, not linked into
  the daemon binaries.
- NDK 27 supports API 21..35 but gomobile defaults to API 16 and **fails**; every build must
  pass `-androidapi >= 21`. (This is the NDK floor, **not** the app's `minSdk` — PB-RUN-1.)

Both are now encoded in **`android/toolchain.env`**, which S13 checked in as PB-TOOL-1's pin;
the table above records the versions and the pin records where they are found. A third fact was
established while closing PB-TOOL-2 and belongs here: `-trimpath` removes the module-cache and
GOROOT prefixes from `libgojni.so` but **not** the builder's checkout path, because `gomobile
bind` synthesises a throwaway module whose `go.mod` replaces the main module with an absolute
directory, and the linker records replacement directories in the build-info blob verbatim.
`-ldflags "-X=runtime.modinfo="` is what removes it, at the cost of the embedded module graph.

**Not available, bounding what "verified" can mean (§10):** no Xcode/Apple account (iOS is
Phase C by design), **no physical Android handset**, no Firebase project, no provisioned VPS relay.

---

## 3. Threat model: what Phase B actually changes

> **AMENDED 2026-07-31 (ADR-007 B133) — THE STOLEN-HANDSET ROW IS RETIRED, AND EVERYTHING BELOW IS
> READ AGAINST THAT.** The trust boundary is now **the wire between the phone and the computer**.
> Both endpoints are trusted; the relay, the network path and **FCM/Google, which reads every push
> payload it carries**, remain the declared adversary. All phone-side user authentication is
> removed **with its code deleted**, so the "biometric-gated content key" named below no longer
> exists, and ADR-007:89's property — *a stolen once-unlocked phone yields no session content* — is
> **retired rather than implemented**. B133 states the accepted residual in as many words: *a stolen
> unlocked phone gives the holder full control of agents that edit code on the Mac; the only
> surviving mitigation is `swarm remote off` / device revoke, issued FROM THE COMPUTER.* **Three
> mechanisms below look like phone-side authentication, are not, and must not be deleted with it**:
> the two-tier wake/content split (PB-KEY-2 — its rationale is now transport, because FCM reads the
> payload), Keystore sealing at rest with `setUserAuthenticationRequired(false)` (it defends
> *offline extraction of the app data directory*, an attacker who has the bytes but not the running
> device — not the holder), and Android backup exclusion (PB-STATE-6/PB-SEC-10 — a backup is a copy
> of the device keys leaving the device over the network). **The fourth and fifth rows of the table
> below are untouched and still drive PB-SEC-10..14.** The SAS emoji comparison is now the ONLY
> human-in-the-loop security step in the product, so it must get harder to skip, never easier.

**Correction from v1 (all three reviewers).** v1 claimed the stolen phone is a *new* adversary
introduced by Phase B. That is wrong. ADR-007 makes it a **founding** threat — "a stolen phone
or a compromised relay must not become code execution or data exfiltration"
(`docs/adr/ADR-007-remote-access.md:10`) — and already claims the property "a stolen
once-unlocked phone yields no session content" (`:89`). ADR-007 D2/A15 (`:31`) specifies the
mechanism: **two epoch keys per epoch**, a content-free **wake key** (after-first-unlock,
readable by the notification path) and a biometric-gated **content key** (not readable by the
notification path, not derivable from the wake key).

So Phase B does not *introduce* this threat — **Phase B is where that claimed property is first
implemented and verified on a real OS.** Until now it has been a design assertion with no
handset to hold it. That reframing raises the bar: PB-KEY-* and PB-SEC-* are not new
defenses, they are the discharge of an existing ADR promise.

| Actor | Phase A | Phase B |
|---|---|---|
| Relay | adversary (E2E sealing + per-(sender,epoch) seq gating) | unchanged |
| Gateway / Daemon | owner-uid trusted / trusted | unchanged |
| **Stolen or lost handset** | asserted-but-unimplemented ADR property | ~~**implemented and verified here**~~ **RETIRED 2026-07-31 (ADR-007 B133)** — the holder is trusted; see the amendment above. The residual is accepted, and revoke-from-the-computer is its only mitigation. |
| **Other apps + the Android platform itself** | not modeled | **new**: backup/restore extraction, notification listeners, exported components and intents, overlay/tapjacking, third-party IMEs, accessibility services, clipboard, ADB/heap dumps, logs |
| **Mobile build supply chain** | not modeled | **new**: Gradle/Maven/gomobile dependencies |

Only the **fourth and fifth** rows are genuinely new (the stolen handset, row three, is
explicitly pre-existing). They drive PB-SEC-10..14.

---

## 4. Five blockers found by reading the tree

These reorder the phase. None was in the roadmap's Phase B plan.

### 4.1 The phone core is not bindable

`internal/phonecore/journal.go:1` documents the package as "gomobile-ready". **The claim is
unenforced — no test guards it**, and it is false. Verified failures include
`crypto.ContentKey [32]byte` (`internal/remote/crypto/epoch.go:64`, an array, in **9** exported
signatures); unsigned `uint32`/`uint64` epoch and seq throughout; `AcceptGrant` returning
**four** values (`accept.go:21`); `[]CachedSession` and `Snapshot.Lines []string`; `(T, bool)`
returns on `SessionCache.Get`, `ReplyCache.Take`, `SnapshotCache.Get`, `MailboxRouter.TakeGrant`;
cross-package types (`crypto.KeyStore`, `protocol.DeviceCommandAuth`, `protocol.Control`,
`protocol.JournalRecord`, `status.Group`, `time.Time`); and `crypto.SAS` returning `[6]string`
(`sas.go:58`).

*(v1 published "34 of 48 exported symbols fail". Two reviewers could not reproduce that count
and it is withdrawn — see §12/W6. The conclusion is unaffected and PB-BIND-2's guard will
produce the true number mechanically.)*

**A façade is mandatory; this is a new layer, not a retrofit.**

### 4.2 The bound package would ship the entire daemon into the app

Verified with `go list -deps`: `internal/phonecore` -> `internal/protocol` -> `internal/daemon`,
pulling in `internal/shim`, `internal/engine`, `internal/vt`, `internal/transcript`,
`internal/persist`, `internal/shimwire`, plus `github.com/creack/pty`, `charmbracelet/x/vt`,
`ultraviolet`, `xo/terminfo`, `muesli/cancelreader` — **52 non-stdlib packages** (`go list -deps
-f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/phonecore | sort -u | wc -l`). Also,
`gobind`'s generated wrapper lives outside the module's `internal/` boundary, so an
`internal/...` package cannot be bound **directly**.

**Verified empirically (2026-07-25), since PB-BIND-1's design rests on it**: a *non-internal*
façade that **imports** `internal/...` packages binds cleanly. A probe module — `internal/core`
holding a `[32]byte` key type and an unsigned-seq function (mimicking `phonecore`'s two worst
bind offenders), wrapped by a non-internal `mobile` façade — produced a working AAR. Only the
bound package itself must be non-internal; it may consume the internal tree freely. This
removes the risk that §4.1 and §4.2 together would have forced `phonecore` itself to relocate.

Shipping the PTY and VT emulator to a device an adversary may hold also cuts against ADR-007
Decision 2, which deliberately keeps them off the network-facing edge.

### 4.3 The handset has no durable state, so typing dies after one process death (UNANIMOUS, most severe)

The phone's outbound sequencer is a bare in-memory counter — `type Sequencer struct{ n
atomic.Uint64 }`, `Next()` returns 1 on first call (`internal/phonecore/input.go:33-36`) — and
`internal/phonecore` performs **no persistence at all**. The gateway rejects `seq <= highest`
as stale (`internal/remote/crypto/envelope.go:33-34,240-243`).

Android kills backgrounded app processes as routine behavior. After **one** process death the
phone restarts at seq=1 under the same epoch and every keystroke, `take_control`, launch and
kill is refused as a replay — permanently, until an epoch rotation or re-pair. The exit
criterion fails on the *second* app launch.

The mirror direction is a **security** regression: `MailboxReceiver.highest` is also in-memory
(`envelope.go:211-216`), so process death resets the phone's replay high-water to zero and the
adversary relay can redeliver retained frames.

The tree proves the problem class was already understood *for the other direction*:
`internal/remotegw/seqstore.go:12-17` exists (committee finding C2b) so a restarted gateway
"never re-emits a seq the phone's **durable** per-(sender,epoch) high-water would stale-drop"
— a comment that presumes a durable phone high-water **that does not exist**. ADR-007 D5
(`:50`) mandates gateway persistence; there is no analogous sentence for the phone anywhere.

### 4.4 Nothing renders a scannable QR — the primary "pairs" path does not exist

`cmd/swarm/remote.go:280-281` prints `"Scan this QR on your phone to pair:"` followed by the
literal string `sess.QR`. **There is no QR encoder in the repo** — no `qrcode` import, no QR
dependency in `go.mod`. There is nothing for a camera to scan. `qr.go:86 DecodeQR` parses a
*string*; it does not read a camera frame.

### 4.5 The relay protocol structurally cannot carry low-latency input (UNANIMOUS)

Both hops are broken for typing, and v1's proposed fix was impossible:

- **Client**: `Conn.roundtrip` holds `c.mu` across write-then-blocking-read
  (`internal/remote/relay/client.go:108-126`). **No request ids, no reply correlation.**
- **Server**: `serveConn` is strictly `readFrame -> dispatch -> readFrame`
  (`server.go:382-390`); a blocking handler stalls the whole connection.
- **Server**: `registerSession` binds exactly one conn per routing id, newest-wins takeover
  (`server.go:675-691`), and revoke/presence severance depend on it — so a second connection
  is not available, and relaxing that would weaken a Phase A anti-abuse property.
- **Gateway**: the command-IN loop polls at a fixed default **500 ms**
  (`internal/remotegw/service.go:27,67-68`) — which ADR-007 itself calls "unusable for live
  typing" (`:461`).

Therefore a blocking long-poll would head-of-line-block the very keystroke sends it exists to
accelerate, and v1's stated rationale ("needs no client demux change") was false. Both
candidate mechanisms need demux, so the tiebreaker evaporates and the decision is re-made in
PB-NET-5 on its merits — **as a protocol change covering both hops**.

---

### 4.6 The GATEWAY's inbound replay guard is in-memory — latent today, live inside Phase B's own window

§4.3 found the phone's durable-state gap. The machine side has the **same defect, and it is
relay-adversary-reachable**, which §4.3's is not in the same way:

- `NewCommandBridge` builds `crypto.NewMailboxReceiver()` fresh on every start
  (`internal/remotegw/command_loop.go:106`) and its read cursor starts at 0 (`:96`). Its own doc
  says a caller "resuming across a restart should seed it via `SetCursor` from durable state" —
  and **`SetCursor` is never called from production startup**; its only call site is `:152`,
  advancing within the same run. The gateway binary opens `OpenSeqSource` for
  `outbound-journal.seq` and `outbound-reply.seq` only (`cmd/swarm-remote/config.go:91,95`) —
  **no inbound state is persisted at all**.
- In `Accept` the staleness test is `hi, seen := r.highest[mk]; if seen && Seq <= hi`
  (`internal/remote/crypto/envelope.go:254-256`). On a fresh receiver `seen == false`, so the
  check is **skipped entirely** and `gap := seen && ...` is false — the first replayed frame at
  any seq is accepted with no gap signal, and so is every contiguous frame after it.
- `NewMailboxReceiver` leaves `maxAge == 0` (`envelope.go:219-221`), so the bounded-age check
  at `:263` is **disabled on the production inbound path**. There is no age backstop.
- Input frames carry **no signature and no expiry** (`internal/phonecore/input.go:21-27`,
  `internal/remotegw/input_in.go:20-26`), unlike commands, which the daemon bounds by
  `ExpiresAt`. `routeInput` drops only on `Gap` or empty session (`command_loop.go:208-216`),
  so a replayed keystroke routes to `Leases.Input` and reaches the PTY.
- The epoch survives reboot — rotation happens only in `RevokeDevice`
  (`internal/skeleton/api.go:231`), which is precisely the premise `seqstore.go` exists for.

**Claimed exploit, and its retraction (round 3).** v3 initially asserted that an adversarial
relay retaining phone->machine frames could, after a gateway restart, re-inject observed
keystrokes into a live lease. **That claim was wrong and is withdrawn.** Round 3 disproved it
and the disproof was independently re-verified; every link holds:

- Gateway shutdown tears down every lease conn and a restart builds a **fresh, empty**
  `LeaseManager` (`internal/remotegw/service.go:91,120`).
- `LeaseManager.Input` **drops** input for a session with no lease conn — "Input for an unknown
  or ended session (no conn in the map) is DROPPED" (`internal/remotegw/leasemanager.go:62-70`).
- A retained `take_control` cannot recreate the old lease: it is expiry-checked and its
  `operation_id` is **single-use**, a duplicate being refused as a replay with no attach
  (`internal/protocol/server.go:1452-1456`).
- Commands and input draw from **one** monotonic sequencer (`internal/phonecore/input.go:28-32`,
  `SenderKeyID` stays zero). So a *new* legitimate `take_control` carries a seq above every old
  input frame; once it is accepted the fresh receiver's high-water exceeds them all and each
  replayed input is `ErrStaleSeq`.
**Link 4 above is not a standing property — corrected (opus round 3).** "A new `take_control`
carries a seq above every old input" holds only for a phone whose send-seq is monotonic across
restarts, which §4.3 proves is exactly what does **not** exist today. With a phone holding
durable keys but a regressed send-seq — the precise state that exists *during* Phase B's own
implementation, before PB-STATE lands — the attack runs:

1. A legitimate fresh `take_control` at seq 1 is accepted (`seen == false`, staleness skipped),
   setting `highest = 1`. It is a **new** operation_id, so idempotency does not dedup it, and
   it is not expired. A lease opens.
2. The relay serves retained inputs at seqs 60..100. Seq 60 sets
   `gap := seen && 60 > 1+1` -> true -> `routeInput` drops it.
3. Seq 61 gives `gap := 61 > 60+1` -> **false** -> routed to the live lease -> the PTY. So do
   62..100.

The `operation_id`/`ExpiresAt` defenses cover the *replayed* take_control; here the lease is
opened by the *legitimate* one, and input frames carry neither defense.

**Therefore the correct standing is "not reachable in today's tree, but reachable inside Phase
B's own implementation window" — not "disproved".** It is unreachable today for a blunter
reason than any of the four links: `internal/phonecore` and `internal/phonesim` are imported by
**no production binary**, and `phonecore` performs zero persistence — there is no shipped phone
client, so a retaining relay has nothing to replay against. Phase B is what creates the client
*and* the durable keys, so PB-GW-1 and PB-STATE-3/-4 must land together or Phase B briefly
builds the very hole this section describes.

- One reviewer proposed a surviving narrow window — a supervised restart *within* the ~60 s
  `ExpiresAt`, letting the relay replay a still-valid `take_control` to re-lease and land the
  inputs — but explicitly did not trace whether that replay is deduped. **It is.** The
  `operation_id` is claimed through the **durable** two-phase idempotency store
  (`ClaimOperation` -> `coreAPI.ClaimIdempotentOp`, `internal/skeleton/api.go:388`), and the
  code states "a consumed operation_id stays consumed ... a captured take_control cannot open a
  second lease" (`internal/protocol/server.go:1455-1462`). The daemon does not restart when the
  gateway does, and the store is durable regardless. The window does not open.

Replayed old input therefore arrives either before any lease exists (dropped) or after a
higher-seq re-lease (stale-dropped). High-level mutating commands may be re-forwarded, but the
daemon's two-phase durable idempotency is the documented downstream defense
(`internal/remotegw/command_loop.go:83`).

**What is actually true**: a real **durability / defense-in-depth defect** with **no reproduced
exploit**. It is worth fixing because the safety currently rests on incidental properties (an
empty lease map, a shared sequencer, single-use operation ids) rather than on the replay guard
that is supposed to provide it — so a future routing or sequencing change could convert a
latent defect into a live one. Narrower effects (replayed unsigned watch/unwatch state, wasted
render work) are plausible but must be demonstrated per action class rather than assumed.

**Consequence for the Phase A closure**: it must NOT be amended to say a
confidentiality/integrity hole existed — that would be a false correction to a signed document,
which is its own harm. PB-DOC-5 records only what was reproduced: the missing durable inbound
high-water and the disabled bounded-age check, plus the fact that the original "no
relay-adversary-reachable hole" claim was scoped to a single gateway run.

It lands in Phase B because PB-LIFE-1/-5 mandate restart-on-exit supervision (making restarts
routine) and because it is the exact mirror of PB-STATE.

---

## 5. Scope decisions taken in this revision

Three contradictions the reviewers surfaced are resolved here rather than left to the implementer.

| Decision | Rationale |
|---|---|
| **v1 is SINGLE-MACHINE. The machine switcher is cut from v1.** | The core is structurally single-machine: one `ContentKey` per `MailboxRouter` (`snapshot.go:137-157`), one machine/target/grant/epoch/sequencer per phone (`phonesim.go:52-59`). Frames from two machines are sealed under different epoch keys and cannot be opened by one router. v1 mandated a switcher (PB-APP-2) that nothing supported — a contradiction all three reviewers flagged. The exit criterion says "a real session", singular. Multi-machine joins multi-device in Phase C. |
| **Light mode is DEFERRED to Phase C.** | The product tokens are dark-only today (verified), the exit criterion is a dark phosphor terminal, and authoring a complete light theme is the single largest non-load-bearing item in v1 (opus). ADR-007's "light+dark token sets" is amended, not silently dropped (PB-DOC-1). |
| **Push keeps its trigger (PB-PUSH-0) rather than being de-scoped.** | Nothing machine-side calls `PushTrigger`/`TokenRegister` today (verified: zero non-relay-test call sites), so v1 would have shipped a push transport with no producer. Roadmap B4's purpose is "wake on Group transitions"; a transport with no producer is incoherent, so the trigger is in scope. |

---

## 6. Requirements

Testable acceptance criteria. TDD mandatory with an evidenced RED run (GG-5).
**New in v2**: PB-STATE, PB-PAIR, PB-KEY, PB-RUN, PB-TIME, PB-INPUT, PB-PUSH-0.

### 6.0 The numeric budget (binding; round 2 required real numbers, not "a stated bound")

Round 2 correctly objected that v2 said "stated bound" everywhere, so an implementation could
choose 10-second typing latency and still pass. These are the binding values. They are chosen
to be consistent with the Phase A constants already in the tree (`RendezvousTTL` 60 s,
`HandshakeTimeout` 30 s, `maxControlSessionTTL` 30 m, `maxCommandValidity` 1 h,
`MailboxAppendPerMin` 600, `OpsPerMin` 600, `RetentionCap` 7 d). Changing any value requires
committee agreement, not implementer discretion.

| Budget | Value | Where it binds |
|---|---|---|
| Input latency, phone `Type` -> PTY write, local relay | p50 <= 150 ms, p95 <= 400 ms, p99 <= 800 ms, n >= 200 | PB-NET-5 **fence:** internal/skeleton/s6b_input_latency_test.go |
| Append latency while a wait is outstanding | <= 50 ms for the append call to complete | PB-NET-5(a) **fence:** internal/remote/relay/s6b_wait_test.go |
| **Echo latency, machine -> phone visible, local relay** | poll wait <= 500 ms plus one non-wait request: **p95 <= 750 ms, p99 <= 1000 ms, n >= 200**. **Closed-test scope only** — set by the owner on 2026-07-30 (ADR-007 B104) after six rounds in which this leg had **no budget at all** and was therefore unfenceable by construction. Production is explicitly NOT covered by this row. | PB-NET-5(b) **fence:** internal/skeleton/s6b_input_latency_test.go |
| Server-side wait (long-poll) maximum | 25 s (under common 30-60 s idle-proxy timeouts) | PB-NET-5 **fence:** internal/remote/relay/s6b_wait_test.go |
| Non-wait request timeout | 10 s | PB-NET-7 **fence:** internal/verify/pbnet7_deadlines_test.go |
| Reconnect backoff | initial 500 ms, factor 2, ceiling 30 s, jitter +/-20% | PB-NET-4 **fence:** mobile/pbnet4_backoff_test.go, cmd/swarm-remote/pbnet4_reconnect_test.go |
| **Input frame rate (client-side coalescing)** | <= 8 frames/s sustained, coalescing a 30 Hz autorepeat burst into one frame per 125 ms | PB-INPUT-5 — must stay under `MailboxAppendPerMin: 600` (= 10/s), which is the **only** cap that applies: `OpsPerMin` explicitly excludes `mailbox_append` ("mailbox_append and push_trigger keep their own dedicated windows", `internal/remote/relay/config.go:39-44`). The 20% headroom is deliberate. **PB-OPS-1 must require the demonstration relay's configured quota to be >= the default**, since quotas are operator-tunable and a lowered one would silently break live typing. **fence:** internal/phonecore/s11_coalesce_test.go |
| Callback queue | 256 items, drop-oldest with a surfaced overflow signal | PB-BIND-6 **fence:** mobile/conformance/robustness_test.go |
| Idempotent op queue | **WITHDRAWN 2026-07-26 — the queue is unbuildable from the commands this system authors.** A queued op is a *pre-signed* `DeviceCommandAuth`; `sealSignedCommand` stamps `ExpiresAt = now + CommandTTLFor(action)`, i.e. **1 minute** for an ordinary op, `opqueue.go` states it is never re-signed on replay, and `deviceauth.go` refuses it as `command expired`. **So the queue delivers nothing for any outage longer than 60 seconds — which is every outage a queue exists for.** Re-signing at drain is unavailable: PB-SEC-2 pins the biometric gate **per use** for exactly D7's op list, so a drain would be a prompt, not a queue. *(**AMENDED 2026-07-31 (ADR-007 B133)** — that last sentence has lost its premise: PB-SEC-2 is VOID and there is no per-use prompt, so re-signing at drain is no longer blocked by a gate. **The withdrawal is unaffected and must not be reopened on this ground**: it rests on the `ExpiresAt` arithmetic above — a pre-signed op expires in 1 minute and the daemon refuses it as `command expired` — which is independent of authentication. Recorded rather than deleted so nobody re-derives the queue from the disappearance of the weaker argument.)* Production also builds `NewOpQueue(0)` — unbounded — so this row's own "64 ops" was never the shipped object's bound. | PB-NET-4 |
| Resync rate | <= 1 per stream per 5 s, <= 12 per 5 min | PB-SYNC-6 **fence:** UNFENCED (found 2026-07-31 writing this column: mobile/app.go resyncBudget implements both halves and NO test in the tree drives them -- grep for resyncPerWindow returns the implementation and nothing else. The lever is the relay's: it decides when a stream looks broken enough to resync) |
| ~~Biometric freshness~~ | **WITHDRAWN 2026-07-31 (ADR-007 B133) — the number is withdrawn because its owning requirement PB-SEC-2 is VOID.** ~~60 s for input/take_control; **per-use** (`CryptoObject`) for revoke, kill switch, launch, kill~~ There is no gate, no per-use tier and no freshness window left to measure, so the budget owes no fence: `android/gate/s20_pbsec2_freshness_test.go` is deleted with the feature rather than adapted. Kept visible so a later reader who finds the number quoted in a deleted test's name does not re-adopt it. | PB-SEC-2 (VOID) |
| Seq reservation block | 256 (bounds seqs burned per crash) | PB-STATE-3 **fence:** internal/phonecore/sendseq_test.go |
| Clock skew | **surface distinctly** beyond +/-30 s. Rejection is the DAEMON's, at its own wider bands (below) — the phone does not refuse its own commands. | PB-TIME-1 **fence:** internal/phonecore/s11_skew_test.go |
| Push coalescing window | 30 s per session | PB-PUSH-0 **fence:** internal/remotegw/push_trigger_test.go |
| Local pairing state TTL | **60 s, matching the relay's authoritative `RendezvousTTL`** (v3 said 10 min and never justified keeping a pairing secret for nine minutes after it became unusable) | PB-PAIR-4 **fence:** mobile/conformance/s16_pairing_test.go |
| Max concurrent pending waits per client | 1 (a second wait is refused, not queued) | PB-NET-5(c) **fence:** internal/remote/relay/s6b_wait_test.go |
| Inbound bounded-age (`maxAge`) | 10 min | PB-GW-2 **fence:** internal/remotegw/s7b_bounded_age_test.go |
| Push envelope TTL / replay window | 10 min, with the replay coordinate persisted per PB-STATE-1 | PB-PUSH-3 **fence:** internal/phonecore/s17_wakereplay_test.go |
| Cached-state freshness before it is shown as stale | **5 min since the newest AAD-covered `IssuedAt` the phone has ACCEPTED from the machine. AMENDED 2026-07-31 (ADR-007 B121): the VALUE is unchanged, the CLOCK is.** "5 min without a successful poll" was the wrong predicate, and it is why nothing was ever built against this row: the declared adversary's cheapest move is to keep ANSWERING the polls -- with empty pages -- so a poll-keyed budget is armed by the party it defends against and never fires. `IssuedAt` is the one machine timestamp the relay cannot forge and can only make WORSE by withholding, so the same 5 minutes measured against it fails closed under exactly that attack. | PB-APP-8, **PB-APP-11** **fence:** mobile/conformance/pbapp11_silence_test.go |
| Latency harness | median of 3 runs, n >= 200 samples each, 20-sample warm-up discarded, 1-16 byte payloads, on an otherwise-idle machine, local relay over loopback; CI records the environment. **The harness MUST use a real file-backed `InboundState`, not the in-memory default**: S2 measured the gateway's per-keystroke fsync at 13-15 ms on an M1/APFS host, so a batch of 8 input frames costs ~120 ms — about 10% of the p50 budget on fast local storage and worse on a network-mounted state dir. Measuring with the in-memory store would measure a fiction. | PB-NET-5 **fence:** internal/skeleton/s6b_input_latency_test.go |
| Max coalesced input payload | 4 KiB per frame (flush early if exceeded) | PB-INPUT-6 **fence:** internal/phonecore/s11_coalesce_test.go |
| **Inbound drain rate (reads + acks), each hop** | **SUSTAINED-REGIME CEILING**, not an every-instant rule: <= 3 reads/s **and** batched acks <= 1/s per routing id, i.e. <= 240/min combined — because **`mailbox_read` and `mailbox_ack` DO meter against `OpsPerMin: 600`** (`internal/remote/relay/server.go:766,798`), unlike `mailbox_append`. §6.0 previously budgeted both *append* legs and neither *drain* leg: at 8 appends/s a wait returning on the first item gives 8 reads/s + 8 acks/s = **960/min > 600**, so the live tail would die with `codeQuotaExceeded` after ~37 s, mid-demo. The same arithmetic applies to the gateway hop once PB-NET-5 removes its 500 ms poll (120/min today). **CORRECTION (S6b RED): read as a flat rate, this number and the p50 <= 150 ms input budget are JOINTLY INFEASIBLE.** An un-batched wait returns one read per item, so at a sustained 8 appends/s, reads/s = appends/s = 8/s. Forcing a flat 3 reads/s at 8 items/s means reading every 333 ms, which adds ~167 ms of mean queueing — more than the entire p50 budget, before any network or fsync cost. The two numbers describe **different regimes** and v3.5.1 never said so. Binding resolution: the drain MUST be **adaptive** — read immediately while the arrival rate is low (the interactive regime that clause (b) measures), and batch only as the sustained budget is approached (a token bucket at 3/s with burst is the reference shape). The ceiling governs the sustained average over a 1-minute relay window; it does not forbid a burst that keeps a single keystroke off the queue. A closed-loop latency harness at p50 150 ms itself issues ~6.7 reads/s, which is why the regimes must be stated rather than assumed. | PB-NET-5(c), PB-GW-7 **fence:** UNFENCED for the READS half -- the token bucket is fenced only in internal/remote/transport, a package with zero production callers (ADR-007 B121, B94), so nothing measures the rate the shipped phone actually reads at. The ACKS half is fenced in production at internal/remotegw/drainack_test.go |
| **Ack placement (latency, not just quota)** | Acks MUST NOT ride inline on the delivery path. Measured on this host, `MailboxAck` p50 30.8 ms / **max 129.2 ms** (one synchronous bolt fsync each) — so a single inline ack can consume **86% of the entire 150 ms p50 input budget** on its own. v3.5.1 motivated batched acks purely on quota grounds; batching is **also** a latency requirement, and ADR B7 must record it as one. | PB-NET-5(b)(c) **fence:** internal/remotegw/drainack_test.go |
| **Machine->phone append rate (gateway coalescing)** | <= 8 appends/s sustained across journal **and** terminal combined (they share one sink and one target), i.e. terminal snapshots coalesced to <= 125 ms — against a render loop that can emit ~62/s | PB-GW-7 **fence:** internal/remotegw/append_budget_test.go |
| **Signed `ExpiresAt` by op class** | ordinary commands **now + 1 min**; **`take_control` now + 15 min** so the lease is not the binding constraint on a typing session. Stated as an explicit exception because PB-TIME-1 otherwise reads as a blanket 1 min. | PB-INPUT-3, PB-TIME-1 **fence:** internal/phonecore/s11_lease_test.go |
| ~~Biometric-freshness renewal~~ | **WITHDRAWN 2026-07-31 (ADR-007 B133) — the number is withdrawn because its owning requirement PB-SEC-2 is VOID.** ~~a typing session crossing the 60 s freshness window must **pause input and re-authorize**, not silently continue or silently drop; the lease itself is not ended by freshness expiry~~ Nothing re-authorizes, so nothing pauses, and `android/gate/s20_pbsec2_timedprompt_test.go` is deleted with the feature. **The surviving half is a behaviour decision this row must not be read as answering**: `internal/phonecore/lease.go:198` still lists *"a biometric-freshness lapse"* among the reasons the phone ends its own lease, and B133 records the open question of **what severs a lease on backgrounding** now that no freshness can lapse. Silently dropping the clause while rewording the comment is a behaviour change made under cover of a documentation edit. | ~~PB-SEC-2~~ (VOID), PB-INPUT-3 |

**Two window subtleties the budget depends on.** (1) The relay's limiter is a **tumbling**
one-minute window (`internal/remote/relay/server.go:105-115`: it resets when
`now.Sub(w.start) >= time.Minute`), not a smooth rate — so "600/min" is not "10/s" in kind, and
a burst can exhaust a window early. Budgets are therefore set against the *window*, and
PB-NET-4's 64-op reconnect drain must not be issued as one burst. (2) `mailbox_append` never
calls `meterOp`, so appends are capped by `MailboxAppendPerMin` alone (`OpsPerMin` does not
apply to them).

**Mechanism-conditional acceptance.** PB-NET-5 permits either request-id correlation with
concurrent dispatch **or** an explicit server-push frame, but its criterion (a) is phrased for a
"wait outstanding" and §6.0 binds a 25 s long-poll — which a server-push implementation cannot
literally satisfy. Criterion (a) therefore applies only to the wait-based mechanism; a
server-push implementation must instead show that an inbound push and a concurrent outbound
append make progress simultaneously on the same connection. The chosen mechanism is recorded in
S0 and fixes which form of (a) applies.

### 6.0b PB-GW — durable gateway state, inbound and outbound (closes §4.6)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-GW-1 | The gateway persists its inbound per-`(sender, epoch)` replay high-water and its mailbox read cursor, and seeds them on start. **A new bridge seam is required**: `CommandBridge.recv` is private (`internal/remotegw/command_loop.go:88`) and exposes no high-water seeding method — `SeedHighWater` exists only on the nested receiver, so v3's claim that production could "seed via the existing seams" was false. `SetCursor` exists but is never called from startup. | RED test: restart the gateway against a retaining relay and assert a retained frame is refused with `ErrStaleSeq` at the receiver — asserted **at the replay guard**, not at a downstream side effect that other mechanisms already prevent. |
| **PB-GW-6** | **Prerequisite for PB-GW-2, and a trap that would have bricked production.** Every phone->machine seal sets only `{Version, EpochID, Seq}` — **no `IssuedAt`** (`internal/phonecore/input.go:59`, `command.go:100,121,143`); the only non-test producer of `IssuedAt` is the *outbound* journal path (`internal/remotegw/relaysink.go:166`). So inbound `IssuedAt` is 0, and turning on a bounded-age check would compute an age of ~56 years and **reject every legitimate command and keystroke**. The phone must stamp `IssuedAt` on inbound command and input seals **before** PB-GW-2's toggle is enabled. | Test asserts a non-zero `IssuedAt` on every inbound seal; a second test asserts PB-GW-2's toggle with real phone-sealed frames still passes traffic. Ordering edge enforced in §11. |
| PB-GW-2 | **The gateway's inbound path enforces a bounded age** on authenticated frames — an age backstop even if a high-water is lost. **Value: 10 minutes** (well above the 60 s command TTL and any plausible delivery delay, well below the 7 d retention cap). **Gated on PB-GW-6**: enforcing it first would break all inbound traffic. *(v3.5.1 said "the inbound receiver enables the bounded-age check, which `NewMailboxReceiver` leaves at `maxAge == 0`". **That named a seam that does not exist and was unimplementable verbatim**: `MailboxReceiver.maxAge` and `.now` are unexported, `NewMailboxReceiver()` takes no arguments, there is no setter (`internal/remote/crypto/envelope.go:211-231`), and crypto is FROZEN — the only code that sets `maxAge` lives inside the package at `crypto/hardening_api_test.go:312`. S7's author hit the same wall and recorded it at `internal/phonecore/issuedat_test.go:22`. The requirement now states the PROPERTY and leaves the seam to the implementation, which is what it should always have done.)* Two routes satisfy it: **(A)** an ADR adds a max-age constructor to crypto, or **(B)** the gateway authenticates via `crypto.OpenMailbox` (which does not touch the receiver), applies the bound, then calls `recv.Accept`. **Route B is chosen** — same property, no change to a frozen package, no ADR. | Test: an authenticated envelope older than the bound is refused with `ErrStaleAge`, **and** legitimate phone-sealed traffic is unaffected — the second assertion is what makes this test honest. **Ordering is itself a requirement**: authenticate, then bound, then `Accept`. Bounding before the AEAD verifies applies the check to a timestamp the untrusted relay supplied; bounding after `Accept` lets the rejection advance the replay high-water, so one retained frame permanently bricks typing. Both orderings have demonstrated mutations that the S7b suite catches. **The bound is one-sided on purpose** (`now.Sub(issued) > maxAge`, never trips for a future stamp) and must not be "improved" into a symmetric window: `IssuedAt` is AAD-covered, so a relay can only make frames *older*, while a symmetric bound would refuse a fast-clocked handset's live traffic and add nothing. Replay by a fast-clocked phone is the seq guard's job. |
| PB-GW-3 | **A per-frame-class crash matrix**, not a single "atomic commit". A local transaction cannot atomically span the persisted high-water, the persisted cursor, an external PTY/daemon side effect, and the relay ack, so the rule differs by class: live input may persist consumption *before* the PTY write and accept loss on crash (it is live-only per ADR-007 D7); high-level operations rely on the daemon's durable two-phase idempotency for duplicate suppression; watch/unwatch needs an idempotent convergence rule. | Each class has a stated allowed-loss / duplicate-prevention rule and a crash-injection test at each boundary. |
| PB-GW-4 | **Per-action-class replay tests** against a retaining (adversarial) relay across a restart: input, take_control, take_control_end, idempotent mutations, and terminal watch/unwatch. **The input class must model a seq-regressed phone** (or an explicitly seeded-low receiver) as its adversary — against a monotonic phone the test passes with or without PB-GW-1, repeating the same "proves nothing" flaw as v3's empty-lease-manager test. The §4.6 trace (legitimate lease at seq 1, then contiguous retained inputs from seq 61) is the scenario to encode. | Each class asserts at the guard that is supposed to enforce it, and **each test must fail against unfixed code for the right reason** — demonstrated, not assumed. |
| PB-GW-7 | **A machine->phone append budget with gateway-side coalescing, and no seq burned on a failed append.** The numbers do not currently close: `renderDebounceWindow = 16 ms` (`internal/daemon/terminalrender.go:33`) lets a live peek emit ~62 snapshots/s, while the relay caps appends at `MailboxAppendPerMin: 600` (= 10/min-window) per target. Worse, `RelaySink` allocates the seq **before** the append and returns on append error (`internal/remotegw/relaysink.go:154,181`), so **every quota-refused snapshot permanently burns an outbound seq** — manufacturing gaps that PB-SYNC-1 must conservatively stale on *both* journal and terminal, exhausting PB-SYNC-6's resync budget within minutes. Journal and terminal share one `RelaySink` and one target, so a peek starves the journal too. `internal/remotegw/gateway.go:29` already states the intended contract ("bounded/coalescing on the relay side") that `RelaySink` does not implement. **This is exit-criterion-fatal: "types into a real session" is meaningless without the live tail (PB-APP-4).** Coalescing holds the outbound rate under the budget in §6.0, and a sustained-peek test runs for >= 60 s without quota refusal, without manufactured gaps, and without starving the journal. **The naive remedy "a failed append never consumes a seq" is FORBIDDEN — it is unsafe.** The relay commits the item *before* replying (`internal/remote/relay/server.go:758-762`) and `MailboxAppend` returns an error when the *response* read fails (`client.go:268`), so failure is not always pre-commit: relay stores seq N -> connection drops before the reply -> gateway reuses N for different plaintext -> the phone accepts whichever seq-N envelope lands first and stale-drops the other, i.e. **silent journal/snapshot loss or reordering**. Required instead: allocate the seq only after local admission/coalescing (so *expected* quota refusals never reach allocation); distinguish a definitive pre-commit refusal from a delivery-unknown failure; and on delivery-unknown either burn the seq or retry **the exact same sealed envelope**. Note the idempotency is *receiver-side and free* — a duplicate of an identical sealed envelope is stale-dropped by `MailboxReceiver` (`internal/remote/crypto/envelope.go:255-257`) — so this needs **no relay protocol change**; `mailbox_append` carries only `{target, envelope}` and does not need an append-identity field. Tests must inject a connection loss *after* relay commit but *before* the response. |
| PB-GW-8 | **The gateway's outbound journal cursor must be durable.** `Gateway.cursor` is a bare `uint64` (`internal/remotegw/gateway.go:47-50`) that nothing persists or seeds, while two comments call it durable — "its **durable** resume point" (`gateway.go:59`) and "resumes journal delivery from its last durable cursor" (`service.go:56-57`). Every restart therefore re-reads from cursor 0 and re-appends the entire journal at fresh seqs into the same 600/window mailbox. This is the **fourth** instance of a comment presuming durability that does not exist, and PB-LIFE-1/-5 make restarts routine. | Restart test: the gateway resumes from its persisted cursor and does not re-append delivered journal records. **"Does not re-append" cannot be achieved by a local cursor write alone** — persisting a cursor is not atomic with a remote append (same distributed-commit hole as PB-GW-7). Requires a durable outbound outbox coupling {journal cursor, sealed envelope, relay outcome}, replayed idempotently on restart. |
| PB-GW-5 | The Phase A closure records **only what was reproduced** (see §4.6): a missing durable inbound high-water and a disabled age check, with the original claim scoped to a single gateway run. It must not be amended to assert an exploit that was disproved. | PB-DOC-5. |

### 6.1 PB-STATE — durable on-device state (NEW; the most severe gap, §4.3)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-STATE-1 | The core persists and restores everything resume-critical: device keys, pinned machine static + sign pub + routing id, epoch id + keys, **outbound send-seq**, **per-(sender,epoch) receive high-water**, **the grant receiver's `(highest epoch, grant_seq)` watermark** — which `internal/remote/crypto/epoch.go:155,167` explicitly requires be "persisted across restart (F3)", or "a relay could replay an old correctly-signed grant after a phone/app restart" — the wake-envelope replay coordinate chosen by PB-PUSH-3, the relay mailbox cursor, session/snapshot caches, pending idempotent ops and their outcomes, and per-bucket stale flags. | Enumerated in one persisted schema; a test asserts each field survives a restart, including a grant-replay-after-restart test. |
| PB-STATE-2 | **Process-death acceptance test**: kill the core process mid-session and restart. Typing, launch and kill must still succeed, and a frame captured before the kill must still be rejected as a replay. **"Typing survives" means the durable send-seq, keys and coordinates survive -- NOT the lease.** Clarified after S11 gated input on a confirmed lease (PB-INPUT-2) and this test went red: the two requirements are both satisfied and their interaction is not obvious. The lease is a live daemon connection and cannot survive a phone restart by construction, so the post-restart sequence is **re-`TakeControl` -> wait for the confirmed generation -> type**. Recorded because the machine-side behaviour is counter-intuitive and was verified in source rather than assumed: **a phone process death does NOT sever the daemon's lease.** The lease is bound to the *gateway's* `LeaseConn` (a unix socket from the on-machine gateway to the daemon, `remotegw/lease.go`), and a phone death drops only the phone's relay websocket -- the gateway never observes the phone's connection at all, so `lc.Dead()` never fires and no `SeveredLease` is emitted. The daemon is left holding an orphaned lease. **It is fully reclaimable and needs no revoke**, by three independent mechanisms: the daemon supersedes on the next `attach` (bumping the generation and detaching the prior controller, `internal/protocol/server.go:498`), the gateway supersedes in `LeaseManager.Begin`, and the daemon expires it lazily at the earliest of the signed `ExpiresAt`, `now+TTLSeconds`, and the 30 min cap. The reclaim closes the OLD conn, which fires a severance notice carrying the OLD generation *after* the new lease is live -- so the phone must ignore notices below its confirmed generation, or the reclaim kills the lease it just re-acquired. | The RED form of this test must first demonstrate today's stale-drop brick. This single test is the guard for §4.3 in both directions (liveness and replay). |
| PB-STATE-3 | Send-seq durability must not cost an fsync per keystroke: reserve-a-ceiling-and-burn-the-gap (block size per §6.0), mirroring `internal/remotegw/seqstore.go`. **Because this deliberately creates outbound seq gaps, the gap consequence must be specified** — see PB-STATE-8. | Decision recorded; a test asserts no seq is ever reused across a crash at any point in the reservation window, including a crash between reservation and use. |
| PB-STATE-4 | Writes are crash-atomic; corruption fails **closed**. **Rollback needs a named trust anchor**: AEAD and atomic writes detect corruption, not rollback — a valid older blob sealed by the same Keystore key stays valid, and KeyMint rollback-resistance protects key blobs, not arbitrary app state. **The v1 anchor is chosen here, not left to the implementer** (v3's "or an explicitly narrowed threat" let an implementation decline rollback protection entirely): **authenticated remote reconciliation on reconnect**, with **a distinct authority per coordinate** — v3.2 named the gateway's inbound high-water as *the* authority for all three, but it describes only phone->machine sequences and carries no information about the other two, so an implementation could pass while rollback still reset them. The authorities are: (a) **phone send-seq** -> the gateway's durable inbound accepted high-water (PB-GW-1); (b) **phone per-bucket receive high-waters** -> the gateway's durable *outbound* sequence ceilings, which are **per bucket**: the journal/terminal bucket's ceiling comes from PB-GW-8's outbox, while the command-reply bucket's authority is the already-durable `outbound-reply.seq` (`cmd/swarm-remote/config.go:95`) — not the journal outbox; (c) **grant watermark** -> the daemon's epoch/grant issuance coordinate. Reserved-but-unused seq blocks (PB-STATE-3) must be accounted for in (a). When an authority is unreachable the phone fails closed for mutating ops, marks the affected channels stale, and reseeds. **AMENDED 2026-07-26 — `RevokeThisDevice` is EXEMPT, and the exemption is stated here rather than left to the implementer.** S18 found that revoke now runs the reconcile gate, so **the phone's panic button refuses on an unreconciled phone** — which is close to the definition of a lost or long-disconnected handset, i.e. the exact state the button exists for. The boundary is not "revoke is special": this gate protects ops whose **target is selected from synchronized state** (`kill`, `launch`, `take_control` — the three this requirement itself enumerates), because stale state makes them act on the wrong object. `RevokeThisDevice` **selects no target** (it names its own signer, which needs no synchronized state to identify) and **only removes capability, never grants it** — so a rollback attacker who forces it gains a denial of service they already had, while blocking it denies the owner their only remote kill. It also fails safe in the other direction: the durable local half (token deletion, key purge) runs before the wire half regardless. **Test both directions**: an unreconciled phone completes a revoke end to end, **and** `kill`, `launch` and `take_control` still refuse with `swarm/unreconciled` on that same phone — an exemption that widens to the other three is the failure this amendment risks. | Test rollback **per coordinate** — send-seq, every receive bucket, and the grant watermark — not send-seq alone; assert a retained machine frame and an older correctly-signed grant are both refused after a rollback. The test may not rely on hidden state unavailable after a real rollback. |
| PB-STATE-7 | **The receive path commits atomically.** Today the high-water advances inside `Accept` (`internal/remote/crypto/envelope.go:254`), caches mutate afterwards (`internal/phonecore/snapshot.go:201`), and the cursor/ack come later still — so a crash between them either loses a frame forever (stale-dropped on redelivery, never applied) or, if reordered, permits replay. `{high-water, relay cursor, decoded cache mutation, stale flags}` must commit as one transaction **before** the ack, with the ack idempotent on retry. | Crash injection at every boundary in the receive sequence; no frame is both acked and unapplied, and none is applied twice. |
| PB-STATE-8 | **Phone->machine gap semantics.** PB-STATE-3 burns seqs, and the gateway currently *silently drops* input/resize frames whose `Gap` bit is set (`internal/remotegw/command_loop.go:208-216`) while ignoring `Gap` on commands — so the first post-restart keystroke can vanish with no signal. The invariant must be stated and tested: a burned gap is absorbed by the re-lease command frame, never by an input/resize frame. High-level operation gaps must trigger durable outcome reconciliation before later state is trusted; live-input gaps may be discarded, but only explicitly. | Test asserts the first post-restart input frame carries no `Gap` bit, and that an operation gap forces reconciliation. |
| PB-STATE-9 | **Which tier seals which state** is specified, not left open: state the wake path must read while locked (push token, dedup coordinate) is sealed under the wake tier; send-seq, receive high-waters and decrypted caches are sealed under the content tier (PB-KEY-2). One undifferentiated "sealed" would let the implementer pick whichever tier passes. **AMENDED 2026-07-25 on the S15 RED author's evidence, in three ways it could not be implemented as written.** **(1) "Only the wake-tier state" is literally unsatisfiable.** Seven of `State`'s 22 exported fields get a tier from this requirement; of the rest, `Machine` and `EpochID` must be read BEFORE any unseal (the load path filters another machine's blob wholesale, and `resealTier` keys carry-verbatim on the epoch), and `RoutingID`/`MachineRelayAuthPub` are what the wake path uses to reach the relay with no user present. The criterion is therefore "only the wake-tier state **and the coordinates the load path must read in order to open it**", with that list pinned by test. **(2) The content tier CANNOT be one sealed blob.** A `Save` taken while locked must PRESERVE the send-seq ceiling and receive high-waters it cannot read — otherwise the phone renumbers from 1 and the gateway stale-drops everything for the life of the epoch — so those must be carried verbatim. But `PurgeKeys` runs AT the screen lock, with the content tier locked by definition, and PB-KEY-7 requires it to destroy the decrypted caches, which carry-verbatim cannot do. The content tier therefore needs **at least two containers: purgeable caches, and non-purgeable replay-guard counters.** **(3) `PendingOps` is content tier and non-purgeable.** The offline op queue carries session ids and, for a launch, the command line the user typed — user content by any reading, though not a *decrypted* cache, so PB-KEY-7's purge must leave it. **FLAGGED 2026-07-31 (ADR-007 B133) — DISPOSITION OWED, NOT TAKEN HERE.** Neither B133 nor the deauth plan names this row, and it is written throughout in the vocabulary of a lock. **The substance survives**: which tier seals which state is exactly the tier boundary PB-KEY-2 keeps, and the two-container split of clause (2) survives on *its own* grounds — the replay-guard counters must be carried verbatim by a writer that cannot read them, which is a correctness argument about renumbering, not about authentication. **What loses a producer:** "state the wake path must read **while locked**", "`PurgeKeys` runs **AT the screen lock**, with the content tier locked by definition", and the criterion's "**the locked-device process**". The actor to re-anchor on is **the push/wake path** — the process context that holds the wake key and must not reach the content key — not a locked device, which no longer exists as a state. Clause (2)'s purge half must also follow PB-KEY-7's trigger from lock to revoke/unpair; clause (3)'s ruling that `PendingOps` is content-tier and **non-purgeable** is unaffected and constrains that purge. | Test asserts the locked-device process can read only the wake-tier state **plus the pinned load-path coordinates**, measured from the BYTES on disk and paired with the positive half (the material was handed to that tier's sealer and never the other), never from a Go accessor. **See the FLAG in the requirement column: the measurement is sound and the ACTOR is stale** — "the locked-device process" must become the push/wake path. The bytes-on-disk method is the load-bearing part and is untouched; a test that reads a Go accessor instead would be satisfiable while the defect ships, which is why the row says so. |
| PB-STATE-5 | A schema version with a forward-migration path; an app upgrade must not lose state or reset counters. | Migration test from vN to vN+1; an unknown future version fails closed. |
| PB-STATE-6 | State at rest is sealed per PB-KEY-2/PB-STATE-9 and excluded from Android backup (PB-SEC-10). *(Verified in slice S15, after Android key custody exists — in v2 this sat in the state slice and created a dependency cycle.)* | Asserted jointly with those requirements. |
| PB-STATE-10 | **Fail-closed must not mean bricked.** PB-STATE-4 fails closed and prompts re-pair, but PB-KEY-3 establishes that re-pairing is *refused* while a device is registered (`BeginPairing` fail-fasts on a non-empty registry), so the phone could brick into a state whose only exit is physical access to the machine. **The recovery flow must be unconditional**, not inherited from PB-KEY-3's optional branch: PB-KEY-3 permits an implementation to choose re-grant instead of an unblock, and re-grant cannot recover a phone whose local state is corrupt and fail-closed. Required as its own owner-side flow: list/identify the stranded device, revoke/unregister it, purge machine and relay state, re-pair. | Test drives the exact CLI-visible path: corruption -> fail-closed -> owner-side recovery -> working re-pair, with no step requiring undocumented knowledge. |

### 6.2 PB-BIND — bindability (§4.1, §4.2)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-BIND-0 | The bound package's dependency closure is constrained by an **executable allowlist of exact import paths**, not categories and not a denylist. (v1 used a denylist that already omitted `internal/shimwire`; v2's categories were not machine-checkable and omitted required transitive deps such as `github.com/coder/websocket`, `github.com/flynn/noise`, and `golang.org/x/crypto`.) The allowlist is a checked-in file of fully-qualified paths, and adding one is a reviewed change. | A test computes `go list -deps` and fails on any package outside the checked-in allowlist file. Phase A suite green through the extraction. |
| PB-BIND-1 | A single façade package at a **non-internal** path is the only bound surface. | `gomobile bind` succeeds on the façade; nothing else is bound. |
| PB-BIND-2 | Only gomobile-legal types (no arrays, unsigned ints, maps, non-`[]byte` slices, generics, variadics, channels, or `(T, bool)`; multi-return only `(T, error)`). | **A Go test runs `gobind` over the façade and fails on any bind-illegal export** — the standing guard §4.1 showed was missing. It also emits the true legal/illegal counts. |
| PB-BIND-3 | The façade covers every capability the v1 screens need: pairing (QR decode, SAS, confirm, cancel), roster + presence, sessions with Group, journal read/subscribe, snapshot peek, take_control acquire/release, input + resize, launch, interrupt/kill, revoke, kill switch, push-token registration, connection/stale state, resync, state lifecycle (Start/Stop/restore), **`terminal_watch`/`terminal_unwatch`** (`internal/protocol/remote.go:88-89`, routed at `internal/remotegw/command_loop.go:238-256` — first-class verbs PB-APP-4's live tail depends on, and without `unwatch` the peek plane leaks per-session server render work), and **push preferences** (see PB-PUSH-8). | A traceability table maps every screen element to a method; a Go test exercises every method against a real in-process backend. Any screen element with no method is a coverage failure. |
| PB-BIND-4 | The JNI boundary carries no *unnecessary* secret. The one deliberate exception is the key-custody artifact defined by PB-KEY-1, which must be named, directional, and justified. (v1's absolute phrasing contradicted PB-SEC-1 — opus H2, fable F5.) | Test asserts no exported method returns raw long-term private keys; PB-KEY-1's artifact is the sole documented crossing. |
| PB-BIND-5 | No Go panic crosses the boundary (a panic through JNI kills the app). | Every entry point recovers into an `error`; a test injects a panic per entry point. |
| PB-BIND-6 | Documented threading/lifecycle contract: any-thread safe; `Start`/`Stop` idempotent; callbacks arrive on a Go goroutine (UI must marshal); a slow callback must not stall the core, with a **stated** queue bound and overflow behavior. | `-race` test hammering concurrent calls and repeated Start/Stop; a deliberately slow callback does not wedge the core and its overflow is observable. |
| PB-BIND-7 | The exported surface is pinned so a breaking change cannot land silently. | Golden-file test of the exported surface. |

### 6.3 PB-PAIR — pairing end to end (NEW; §4.4)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-PAIR-1 | **Machine-side QR rendering**: `swarm remote pair` renders a genuinely scannable symbol, not a raw string. Three constraints v3 missed: (a) it must render on a **light quiet-zone background** — filled blocks on a dark terminal produce an inverted symbol most scanners reject, and §5 pins the product theme dark; (b) the ECC level and module count must be stated and the symbol must fit a **standard 80x24 terminal** (a ~161-character payload in half-block glyphs needs roughly 31-35 rows, which does not fit — so either the payload shrinks or the rendering is denser); (c) a fallback for terminals that cannot render it. | **Scannability, not a string round-trip.** v3's criterion round-tripped against `DecodeQR`, which parses a *string* and would need a QR *symbol* decoder that does not exist in the tree and was never budgeted. Accept either a symbol-level decode of the rendered raster, or an evidenced manual scan with a real phone recorded under `docs/verification/`. |
| **PB-PAIR-7** | **The QR must carry a destination — today it does not, so "pairs" has no endpoint.** `internal/skeleton/pairing.go:112` mints `EncodeQR(QRPayload{RendezvousID, PairingSecret})`: `RelayURL` is never set, although the codec reserves it (`internal/remote/pairing/qr.go:40,50`) and the URL is available two frames up (`loadRelayURL`, `pairing_config.go:66`) but is used only to build the rendezvous closure and then discarded. `MachineStaticPub` (`QRFlagMachineStaticPub`) is likewise dead. A scanning phone therefore receives a rendezvous id and a secret **and no endpoint to dial**, so it cannot claim the rendezvous — and PB-PAIR-6's whole threat model ("a malicious QR cannot silently point the phone at an attacker-chosen relay") presupposes a destination the QR does not carry. This is round 1's "no QR encoder" one layer down. **Ceiling derived and enforced (S3, 2026-07-25): 39 characters.** Payload length is `13 + base64url(3 + L + 16 + 32)`, so the relay URL is the only free variable: at L=39 the payload is 133 chars -> ECC-L version 6 (41 modules), which a standard terminal can draw; at L=40 it jumps to version 7 (45 modules, 49x25 cells) and **no 24-row terminal can show it**; at L=90 `EncodeQR` refuses outright (`QRMaxBytes`). Unbounded, this silently deleted the slice's entire deliverable for ordinary URLs — `wss://swarm-relay.us-east-1.example.com:8443` is 44 characters — while the operator was told "terminal too small" on an 80x24 terminal that was nothing of the sort. `swarm remote init --relay-url` now refuses blank, whitespace-only, unparseable, non-ws/wss, host-less and over-length URLs before any filesystem write, and the QR fallback names the real cause. Requires: `BeginPairing` populates `RelayURL`, plus an explicit decision on pinning `MachineStaticPub` in the QR. **Decided (S3 RED, 2026-07-25): NOT pinned in v1.** The sizing was re-derived from real payloads rather than the spec's earlier estimate: today 81 chars; with `RelayURL` 119 chars -> byte-mode ECC-L version 6 = 41 modules; with `MachineStaticPub` as well ~162 chars -> version 8 = 49 modules. At half-block density (2 modules per row) an 80x24 terminal nominally admits `size + 2*QZ <= 48`, but the shipped renderer is handed `rows - 1` (one row funds the newline after the last symbol row), so the real budget is **46** module rows and 41 modules ship at **quiet zone 2**, not 3 — two below the QR standard's 4, recorded as a deliberate tradeoff and the single riskiest number in the slice. **49 does not fit under either budget.** Declining the static pub is also defensible on its merits: the machine static is already pinned from Noise msg2 and the six-emoji SAS compare is the designed human anti-MITM check, so the QR pin would be belt-and-braces, not the primary defense. Revisit only with a denser glyph family (sextants, U+1FB00) or a shorter payload encoding. | Test: a decoded QR yields a dialable relay URL; a phone driven only from the QR completes pairing with no out-of-band configuration. **PB-PAIR-1(b)'s sizing must be re-derived from the real payload**: production currently emits ~81 chars (fits ~23 half-block rows), not the ~161 v3 assumed, so the "does not fit 80x24" constraint was computed from a payload the code never produces and only becomes true once the URL and static pub are added. |
| PB-PAIR-2 | **Phone-side camera capture + decode**, with the `CAMERA` runtime permission requested, and a manual-entry fallback when it is denied or permanently denied. | Tests for granted, denied, and permanently-denied paths; manual-entry encoding is specified, not improvised. |
| PB-PAIR-3 | The scanner dependency is justified under PB-SEC-14 (ML Kit pulls Google Play Services, in tension with a minimal dependency set) — the choice is explicit. | Decision recorded in the ADR with the tradeoff stated. |
| PB-PAIR-4 | A **persisted** pairing state machine: process death at any transition (Noise msg1/2/3, SAS display, machine decision wait, local pin commit, grant bootstrap) resumes or fails closed — never a half-paired device. | Kill/restart test at each transition; a machine that committed while the app died before persisting pins is detected and resolved. |
| PB-PAIR-5 | Explicit terminal states for: declined (`pairing.go:71 ErrPairingDeclined`), SAS mismatch, rendezvous timeout, expired/consumed QR, and **a QR naming a DIFFERENT MACHINE from the one this phone is already pinned to**. Abandoned device keys and partial local records are cleaned up. **AMENDED 2026-07-25 — the fifth state was "already-paired" and is UNREACHABLE on the phone.** The machine refuses a second pairing fail-fast, **before minting any rendezvous id, secret or QR** (`internal/skeleton/pairing.go:82-90`, single-device v1 per ADR-007 2026-07-24), so a phone has nothing to scan and can never enter that state; the condition is CLI-visible machine-side only. Implementing it phone-side as a local fail-fast was tried and **broke a supported flow** — S8's revoke-then-re-pair, which `pin()` exists to serve by re-arming the fail-closed gates on an epoch change — and required a carve-out for revoked/invalidated handsets to avoid making PB-APP-10's own remedy unreachable. A constraint needing a carve-out to stop it contradicting another requirement is the wrong constraint. **The slot is redefined onto a real, reachable defect found in the same investigation**: `mobile/pairing.go` `pin()` assigns `MachineStatic`, `MachineSignPub` and `MachineRelayAuthPub` **unconditionally**, so a phone paired to machine A that scans machine B's QR silently re-pins to B and abandons A — no warning, no terminal state, and the user's first sign is an empty roster. v1 is single-machine (§5 cut the switcher), so this is a user error the product currently honours destructively. It is reachable mid-handshake, since the phone learns the machine identity from Noise msg2 (PB-PAIR-7 decided not to pin the machine static in the QR), and it leaves the re-pair flow working: same machine, new epoch proceeds. | Test per state; each is user-legible, not an opaque error. The different-machine state needs a `pin()` guard and must NOT refuse a same-machine re-pair. |
| PB-PAIR-6 | A malicious QR cannot silently point the phone at an attacker-chosen relay. **The LAN case must be resolved explicitly**, because a blanket private-address rule would reject the very handset demonstration PB-OPS-1 describes (a phone reaching the laptop over the LAN): private/LAN destinations are **allowed only after the origin is displayed and explicitly confirmed by the user**; public destinations follow the same display-and-confirm rule; no destination is joined silently. | Tests: LAN target requires confirmation and succeeds after it; a target swapped after display is rejected; nothing is joined without the origin being shown. |

### 6.4 PB-KEY — key tiers and grant recovery (NEW; §3, opus M3/H2, fable F5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-KEY-1 | The **JNI key-custody contract**: exactly which artifact crosses the boundary, in which direction, and why that is acceptable — reconciling the Go core's native-heap keys with the Java-only Android Keystore API. | Contract documented in the ADR and in the façade package doc; a test pins the crossing to that one artifact. |
| PB-KEY-2 | ADR-007 A15's **two-tier split is honored on Android**: the wake key is after-first-unlock and readable by the push path; the content key is user-authentication-gated and **not** readable by the push path or derivable from the wake key. **The enforcement mechanism must be stated**: on iOS A15 relies on NSE process isolation, but on Android `FirebaseMessagingService` runs in the app process, so enforcement is Keystore auth-gating (unwrap fails while locked) plus code discipline — *not* OS isolation. The emulator's software Keystore proves the code path, not the hardware guarantee (§10). **NARROWED 2026-07-31 (ADR-007 B133) — one clause is killed, the requirement survives, and the enforcement sentence is rewritten rather than glossed.** **Killed:** "the content key is **user-authentication-gated**". There is no gate; both tiers' KEKs are now generated with `setUserAuthenticationRequired(false)`. **Kept, and now the whole requirement:** the split itself, and "**not** readable by the push path or derivable from the wake key" — because the wake key must be readable by a path whose carrier (FCM) reads the payload, and the content key must not be derivable from it. That is a property of the wire and is untouched by anything removed from the phone. **The honest enforcement sentence:** on Android `FirebaseMessagingService` runs in the app process rather than an iOS-style NSE, so with auth-gating gone **code discipline is the ONLY phone-side enforcement of the tier boundary** — a real reduction, accepted in B133. **The surviving defence is sender-side**: PB-PUSH-0's criterion keeps the push path holding the wake key alone, and the property the split exists to buy — that the carrier of the push cannot read session content — never rested on the phone-side half, which was always the weaker one on Android. | Tests assert the push path cannot obtain the content key and a locked device cannot decrypt session content; the test states which of the two properties it actually proves. **AMENDED 2026-07-31 (ADR-007 B133): the second assertion loses its subject.** "A locked device cannot decrypt session content" has no lock event and no auth-gated unwrap left to fail, so it is **not repairable into a true statement about the phone** and must not be reworded into one. What remains testable, and is the whole criterion: **the push path cannot obtain the content key**, asserted at the sender per PB-PUSH-0 and by code-discipline fences on the phone that state plainly that discipline is what they check. |
| PB-KEY-5 | **Custody tier per role**, not one undifferentiated "core key". `crypto.KeyStore` is a single interface over `NoiseStatic`, `OpenSealedBox`, `SignCommand`, `SignRelayAuth` (`internal/remote/crypto/keystore.go:47-56`), and background reconnect needs `SignRelayAuth` while locked, while `OpenSealedBox` recovers **both** wake and content keys from a grant. If the recipient key is therefore after-first-unlock, then a stolen once-unlocked handset **plus the persisted sealed grant** yields the content key — falsifying ADR-007:89 in the very phase meant to implement it. Assign a tier to each of `{NoiseStatic, Recipient, CommandSign, RelayAuth}` and state whether the sealed grant blob is discarded after opening or retained under the content tier. **FLAGGED 2026-07-31 (ADR-007 B133) — DISPOSITION OWED, NOT TAKEN HERE.** The **body** of this row is role separation and survives whole: it is about which of four roles gets which custody tier, and it has nothing to do with authentication. **Its ACCEPTANCE CRITERION is another matter, and the deauth plan gets this backwards.** The plan states that the "after-first-unlock attacker reaches no content key" claim "actually lives in PB-KEY-2's dying clause, not PB-KEY-5"; verified against this file, the phrase appears **verbatim in this row's criterion and nowhere else in §6** (PB-KEY-2's own auth clause is the different sentence "the content key is user-authentication-gated", with the criterion "a locked device cannot decrypt session content"). Both rows carry an auth-dependent claim; they are **not the same claim**. B133 does not mention PB-KEY-5 at all. **The consequence, stated rather than resolved:** with `setUserAuthenticationRequired(false)` the content key is reachable after first unlock **by design**, so this criterion is **FALSIFIED, not narrowed** — a narrowed requirement keeps a true residue and this one does not. It must not be reworded into something true. Recording it as an owed decision rather than deciding it here, because reclassifying a criterion is a disposition and dispositions belong in the ADR. | Test: an attacker with after-first-unlock access and everything at rest reaches no content key. **See the FLAG in the requirement column: this criterion is falsified by B133 and a decision on it is owed.** `PerRoleCustodyTest.kt`'s two after-first-unlock attacker tests fence exactly this and are therefore unrepairable — only removable or re-premised. They are also the phase's most dangerous vacuous-green candidates, since they read as its central security claim and would pass against a fake's lock. |
| PB-KEY-8 | **A platform capability matrix, so the custody design is implementable on real devices.** The protocol needs X25519 raw-key operations for Noise, `nacl/box`-compatible anonymous sealed-box opening (`internal/remote/crypto/keystore.go:163`), wire-compatible Ed25519 signatures, per-use biometric authorization for some roles, and locked-device relay auth for another — but Curve25519 entered KeyMint only in Android 13 and hardware backing is device-dependent. For each of `{NoiseStatic, Recipient, CommandSign, RelayAuth}` state whether it is generated/used natively in Keystore, held as an app-format key wrapped by an authenticated Keystore AES key, or software-only with a documented residual. Bind this to PB-RUN-1's minSdk. **NARROWED 2026-07-31 (ADR-007 B133) — the matrix no longer expresses auth-gated key generation for any role.** Two clauses above lose their subject: "**per-use biometric authorization** for some roles" and "**locked-device** relay auth for another" — with no gate, no role is per-use authorized and every role is usable whenever the app is running, so the second was only ever meaningful as the complement of the first. **The matrix itself survives in full**, and so does everything it exists for: X25519 raw-key Noise operations, `nacl/box`-compatible sealed-box opening, wire-compatible Ed25519, the Android-13 Curve25519 KeyMint floor, and the per-role native / Keystore-wrapped / software-only decision with its documented residual. Dropping an authenticator from a capability matrix is a **NARROWING**, which ADR-007 B8 permits; widening it back is not. | Wire KATs against the current Go implementation for every role; a defined refusal/fallback when the handset lacks the required algorithm or auth capability; PB-E2E-5 asserts the achieved backing via `KeyInfo`. **AMENDED 2026-07-31 (ADR-007 B133): "or auth capability" is struck** — a handset with no enrolled biometric is no longer a handset this product refuses, which is the strand B59 priced and B132 met (a Galaxy A26 with a PIN and no fingerprint, behind six inoperable controls). **The ALGORITHM refusal is untouched and stays fail-closed**, including the secure-hardware floor whose correctness PB-E2E-2's re-scoping rests on. A capability enum entry that survives with no producer makes its test vacuous rather than passing. |
| **PB-KEY-9** | **The Go core must expose a FAILABLE, SEALABLE custody seam, or PB-KEY-6 and PB-SEC-1 are unsatisfiable from anywhere on the Android side.** Found by the S14 RED author with machine-checkable evidence; both halves verified independently. **(a) Failability.** ADR-007 **B14 already decided** that `crypto.KeyStore` becomes failable and that "the signature change lands in the Go core, not the Android slice" -- but it was never implemented: `SignCommand(msg []byte) []byte` and `SignRelayAuth(challenge []byte) []byte` are still errorless and `NoiseStatic() *NoiseStatic` still materialises the raw private scalar (`internal/remote/crypto/keystore.go:47-56`). PB-KEY-6's criterion is "a test drives an auth-required failure and a key-invalidated failure through every signing path", and **no test in `android/` can drive a failure through a Go function that has no error return.** A decided ADR item that no slice delivered. **(b) Sealing.** PB-SEC-1 requires key material at rest to be sealed under a Keystore-backed KEK. Today nothing is: `<stateDir>/device.key` is **128 RAW bytes** -- all four device private scalars concatenated, one file, one tier, in the clear (`internal/remote/crypto/keystore.go`, reached via `phonecore.openKeyStore`) -- and `<stateDir>/phone-state.json` carries `wake_key` and `content_key` as plain base64 (`internal/phonecore/state.go:176-177`). The second **also collapses PB-KEY-2's two-tier split at rest independently of anything Kotlin does**: one file cannot be gated two ways, so the content key is recoverable without the biometric the entire tier design exists to require. Android cannot fix either: Go opens these files at `phonecore.Resume` and rewrites them while the app runs, `internal/remote/crypto` is frozen behind this same ADR, and the gomobile facade is golden-pinned with no verb to add one. **A Kotlin staging dance (materialise on unlock, delete on lock) was considered and rejected** -- it cannot work for `phone-state.json`, which Go rewrites continuously, so it solves half the problem and adds a race. | The seam is injectable at `phonecore.Resume` and covers both the epoch-key state file and the device-key file, with the KEK supplied from outside the Go core. **S15's PB-STATE-6 needs the same mechanism**, so it is built once here rather than twice or wrongly. Acceptance is a test that reads the BYTES ON DISK, not a declaration: the S14 RED author added exactly that (`android/gate/keycustody_test.go`) after noting a Kotlin test reading an at-rest inventory could be made green by writing `sealedByKeystore = true` next to a file in the clear -- this project's standing "requirement satisfiable while the defect ships" class. Failability acceptance is B14's own: an auth-required failure and a permanent-invalidation failure driven through every signing path. **FLAGGED 2026-07-31 (ADR-007 B133) — DISPOSITION OWED, NOT TAKEN HERE.** B133 records this row UNAFFECTED and the **seam is**: sealing at rest defends offline extraction of the app data directory, which is an extraction-side concern the new boundary does not trust, and the 128 raw bytes of `device.key` are just as raw whoever is holding the phone. **Two things in this row nonetheless changed meaning.** (1) The sentence *"the content key is recoverable without the biometric the entire tier design exists to require"* described a defect; under B133 it describes the **design**, so it must not be cited as an open finding. (2) The failability acceptance above inherits PB-KEY-6's flag — **"an auth-required failure" has no producer** once keys are generated with `setUserAuthenticationRequired(false)`; the permanent-invalidation half keeps one. **The at-rest, bytes-on-disk acceptance is untouched and is the part that matters most now**, since it is the only thing standing between an extracted app data directory and four device private scalars. |
| **PB-KEY-10** | **The phone can never OBTAIN an epoch key, so every other requirement about sending, typing or opening a frame is unreachable in production.** Found by a fixture-seeding audit and verified independently: `State.Keys` is written ONLY by `InstallWakeKey`/`InstallContentKey` (`mobile/app.go:369,386`), which are inbound from Kotlin -- and **nothing supplies those bytes**. The machine delivers the epoch key as a sealed `crypto.EpochGrant` in a tagged bootstrap frame appended to the phone's mailbox (`cmd/swarm-remote/deliver.go:29-40`), whose own comment says the phone consumes it BEFORE it can build a ContentKey-keyed router "(the grant is what DELIVERS the ContentKey)". But `phonecore.AcceptGrant` has exactly ONE production caller and it is `internal/phonesim` -- the test simulator. `MailboxRouter.TakeGrant` has **zero** production callers; its own comment says "route+expose only" and defers consumption to a work item ("C5") that was never done. `Core.Grants()` has zero callers anywhere. Kotlin cannot supply the bytes either: the custody surface is inbound-only by design (ADR-007 B8) and the golden-pinned facade contains **no verb that ingests a grant**. **Consequence on a real handset**: `resolveSend` returns `errNoContentKey` for every send -- take_control, kill, launch, input, paste, resize -- and every inbound frame fails to open, so the relay cursor never advances and the drain polls the same page forever. **Why the suite is blind**: no test in `mobile/` calls `App.BeginPairing`; the conformance harness states out loud that it seeds durable state "rather than running a pairing handshake"; and even the PB-NET-1 real-wire test generates the epoch keys in-test and hands the content key to `InstallContentKey` -- **the no-fakes test performs by hand the exact step the facade is missing.** Standing class (v), and the most severe instance found in Phase B. | The facade consumes the bootstrap grant on the real first-run path: pair a FRESH install, and without any test calling `Install*Key`, assert the phone holds the epoch key and can both send a signed command and open an inbound frame. Rotation must work the same way (see PB-KEY-4). The bootstrap frame must also be acked/compacted -- today `AcceptCommit` acks only on stale-seq or stale-age, so an unopened bootstrap frame is never compacted from the relay mailbox. |
| PB-KEY-6 | **`crypto.KeyStore` must become failable.** *(Sequencing: the **signature** change is hoisted into S7 because S6/S7/S8 all consume this interface — leaving it at S14 in stage 2 would guarantee rework across every Go-side slice. Only the Android **implementation** stays at S14.)* `SignCommand(msg []byte) []byte` is errorless and `NoiseStatic() *NoiseStatic` exports raw private material — neither is implementable against Android Keystore, which never exports private keys and whose every operation can fail (user-auth-required, and permanent invalidation on biometric-enrollment change, which PB-SEC-2 explicitly requires). PB-SEC-2's "Keystore-enforced sign authorization" is unimplementable until this changes. `crypto` is inside PB-BIND-0's allowlist, so this is a cross-cutting interface change that must be owned by a slice. **AMENDED 2026-07-31 (ADR-007 B133) — two references above point at a VOID requirement, and the criterion has a FLAG.** B133 records this row as UNAFFECTED and the **requirement is**: Android Keystore never exports private keys and its every operation can fail, so a failable interface is required regardless of who is authenticated. What no longer holds is the *rationale*: "permanent invalidation on biometric-enrollment change, **which PB-SEC-2 explicitly requires**" and "**PB-SEC-2's** 'Keystore-enforced sign authorization' is unimplementable until this changes" both cite a requirement that is now VOID, and `setInvalidatedByBiometricEnrollment` has no subject on a key generated with `setUserAuthenticationRequired(false)`. **FLAGGED, DISPOSITION OWED:** the acceptance criterion's "an **auth-required** failure ... through every signing path" loses its producer exactly as PB-PUSH-4's clause does — a non-auth-bound Keystore key does not raise user-not-authenticated. The **key-invalidated** half keeps a producer (app-data clear, uninstall, factory reset, KeyMint failure), so this narrows rather than falsifies, but B133 did not say so and the call is not mine to make. | Interface returns errors; a test drives an auth-required failure and a key-invalidated failure through every signing path. **See the FLAG in the requirement column: the auth-required half has no producer once the gate is deleted.** `GoCustodyFailureTest.kt`'s `AUTH_REQUIRED`-token classification is on the vacuous-green list for precisely this reason — minimally patched to compile, it would keep passing while fencing a failure the platform can no longer raise. |
| PB-KEY-7 | ~~**Lock purges live memory.**~~ **NARROWED 2026-07-31 (ADR-007 B133) — the MECHANISM survives whole; only its TRIGGER moves.** Original text, kept for the record: *"Invalidating the biometric gate is not enough: `MailboxRouter` holds `ContentKey` by value for its lifetime and caches decrypted sessions/snapshots (`internal/phonecore/snapshot.go:88,132`), so 'locked device cannot decrypt' can pass while the process still holds the key and already-decrypted content. On lock, background, or auth expiry the core must stop content operations, zeroize/discard native key custody, purge decrypted session/snapshot/reply caches and sensitive UI state, and require a fresh unwrap before restoring content."* **The three named triggers — lock, background, auth expiry — no longer exist as events.** The requirement becomes: **on revoke/unpair** the core must stop content operations, zeroize/discard native key custody, and purge decrypted session/snapshot/reply caches and sensitive UI state. **The purge is not optional and its reason is unchanged**: `MailboxRouter` still holds `ContentKey` by value, so a revoked device that keeps a resident key and decrypted content has not been revoked in any sense the owner would recognise — and B133 makes revoke-from-the-computer the *only* surviving mitigation, which raises this row rather than lowering it. **Three expectations need re-deciding rather than renaming, and are NOT decided here** (B133; deauth-plan "Design decisions OWED"): whether the wake tier is purged too, whether the content tier is recoverable without a re-grant, and whether watermarks and the op queue are carried across. Note PB-STATE-9(2)/(3) already bound the answer in part — the replay-guard counters are non-purgeable and `PendingOps` must survive a purge — so a purge that takes them would brick the phone. | ~~Test asserts no content key and no decrypted session content remains reachable after lock.~~ **AMENDED 2026-07-31 (ADR-007 B133):** test asserts no content key and no decrypted session content remains reachable **after revoke/unpair**, paired with the negative half that the non-purgeable coordinates PB-STATE-9 pins (send-seq ceiling, receive high-waters, `PendingOps`) are **not** destroyed by it. A test driving a lock event is fencing a state production can no longer enter. |
| PB-KEY-3 | **Epoch-grant recovery.** Today a grant can be lost with no recovery: the relay refuses appends past the mailbox depth cap (`server.go:743-747`) and `SweepRetention` purges items older than `RetentionCap` (**default 7 days**, `config.go:90`) **even if never acked** (`server.go:1136-1139`); no re-grant verb exists anywhere. A phone offline across a rotation is then permanently unable to decrypt, and re-pairing is refused because `BeginPairing` fail-fasts while a device is registered. | Either a re-grant request path, or a defined user-legible terminal state plus a documented machine-side unblock. A test drives the offline-across-rotation scenario to a defined, recoverable end — not an indefinite decrypt-failure loop. |
| PB-KEY-4 | Key rotation while the app is backgrounded/offline is handled without data loss or silent breakage. **It must update the device record's `GrantedEpoch`**: `reconcilePairedDevices` removes any device whose `GrantedEpoch != curEpoch` on every daemon start (`internal/skeleton/serve.go:499-505`), so a re-grant or offline-rotation convergence that does not update it **silently unpairs the only device on the next restart**. §7's deferral of "the epoch-equality reconcile revisit" is scoped to *multi-device*; single-device re-grant hits the same mechanism and is therefore in scope here. | Test: rotate while offline, reconnect, converge, **restart the daemon**, and assert the device is still paired. |

### 6.5 PB-NET — transport (§4.5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-NET-1 | The real `relay.Client` drives the core through the façade; the `phonesim` mailbox seam (`phonesim.go:42`) stays for testability. | Integration test: façade + real client + real in-process relay: pair -> read -> ack -> append. |
| PB-NET-2 | TLS verified by default; a pinned self-signed cert is an explicit opt-in for self-hosted relays; cleartext refused **except** an explicit, narrowly-scoped loopback carve-out for the in-process test relay — which is `ws://`-only today (`server.go:228`), making v1's unconditional ban self-contradictory (fable F6). The Go client's **trust-root source on Android must be stated** (embedded bundle vs pinning-only); `x509.SystemCertPool` is not usable as on desktop (opus H3). | Tests: bad cert fails closed; non-loopback cleartext rejected; the carve-out cannot be enabled in a release build **BY EXTERNAL CODE — AMENDED 2026-07-26.** The criterion as written is now false and the code is right: `MachineSecurity()` sets an **unexported** `loopbackInRelease` honoured in release builds, in three production binaries, because the exit demonstration spawns the real sidecar against a loopback relay and `testing.Testing()` is false there. **The property that actually holds, and that the test enforces, is that no package outside `internal/remote/relay` can enable it** — the field is unexported and only that one constructor sets it, admitted only for a loopback **IP literal** (never a name, re-checked per redirect hop, and withdrawn outright by a configured pin). A round-2 reviewer attacked that mechanism specifically — userinfo confusion, decimal/hex/octal literals, IPv4-mapped IPv6, hostname resolution, redirect into cleartext, and whether the websocket library replaces the caller's redirect policy — and found no path to a non-loopback cleartext dial. **The carve-out is defensible; the un-amended criterion, sitting beside a test named for it, was the defect**; pinning accepts only the pinned cert. |
| PB-NET-3 | The transport handles only opaque sealed frames and never holds content keys. | Test asserts a known plaintext marker never appears on the wire. |
| PB-NET-4 | Resilience: automatic reconnect, bounded exponential backoff with **stated numeric** ceiling and jitter, re-auth after reconnect, connection state surfaced. **Input and resize are never queued or replayed** (ADR-007 D7 `:60-62`: live-only, "delivery unknown / not sent"); **AMENDED 2026-07-30 (ADR-007 B90) — THE QUEUE CLAUSE IS WITHDRAWN, resolving a contradiction this specification carried against itself.** This row required "only high-level idempotent ops may queue, with a stated bound" while §6.0 declared that same queue **WITHDRAWN as unbuildable** — and §6.0 is right, on concrete grounds: a queued op is a *pre-signed* `DeviceCommandAuth` stamped with `ExpiresAt = now + CommandTTLFor(action)`, one minute for an ordinary op, so **anything that sat in a queue long enough to need queueing would be expired when it left.** Production matches the withdrawal rather than this row: `NewOpQueue(0)` is unbounded and `Enqueue` has **zero production callers**, which a round-5 reviewer found and which is what forced this adjudication. **Two rows disagreed for four days and each was individually defensible — nothing checks a specification against itself.** What remains required here is the resilience half, which is implemented and fenced: reconnect, bounded backoff with a stated ceiling and jitter, re-auth, and surfaced connection state. The dead `OpQueue` type is left in place deliberately and recorded as dead rather than deleted under audit pressure; removing production code to close a requirement is a change that deserves its own slice. | Tests against a flapping relay assert the retry ceiling, state transitions, re-auth, that no keystroke is ever replayed, and the idempotent-op queue's bound and drop signal. |
| PB-NET-5 | **Low-latency input across BOTH hops.** The mechanism is a stated protocol change — request-id correlation with concurrent dispatch, or an explicit server-push frame — because §4.5 proves a naive long-poll head-of-line-blocks the keystroke path and a second connection is not available. It must also drop the gateway's 500 ms command-IN poll (`service.go:27`), which ADR-007:461 calls "unusable for live typing"; a phone-side-only fix passes v1's criterion while typing stays 500 ms-gated (fable F4). Interaction with presence must be stated. *(v2 described this wrongly: `SweepPresence` (`internal/remote/relay/server.go:1105-1132`) fires when the MACHINE's presence entry times out, toward paired-peer tokens — the phone's own connectivity is never consulted. The interactions actually worth stating are that a GATEWAY parked in a wait keeps the machine's presence online, and that pushes fire redundantly at an already-connected phone.)* | **Acceptance is end-to-end and bidirectional, against §6.0's numbers**: (a) with a wait outstanding, a keystroke append from the same client completes within 50 ms; (b) phone `Type` -> PTY write measured end-to-end at p50 <= 150 ms / p95 <= 400 ms / p99 <= 800 ms over n >= 200; (c) cancellation, max pending waits, quota accounting, and reconnect behavior each tested; (d) the Phase A per-source connection cap and cumulative handshake deadline still hold, and the newest-wins takeover property is not weakened. |
| PB-NET-6 | Phase A's relay-adversary properties hold through the real client **across process restarts** — seq gating, replay/reorder/dup rejection, mailbox cap, hostile-pagination termination. (v1's single-process criterion was satisfiable while the property was false on a handset — opus M1.) | The Phase A adversarial suite runs against the real client, **plus** the PB-STATE-2 restart case. |
| PB-NET-7 | Hygiene: timeouts everywhere, cancellation honored, no goroutine leaks across connect/disconnect cycles. | `-race` + goroutine-leak assertion over repeated Start/Stop. |

| **PB-NET-8** | **THE MACHINE HOP RECOVERS FROM A DEAD RELAY LINK WITHOUT A HUMAN, AND NO SUPERVISION POLICY WRITTEN AGAINST PROCESS *EXIT* CAN DO IT (NEW 2026-07-31, ADR-007 B120/F1).** PB-NET-4 says "automatic reconnect" without naming a hop and is fenced on the PHONE's; the gateway's was absent for the whole project and **no row required it**, which is why seven rounds of re-deriving rows could not find it. `remotegw.Service` is handed ONE already-dialled client and cannot redial: its journal loop reconnects to the **daemon**, its command loop retried a dead client forever, and `relay.Client.Done()`/`Err()` — which exist precisely to notice a drop without issuing a request — had **zero production callers**. So a desktop WiFi blip ended remote control until a human restarted the sidecar: the process never exited, therefore launchd's `KeepAlive{SuccessfulExit:false}` and systemd's `Restart=on-failure` (PB-LIFE-1/-5) never fired, **a supervision policy written against exit cannot restart a zombie**, the phone meanwhile reconnected and reported `online`, and nothing appeared in any log. **REQUIRED**: the gateway watches the relay connection itself; a link that dies ENDS the generation and is reported as its own condition (distinct from shutdown and from a revoke); the owning process REDIALS on §6.0's reconnect backoff, resetting it only on evidence that **traffic crossed** the link, never on a successful dial; a cancelled parent is a shutdown and is NOT redialled; conditions no redial can fix are terminal and returned rather than retried forever; each generation is REBUILT (every lease conn and terminal peek torn down, so the daemon is not left holding a control gate for a severed phone) while the durable coordinates — outbox, inbound checkpoint, the three seq sources — are resolved ONCE and carried across, so a redial **resumes** rather than restarts. The liveness observer is discovered by type assertion, so it must be pinned at **compile** time on the production client: a client that quietly stopped reporting liveness would not fail the build, it would fail to reconnect, silently, in the field. | Tests against a REAL relay: cut the link under a running `Service` and assert (a) `Run` returns with the link-gone condition rather than parking; (b) the owning loop redials with no human action and traffic flows again over the NEW connection; (c) the gap between redials grows on §6.0's backoff instead of hammering; (d) a cancelled context returns instead of redialling; (e) a compile-time assertion binds the production client to the liveness interface. |

### 6.6 PB-SYNC — per-stream gap repair (rewritten; codex#2, opus H4, fable F11)

v1 assumed one journal reseed repairs everything. Verified false: the phone receives **multiple
independent sealed streams** — journal and terminal snapshots share one seq stream
(`internal/remotegw/relaysink.go`, `Terminal` seals "on the SAME seq stream as the journal"),
command replies use a **separate sender bucket** by deliberate design (`command_in.go:104-109`,
"Do NOT unify SenderKeyID"), and the grant is a third kind. Worse, `RelaySink.Snapshot(roster,
_ uint64)` **discards the journal cursor**, so a journal cursor is not an envelope-seq
coordinate at all.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SYNC-1 | **Staleness is tracked per SEQ BUCKET; repair is per CHANNEL.** v2 required "a gap in one stream marks only that stream stale", which round 2 proved **impossible**: journal and terminal frames share one `(sender, epoch)` sequence space, and `MailboxResult` carries only `{Plaintext, Gap bool}` (`internal/remote/crypto/envelope.go:195-200`) — a bare boolean with no frame kind — so a skipped seq cannot be attributed to journal-vs-terminal. There are **TWO seq buckets and FOUR repair channels** — corrected 2026-07-30 from "three buckets ... and four repair channels" (ADR-007 B85), which counted the grant as a seq bucket. It is not one, and the miscount is dangerous rather than cosmetic: it invites a reader to believe there is a spare discriminator value to spend. The two buckets are the **shared journal+terminal+reconcile** bucket (the machine's routing key id) and the **command-reply** bucket. **The discriminator is `Bucket{SenderKeyID, EpochID}`, and `streamsOf` (`internal/phonecore/snapshot.go`) is the ONLY place the bucket→channel mapping lives.** A gap in the shared bucket must conservatively stale **both** journal and terminal. **THE GRANT IS NOT A SEQ BUCKET.** The bootstrap grant is raw plaintext (`grantwire.BootstrapKind`) that never becomes an envelope, so it has no sender, no seq and no bucket; the rotation grant *is* content-key-sealed and therefore rides the **shared** bucket. Grant staleness is set by `Core.MarkGrantLost` (install-time replay detection) and cleared by `installGrant` — entirely outside the seq machinery. **Consequence, so nobody reasons past it:** a gap that swallows a rotation grant stales journal and terminal and leaves grant **healthy** — the channel that lost data is not the one flagged. **This is NOT a live hole and must not be "fixed" into one:** a missed rotation is caught at install time, and a rotation changes `EpochID` and therefore the bucket, so the phone cannot open the new epoch's frames at all and fails closed rather than proceeding over stale content. **AND THE CONSTRAINT ON ANY NEW DISCRIMINATOR (B84/B85, measured):** a direction or kind tag may live in neither `SenderKeyID` nor `EpochID`, because both ARE the bucket key — a value in either forks the buckets per tag or collides two streams into one. A collision was measured: the reply bucket's high-water jumped 2→40, the colliding frame's content was dropped while still being acked, the staleness landed on the wrong channels, and the next genuine reply was refused `ErrStaleSeq`. Since command replies carry the lease confirmation and PB-INPUT-2 forbids a keystroke without a confirmed generation, that costs typing for the life of the epoch. Tag inside the AEAD-covered plaintext (the existing `kind`), or in a header field that is not part of `Bucket`. | Test: a gap in the shared bucket marks journal AND terminal stale; a command-reply gap marks neither. Attributing a shared-bucket gap to one channel is a failing implementation. **And no frame clears a channel that is not its own repair** — only a contiguous roster+events reseed clears journal, only a fresh snapshot clears terminal, and NOTHING clears the reply channel (PB-SYNC-2's "or the stream stays unresolved"); the fence must drive a **contiguous** frame AFTER a gap, since the frame that opens a hole can never be the one that clears it (`internal/phonecore/b85_replyclear_test.go`). |
| PB-SYNC-2 | Repair per stream: journal via an atomic roster+events snapshot; terminal via a fresh full snapshot (a journal reseed cannot repair a missed grid); command replies via the durable operation outcome, or the stream stays unresolved; grant via PB-KEY-3. | Test per stream, including that a journal reseed alone does **not** clear terminal staleness. |
| PB-SYNC-3 | `Stale()` clears only after a successful reseed **of that stream**, committed atomically with the matching transport watermark. Failed resync stays stale (fail-closed). | Test asserts no optimistic clearing and no watermark/coordinate confusion. |
| PB-SYNC-4 | **Authorization is specified correctly.** v1 claimed the resync rides `requireRemoteAuthz`; it does not — `handleJournalRead` gates on the negotiated `journal` capability and the kill switch only (`internal/protocol/server.go:1657-1683`), while `requireRemoteAuthz` guards the mutating ops. The requirement must state which gate applies. | The chosen gate is implemented and tested; an unauthorized resync is refused. |
| PB-SYNC-5 | If the resync is device-signed, a new `Action*` constant must be added **and mapped** in `actionClass` (`internal/skeleton/deviceauth.go:17-26`), a closed switch that fails closed on unknown actions. The capability-tier consequence must be decided: the only fitting existing class is `ActionControl`, which would make a read-repair require the control tier, and `rec.Capability` is pinned at enrollment (`pairing.go:205`) and never read from the wire — so an observe-tier device could never resync. | Decision recorded; test asserts the intended tier can resync and the unintended one cannot. |
| **PB-SYNC-7** | **A machine->phone reconciliation frame must exist, or PB-STATE-4 is unimplementable and the phone bricks.** PB-STATE-4 (added in round 3) names three rollback authorities, but the phone's entire inbound plaintext set is journal record, terminal snapshot, `command_reply` and epoch grant (`internal/phonecore/snapshot.go:27-30`) — **none carries the gateway's inbound high-water, its outbound ceilings, or the daemon's grant-issuance coordinate**, and no `Action*`, no façade method and no ADR item introduces one. Read literally, the authority is permanently unreachable, so PB-STATE-4's "fail closed for mutating ops" becomes **permanent**: `take_control`, `launch` and `kill` all refused, and "launches"/"types" fail. This is exactly the defect the spec already caught for push preferences (PB-PUSH-8, "local filtering is not sufficient") and then reintroduced for its own rollback anchor. Define the frame: fields, seal tier, seq bucket, first-connect and post-rotation bootstrap, and relay-withholding = fail-closed. Fold in the two adjacent missing verbs: a **lease confirmation** (today `routeCommand` returns nil for `take_control` with no reply sealed, `internal/remotegw/command_loop.go:225-241`, so PB-INPUT-2's "no keystroke without a confirmed lease generation" has nothing to confirm against) and a **reply correlation id** (`replyOK`/`replyError` omit `OperationID` although `Control` carries the tag, and `ReplyCache` is an unkeyed FIFO — PB-SYNC-2, PB-STATE-1, PB-INPUT-4 and PB-APP-9 all need attribution). | **Carrier (recommended, avoids a protocol trap)**: the gateway seals a reconcile record onto the *existing* machine->phone outbound stream, so no new signed device action is needed. A phone-*initiated* signed reconcile instead walks straight into PB-SYNC-5's trap — `actionClass` is a closed fail-closed switch (`internal/skeleton/deviceauth.go:17-26`) and capability is pinned at enrollment, never read from the wire. The frame needs a pinned schema (§9 rule 4). Owned by a slice ordered **before** S7, since PB-STATE-4 consumes it. Tests: each authority is obtainable, a withheld frame fails closed rather than bricking, and a lease is never assumed without its confirmation. |
| PB-SYNC-8 | **A journal reseed must REPLACE the phone's cache cursor, not merge into it — otherwise the designated repair channel is a no-op.** The daemon emits roster records with `Cursor` **deliberately unset (0)** — "a roster record is a set member keyed by SessionID, NOT a point in the cursor-ordered event stream" (`internal/daemon/journal.go:60-73`) — while `SessionCache.Apply` drops any record with `rec.Cursor < c.cursor` (`internal/phonecore/journal.go:110-115`). So once the first event advances the cursor, **every subsequent roster snapshot is silently discarded**, and `Gateway.RunJournal` re-snapshots the roster on every daemon reconnect into that dead path. PB-SYNC-2 makes "journal via an atomic roster+events snapshot" *the* journal repair channel, so it is unimplementable as written. The daemon also states the roster is the **only** enumeration path for reconcile-adopted Running sessions, which would be permanently invisible. | Either the reseed replaces the cursor wholesale, or roster records carry `res.Cursor`. **Plus a fixture rule**: no test may use a nonzero roster cursor, since production never emits one — that is precisely why the existing fixtures hide this. |
| PB-SYNC-6 | Resync is bounded and non-amplifying; a hostile relay cannot drive unbounded work. | Non-advancing pages terminate (`errStuckPage` discipline); a **stated** rate bound on resync attempts. |

### 6.7 PB-INPUT — live-input and lease semantics (NEW; codex#10, opus Md5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-INPUT-1 | Input/resize are live-only per ADR-007 D7: never queued, never replayed; a disconnect resolves as an explicit **"delivery unknown / not sent"** surfaced to the user. | Test asserts no replay after reconnect and that the UX state appears. |
| PB-INPUT-2 | Lease lifecycle is defined across gateway restart, daemon restart, session exit under the user, app backgrounding, and process death; input is suppressed until a new lease is visibly confirmed. | Test per event; no keystroke is ever sent without a confirmed current lease generation. |
| PB-INPUT-3 | Lease TTL expiry mid-use has defined UX. **The 30-minute figure is not the operative one**: the lease is the *earliest* of `now+maxControlSessionTTL`, `now+TTLSeconds`, and the device-signed `ExpiresAt` (`internal/protocol/server.go:1500-1504`), so with PB-TIME-1's "phone signs `now + 1 minute`" the real lease is **60 s** — and PB-INPUT-5's ">= 60 s sustained typing" test sits exactly on it, as does §6.0's 60 s biometric freshness. Three independent 60-second walls collide. **AMENDED 2026-07-31 (ADR-007 B133) — there are now TWO walls, not three.** §6.0's biometric-freshness row is VOID, so the third wall is gone; the signed-`ExpiresAt` wall and PB-INPUT-5's sustained-typing floor both stand unchanged, and §6.0's 15-minute `take_control` exception is still what keeps the lease from being the binding constraint. **This row's UX obligation is untouched.** **What it must NOT be read as answering** — B133 records it as an open behaviour decision, and it is owed before any Go comment pass touches those lines: `internal/phonecore/lease.go:52,198` justify the 15-minute TTL partly by the 60 s freshness window and list *"a biometric-freshness lapse"* among the reasons the phone ends its own lease. **With no freshness to lapse, what severs a lease on backgrounding is undecided.** The candidates are not equivalent (sever on background, sever on transport loss only, or keep a timer under a non-authentication justification), and silently dropping the clause while rewording the comment would be a behaviour change made under cover of a documentation edit. §6.0 now sets the signed `ExpiresAt` to 15 min for take_control so the lease is not the binding constraint, while command TTL stays 60 s. | Test asserts a typing session survives well past 60 s, and that expiry when it does arrive has defined UX rather than silent keystroke loss. |
| PB-INPUT-4 | Retry policy is keyed on stable server error codes, never blind resend. **AMENDED 2026-07-30 (ADR-007 B92) — the RETRY clause presupposes a mechanism ADR-007 D7 forbids for this family, and the BINDING half holds by construction.** D7 makes raw `input`/`resize` **live-only**: never durably queued, never replayed, and on disconnect a keystroke resolves to an explicit *"delivery unknown / not sent."* Production matches that exactly — `mobile/commands.go` calls `MailboxAppend` once and returns its error. **It never resends, so "never blind resend" is satisfied absolutely rather than by policy.** The `RetryFor` table and `SendLive` exist with **zero production callers** (found by the round-4 external reviewer, confirmed twice since), and they are dead code for a retry this family may not perform. **The requirement is met on the clause that binds; the retry clause is withdrawn for input.** The dead table is left in place and recorded as dead rather than deleted under audit pressure — the same ruling as B90 made for the op queue, and for the same reason: removing production code to close a requirement deserves its own slice. | Test maps each error class to its policy — **and, post-amendment, that the input path performs no resend at all.** |
| PB-INPUT-6 | **Coalescing must preserve ordering and flush at every boundary.** A sustained-rate test alone would pass while the last buffered keystrokes are lost whenever the user releases control. Required: byte-order preservation across frames; flush before resize; flush before release/take_control_end; defined handling of buffered input on background, auth expiry and disconnect (flushed or explicitly reported as "delivery unknown" per PB-INPUT-1, never silently dropped); a max coalesced payload (§6.0); and stated treatment of paste and IME composition, which are not keystroke streams. | A test per boundary asserts no reordering and no silent loss. |
| PB-INPUT-5 | **Input must be coalesced to stay under the relay's quotas.** `MailboxAppendPerMin: 600` (`internal/remote/relay/config.go:99`) allows 10 appends/s — `OpsPerMin` does **not** apply, since `handleMailboxAppend` never calls `meterOp`, so a ~30 Hz key-autorepeat or fast interactive typing trips `codeQuotaExceeded` mid-lease after roughly 20 s — while short-burst latency tests still pass. Coalescing bound per §6.0 (<= 8 frames/s sustained, one frame per 125 ms). | **A sustained-typing acceptance test** (not a burst): continuous input for >= 60 s at autorepeat rate stays within quota and loses no keystrokes. |

### 6.8 PB-TIME — clock skew (NEW; codex, opus Md1, fable F7)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TIME-1 | The phone signs `ExpiresAt = now + 1 minute` and the daemon rejects expired commands (`internal/skeleton/deviceauth.go:74-76`) and also `ExpiresAt > now + maxCommandValidity` (1 h; const at `internal/protocol/server.go:164`, enforced in `requireRemoteAuthz`). The usable window is therefore roughly "phone ≤1 min slow" — a handset two minutes behind fails **every** command with an opaque "not authorized". Skew must be detected, bounded (§6.0: ±30 s), and surfaced distinctly. **The ±30 s budget governs SURFACING, not local rejection** (§6.0 amended 2026-07-25; its one-line row previously read "reject and surface" without saying who rejects, which contradicted this row's own surfacing-only criterion). Refusing locally at ±30 s was considered and **rejected on merits**: the daemon's real accept band is far wider — skew(machine−phone) in [−59 min, +60 s] for ordinary commands (`deviceauth.go:74` plus `maxCommandValidity` 1 h at `server.go:164`) and [−45 min, +15 min] for `take_control` — so a phone 45 s out is outside the budget yet fully served, and a local refusal would refuse a command the machine would have honoured. **Recorded gap**: the daemon's own refusal is still opaque — `server.go:2427` replies `"device command not authorized"` and DISCARDS `authorizeCommand`'s specific `"command expired"` — so the distinct explanation reaches the user only on the phone's event plane (`reportSkew`), never on the outcome plane. Closing that needs a distinguishable expiry code from the daemon; not authorised in Phase B. | Test with a skewed phone clock asserts a distinct, user-legible error (not the generic authorization failure). |
| PB-TIME-3 | **A skew-detection protocol, since neither the relay nor an unauthenticated wall clock may be the authority.** A two-minute-slow phone is inferable from an expired command, but a 31-second skew is not measurable from `ExpiresAt` alone. Requires: an authenticated machine-time exchange, an RTT allowance, a stated monotonic-vs-wall-clock split, and defined offline behavior. | Boundary tests at exactly +/-29, 30 and 31 s; a test that the relay cannot influence the phone's notion of machine time. |
| PB-TIME-2 | **Every security-relevant timestamp** has a stated authoritative clock and skew behavior, not just command expiry: envelope `IssuedAt` (and the PB-GW-2 bounded-age check that consumes it), push expiry/replay, QR/rendezvous expiry display (`RendezvousTTL` 60 s), lease TTL display (`maxControlSessionTTL` 30 m), reconnect timers, and cached-state freshness. **Includes the machine->phone direction, where PB-GW-6's trap is mirrored and still open**: `remotegw.SealControlReply` (`internal/remotegw/command_in.go:117`) seals command replies with **no `IssuedAt`**, while the journal/terminal path does stamp (`relaysink.go:432`). So if any slice enables a bounded-age check on the PHONE's receiver, command replies compute a ~56-year age and brick exactly as inbound would have — the same defect, one direction over and one slice later. Found by the S7b RED author. | Each timestamp's authority and tolerance recorded and tested. **The reply-seal gap must be closed before S11**, and the test is the mirror of PB-GW-2's honest half: real machine-sealed replies still pass with the bound on. |

### 6.9 PB-RUN — Android runtime model (NEW; codex#6, opus Md3, fable F9)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-RUN-1 | `minSdk` and `targetSdk` are chosen and recorded with a supported-version matrix (the gomobile `-androidapi` floor is the NDK's, not the app's). | Recorded; build enforces it. |
| PB-RUN-2 | Runtime permissions are handled with denial paths: `CAMERA` (PB-PAIR-2) and `POST_NOTIFICATIONS` (API 33+, without which PB-PUSH-4's notifications are silently dropped). | Tests for granted/denied/permanently-denied per permission. |
| PB-RUN-3 | An explicit foreground/background connectivity policy: what the socket does on backgrounding, Doze, App Standby, and battery saver; whether a foreground service is used and with what `foregroundServiceType`. | State machine documented and tested; the policy is compatible with PB-NET-5's waiting mechanism. |
| PB-RUN-4 | FCM message priority is chosen deliberately (normal-priority is deferred in Doze; high-priority wakes the device but is quota'd). | Decision recorded; behavior tested. |
| PB-RUN-5 | Lifecycle events do not corrupt state: force-stop, reboot, app upgrade, and network handoff (Wi-Fi <-> cellular) all converge. | Tests per event, composed with PB-STATE-2. |

### 6.10 PB-LIFE — gateway lifecycle (corrected; codex#5)

Verified: no unit files exist; `swarm-remote` is not in the release matrix
(`.goreleaser.yaml:12-15`); the gateway is started by hand. ADR-007:50 requires an **external**
supervisor and forbids the daemon spawning it.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-LIFE-1 | launchd plist + systemd unit generated from one source, running as the owner, restart-on-exit. | `plutil -lint` passes; test asserts restart policy, user, and no embedded secrets. |
| PB-LIFE-2 | A successful `swarm remote pair` ensures the gateway is running — no manual restart. Hook: `runRemotePair` after `res.Paired`; unit installation in `runRemoteInit`. The daemon never spawns it. | Integration test through the supervisor abstraction. |
| PB-LIFE-3 | **Three explicit states**, correcting v1's impossible "restart after revoke". `resolveGatewayParams` fails unless exactly one device exists (`cmd/swarm-remote/config.go:76`), so after a revoke there are **zero** devices and naive restart-on-exit becomes a permanent crash loop: (a) **no paired device** -> unit quiescent, and this is *not* a failure; (b) **paired** -> gateway active, grant delivery completes; (c) **revoked** -> process exits, unit returns to quiescent, and only a later successful re-pair activates a gateway under the new epoch. | Test drives `revoke -> zero-device quiescence -> re-pair -> new-epoch startup`, and asserts no crash loop. |
| PB-LIFE-4 | Units carry no credentials; installed files owner-only. | Permission + content assertions. |
| PB-LIFE-5 | Crash-looping is throttled. | Backoff in both unit types, asserted. |
| PB-LIFE-6 | `swarm-remote` is a released artifact. | Added to the release matrix and built. |
| **PB-LIFE-7** | **The daemon and the gateway must agree on the remote socket, or the default install pairs and then goes silent.** Found by the S4 re-reviewer, verified independently. The gateway's unit points at ADR-007 D4's canonical `<stateDir>/remote.sock` (`cmd/swarm/remote.go:90,262,318-321`), but the daemon opens a remote socket **only** when `SWARM_DAEMON_REMOTE_SOCK` is set in the *daemon's own environment* — `RemoteSocketPath: os.Getenv(daemon.EnvRemoteSocket)` with no default (`cmd/swarm/main.go:309`), documented as "empty => remote control off" (`internal/daemon/client.go:49`). So on a stock install the daemon serves no remote socket, `swarm remote init` installs a unit pointing at nothing, the gateway exits failure and the supervisor respawns it every `ThrottleInterval` **indefinitely** — a throttled spin loop whose user-visible symptom is exactly "the phone pairs, then silence". B3's remediation makes `init` actively start such gateways, so the fix must land with it. **This is exit-criterion-fatal on the flagship path**: the criterion is "your Android phone pairs, observes, launches, and types into a real session", and this configuration delivers the pair and nothing after it. ADR-007 D4 names `<stateDir>/remote.sock` as *the* dedicated remote-tier UDS, not as an optional one, so the tree is currently in **neither** of the two defensible designs — one side defaults, the other opts in. Resolve it deliberately and record the decision: either (a) the daemon opens the canonical socket once remote is provisioned, or (b) enabling remote stays an explicit owner action and `swarm remote init` **refuses loudly** rather than installing a unit that cannot work, naming the exact step the operator must take. Option (a) changes a security default (remote control off unless asked) and therefore needs an ADR amendment; option (b) does not. A silent spin loop is not an acceptable third option. | Test drives the DEFAULT install end to end: daemon started with no remote env var -> `swarm remote init` -> `swarm remote pair` -> assert the operator is either served a working gateway or told precisely what to do, and assert **no restart loop** in either case. Must compose with PB-LIFE-3's three states and PB-LIFE-5's throttling. |

### 6.11 PB-PUSH — push, end to end (fable F2)

Verified: **no APNs implementation exists** (`apns.go`, 22 lines, interface-only), and
**nothing machine-side ever calls `PushTrigger` or `TokenRegister`** — the only push that fires
today is the presence-timeout sweep. FCM will be the first real backend *and* needs a producer.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-PUSH-0 | **A gateway-side trigger**: which journal transitions fire a push, with coalescing/debounce (ADR-007 D6's "push-wakes + coalesced snapshots"), sealed under the **wake key** (PB-KEY-2). **The wake key must first reach the gateway**: `gatewayParams` carries only `ContentKey`, and `WakeKey` appears nowhere in `internal/remotegw/`, `cmd/swarm-remote/`, or `internal/remote/relay/` outside tests — so this introduces a new key crossing into the sidecar that no requirement currently names, with its own custody and blast-radius consequences (the sidecar is the network-facing edge). **Resolved by ADR-007 B19 (2026-07-25): GRANTED.** The premise above is true at the package boundary and false at the process boundary — `machineid.marshal`/`unmarshal` already read the wake key into the sidecar at startup alongside the content key and both private signing keys, and `resolveGatewayParams` merely drops it. Blast radius is therefore unchanged; this widens the package's key INVENTORY, not the process's exposure. | Tests for trigger selection, coalescing, that the content key is never used, and that **the PUSH PATH** holds the wake key only — amended from "the gateway", which is unimplementable as written since the gateway must hold the content key to seal every journal frame and open every phone command (ADR-007 B19). Enforce reflectively over `PushConfig`, with a positive control requiring a `WakeKey` field so an empty struct cannot pass. |
| PB-PUSH-1 | Rename the seam transport-neutral (`PushSink`/`PushPayload`); it is already content-agnostic and keeping the APNs name for FCM is a documented landmine. | Rename lands with Phase A tests green. |
| PB-PUSH-2 | An FCM v1 sender implementing the seam. | Fake-endpoint tests: send, OAuth acquisition + refresh, 5xx retry, `UNREGISTERED` pruning. |
| PB-PUSH-3 | **A specified payload schema** — not merely "opaque fields": which key seals it, replay/expiry gating, and no session names, hostnames, agent names, or Group labels visible to the provider. **The "token, timing, size" claim is FALSE for the obvious implementation and is now enforceable rather than aspirational (ADR-007 B20).** `crypto.Envelope.Marshal` emits a 62-byte CLEARTEXT header carrying `RecipientKeyID` and `SenderKeyID` (8 bytes each) plus a monotonic seq, and `handlePushTrigger` puts the whole marshalled envelope in the payload — so reused verbatim the provider also observes two STABLE endpoint identifiers linking every wake to one machine/device pair for the life of the epoch. D11 forbids claiming less exposure than exists. | Schema pinned by test; ADR states exactly what the provider observes. **The wake envelope's key ids MUST be zero on the push path, and the payload MUST be a constant 78 bytes** (`headerLen` 62 + a 16-byte AEAD tag over an EMPTY plaintext), both pinned by test — "size" is a benign disclosure only while it is CONSTANT, since a size varying with the session name or the coalesced-transition count is a covert channel that would make the honesty claim untrue with nothing failing. |
| PB-PUSH-4 | The app receives a push and renders a **content-free** notification unless the user has authenticated; it never decrypts session content with a locked device (PB-KEY-2). Lock-screen redaction and notification-channel privacy are set. | **AMENDED 2026-07-26 on the evidence backfill's finding: the criterion as written could not be met without committing the defect the requirement forbids.** "authenticated -> content rendered" presumes there is content at the notification to render. There is none: the push payload is content-free by construction (ADR-007 B20 — zero key ids, constant 78 bytes), so rendering session content in the notification would require **fetching** it from the wake path, which is exactly what this requirement's body prohibits and what S17's forbidden-verb guard exists to prevent. The shipped code appends a second constant string and the test asserts only that the two notifications differ; the backfill flagged that as a weakened pass rather than recording it as met, which was correct. **The criterion is the weaker statement, because the stronger one is wrong**: locked -> generic alert only; authenticated -> a *distinguishable* notification that still reads **no** session content. Rendering real content is the app surface's job once opened (PB-APP-*), not the notification's. **NARROWED 2026-07-31 (ADR-007 B133) — the conditional loses its producer; the content-free rendering is KEPT.** *"unless the user has authenticated"* has nothing left to authenticate, so the locked/authenticated **pair of states collapses to one**. **Content-free rendering is not narrowed with it and must not be deleted as gate wreckage**: FCM reads every push payload it carries, the payload is content-free by construction (ADR-007 B20 — zero key ids, constant 78 bytes), and the lock screen still exists on a device whose holder is trusted but whose shoulder-surfer is not addressed either way. So the requirement becomes: **the app renders a content-free notification, full stop**, with lock-screen redaction (`VISIBILITY_SECRET`) and notification-channel privacy still set. | Robolectric test: locked -> generic alert only; authenticated -> a distinguishable notification, **paired with the assertion that no content verb is reachable from the notification path at all** — the distinguishability alone is satisfiable by a defect, the unreachability is what makes it safe. **AMENDED 2026-07-31 (ADR-007 B133): the distinguishability half is struck, the unreachability half is the whole criterion.** With one state there is nothing to distinguish from, and the `contentReady=false` trio is on the vacuous-green list — if production hardwires `true` those tests keep passing while fencing nothing. **What survives and is load-bearing**: no content verb is reachable from the notification path, `VISIBILITY_SECRET`, the no-leak and no-interpolation assertions. **What sets `contentReady` now that the clause has no producer is an OWED decision** (B133 / deauth-plan "Design decisions OWED"), not a rename. |
| PB-PUSH-5 | Missing/invalid credentials degrade gracefully and loudly; the system works without push. | Test: misconfigured sink -> no crash, explicit error, core paths unaffected. |
| PB-PUSH-6 | Push tokens survive a relay restart, or the loss is an accepted, recorded residual. Today `tokens` is an in-memory map (`server.go:173`, sole write `:830`) — a relay restart silently disables push exactly when it is needed. | Persisted and tested, or explicitly recorded with its user-visible consequence. |
| PB-PUSH-7 | The single-token-per-routing-id limitation is documented as acceptable for single-device v1 or fixed. | Decision + a test pinning the behavior. |
| PB-PUSH-8 | **The push toggles need a transport verb.** PB-APP-7 requires toggles that "demonstrably suppress delivery", but delivery is decided by PB-PUSH-0's *gateway-side* trigger, and no push-preference op exists in the signed action set (`internal/protocol/remote.go:74-79,88-89`). Local filtering is not sufficient: the push would still have been sent and the provider would still see token/timing/size, contradicting PB-PUSH-3. A device->machine preference verb is therefore required — which drags in PB-SYNC-5's problem (a new `Action*` constant, an `actionClass` mapping, and a capability-tier decision). | Verb implemented and gated; test asserts a disabled toggle means **no push is sent**, verified at the sender, not the receiver. |
| PB-PUSH-10 | **The push preference must be durable where delivery is decided.** PB-PUSH-8 puts suppression on the gateway side, but nothing says the machine-side preference survives a gateway restart, a daemon restart, an app reconnect, or a lost preference-command outcome — so a test can pass in one process while a restart silently re-enables pushes and leaks token/timing/size contrary to the setting the user is looking at. Requires a durable machine-authoritative record with acknowledged, versioned updates. | Restart test: a disabled preference still suppresses **at the sender** after gateway and daemon restarts. |
| PB-PUSH-9 | **Client-side FCM token lifecycle**: initial `getToken`, `onNewToken` rotation, re-registration on every authenticated reconnect (which also largely neutralizes PB-PUSH-6's relay-restart loss), deletion on revoke/disable, and correct behavior across process death and app upgrade. A façade method can exist while no Android code ever calls it. | End-to-end test with a fake FCM and a real relay: rotate the token and assert delivery still works; restart the relay and assert re-registration restores it. **AMENDED 2026-07-25 — the second half was VACUOUS as written.** S12 made relay push tokens persistent, so a restart restores delivery with the phone doing nothing at all: the criterion could not fail, and would have been reported as evidence for a re-registration path that was never exercised. The restart must be against an **EMPTY token store**, which is the only configuration in which re-registration is the thing being measured. Standing class (i), found by the S17 RED author against this row rather than against the code. |

### 6.12 PB-SEC — handset security (expanded; §3)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SEC-1 | Key material at rest is sealed under an Android-Keystore-backed KEK per the PB-KEY-1 custody contract and the PB-KEY-2 tier split. | Persisted blob is not the raw key and does not decrypt without the keystore key. |
| PB-SEC-2 | **VOID 2026-07-31 (ADR-007 B133). NOT deleted, NOT failed, and the id is never reused.** Original text, kept verbatim so the record reads: *"The biometric gate is **cryptographically enforced, not cosmetic**: a stated freshness window per operation; invalidation on background, screen lock, process death, and biometric-enrollment change; defined cancel/failure/lockout and concurrent-prompt behavior; Keystore-enforced unwrap/sign authorization rather than a UI boolean; no reuse of one authentication for a different action unless explicitly allowed."* **Its entire subject is the biometric gate, and there is no gate**: B133 removes every phone-side user-authentication mechanism *with its code deleted*, because a disabled gate that still compiles is a gate someone re-enables by accident. **A requirement whose subject has left the product is VOID, not NOT MET** — the distinction matters, since "not met" would invite a future slice to close it. **Nothing in it is rescued elsewhere**: the round-7 residual against it (the timed tier reading an in-memory ledger timestamp rather than binding a Keystore operation to the Go action) described a defect in a mechanism that no longer ships. **What it must NOT be read as voiding**: `GateToken` survives entirely and matters more — it was never a cryptographic biometric attestation but a random one-shot anti-swap token whose hash the daemon recomputes (`internal/protocol/server.go:1519,1539-1541`), which is a wire property; only the word "biometric-attested" in its comments is false. Rows that cited this one for a gating obligation are amended individually: §6.0's two freshness rows (VOID), PB-APP-5, PB-APP-7, PB-KEY-6. | ~~Tests per clause. A test must fail if the implementation is an in-memory `authenticated = true` flag.~~ **n/a — requirement VOID.** Its fences (`android/gate/s20_pbsec2_*_test.go`) are deleted rather than adapted: a gate test that still compiles against a deleted feature is the vacuous-green class this project has eight recorded instances of. |
| PB-SEC-3 | No plaintext session content persisted unencrypted; no secrets or session content in logs. | Automated log scan + storage assertion (evidence artifact required, not "reviewed"). |
| PB-SEC-4 | `FLAG_SECURE` on pairing and terminal-peek screens; sensitive content excluded from recents. **AMENDED 2026-07-25 — unsatisfiable as written, and the fix is a SCOPE call rather than a wording change.** The module declares **no `<activity>`**: S16 shipped screen *models* (data classes and enums), so `FLAG_SECURE` has no Window and `filterTouchesWhenObscured` (PB-SEC-12 clause 1) has no View. Both halves therefore have **no subject**. The RED author refused the two bad resolutions — a skip reads green, and a loop over zero Activities *passes* — and made them fail loudly naming the blocking fact, which is correct. **Ruling: S18 delivers a MINIMAL Activity** sufficient to host the pairing and peek surfaces. It is assigned there because S18 is the first slice blocked on it, no slice ever owned it, and **PB-E2E-2 independently requires it** — an on-emulator smoke that pairs, observes, takes control and types cannot run against data classes. Scope is bounded: enough Window and View to carry these assertions and S19's smoke, not a finished app. **AMENDED 2026-07-26 — WITHDRAWN BY PRODUCT DECISION, and the requirement now asserts the OPPOSITE.** The owner ruled that the app should allow screenshots. `FLAG_SECURE` and the recents exclusion are removed from the shipped app; see ADR-007 B65. The reasoning is on the record there rather than here, but its short form is that the protection is a compositor hint this file's own documentation already concedes is weak — it stops no camera pointed at the screen, it is not attested, and an accessibility service reads the rendered screen regardless — while the cost is that users of a developer tool cannot share terminal output. **The requirement is NOT deleted, and the gate is NOT deleted: both are INVERTED.** Reinstating `FLAG_SECURE` must now fail a gate, so that a future re-add is a conscious decision rather than silent drift, which is the same property the original requirement bought and is the only part of it worth keeping. Note the two rows that carried a SPECIFIC argument rather than a generic one — the SAS on the pairing screen, and the terminal grid whose at-rest sealing this undoes one layer up — are answered individually in `android/window-security.tsv`, not overwritten. **PB-SEC-12 is untouched**: its tapjacking, clipboard and IME/accessibility clauses have no screenshot half, and `filterTouchesWhenObscured` stays. | Window-configuration assertion **against a real Activity**, plus the policy-artifact half (`android/window-security.tsv`), which is assertable independently and stands either way. **Post-amendment the assertion runs in the negative**: no source file names `FLAG_SECURE` or `setRecentsScreenshotEnabled`, the TSV join stays bidirectional, and both directions are mutation-proven. |
| PB-SEC-5 | Cleartext traffic is disabled at the platform level **for the Java/WebView stack**. v1 wrongly claimed this backstops PB-NET-2: `networkSecurityConfig` does not govern Go's `crypto/tls` inside a native `.so` (opus H3), so PB-NET-2 is the sole control for the relay transport. | Manifest assertion, with the scope limitation stated so it is not mistaken for transport protection. |
| PB-SEC-6 | The app cannot bypass any server-side control: kill switch, lease, capability, expiry, seq gating stay authoritative server-side. | Adversarial test through the real transport: no typing without a lease or while the kill switch is engaged. |
| PB-SEC-7 | Device-loss response works end to end: revoke -> epoch rotation -> gateway severs and exits -> lost device dead. | Phase A revoke evidence re-asserted through the real transport; ADR documents threat + response. |
| PB-SEC-8 | No analytics/telemetry SDKs; dependencies minimal and justified. | Dependency inventory as an evidence artifact; assertion that no analytics dependency is present. |
| PB-SEC-10 | Excluded from Android backup and device-to-device restore (`allowBackup=false` / backup rules); state must not be extractable via ADB backup. | Manifest + rules asserted. |
| PB-SEC-11 | Exported-component hygiene: an explicit `android:exported` allowlist, validated intents/deep links, no component reachable by a third-party app that can act on the session. | Manifest assertion + intent-validation tests. |
| PB-SEC-12 | UI-redress and input-path defenses: overlay/tapjacking protection on ~~gated actions~~ **privileged actions** (`filterTouchesWhenObscured` or equivalent), no sensitive clipboard use, and documented limits regarding third-party IMEs and accessibility services. **AMENDED 2026-07-31 (ADR-007 B133) — a wording change with a substantive direction: this row SURVIVES and matters MORE.** "Gated actions" was a term of art naming the set the biometric stood in front of, and that set no longer exists; the actions do. **Re-anchored on the privileged set** — revoke, kill switch, take-control, launch, kill — which are now reachable with a single unobstructed tap, so an overlay that steals one is no longer stopped by a prompt the user would have had to satisfy. B133 did not name this row; the deauth plan reaches the same conclusion one layer down, ruling `PhoneActivityWindowTest.kt` a rename-level rewrite that must be re-anchored rather than deleted. The clipboard and IME/accessibility clauses are untouched, as is PB-SEC-4's note that PB-SEC-12 has no screenshot half. | Tests where testable; documented where not. **AMENDED 2026-07-31: the filtered set is enumerated against the privileged actions above**, so a newly added privileged control cannot ship unfiltered by default — the same dropped-quantifier shape this project has found at seven other rows. |
| PB-SEC-13 | Release builds are `debuggable=false`, non-profileable, with no debug backdoor; heap-dump/crash-report exposure considered. | Build-config assertion. |
| PB-SEC-14 | Build supply chain: dependency locking with checksum verification for Gradle/Maven and pinned gomobile/NDK. | Lockfile present and verified in the build gate. |

### 6.13 PB-TOK — design tokens (reduced per §5)

Verified: **31 distinct** `--p-*` tokens (v1 said 38 — corrected) across 4 directions in
`docs/research/remote-control-design-directions.html`; dark-only; no spacing scale.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TOK-1 | One machine-readable token source (JSON) is the single origin for the Android theme. **REASSIGNED S5 -> S16 on 2026-07-25**: an evidence backfill found this criterion NOT met while the requirement was marked shipped, and I verified it. `internal/design/tokens.json` and `android/.../res/values/colors.xml` **disagree on their values** (`--p-bg` `#08090a` vs `swarm_background` `#FF101114`; `--p-ink` `#f7f8f8` vs `swarm_text_primary` `#FFE6E8EB`), and nothing anywhere references `tokens.json` outside `internal/design/` — there is no join in either direction. S5 delivered the JSON and drift-guarded it against the design source, which is real but is only half of this requirement; the other half is only satisfiable where the Android theme lives. Disclosed rather than hidden — `colors.xml`'s own comment calls its values "placeholders for the skeleton" — but disclosure is not delivery. | Theme generated from or asserted against the JSON. **The values must actually agree**, and the assertion must fail when they diverge. |
| PB-TOK-4 | **The Android app does not follow the system `uiMode`, by ANY of the three routes that produce it.** *(Widened after the S13 RED author showed the original text was satisfiable while the defect ships: it named only the `DayNight` parent, but a `values-night/` qualifier reproduces it with a compliant parent, and `AppCompatDelegate`'s default is `MODE_NIGHT_FOLLOW_SYSTEM` -- so an app can have a non-DayNight theme, no night resources, and still hand the platform the system uiMode, leaving no trace in any resource file. All three must be closed: no DayNight/system-mode parent, no night-qualified resources, and the default night mode explicitly overridden. The structural and behavioural halves are non-redundant -- an app overriding every colour attribute resolves identically under both qualifiers and would pass a behavioural check while carrying a DayNight parent.)* Split out of PB-TOK-2 by the S5 reviewer: PB-TOK-2's second criterion was an assertion about the *app*, but S5 owns only the token source and S16 depends on S5, so under PB-DOC-7's exactly-once rule the assertion had nowhere to go — a `DayNight` parent could ship with no test failing, which is precisely the defect PB-TOK-2 exists to prevent. | Owned by S13 (Android skeleton/theme): a test asserts the app theme does not inherit a DayNight/system-mode parent. |
| PB-TOK-2 | Exactly one skin is chosen for v1 and recorded **in the ADR** (the ADR currently names the retained *pair*, Substrate + Void, not a choice), and the token source pins it — since §5 defers light mode, a system-light handset must not render the app unstyled or low-contrast (and PB-E2E-2's screenshots are the evidence artifact). | **Decision: Substrate (d1)** — the artifact's default direction, whose restrained near-black surface ladder suits an information-dense monitoring list better than Void's true-black treatment. Recorded in the ADR by PB-DOC-1 and pinned by the S5 test. |
| PB-TOK-3 | The terminal peek keeps the phosphor-green monospace treatment; purple stays retired. | Asserted against the token source + emulator evidence. |
| PB-TOK-5 | **Every colour token reaches the app, not three of them.** *(AMENDED 2026-08-01: the count is **17**, not 16. An audit committee found `--p-tabbg` -- `rgba(8,9,10,0.88)`, which this table's own header calls a colour -- was typed `effect` in `tokens.json` because neither parser could read `rgba()` notation, so "all 16 reach the app" was true by excluding the one colour nobody could read. Both parsers read `rgba()` now, the token is typed `color` and joined to `swarm_tabbar_background`, and the pins are a floor of 17 rather than an equality of 16.)* PB-TOK-1 built a bidirectional join and then ran three rows through it (`--p-bg`, `--p-ink`, `--p-ink2`); the remaining 13 colours have no Android representation, so 14 of 17 tokens were pinned against a design source that no screen could consume. The join is correct and under-fed. *(`--p-cta-bg` and `--p-cta-ink` are value-aliases of `--p-hero`/`--p-hero-ink`; the mapping must still name them separately, because a future skin can break the alias and a join that silently deduplicates would not notice.)* | All 17 colour-typed tokens have a row in `android/design-tokens.tsv` and a `<color>` in `colors.xml`; the existing bidirectional gate (unmapped `<color>` fails, divergent value fails) is unchanged and now covers 17 rows. |
| PB-TOK-6 | **The non-colour tokens get a typed conversion path.** `android/gate/s16_tokens_test.go` and `DesignTokens.kt` currently *refuse* any token without an ARGB form rather than inventing a conversion — correct when nothing consumed them, and the reason 15 of 31 tokens (5 radii, 5 typographic, 5 effects) can never ship. The refusal is replaced by per-kind converters, each of which either produces an exact Android value or declares the token lossy and names its substitute. | `tokens.json` gains a `kind` per token (`color`/`dimen`/`font`/`weight`/`tracking`/`effect`); the TSV join grows a kind column; radii land in `values/dimens.xml`, tracking as `em` floats, weight as `textFontWeight`. A token whose kind has no converter fails the gate rather than being skipped. |
| PB-TOK-7 | **Derived values are computed, never transcribed.** The artifact resolves four colours with `color-mix(in srgb, …)` — the attention-row border `#6D5220`, the deny fill `#21FF6369`, and the two dot glows `#B3F1A10D` / `#8C00C2D7` — plus the destructive-outline and approval-card tints. Transcribing a resolved hex creates exactly the third-copy-of-the-palette defect PB-TOK-1 was written to catch, one indirection further out where the existing gate cannot see it. | Every derived colour is produced by a single documented blend function over token inputs; a gate asserts no Kotlin or XML literal equals a derivation's output, and that changing a base token moves the derived value. |
| PB-TOK-8 | **The four session Groups are bound to tokens, machine-readably — including `ReadyForReview`, which Substrate never coloured.** This is the largest hole in the design: `ReadyForReview` is a server-derived first-class Group that the phone renders verbatim, the mock paints it `#bf5af2`, and the directions artifact retires purple and silently omits the section from its demo phone. An implementer reaching this row today must invent a colour. **Decision: the four Groups take `--p-att` / `--p-work` / `--p-ok` / `--p-ink3`** — Substrate's demo labelled the green dot "Done", and this rebinds green to `ReadyForReview` and gives `Completed` the recessive grey, which is what swarm's own TUI identity already does (`docs/design/ui-preview.html`: review green, completed grey) and what a triage surface needs, since finished work should recede. Zero new tokens; four distinct hues. | The Group→token mapping is a checked-in table joined bidirectionally to both `status.Group` and the theme, in the style of `design-tokens.tsv`. A Group with no token, or a token bound to two Groups, fails. Recorded in the ADR. |

*(v1's light-mode authoring is cut per §5. The drift test is RETAINED but narrowed: a
single-skin HTML<->JSON check, which is what makes PB-TOK-1's "asserted against the JSON"
mechanically true. What was cut is the four-direction and light-mode scope, not the check
itself — the S5 test author surfaced this ambiguity rather than silently picking one reading.)*

### 6.14 PB-SAS — SAS on the handset

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SAS-1 | The SAS is computed by the shared Go core (`sas.go:58`) and returned as a display string; the emoji table is never re-implemented in Kotlin — removing the cross-language failure mode rather than testing around it. | Test asserts no emoji table in Kotlin sources. |
| PB-SAS-2 | A KAT pins channel binding -> six emoji. | Go KAT; emulator evidence shows phone SAS == machine SAS. |
| PB-SAS-3 | The UI presents the SAS as a compare-both-screens confirmation, never typed. | UI test + evidence screenshot. |
| PB-SAS-4 | **ADDED 2026-07-30 — this row was MISSING, and its absence is the finding (ADR-007 B86).** The channel binding the SAS is computed from MUST attest the accept/decline exchange, not merely the handshake that precedes it. Today msg4 — the final acceptance — rides OUTSIDE the SAS transcript. That is cryptographically sound in itself (ADR-007 B70-R3: the binding is byte-identical before and after, and the peer is authenticated by XXpsk0 under the QR secret, so the party the SAS exists to catch cannot substitute it) — **but it means the SAS attests NOTHING about whether the two sides agreed**, which is why the half-pair of PB-PAIR-4 is invisible to BOTH operators comparing emoji they have every reason to trust. **PB-SAS-1..3 can all be perfectly met while this stands**: they cover the absence of an emoji table in Kotlin, a known-answer test over the binding, and a compare-don't-type UI rule. None reaches this property. **NOT MET, and not closable by tuning** — it needs the acknowledged final frame PB-PAIR-4 also requires, so the two are one protocol change rather than two. | A test showing that a decision frame altered or dropped in transit cannot leave the two sides believing they agreed — driven through a hostile transport, not an injected error. |

### 6.15 PB-APP — the client (single-machine per §5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-APP-1 | **Pairing/onboarding** per PB-PAIR-*. | See PB-PAIR. |
| PB-APP-2 | **Triage inbox**: the four Groups as sections with one-line need summaries. *(Machine switcher cut — §5.)* | UI test covering all four Groups and the empty state. |
| PB-APP-3 | **Session detail**: journal events + snapshot cards, persistent Stop. | UI test; Stop maps to interrupt/kill with confirmation. |
| PB-APP-4 | **Terminal peek + take-control**: renders daemon-sanitized `SnapText` (no VT emulator on device — ADR-007 D2), live tail, lease acquire, on-screen keyboard, release. | UI + integration test; asserts only sanitized text is rendered. |
| PB-APP-5 | **Machine pane**: presence, paired device, revoke, kill switch, activity log. **AMENDED 2026-07-31 (ADR-007 B133) — its criterion cited a requirement that is now VOID.** Neither B133 nor the deauth plan names this row; it is caught here because "gated per PB-SEC-2" is a dangling reference the VOID creates. **The pane's contents do not change, and revoke and the kill switch get MORE important, not less**: B133 makes `swarm remote off` / device revoke the only surviving mitigation for a lost handset, and `SeverAllRemoteControl` (`internal/protocol/server.go:1395`) is now the difference between a recoverable loss and an unrecoverable one. Note the phone-side revoke button is only half of that — B133's mitigation is issued **from the computer** — so this pane must not be presented as the kill switch of record. | ~~UI test; revoke + kill switch gated per PB-SEC-2.~~ **AMENDED 2026-07-31 (ADR-007 B133):** UI test; revoke and the kill switch are **reachable without any gate**, and are **confirmed** rather than authorized (a destructive control still deserves a deliberate second tap). PB-SEC-12's tapjacking filtering applies to both and matters more here than it did when a biometric stood in front of them. |
| PB-APP-6 | **Launch**: submit a spec via the v1 builder/policy path; policy rejection surfaced. | UI + façade test. |
| PB-APP-7 | **Settings**: two coarse push toggles honored by PB-PUSH-0's trigger, ~~plus the biometric gate toggle~~. **NARROWED 2026-07-31 (ADR-007 B133): the biometric-gate toggle goes with its subject; BOTH push toggles stay.** The push toggles are unaffected — they are honored at the *sender* by PB-PUSH-0's gateway-side trigger through PB-PUSH-8's transport verb, which is a wire concern the boundary change does not touch. The settings surface therefore shrinks by exactly one control. | UI test; toggles persist and demonstrably suppress delivery. **AMENDED 2026-07-31 (ADR-007 B133): the toggle set is TWO, and the count is what the test should assert** — `SettingsScreenTest.kt` currently pins three, so it breaks loudly on the deletion, which is correct. "Demonstrably suppress delivery" is unchanged and is still verified **at the sender, not the receiver** (PB-PUSH-8): local filtering would leave the provider seeing token, timing and size. |
| PB-APP-8 | **Connection/stale UX**: offline, reconnecting, and **per-stream** stale/resyncing states visible; a stale view is never presented as live. | UI test driving each state, including PB-SYNC-1's per-stream staleness. |
| PB-APP-9 | Errors reach the user: an **exhaustive error-taxonomy mapping test** over the façade's pinned surface (v1's "test/lint; reviewed" was unenforceable). | Every façade error class maps to a rendered state; a new error class without a mapping fails the test. |
| PB-APP-10 | A revoked device shows an explicit re-pair prompt, not a failure loop; a grant-loss device shows PB-KEY-3's state. **AMENDED 2026-07-25**: and a paired device that is KEYLESS BUT NOT TERMINAL — the bootstrap grant has not arrived yet — shows a **transient waiting** state, distinct from the grant-loss one. Both states are keyless and only one is terminal; the design deliberately refuses to call the first terminal, because the gateway re-appends its sidecar every session so it self-heals, and the remedy is waiting rather than a re-grant. Without this the first-pairing window and a permanently lost key are indistinguishable on screen, which is the failure loop this row already forbids in its other half. Gap surfaced by the S16 RED author when a setup was corrected for re-litigating PB-KEY-3, which S10 owns — flagged as an amendment rather than smuggled back in as a fixture. | Test per state, **three now**. The waiting state must NOT be reachable by the grant-loss detector (`keyless && ErrGrantReplay`), and must clear on its own when the grant arrives. **AMENDED 2026-07-26 (ADR-007 B45/B52): the non-recoverable state set grows from three to five.** Applying transport policy created two verdicts that waiting can never resolve, and which were falling through to `reconnecting` — a spinner, forever, which is the exact failure this requirement forbids and which `ConnectionUi.kt` names: *"A spinner is a promise that waiting is enough."* The two are **`relay_untrusted`** (pin mismatch, pin required, or malformed pin -> the relay is not presenting the identity the machine published) and **`relay_insecure`** (cleartext refused). They are **separate states, not one**, because the remedies differ: a re-pair re-delivers the same relay URL, so telling the cleartext user to pair again sends them round a loop. Both survive a retry inside the post-pairing grace window, since a pairing that just completed may have delivered the very pin that makes the verdict stale. |

| **PB-APP-11** | **NOTHING IS PRESENTED AS LIVE ON THE STRENGTH OF A POLL THE RELAY ANSWERED (NEW 2026-07-31, ADR-007 B121/M-1).** Every staleness mechanism in this system keys on a GAP, and a gap is observable only when a LATER seq arrives — so the declared adversary (D9) does not have to forge, reorder or replay. **It stops delivering the newest frames and keeps answering polls with an empty page.** No gap forms, so nothing is marked stale; the poll SUCCEEDS, so no connection-state machinery fires; and `Presence()` asks **the withholding party** whether the machine is alive. The phone then renders arbitrarily old sessions and terminal grids as live, indefinitely, with `ConnectionState` reading `online` — which is the exact condition PB-APP-8 forbids and which no row forbade, because every row is written in terms of gaps. §6.0 has carried the 5-minute budget for this since v2 and **it was never implemented**: the staleness decision (`App.StreamState` -> `streamStale` -> `Core.StreamStale` -> the persisted per-channel flags) had **no clock input at any layer**. The phone MUST therefore bound the age of everything it presents as live by **the newest AAD-covered `IssuedAt` it has ACCEPTED from the machine** — the one machine timestamp the relay cannot forge and can only make worse — and past §6.0's budget it MUST degrade to an **explicit "not heard from your machine since HH:MM"** state carrying that timestamp, never to a successful empty poll. The coordinate is **durable** (a restart presents restored caches, and a live-only mirror would come back clear and re-present them as live) and **monotonic** (a retained old frame delivered late must not move it backwards). Relay presence is NOT this signal and may never stand in for it: the screen that renders presence must render the freshness verdict beside it. **What this does NOT distinguish, stated rather than implied**: with no machine-side liveness beacon on the wire, an idle machine and a withheld one look identical to the phone, so the state is worded as what the phone actually knows ("not heard from") rather than as a claim about the machine. Choosing a beacon interval is a §6.0 number nobody has decided; it is recorded as residual, not invented here. | Against a real relay and the real machine-side sealer: (a) content the machine stamped INSIDE the budget is presented live; (b) the SAME content stamped past it leaves **every** read model stale (`StreamState`, `SessionList.Stale`, `JournalPage.Stale`, `Snapshot.Stale`) and the freshness verb explicitly not-heard, carrying the machine's own stamp — **while the poll keeps succeeding with empty pages and `ConnectionState` still reads `online`**, which is the attack's whole shape; (c) the verdict and the timestamp survive a restart over the same state directory; (d) a phone that has never heard from its machine is not-heard, not live; (e) a frame delivered late does not move the coordinate backwards; (f) the Go/Kotlin seam is fenced: the machine pane cannot render relay presence without the freshness verdict. |

### 6.16 PB-TOOL — build

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TOOL-1 | Toolchain pinned in-repo (JDK, SDK, build-tools, NDK, gomobile, `-androidapi`). | A fresh shell sourcing it can build. |
| PB-TOOL-2 | One command builds the AAR for an **explicit ABI set including `arm64-v8a`** (v1's `jni/<abi>` allowed an x86-only AAR — codex#12). | Artifact inspected for each required ABI. |
| PB-TOOL-3 | One command builds the debug APK; release signing reads an operator keystore from config/env, never the repo. | Installable APK; no keystore or password in git. |
| PB-TOOL-4 | Gradle wrapper checked in with a pinned distribution. | `./gradlew --version` works without system gradle. |
| PB-TOOL-5 | No Go regression. | `go build ./... && go vet ./... && go test -race ./...` green. |
| PB-TOOL-6 | Android lint + unit tests in the gate. | `./gradlew lint test` green. |
| PB-TOOL-7 | CI covers the new artifacts (no android lane exists today). | A CI lane builds the AAR and runs the Gradle gate. |

### 6.17 PB-E2E — verification

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-E2E-1 | A Go end-to-end test with **no fakes and no `phonesim` seam**: real relay, real client, real façade, real gateway, real daemon — pair -> observe -> launch -> take_control -> type -> revoke. | Passes under `-race`. Explicitly forbids the injected mailbox seam. |
| PB-E2E-2 | On-emulator smoke: APK installs, pairs against a local relay + daemon, SAS matches, observes, takes control, types — **including one real `adb shell am force-stop` mid-session** (PB-STATE-2 on a real runtime). *(Upgraded from "one process death" after the S13 RED author showed force-stop is strictly stronger and is the clause PB-RUN-5 cannot otherwise cover: it also puts the package in the STOPPED state, so no implicit broadcast -- `BOOT_COMPLETED` included -- reaches the app until the user launches it by hand. A plain process kill does not exercise that. The emulator can issue it, so it belongs here rather than being deferred to PB-E2E-5.)*. | Evidence (log + screenshots) + reproducible runbook. | **AMENDED 2026-07-30 (ADR-007 B91) — RE-SCOPED TO PHYSICAL HARDWARE, because it is unsatisfiable on an emulator BY CONSTRUCTION and that is correct behaviour, not a gap.** The emulator keymaster reports `SECURITY_LEVEL_SOFTWARE`; PB-KEY-8's hardware-downgrade refusal fails closed **before any screen renders**, so the app cannot start there at all. Measured by running it (B56), and independently corroborated in round 5 by mutation: disabling the secure-hardware refusal kills both floor tests, **so the refusal that blocks the emulator is real and correctly placed.** The two requirements were in direct conflict and the conflict is resolved in **PB-KEY-8's** favour — an app that silently accepted software-backed keys to make a smoke test pass would be the worse outcome by a wide margin. **This row is therefore folded into PB-E2E-5**, the deferred physical-handset gate, and carries its deferral rather than a false NOT MET: the smoke it describes is exactly what a handset run will exercise. **It is NOT counted as met** — it is counted where its evidence will actually come from.
| PB-E2E-3 | Evidence files per repo convention with a RED-first run per slice, and each evidence file states **what it proves**, not merely that it exists. | GG-5 satisfied per slice. | **AMENDED 2026-07-30 (ADR-007 B93) — RESTATED IN A VERIFIABLE FORM, WITH THE SLICES THAT CANNOT SATISFY IT NAMED RATHER THAN WAIVED.** As written this asked for "RED-first evidence per slice", which its own fence could not check: that fence verifies an evidence file NAMES a requirement, not that a failing test preceded the implementation — **so the requirement policing the method was satisfied by a check that cannot see the method** (found by the round-5 external reviewer). **The verifiable form is a COMMITTED failing state**: a RED commit carrying the failing output in its message, preceding the implementation commit for the same requirement. That is checkable from history rather than asserted in prose. **Satisfied for every slice landed since 2026-07-30** — `a8bdc31` (PB-PAIR-4/PB-SAS-4), `1f0a409` (PB-PUSH-3, PB-SEC-2), `f7aaab2` (PB-APP-6, PB-INPUT-2), each followed by its own GREEN commit. **PERMANENT NAMED EXCEPTIONS, recorded because they cannot be repaired retroactively and pretending otherwise is the defect this requirement exists to prevent: S10 and S12**, whose own evidence files admit tests and implementation landed together, **and S17 and S18b**, which the residuals already record as unable to satisfy GG-5 after the fact. **Those four are exceptions, not passes.** No fence is claimed over the commit-precedence rule yet; building one is owed and is stated here rather than implied, because an unbuilt check recorded as built is exactly how this row read met while two slices admitted otherwise.
| PB-E2E-4 | No Phase A regression at any slice boundary. | Full suite + four gates green. |
| PB-E2E-5 | **A physical-handset gate.** The exit criterion says "your Android phone", and §10's honesty clause — while it does prevent a false "production-ready" label — otherwise defines that criterion away (codex#5). The gate covers: pair via the real camera, lock/unlock, process death, reboot, observe, launch, lease, type, Wi-Fi<->cellular handoff, and revoke, on hardware-backed Keystore ~~with real biometrics~~ — **NARROWED 2026-07-31 (ADR-007 B133), and legitimately: "real biometrics" and "lock/unlock" leave this gate because the FEATURE leaves the PRODUCT, not because the gate was found inconvenient.** The test that distinguishes removal-by-feature-deletion from reclassification by fiat is exactly this: **nothing that still ships was moved out.** Real camera pairing, real FCM registration and delivery, high-priority wake from Doze, notification behaviour after reboot, token rotation, reboot, process death, Wi-Fi<->cellular handoff and hardware Keystore attestation via `KeyInfo` **all stay deferred and all stay in this gate** — and the hardware-backing assertion becomes the single most load-bearing item in it, since at-rest sealing is what remains of the extraction defence. **The gate is not weakened as a gate**: §13 still forbids declaring "done" without it, and still forbids reclassifying it as a §10 limit. — **plus the push path, which v3 omitted while §10 simultaneously listed real FCM and Doze as unverified**: real FCM registration and delivery, high-priority wake from Doze, notification behavior after reboot, token rotation, and locked-device push handling. It must also assert hardware backing through `KeyInfo`/attestation rather than asserting "hardware-backed" by assumption. | (This is why push cannot be declared done on emulator evidence alone; the alternative considered and rejected was cutting push from Phase B entirely, which would contradict roadmap B4.) | **Until this runs, Phase B is "provisionally implemented", not done** (§13). It cannot be executed on this machine (no handset), so it is an explicitly deferred gate with a named owner and a runbook — not a silently accepted limit. |

### 6.18 PB-OPS — operability

| ID | Requirement | Acceptance criteria |
|---|---|---|
**Scope correction (round 2, codex#14):** v2 silently pulled Phase C work into Phase B. The
roadmap puts relay ops — "Dockerfile / systemd unit, TLS termination runbook, VPS provisioning,
key-backup UX, onboarding docs" — in **C2** (`docs/research/remote-v1-roadmap.md:263`). Phase B
keeps only what the handset demonstration actually needs; the rest returns to Phase C.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-OPS-5 | **The certificate pin must survive renewal.** S6 pins a leaf DER, so on Android — where the trust-root source is pinning-only — **every Let's Encrypt renewal (60-90 days) breaks the handset**. PB-OPS-1 offers "a pinned OR real certificate", but under pinning-only the "real certificate" half does not exist: a publicly-trusted cert must still be pinned. Pinning the **SPKI hash** rather than the full leaf DER survives renewal at the same security level — **AMENDED 2026-07-26: only if the renewal REUSES THE KEY.** certbot generates a fresh keypair per renewal by default, and a fresh key is a fresh SPKI, so an unqualified SPKI pin breaks on exactly the cadence it was adopted to survive. It is a **necessary half**; the operator must also configure key reuse and the runbook must say so. The S20 implementer declined to restate the claim and pinned it the other way — `TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey` asserts the pin **failing** on a rekeyed renewal. **Also note the premise has moved**: ADR-007 B34 records that no production caller reaches `DialSecure`, so neither pin is on the path a phone takes and the live defect is the absent policy, not renewal fragility. | Runbook states the renewal hazard; either the pin is SPKI-based or the operational consequence is documented and accepted. |
| PB-OPS-1 | A **local/TLS relay runbook sufficient for the handset demonstration** — enough to stand up a reachable relay with a pinned or real certificate. Production deployment, VPS provisioning and image publishing return to Phase C. | Runbook executed once with an artifact as evidence. |
| PB-OPS-2 | Operator runbook for the flows Phase B introduces: install, pair, revoke, kill switch, device loss, push configuration. | Each step executed once during verification. |
| PB-OPS-3 | Honest metadata disclosure covering relay operator and push provider. | ADR section consistent with PB-PUSH-3 and ADR-007 D11. |
| PB-OPS-4 | `swarm-relay` and `swarm-remote` are buildable release artifacts (today `.goreleaser.yaml` builds `./cmd/swarm` only, and its own comment at `:9-11` is stale — it names `swarm-char`/`swarm-fake-agent` but not `swarm-relay`). One change serves this and PB-LIFE-6. | Both binaries built by the release path. |

*(Returned to Phase C: production backup/restore, disk-full behavior, log rotation, health
checks, TLS renewal automation, resource limits, and cross-version compatibility — recorded in
§7 rather than dropped.)*

### 6.19 PB-DOC

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-DOC-1 | An ADR-007 amendment records every Phase B decision: façade + dependency surgery, durable phone state + seq strategy, the transport protocol change, per-stream resync + its authz/capability decision, JNI key custody, wake/content tiers on Android, push trigger + payload schema, supervision's three states, chosen skin, **single-machine v1**, and **light-mode deferral**. | Merged before the final audit. |
| PB-DOC-2 | Phase B exit criteria in `implementation-goals.md`; a verification file maps every PB-* ID to evidence. | Full ID coverage. |
| PB-DOC-3 | Residuals recorded in the Phase A closure style: what, why, adversary-reachable or not. | Reviewed in the final round. |
| PB-DOC-4 | `docs/research/remote-v1-roadmap.md:286` ("Implementers are sonnet/opus subagents — never fable/haiku") is amended rather than silently contradicted by §11's model assignment. | Roadmap updated. |
| PB-DOC-5 | **The Phase A closure gains a scoped note, not a retraction.** §4.6's exploit claim was disproved in round 3, so the closure's "no relay-adversary-reachable confidentiality/integrity hole" statement stands. What it must record is the narrower true finding: the gateway's inbound replay guard and read cursor do not survive a restart and the bounded-age check is disabled, so that property currently rests on incidental downstream mechanisms rather than on the guard itself, and the original claim was verified within a single gateway run. | Closure amended with the reproduced finding only; a note distinguishing the two claims: the **shipped-Phase-A exploit** was investigated and **disproved**, while the **conditional Phase-B trace** (a seq-regressed phone, §4.6) remains valid and is what PB-GW-1 + PB-STATE-3/-4 exist to prevent. |
| PB-DOC-7 | **A machine-checked slice-ownership manifest.** Every concrete PB-* id appears **exactly once** as an owned requirement, wildcard ownership ("all") is prohibited, every dependency edge is enumerated, and acyclicity is validated in CI. Rounds 2 and 3 both found homeless requirements (PB-KEY-2, then PB-STATE-10 and PB-SAS-2) and ambiguous cycles by hand; this makes that class of error mechanical. | A test parses §11 and the requirement tables and fails on any unowned id, duplicate owner, wildcard, dangling edge, or cycle. |
| ~~PB-DOC-6~~ | **WITHDRAWN (round 3).** v3 claimed ADR-007:313's "light+dark token sets" was never true of its cited artifact. That is wrong: `remote-control-design-directions.html` **does** ship a light set — `@media (prefers-color-scheme: light)` at `:8-10` and `:root[data-theme="light"]` at `:12`. Only the four `--p-*` *product skins* are dark-only, which is exactly what §5 already says. Acting on this would have written a **false correction into the ADR**. The light-mode deferral (§5) stands on its own merits and needs no such justification. | n/a — requirement withdrawn. |

### 6.20 PB-DS — the design system (NEW 2026-07-31; ADR-007 B134)

> **STATUS 2026-08-01, after an adversarial audit committee (GPT-5.6 sol, opus, sonnet).
> Verdict: REVISE. Four requirements below were claimed MET and are PARTIAL.**
>
> Three rows below are PARTIAL and one is NOT MET.
>
> | ID | Claimed | Actual | Why |
> |---|---|---|---|
> | PB-DS-1 | MET | **PARTIAL** | The scale exists and the raw-pixel `PADDING` is gone, but `PairingSurface.kt:598` still carries `SCANNER_HEIGHT = 720` raw pixels — a worse instance of the exact defect this requirement names, and one the S22 evidence's own count of five surviving literals missed. |
> | PB-DS-2 | MET | **PARTIAL** | 18 styles ship, but one (`Label.StatusBar`, origin `.ptime`) is the mock's *simulated* iOS status-bar clock, which `substrate-components.md:230,343` classifies as platform-owned and says "pretending [a token] applies would be an invention". It has zero call sites. Meanwhile `substrate-components.md:333` already calls for a 19th style, `Display.SAS`, and says the gate must fail until it exists — it does not fail, because the gate joins only against the skin CSS and not against the derived spec. |
> | PB-DS-6 | MET | **NOT MET** | "The kit is the only way a screen is built." The kit has **zero production call sites**. `PhoneSurface.kt`, `PairingSurface.kt` and `SettingsSurface.kt` import nothing from it. Across ~11.6k inserted lines the only user-visible change is one padding moving from 24px to 24dp. The gate also narrows "every component" to 11 hand-listed Inbox factories against the family's own 38, with no amendment recording the narrowing. |
> | PB-DS-10 | MET | **PARTIAL** | *(Updated 2026-08-01 after the fix pass: the two self-comparisons named here were repaired in `7d9c5ef`/`5a06aac`, and a re-audit confirmed the new Bitmap glow test and the mock-parsing motion tests are genuinely non-circular, verified by an independent six-mutation ledger. What keeps this PARTIAL is the evidence, not the assertions.)* The recorded negative controls in `remote-phaseB-s23-red/negative-controls.txt` contain stale or mispasted output for four mutations — which is the whole basis for claiming the assertions can fail. |
>
> **PB-DS-5 remains owned by S22 and unmet on both counts**: no grain raster exists, and the
> tab-bar blur has no implementation because `RenderEffect` blurs the view it is set on rather
> than the content behind it — CSS `backdrop-filter` has no Android equivalent at any API level,
> so ADR-007 B134's claim that `minSdk 33` retires that fallback was wrong. Corrected in the ADR.
>
> **The structural finding, which is about how this section was written rather than about any one
> row.** The committee observed that the rigour here is inversely proportional to how much was
> invented: values *transcribed* from Substrate are graded by computed equality against a
> machine-read origin, while values this effort *authored* — all 24 derived components, every
> `derived:` constant, all of the motion work — are graded on the presence of a citation.
> PB-DS-7's criterion ("no cell is a bare hex") grades a document's **shape**, and it is the
> acceptance criterion for the single largest piece of design authoring in the family. That is
> backwards from where the risk actually is, and it is the honest answer to the question of
> whether these requirements were written to describe what was convenient to build.

The Substrate skin is chosen (PB-TOK-2), its 31 tokens are pinned and drift-guarded, and **none of
it renders**. `android/app/src/main/kotlin/` contains no `setTextColor`, no `R.color` reference and
no `R.dimen` reference at all; the entire visual output of the app is `setPadding(24)` in raw pixels,
one `Typeface.MONOSPACE` and one `Typeface.BOLD` across 1582 lines of surface code. The nine files
under `ui/` are data classes and string tables that render nothing. This family is what turns a
verified token supply chain into a product that looks like the artifact.

**Two findings shape it.** First, the design is *incomplete*: 14 of 38 components carry a Substrate
spec and the other 24 exist only in `remote-control-mock.html`, which predates the skin work and is
painted in an iOS-derived palette the product retired. Deriving those 24 is design authoring, not
transcription, and it is inside this scope. Second, `minSdk` is **33**, which retires the three
conversion problems that would otherwise need fallbacks: `BlendMode.SOFT_LIGHT` (API 29) and
`RenderEffect.createBlurEffect` (API 31) are both unconditionally available.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-DS-1 | **A declared spacing scale.** The design has none — 16 distinct literal values in Substrate alone, mode `12px`, and a 4dp grid uniformly loosens the layout by ~10% because the design's tightest rhythm is a 7px gap. **Decision: a 2dp grid of ten steps** (2, 4, 6, 8, 10, 12, 14, 16, 18, 24) plus three named frame constants (`screen_top` 54dp, `screen_bottom` 76dp, `tabbar_height` 74dp). Seven of sixteen values move, six by 1dp and one by 2dp (`26→24`). | `values/dimens.xml` carries the scale; a gate asserts no layout dimension in Kotlin is a raw literal — every padding, margin and gap resolves to a scale entry or to a frame constant. The current `PADDING = 24` raw-pixel constant is deleted (it is px, not dp, and renders at ~8dp on a 3× handset). |
| PB-DS-2 | **A typography scale of named styles.** 18 distinct product text styles are in use across 12 sizes, 4 weights and 5 tracking values; the app currently expresses zero of them. Each style is one named `TextAppearance` carrying size, weight, tracking, family, line-height and colour token — not six attributes re-specified per call site. | `values/type.xml` defines `TextAppearance.Swarm.*` for all 18; a gate joins the style set to the recorded scale bidirectionally (an unlisted style fails, an unimplemented row fails). No `setTextSize`/`setTypeface`/`setLetterSpacing` call survives in surface code outside the theme. |
| PB-DS-3 | **The font substitution is a recorded decision, not a default.** `--p-font` names SF Pro and `--p-mono` names SF Mono; neither is licensable off Apple, so *every* text style in this app is already rendering a substitute chosen by nobody. **Decision: the platform families** — `sans-serif` and `monospace`, zero bundled assets. `--p-display-wt: 650` is reachable because `android:textFontWeight` works against the platform's variable Roboto at API 33. **Recorded residual:** Android's `monospace` (Droid Sans Mono) does not cover U+2500–257F, so a terminal peek carrying box-drawing from an agent TUI may show tofu; the upgrade path is bundling JetBrains Mono, and it is deliberately not taken until the peek is seen to need it. | The decision and its residual are in the ADR. A gate asserts the mono style's family is the one recorded, and a test renders a box-drawing string through it so the residual is observed rather than assumed. |
| PB-DS-4 | **A shape scale from the radius tokens**, with the degeneracy recorded: `--p-dot-r: 4px` is declared against a 7px box, so `4 ≥ 3.5` renders a full circle and the literal `4` is unreachable. A token whose declared value never renders must say so where an implementer reads it, or it will be re-derived as a rounded rect. | Four radii in `dimens.xml`; the dot is an `oval` shape and the ADR records why its token value is not its rendered value. |
| PB-DS-5 | **The five effects have exact implementations, and `--p-card-fx` is not approximated by elevation.** Substrate bans drop shadows — elevation is one ladder step lighter, never a shadow — so `View.elevation` is the one implementation that is *wrong* despite being the obvious one. The inset key-light is a `layer-list` with a 1dp top-edge rect at `#0BFFFFFF` clipped to the card radius; the two dot glows and the CTA bloom are `Paint.setShadowLayer(r, 0, 0, colour)` on a software layer (the same conversion, solved once); the workbar gradient must keep RGB in its transparent stop (`#0000C2D7`, never `#00000000`, which fades through black and greys the bar); grain is a pre-rendered 140×140 raster tiled under `BlendMode.SOFT_LIGHT` at 5%, checked in rather than regenerated because `feTurbulence` output is implementation-defined. | Each effect has a test asserting its distinguishing property (the key-light layer exists and is 1dp; the glow is symmetric with zero offset; the gradient's end stop has non-zero RGB). A gate asserts no `View.setElevation`/`android:elevation` appears in surface code. |
| PB-DS-6 | **A component kit is the only way a screen is built.** Every visual element is one factory in a single package, styled entirely from the theme; a screen composes components and passes data. Without this the 24 derived specs land as copy-paste and drift on first edit, which is the same failure mode as the third palette copy. | Surface files contain no colour, dimension, radius or typeface reference — a gate fences them to component calls plus layout. Kit coverage is joined to the component inventory bidirectionally. |
| PB-DS-7 | **The 24 unspecified components get derived Substrate specs, in a reviewable table.** Toast, push banner, badge, toggle, grab handle, QR frame, SAS display, empty state, composer, quick-reply chips, machine row, kill-switch panel, paired-device row, activity row, settings row, streaming caret, lock screen, pairing scaffold, status bar, screen scaffold, grain overlay, read-only note, focus ring and the disabled-CTA pair are specified only in the mock's retired palette. Two need genuine invention rather than substitution: **the focus ring**, which currently uses the documentation chrome's `#e2a33b` and is undefined for the product, and **the scrim and grab handle**, which have no near token. | One table, one row per component, each cell either a token, a documented derivation, or a named exception with its reason (the QR's black-on-white is the one legitimate palette exception, for scannability). No cell is a bare hex. |
| PB-DS-8 | **Motion: Substrate is static, and the exceptions are named.** The directions artifact declares no `@keyframes`, no `transition` and no `animation` — the working affordance is the *static* dot glow plus the *static* workbar, and the skin's own rule is "nothing glows unless it is alive". The mock's pulsing dot is a conflict inherited from the pre-skin palette. **Decision: no decorative animation.** Only navigation affordances move — bottom sheet and push banner, both `translateY` over 350ms on `cubic-bezier(0.32, 0.72, 0, 1)` — plus the streaming caret, which is a liveness signal rather than decoration. | Reduced motion is honoured via `ANIMATOR_DURATION_SCALE == 0` checked at animator construction, and it covers the toggle, which the artifact's own `prefers-reduced-motion` selector list omits. A gate asserts no animator is constructed outside the kit. |
| PB-DS-9 | **The screens are recomposed on the kit, and the orphaned models are wired.** `SessionDetail`, `JournalPageView`, `JournalRow`, `StopAction`, `StreamView`, `StreamBadge`, `ClockBanner` and `MachinePane` are fully modelled, fully unit-tested and reach no pixel; `TriageInbox` — four Groups, sections, empty states, the whole triage design — is consumed as `.flatMap{}.firstOrNull()?.id`, a session picker. The single flat `LinearLayout` holding all 20 children is replaced by real screens. **Triage inbox is first**: it is the root, it exercises the most components, and it is where the four-Group identity actually shows. | Per screen: the composition matches the recorded inventory in order and spacing, empty sections still render as sections with their empty copy (dropping them is the obvious implementation and is wrong for a triage surface), and every UI string is the recorded copy. `android/unbound-verbs.tsv` shrinks by the verbs the screens now reach. |
| PB-DS-10 | **Appearance is tested, not just behaviour.** Every existing UI test asserts strings, enums and booleans; nothing anywhere asserts a colour, a dimension, a font or a layout. That is precisely how `colors.xml` drifted to a third palette while its own test stayed green — the test certified that the app renders whatever `colors.xml` says. Robolectric resolves real theme attributes and real view state, so this needs no new dependency and no screenshot corpus. | For every component: a test inflates it and asserts the *resolved* colour int, dimension px, typeface and letter-spacing against the token origin — not against a constant recorded from the implementation. Each such test carries a negative control proving it fails when the value diverges, in the style of `TestPBTOK1_TheComparisonCanActuallyFail`. |
| PB-DS-11 | **No visual constant may enter the app except through the theme.** The fence that makes every requirement above durable: an ARGB literal, a `dp` literal, a font name or a radius appearing in surface code is the defect, independent of whether its value is currently correct. | A gate scans all production Kotlin and XML outside `theme/` and fails on any colour literal, any raw dimension, and any `Typeface.` or `Color.` reference. Existing violations are fixed, not allowlisted. |
| PB-DS-12 | **Accessibility floors that the artifact does not state.** A 386×812 CSS mock has no touch targets, no content descriptions and no font-scaling behaviour, and three of its type styles are below 10px. These are not design decisions to be recovered; they are floors the design must be made to meet. | Every interactive element is ≥48dp in its touch target (visual size may stay smaller); every non-text control has a content description; text sizes are `sp` and the layout survives a 1.3× font scale without clipping; the focus ring from PB-DS-7 is applied to every focusable; `--p-ink3` on `--p-bg` is measured for contrast and its failures are recorded with the surfaces they affect. |

---

## 7. Non-goals

| Out of scope | Why |
|---|---|
| iOS client | Phase C; needs Xcode + Apple account. The façade is shared, so iOS is a rebind. |
| **Multi-machine (and the machine switcher)** | §5: the core is structurally single-machine; the exit criterion is one session on one machine. Phase C, with multi-device. |
| **Light mode** | §5: dark-only tokens today; the exit criterion is a dark phosphor terminal; largest non-load-bearing item. Phase C. |
| Chat transcript / `transcript_delta` | ADR-007: Phase 2, gated on spike S-A. |
| Approval sheets / `interaction_request` | ADR-007: Phase 2, gated on S-B/S-C. |
| Voice, quiet hours, Live Activities, activity-feed depth | Design §9: Phase 2. |
| Multi-device pairing + `SenderKeyID` binding + the epoch-equality reconcile revisit | One phone; `AddSole` holds. Reopening it gains nothing for the exit criterion. Phase C. |
| Admin tier | Phase A deferral; no exit-criterion need. |
| ME-1 relay socket close | Non-load-bearing since the gateway exits on revoke. |
| Multi-subscriber observer fan-out | Design §7.3: Phase 3. |
| Play Store distribution | Sideload satisfies the milestone. |
| Real APNs | No Apple account; the PB-PUSH-1 rename keeps iOS a drop-in. |

---

## 8. Traceability: reviewer findings -> requirements

| Finding | Raised by | Lands in |
|---|---|---|
| No durable phone state; process death bricks typing + resets replay guard | codex#1, opus M1, fable F1 (unanimous) | PB-STATE-1..6, PB-NET-6 |
| Long-poll head-of-line-blocks typing; rationale false; both hops | codex#3, opus H1, fable F3/F4 | PB-NET-5 |
| Resync repairs the wrong coordinate; multiple streams | codex#2, opus H4, fable F11 | PB-SYNC-1..6 |
| Wake/content key tiers vs background push vs JNI | codex#4, opus H2, fable F5 | PB-KEY-1/2, PB-BIND-4, PB-PUSH-4 |
| No QR encoder exists | opus M2 | PB-PAIR-1/2 |
| Epoch-grant loss unrecoverable | opus M3 | PB-KEY-3 |
| PB-LIFE-3 impossible (zero-device crash loop) | codex#5 | PB-LIFE-3 |
| Push has no producer | fable F2 | PB-PUSH-0 |
| `networkSecurityConfig` does not cover the Go transport; trust roots unstated | opus H3 | PB-SEC-5, PB-NET-2 |
| `ws://`-only relay vs unconditional cleartext ban | fable F6 | PB-NET-2 |
| `journal_read` is not `requireRemoteAuthz`; `actionClass` closed switch | opus H4 | PB-SYNC-4/5 |
| Clock skew | codex#10, opus Md1, fable F7 | PB-TIME-1 |
| Android runtime/permissions/lifecycle | codex#6, opus Md3, fable F9 | PB-RUN-1..5 |
| Multi-machine contradiction | codex#7, opus Md2, fable F8 | §5 decision, PB-APP-2 |
| Live-input semantics + lease lifecycle | codex#10, opus Md5 | PB-INPUT-1..4 |
| Push token durability | opus Md4, fable F10 | PB-PUSH-6 |
| Handset attack surface incomplete | codex#11 | PB-SEC-10..14 |
| Vibe criteria | codex#14, opus, fable F12 | §9 |
| ABI set / production-ready escape hatch | codex#12 | PB-TOOL-2, §10, §13 |
| Relay ops floor | codex#13 | PB-OPS-1..5 (**corrected 2026-07-26**: the note claiming v3's PB-OPS-5 was deleted and that this row pointed at a phantom id is STALE — PB-OPS-5 is live at §6.17, owned by S20, and delivered) |
| Sequencing | all three | §11 |

### Round 2

| Finding | Raised by | Lands in |
|---|---|---|
| Gateway inbound replay guard + cursor in memory — **latent in today's shipped tree** (no production phone client), **exploitable during Phase B** if durable keys land before durable phone/gateway sequence state | opus H1, re-scoped in rounds 3-4 | §4.6, PB-GW-1..8, PB-DOC-5 |
| "Stated bound" everywhere — numbers still delegated to the implementer | codex#1 | §6.0 budget table |
| PB-SYNC-1 impossible: shared seq bucket, `Gap` carries no kind | codex#2, fable#2 | PB-SYNC-1 |
| Receive path has no atomic commit; crash loses or replays a frame | codex#3, fable#8 | PB-STATE-7 |
| Lock invalidates auth but does not purge the key/caches in Go memory | codex#4 | PB-KEY-7 |
| Emulator evidence defines the "your Android phone" criterion away | codex#5 | PB-E2E-5, §13 |
| Grant replay high-water omitted from persisted state | codex#6 | PB-STATE-1 |
| Burn-the-gap creates outbound gaps; gateway silently drops gapped input | codex#7, opus H5, fable#1 | PB-STATE-8 |
| FCM token lifecycle unwired (getToken/onNewToken/re-register) | codex#8, fable#7 | PB-PUSH-9 |
| Rollback detection has no trust anchor | codex#9 | PB-STATE-4 |
| S3<->S4 cycle; PB-STATE-6 cycle; PB-KEY-2 homeless; missing edges | codex#10/11, opus H3, fable#9 | §11 (rebuilt DAG) |
| PB-BIND-0 allowlist not executable; omits transitive deps | codex#12 | PB-BIND-0 |
| PB-TIME covers only command expiry | codex#13 | PB-TIME-2 |
| PB-OPS silently pulled Phase C work into Phase B | codex#14 | §6.18 scope correction |
| Per-role custody tiers; sealed grant is a content-key equivalent; `KeyStore` must be failable | opus H2 | PB-KEY-5, PB-KEY-6 |
| Push toggles have no transport verb; `terminal_watch`/`unwatch` missing from the façade | opus H4 | PB-PUSH-8, PB-BIND-3 |
| Input coalescing vs `MailboxAppendPerMin` (autorepeat trips quota) | fable#4 | PB-INPUT-5, §6.0 |
| Nothing pins a fixed dark theme; ADR attributes light+dark to a dark-only artifact | opus M8 | PB-TOK-2, PB-DOC-6 |
| Which tier seals which state | opus M9 | PB-STATE-9 |
| Fail-closed dead-ends on the re-pair block | opus M6 | PB-STATE-10 |
| Presence-interaction mechanism described wrongly | opus L1, fable#3 | PB-NET-5 |
| Ten further falsified claims | opus M1-M5, fable#5/6 | §12 round-2 table |

---

## 9. Criteria discipline

Round 1 found many criteria satisfiable while the requirement stayed unmet. Rules now binding:

1. **No "reviewed" as a pass condition.** Every such criterion names an evidence artifact under
   `docs/verification/`.
2. **No single-process proof of a multi-process property.** Anything about replay, resume, or
   restart must include a real process kill (PB-STATE-2).
3. **Numbers, not targets.** Latency, backoff, queue bounds, rate limits and freshness windows
   state pass/fail values, sample counts, and percentiles.
4. **Schemas, not adjectives.** "Opaque payload" is replaced by a pinned schema test.
5. **Adversarial framing.** Where a lazy implementation could satisfy the letter (a boolean
   "authenticated" flag, a phone-side-only latency fix), the criterion names that shortcut and
   fails it.

---

## 10. Verification strategy and honest limits

**Tiers**: (1) Go end-to-end with real components (PB-E2E-1) — everything but the Kotlin UI;
(2) Android JVM/Robolectric + `gradlew lint test`; (3) emulator smoke (PB-E2E-2) — the only
tier proving the APK runs and JNI works on a real Android runtime, on the shipping ABI.

| Limit | Consequence | Mitigation |
|---|---|---|
| **No physical handset** | Hardware-backed Keystore, ~~real biometrics,~~ real cellular/Doze behavior unproven *(biometrics struck 2026-07-31, ADR-007 B133 — the feature left the product, so it is not an unproven limit; see PB-E2E-5)* | Emulator exercises the code path on the shipping ABI; the hardware guarantee is **not** proven here. This gates the "production-ready" claim (§13). |
| No Firebase project | Real FCM delivery unverifiable | Full implementation + fake-endpoint tests + credential runbook; PB-PUSH-5 keeps the system usable without push |
| No provisioned VPS relay | Real-network latency/NAT unverified | Local relay proves protocol correctness; PB-OPS-1 makes deployment reproducible |

---

## 11. Slices and the agent dance

opus and fable subagents only; **independent agents per role** (test author, implementer,
reviewer do not share context); TDD with an evidenced RED run per slice.
*(This contradicts roadmap:286; PB-DOC-4 amends it.)*

Reordered per unanimous round-1 feedback: architecture and custody decisions move **before** the
façade is frozen.

Round 2 found a real **cycle** (PB-STATE-6 sat in the state slice but required Android backup
exclusion, which depended transitively back on it), a second cycle (the transport slice owned
PB-NET-1, which needs the façade that depended on it), a **homeless requirement** (PB-KEY-2's
tests need an Android project that no listed dependency provided), and four missing edges. The
graph below is an acyclic DAG: Go-only work first, Android work second, integration last.

**Stage 1 — Go core (no Android toolchain needed)**

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| **S0 ADR decisions**: custody tiers, state model, transport change, resync buckets, single-machine, light-mode deferral | PB-DOC-1 (decisions only) | opus | — |
| **S1 Dependency-edge surgery** (executable allowlist) | PB-BIND-0 | opus | — |
| **S2 Gateway inbound durability** (§4.6) | PB-GW-1, PB-GW-3, PB-GW-4, PB-GW-5, PB-DOC-5 | opus | — |
| **S1b Protocol additions**: reconciliation frame, lease confirmation, reply correlation id | PB-SYNC-7 | opus | S1 |
| **S2b Gateway outbound durability**: append budget, coalescing, delivery-unknown semantics, outbox | PB-GW-7, PB-GW-8 | opus | S2 |
| **S3 QR renderer + payload** (machine-side; zero façade coupling — startable immediately) | PB-PAIR-1, **PB-PAIR-7** | opus | — |
| S4 Gateway supervision (3 states) + release artifacts | PB-LIFE-1..6, PB-OPS-4 | opus | — |
| S5 Design tokens | PB-TOK-2, PB-TOK-3 | fable | — |
| **S4b Gateway supervision: the revoked terminal state** (split from S4) | PB-LIFE-7 | opus | S4 |
| **S6 Transport resilience + TLS** | PB-NET-2, 3, 4, 6, 7, **PB-NET-8** *(added 2026-07-31, ADR-007 B120: PB-NET-4's other hop, which no row required)* | opus | S1 |
| **S6b Low-latency input path**: request-id correlation + concurrent dispatch, both hops (ADR B7) | PB-NET-5 | opus | S6 |
| **S7 Durable phone state** (Go-side; the Android *sealing* parts are **S15**) | PB-STATE-1..5, 7, 8; **PB-GW-6** (the phone `IssuedAt` seal change PB-GW-2 depends on) | opus | S0, S1, **S2, S2b** (PB-STATE-4's rollback authorities *are* PB-GW-1's inbound high-water and PB-GW-8's outbound ceilings, so neither can ship after it), S1b |
| **S7b Gateway age check** (split out: it depends on the phone seal change) | PB-GW-2 | opus | S2, S7 |
| **S8 Façade + bind guard** | PB-BIND-1..7, PB-SAS-1, **PB-SAS-2** (sole owner; S19 contributes emulator evidence but does not own it), **PB-SAS-4** (added 2026-07-29 with PB-PAIR-4; one protocol change, not two) | opus | S6, S7 |
| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |
| S10 Per-bucket resync + grant recovery | PB-SYNC-1..6, **PB-SYNC-8**, PB-KEY-3/4, **PB-KEY-10** | opus | S8, S9 |
| S11 Input/lease semantics, coalescing, clock skew | PB-INPUT-1..6, PB-TIME-1..3 | opus | S8 |
| S12 Push: trigger, seam rename, FCM sender, preference verb | PB-PUSH-0..3, 5..8, **PB-PUSH-10** (machine side) | opus | S0, S8 (PB-PUSH-8 needs a façade method + signed-action work) |

**Stage 2 — Android**

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| S13 Android skeleton + build + CI + runtime policy | PB-TOOL-1..7, PB-RUN-1..5, **PB-TOK-4** *(reassigned from S5)* | fable | S8 |
| **S14 Key custody on Android** (resolves PB-KEY-2's homelessness) | PB-KEY-1, 2, 5, 6, 7, **8**; PB-SEC-1, 2 | opus | S13, S0, S7 *(PB-KEY-6's failable-`KeyStore` **signature** lands in S7 so S6/S7/S8 build against it once; only the Android implementation is here)*, S14a |
| **S14a Epoch key rotation on Android custody** (split from S14) | PB-KEY-9 | opus | S7 |
| S15 State sealing + backup exclusion (breaks the v2 cycle) | PB-STATE-6/9, PB-SEC-10 | opus | S14, S7 |
| S16 Screens + phone-side pairing | PB-APP-1..11 *(PB-APP-11 added 2026-07-31, ADR-007 B121: §6.0's freshness budget, specified since v2 and never built)*, PB-PAIR-2..6, PB-SAS-3, **PB-TOK-1** *(reassigned from S5)* | fable, opus review | S13, S5, S3, S10, S11, S12, S14 |
| S17 Push receiver | PB-PUSH-4, **PB-PUSH-9** (sole owner: token lifecycle is client-side by definition) | fable | S13, S12, S14, S15 |
| S18 App security hardening | PB-SEC-3..8, 11..14 *(no PB-SEC-9 exists; PB-SEC-10 is owned by S15)* | opus | S16, S17, S4 |
| **S18b Fail-closed recovery** (was homeless: needs both the fail-closed path and the unblock) | PB-STATE-10 | opus | S7, S10, S16 |

**Stage 3 — integration**

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| S19 E2E + emulator smoke | PB-E2E-1, PB-E2E-3, PB-E2E-4 (PB-E2E-2 re-scoped to S21 on 2026-07-30, ADR-007 B91: unsatisfiable on an emulator by construction) | opus | S4, S4b, S6b, S7b, S9, S10, S11, S15, S16, S17, S18, S18b |
| S20 Docs / ADR / ops runbooks | PB-DOC-2, 3, 4, 7 (PB-DOC-6 withdrawn); PB-OPS-1..3, **PB-OPS-5** *(PB-DOC-1 is owned by S0 and PB-DOC-5 by S2 — not duplicated here)* | fable, opus review | S19 |
| S21 Physical-handset gate (deferred; no device here) | PB-E2E-5, **PB-E2E-2** (re-scoped here on 2026-07-30, ADR-007 B91) | — | S19 |
| S22 Design system foundation | PB-TOK-5, PB-TOK-6, PB-TOK-7, PB-TOK-8, PB-DS-1, PB-DS-2, PB-DS-3, PB-DS-4, PB-DS-5 | opus, sonnet review | S13, S16 |
| S23 Component kit | PB-DS-6, PB-DS-7, PB-DS-8, PB-DS-10 | opus, sonnet review | S22 |
| S24 Screen recomposition | PB-DS-9, **PB-DS-11** *(reassigned S23 -> S24 on 2026-08-01: the requirement forbids allowlisting existing violations, and every existing violation lives in the three surface files S24 rewrites. Owned by S23 it was satisfiable only by the allowlist its own text prohibits.)*, PB-DS-12 | sonnet, opus review | S23, S16 |

**Ownership is machine-checked, not prose.** The authoritative assignment lives in
`docs/specifications/remote-phaseB-manifest.tsv` (one row per requirement, exactly one owning
slice), enforced by `scripts/check-phaseb-manifest.py`. The slice table above is the readable
view; the manifest is the source of truth.

This exists because ownership-in-prose failed three rounds running: round 2 found PB-KEY-2
homeless, round 3 found PB-STATE-10 and PB-SAS-2, and round 4 found PB-GW-7, PB-GW-8,
PB-KEY-8 and PB-PUSH-10 — one of them the spec's own exit-criterion-fatal requirement. Each
time the requirement was written into §6 and never wired into the DAG, and each time only a
careful human reader caught it. The checker is verified against a negative control (deleting a
row fails the run). v3 also gave both S19 and S20 the dependency "all", which read literally
made each depend on the other; every edge is now enumerated.

The DAG itself is machine-readable in `docs/specifications/remote-phaseB-slices.tsv` and the
same checker enforces acyclicity **and that no slice is an orphan** — unreachable from S19, the
exit demonstration. Round 5 found S2b in exactly that state: it owned the exit-criterion-fatal
live-tail requirement, yet nothing depended on it, so S19 could have passed every gate without
it ever being built. Running the check then caught a second orphan the reviewers had not
flagged, S4 (gateway supervision) — PB-E2E-1's `pair -> ... -> revoke` flow needs the gateway
running after pairing, so S19 depends on it too. Ownership and reachability are both mechanical
now; neither is asserted in prose.

**Two split points, recorded so they do not become cycles.** (a) PB-BIND-3 lists a
push-preference façade method, but the verb it calls (PB-PUSH-8) is owned by S12, which
depends on S8 — so S8 owns the *surface* and S12 owns the *wired-to-real-verb* test.
(b) PB-PAIR-3 justifies its scanner choice "under PB-SEC-14", owned by S18 which depends on
S16 — so S16 owns the *decision* and S18 owns its *enforcement*. PB-INPUT-6's
background/auth-expiry boundary tests are likewise contributed by S18, which already follows
S11 transitively.

S0/S1/S2/S3/S4/S5 start in parallel. Each slice gate: evidenced RED -> implementation
(independent agent) -> independent review -> `go build/vet/test -race ./...` (plus the Gradle
gate once S13 lands) green before any dependent slice starts.

---

## 12. Corrections to v1 (reviewer-falsified)

| # | v1 claim | Correction |
|---|---|---|
| W1 | "38 `--p-*` tokens" | **31** distinct (opus + fable independently) |
| W2 | "`apns.go` is 23 lines" | **22** |
| W3 | token evidence cited `server.go:898,935` | those are a read and a delete; the decl is `:173`, sole write `:830` |
| W4 | unqualified `server.go` citations | three different `server.go` files exist; all citations now qualified |
| W5 | "QR decode at `qr.go:86`" after a `crypto/` citation | it is `internal/remote/pairing/qr.go:86` |
| W6 | "34 of 48 exported symbols fail" | not reproducible; withdrawn. Conclusion unchanged; PB-BIND-2 emits the true count |
| W7 | PB-BIND-0 denylist | omitted `internal/shimwire`; converted to an **allowlist** |
| W8 | "stolen phone is new in Phase B" | ADR-007:10/:89 makes it a founding threat; Phase B implements/verifies it (§3) |
| W9 | "emulator is x86_64" | host is Apple M1; the AVD is `arm64-v8a`, the shipping ABI |

### Round-2 corrections (v2 -> v3)

| # | v2 claim | Correction |
|---|---|---|
| X1 | "`ContentKey` in 8 exported signatures" | **9** exported non-test signatures in `internal/phonecore` (scope now stated; repo-wide the count is higher, which is why the scope matters) |
| X2 | "53 non-stdlib packages" | **52**, by `go list -deps -f '{{if not .Standard}}...'` and by an explicit stdlib filter — both agree (one reviewer reported 53) |
| X3 | §3 "only the third and fourth rows are new" | the **fourth and fifth**; the stolen handset (row three) is explicitly pre-existing |
| X4 | "capability pinned at `pairing.go:205`" | ambiguous and half-wrong: `internal/remote/pairing/pairing.go:205` is the rate limiter; the pin is `internal/skeleton/pairing.go:205`. Two reviewers "disagreed" because each read a different file — exactly the W4 hazard |
| X5 | W3's own correction | also wrong: `server.go:199` initializes the token map, `:830` is the sole write, `:843`/`:935` delete |
| X6 | W4 "all citations now qualified" | self-falsifying; several `server.go`/`config.go` citations remained unqualified. Load-bearing ones are now qualified, and the claim is narrowed to that |
| X7 | "ADR-007 D5 (`:50`) mandates gateway persistence" | an over-read: `:50`'s subject is process isolation; persistence appears only in a subordinate parenthetical. §4.3's argument stands on the code, which is the right ground |
| X8 | §2 "checked-in wrapper" | not checked in at `8cf5bee` — wrapper **generation** was verified in a scratch build; checking it in is PB-TOOL-4 |
| X9 | PB-SYNC-1 "per-stream staleness" | **impossible** as written; corrected to per-seq-bucket staleness with per-channel repair |
| X10 | citation off-by-N | `deviceauth.go:73-75`->`:74-76`; `cmd/swarm-remote/config.go:76`->`:77-78`; relay `server.go:1136-1139`->`:1135-1140`, `:743-747`->`:743-749`; `protocol/server.go:164` is the const, enforcement is in `requireRemoteAuthz` |

---

## 13. Definition of Done

Phase B is done when:

1. Every PB-* requirement has passing evidence. **A requirement may become a §10 limit only if
   the committee agrees AND it is not load-bearing for the exit criterion** — closing v1's
   escape hatch, which made the whole document non-binding (codex#12).
2. The exit criterion is demonstrated: an Android build pairs, observes, launches, and types
   into a real session over a real relay (PB-E2E-1 machine-checkable; PB-E2E-2 on emulator,
   including a process death).
3. `go build ./... && go vet ./... && go test -race ./...` green; `./gradlew lint test` green.
4. No Phase A regression, re-asserted through the real transport and across a process restart.
5. ADR amendment + verification evidence merged.
6. The committee agrees, with residuals documented, non-adversary-reachable, and accepted.

**Completion status, and why "done" is not available here.** Round 2 objected that v2's honesty
clause, while it did block a false "production-ready" label, otherwise defined the binding exit
criterion away — the criterion says "your Android phone", and emulator evidence is not that.
So Phase B has two distinct end states, and the final audit must name which was reached:

- **Provisionally implemented** — items 1-6 above hold, with PB-E2E-5 (the physical-handset
  gate) outstanding. This is the ceiling achievable on this machine: no handset, no Firebase
  project, no deployed relay. Hardware-backed Keystore, ~~real biometrics,~~ Doze, cellular
  handoff, and real camera pairing remain unverified. *(Biometrics struck 2026-07-31, ADR-007
  B133: removed from the product, so it is not an unverified item. Nothing else in this list
  moves — that is the test separating removal-by-feature-deletion from reclassification.)*
- **Done** — additionally PB-E2E-5 has been executed on real hardware.

Declaring "done" without PB-E2E-5 is not permitted, and neither is quietly reclassifying
PB-E2E-5 as a §10 limit: it is a deferred gate with a runbook, not an accepted gap.

---

## 14. Committee convergence on the requirements (rounds 1-5)

The goal gated implementation on the committee agreeing the requirements are well-defined with
complete coverage. That gate is met at v3.5.1.

| Round | codex | opus | fable |
|---|---|---|---|
| 1 | REVISE | REVISE | REVISE |
| 2 | REVISE | REVISE | REVISE |
| 3 | REVISE | REVISE | REVISE |
| 4 | REVISE | REVISE | REVISE |
| 5 | REVISE (one blocking edge, now fixed) | **requirements-complete** | **nothing blocking; recommends converging on v3.5** |

**Why this is convergence rather than fatigue.** codex's round-5 objection was a single missing
DAG edge that left the slice owning the live-tail requirements unreachable from the exit
demonstration. It is fixed in v3.4, and both other reviewers then re-derived the DAG
independently and confirmed the fix — fable running six negative controls against the checker
(deleted row, duplicate owner, orphaned slice, injected cycle, phantom id, withdrawn-but-owned;
all fire, pristine passes) and opus injecting a cycle of its own. codex has not re-reviewed
v3.5, which is stated here rather than glossed; it gets its say in the final implementation
audit, which the goal also requires.

**What the five rounds bought.** Each round found gaps that were fatal, not cosmetic: a phone
that stops typing forever after one Android process kill; a QR with no encoder, then a QR with
no destination; a keystroke path that could not be low-latency on either hop; an unrecoverable
epoch-grant loss; a push transport with no producer; a supervisor that crash-loops after every
revoke; a live tail whose budget could not close; a journal repair channel that is a no-op
against the shipped phone cache; and two drain legs that bust the op quota mid-demonstration.

It also caught four errors of mine: an overstated security claim (retracted), a bounded-age
check that would have rejected every legitimate keystroke, a "never burn a seq" rule that would
have caused silent data loss, and a rollback anchor that named authorities no frame can carry —
which would have left the phone permanently fail-closed.

**Structural outcome.** Two classes of error recurred until they were made mechanical rather
than reviewed: requirement ownership (three rounds running) and slice reachability. Both are
now enforced by `scripts/check-phaseb-manifest.py` over
`remote-phaseB-manifest.tsv` + `remote-phaseB-slices.tsv`, verified by negative control.

**Carried forward as non-blocking hygiene** (recorded, not gating): citation drift in body
prose that §12 already corrects; the checker not yet wired into CI (S20); §11's readable table
not cross-checked against the authoritative manifest; and the S19<-S4 edge being conservative
(a Go E2E can run the gateway in-process).
