# Phase B requirements — Android handset (the v1 milestone)

**Status**: v3.5, after audit-committee rounds 1-5. Rounds 1-4 returned REVISE from all three
reviewers; round 5 returned **requirements-complete** from opus, with codex's single blocking
edge and opus's late round-4 blockers integrated. Rounds 1 and 2 returned REVISE from
all three reviewers. **Round 3 disproved v3's own claim** that §4.6 was a live Phase A security
hole; the claim is withdrawn and the finding is now correctly scoped as a durability /
defense-in-depth defect with no reproduced exploit (PB-GW-*). Recording that retraction
prominently is deliberate: an unnecessary amendment to a committee-signed closure is its own harm.
**Date**: 2026-07-25.
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
| Gradle | 9.6.1 system; wrapper **generation** verified in a scratch build pinning 8.11.1. The wrapper is **not yet checked in** — that is PB-TOOL-4. | scratch build |
| Emulator + AVD | `swarmtest`, Android 15, `google_apis/arm64-v8a`; boots headless in ~30 s, adb attaches | `$ANDROID_HOME/emulator` |
| Host CPU | Apple M1 (arm64) | — |
| Go | 1.26.1 toolchain (module declares `go 1.24.2`, `go.mod:3`) | system |

Proven by producing a real AAR containing `jni/arm64-v8a/libgojni.so`. Two toolchain facts the
build scripts must encode:

- `gomobile bind` requires `golang.org/x/mobile` **in the module dependency graph**
  (`go get -tool golang.org/x/mobile/cmd/gobind`); a Go 1.24+ tool directive, not linked into
  the daemon binaries.
- NDK 27 supports API 21..35 but gomobile defaults to API 16 and **fails**; every build must
  pass `-androidapi >= 21`. (This is the NDK floor, **not** the app's `minSdk` — PB-RUN-1.)

**Not available, bounding what "verified" can mean (§10):** no Xcode/Apple account (iOS is
Phase C by design), **no physical Android handset**, no Firebase project, no provisioned VPS relay.

---

## 3. Threat model: what Phase B actually changes

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
| **Stolen or lost handset** | asserted-but-unimplemented ADR property | **implemented and verified here** |
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
| Input latency, phone `Type` -> PTY write, local relay | p50 <= 150 ms, p95 <= 400 ms, p99 <= 800 ms, n >= 200 | PB-NET-5 |
| Append latency while a wait is outstanding | <= 50 ms for the append call to complete | PB-NET-5(a) |
| Server-side wait (long-poll) maximum | 25 s (under common 30-60 s idle-proxy timeouts) | PB-NET-5 |
| Non-wait request timeout | 10 s | PB-NET-7 |
| Reconnect backoff | initial 500 ms, factor 2, ceiling 30 s, jitter +/-20% | PB-NET-4 |
| **Input frame rate (client-side coalescing)** | <= 8 frames/s sustained, coalescing a 30 Hz autorepeat burst into one frame per 125 ms | PB-INPUT-5 — must stay under `MailboxAppendPerMin: 600` (= 10/s), which is the **only** cap that applies: `OpsPerMin` explicitly excludes `mailbox_append` ("mailbox_append and push_trigger keep their own dedicated windows", `internal/remote/relay/config.go:39-44`). The 20% headroom is deliberate. **PB-OPS-1 must require the demonstration relay's configured quota to be >= the default**, since quotas are operator-tunable and a lowered one would silently break live typing. |
| Callback queue | 256 items, drop-oldest with a surfaced overflow signal | PB-BIND-6 |
| Idempotent op queue | 64 ops, reject-new with an error (never a silent drop) | PB-NET-4 |
| Resync rate | <= 1 per stream per 5 s, <= 12 per 5 min | PB-SYNC-6 |
| Biometric freshness | 60 s for input/take_control; **per-use** (`CryptoObject`) for revoke, kill switch, launch, kill | PB-SEC-2 |
| Seq reservation block | 256 (bounds seqs burned per crash) | PB-STATE-3 |
| Clock skew | reject and surface distinctly beyond +/-30 s | PB-TIME-1 |
| Push coalescing window | 30 s per session | PB-PUSH-0 |
| Local pairing state TTL | **60 s, matching the relay's authoritative `RendezvousTTL`** (v3 said 10 min and never justified keeping a pairing secret for nine minutes after it became unusable) | PB-PAIR-4 |
| Max concurrent pending waits per client | 1 (a second wait is refused, not queued) | PB-NET-5(c) |
| Inbound bounded-age (`maxAge`) | 10 min | PB-GW-2 |
| Push envelope TTL / replay window | 10 min, with the replay coordinate persisted per PB-STATE-1 | PB-PUSH-3 |
| Cached-state freshness before it is shown as stale | 5 min without a successful poll | PB-APP-8 |
| Latency harness | median of 3 runs, n >= 200 samples each, 20-sample warm-up discarded, 1-16 byte payloads, on an otherwise-idle machine, local relay over loopback; CI records the environment | PB-NET-5 |
| Max coalesced input payload | 4 KiB per frame (flush early if exceeded) | PB-INPUT-6 |
| **Inbound drain rate (reads + acks), each hop** | <= 3 reads/s **and** batched acks <= 1/s per routing id, i.e. <= 240/min combined — because **`mailbox_read` and `mailbox_ack` DO meter against `OpsPerMin: 600`** (`internal/remote/relay/server.go:766,798`), unlike `mailbox_append`. §6.0 previously budgeted both *append* legs and neither *drain* leg: at 8 appends/s a wait returning on the first item gives 8 reads/s + 8 acks/s = **960/min > 600**, so the live tail would die with `codeQuotaExceeded` after ~37 s, mid-demo. The same arithmetic applies to the gateway hop once PB-NET-5 removes its 500 ms poll (120/min today). | PB-NET-5(c), PB-GW-7 |
| **Machine->phone append rate (gateway coalescing)** | <= 8 appends/s sustained across journal **and** terminal combined (they share one sink and one target), i.e. terminal snapshots coalesced to <= 125 ms — against a render loop that can emit ~62/s | PB-GW-7 |
| **Signed `ExpiresAt` by op class** | ordinary commands **now + 1 min**; **`take_control` now + 15 min** so the lease is not the binding constraint on a typing session. Stated as an explicit exception because PB-TIME-1 otherwise reads as a blanket 1 min. | PB-INPUT-3, PB-TIME-1 |
| Biometric-freshness renewal | a typing session crossing the 60 s freshness window must **pause input and re-authorize**, not silently continue or silently drop; the lease itself is not ended by freshness expiry | PB-SEC-2, PB-INPUT-3 |

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
| PB-GW-2 | The inbound receiver enables the bounded-age check, which `NewMailboxReceiver` leaves at `maxAge == 0` — an age backstop even if a high-water is lost. **Value: 10 minutes** (well above the 60 s command TTL and any plausible delivery delay, well below the 7 d retention cap). **Gated on PB-GW-6**: enabling it first would break all inbound traffic. | Test: an authenticated envelope older than the bound is refused with `ErrStaleAge`, **and** legitimate phone-sealed traffic is unaffected — the second assertion is what makes this test honest. |
| PB-GW-3 | **A per-frame-class crash matrix**, not a single "atomic commit". A local transaction cannot atomically span the persisted high-water, the persisted cursor, an external PTY/daemon side effect, and the relay ack, so the rule differs by class: live input may persist consumption *before* the PTY write and accept loss on crash (it is live-only per ADR-007 D7); high-level operations rely on the daemon's durable two-phase idempotency for duplicate suppression; watch/unwatch needs an idempotent convergence rule. | Each class has a stated allowed-loss / duplicate-prevention rule and a crash-injection test at each boundary. |
| PB-GW-4 | **Per-action-class replay tests** against a retaining (adversarial) relay across a restart: input, take_control, take_control_end, idempotent mutations, and terminal watch/unwatch. **The input class must model a seq-regressed phone** (or an explicitly seeded-low receiver) as its adversary — against a monotonic phone the test passes with or without PB-GW-1, repeating the same "proves nothing" flaw as v3's empty-lease-manager test. The §4.6 trace (legitimate lease at seq 1, then contiguous retained inputs from seq 61) is the scenario to encode. | Each class asserts at the guard that is supposed to enforce it, and **each test must fail against unfixed code for the right reason** — demonstrated, not assumed. |
| PB-GW-7 | **A machine->phone append budget with gateway-side coalescing, and no seq burned on a failed append.** The numbers do not currently close: `renderDebounceWindow = 16 ms` (`internal/daemon/terminalrender.go:33`) lets a live peek emit ~62 snapshots/s, while the relay caps appends at `MailboxAppendPerMin: 600` (= 10/min-window) per target. Worse, `RelaySink` allocates the seq **before** the append and returns on append error (`internal/remotegw/relaysink.go:154,181`), so **every quota-refused snapshot permanently burns an outbound seq** — manufacturing gaps that PB-SYNC-1 must conservatively stale on *both* journal and terminal, exhausting PB-SYNC-6's resync budget within minutes. Journal and terminal share one `RelaySink` and one target, so a peek starves the journal too. `internal/remotegw/gateway.go:29` already states the intended contract ("bounded/coalescing on the relay side") that `RelaySink` does not implement. **This is exit-criterion-fatal: "types into a real session" is meaningless without the live tail (PB-APP-4).** Coalescing holds the outbound rate under the budget in §6.0, and a sustained-peek test runs for >= 60 s without quota refusal, without manufactured gaps, and without starving the journal. **The naive remedy "a failed append never consumes a seq" is FORBIDDEN — it is unsafe.** The relay commits the item *before* replying (`internal/remote/relay/server.go:758-762`) and `MailboxAppend` returns an error when the *response* read fails (`client.go:268`), so failure is not always pre-commit: relay stores seq N -> connection drops before the reply -> gateway reuses N for different plaintext -> the phone accepts whichever seq-N envelope lands first and stale-drops the other, i.e. **silent journal/snapshot loss or reordering**. Required instead: allocate the seq only after local admission/coalescing (so *expected* quota refusals never reach allocation); distinguish a definitive pre-commit refusal from a delivery-unknown failure; and on delivery-unknown either burn the seq or retry **the exact same sealed envelope**. Note the idempotency is *receiver-side and free* — a duplicate of an identical sealed envelope is stale-dropped by `MailboxReceiver` (`internal/remote/crypto/envelope.go:255-257`) — so this needs **no relay protocol change**; `mailbox_append` carries only `{target, envelope}` and does not need an append-identity field. Tests must inject a connection loss *after* relay commit but *before* the response. |
| PB-GW-8 | **The gateway's outbound journal cursor must be durable.** `Gateway.cursor` is a bare `uint64` (`internal/remotegw/gateway.go:47-50`) that nothing persists or seeds, while two comments call it durable — "its **durable** resume point" (`gateway.go:59`) and "resumes journal delivery from its last durable cursor" (`service.go:56-57`). Every restart therefore re-reads from cursor 0 and re-appends the entire journal at fresh seqs into the same 600/window mailbox. This is the **fourth** instance of a comment presuming durability that does not exist, and PB-LIFE-1/-5 make restarts routine. | Restart test: the gateway resumes from its persisted cursor and does not re-append delivered journal records. **"Does not re-append" cannot be achieved by a local cursor write alone** — persisting a cursor is not atomic with a remote append (same distributed-commit hole as PB-GW-7). Requires a durable outbound outbox coupling {journal cursor, sealed envelope, relay outcome}, replayed idempotently on restart. |
| PB-GW-5 | The Phase A closure records **only what was reproduced** (see §4.6): a missing durable inbound high-water and a disabled age check, with the original claim scoped to a single gateway run. It must not be amended to assert an exploit that was disproved. | PB-DOC-5. |

### 6.1 PB-STATE — durable on-device state (NEW; the most severe gap, §4.3)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-STATE-1 | The core persists and restores everything resume-critical: device keys, pinned machine static + sign pub + routing id, epoch id + keys, **outbound send-seq**, **per-(sender,epoch) receive high-water**, **the grant receiver's `(highest epoch, grant_seq)` watermark** — which `internal/remote/crypto/epoch.go:155,167` explicitly requires be "persisted across restart (F3)", or "a relay could replay an old correctly-signed grant after a phone/app restart" — the wake-envelope replay coordinate chosen by PB-PUSH-3, the relay mailbox cursor, session/snapshot caches, pending idempotent ops and their outcomes, and per-bucket stale flags. | Enumerated in one persisted schema; a test asserts each field survives a restart, including a grant-replay-after-restart test. |
| PB-STATE-2 | **Process-death acceptance test**: kill the core process mid-session and restart. Typing, launch and kill must still succeed, and a frame captured before the kill must still be rejected as a replay. | The RED form of this test must first demonstrate today's stale-drop brick. This single test is the guard for §4.3 in both directions (liveness and replay). |
| PB-STATE-3 | Send-seq durability must not cost an fsync per keystroke: reserve-a-ceiling-and-burn-the-gap (block size per §6.0), mirroring `internal/remotegw/seqstore.go`. **Because this deliberately creates outbound seq gaps, the gap consequence must be specified** — see PB-STATE-8. | Decision recorded; a test asserts no seq is ever reused across a crash at any point in the reservation window, including a crash between reservation and use. |
| PB-STATE-4 | Writes are crash-atomic; corruption fails **closed**. **Rollback needs a named trust anchor**: AEAD and atomic writes detect corruption, not rollback — a valid older blob sealed by the same Keystore key stays valid, and KeyMint rollback-resistance protects key blobs, not arbitrary app state. **The v1 anchor is chosen here, not left to the implementer** (v3's "or an explicitly narrowed threat" let an implementation decline rollback protection entirely): **authenticated remote reconciliation on reconnect**, with **a distinct authority per coordinate** — v3.2 named the gateway's inbound high-water as *the* authority for all three, but it describes only phone->machine sequences and carries no information about the other two, so an implementation could pass while rollback still reset them. The authorities are: (a) **phone send-seq** -> the gateway's durable inbound accepted high-water (PB-GW-1); (b) **phone per-bucket receive high-waters** -> the gateway's durable *outbound* sequence ceilings (PB-GW-8's outbox); (c) **grant watermark** -> the daemon's epoch/grant issuance coordinate. Reserved-but-unused seq blocks (PB-STATE-3) must be accounted for in (a). When an authority is unreachable the phone fails closed for mutating ops, marks the affected channels stale, and reseeds. | Test rollback **per coordinate** — send-seq, every receive bucket, and the grant watermark — not send-seq alone; assert a retained machine frame and an older correctly-signed grant are both refused after a rollback. The test may not rely on hidden state unavailable after a real rollback. |
| PB-STATE-7 | **The receive path commits atomically.** Today the high-water advances inside `Accept` (`internal/remote/crypto/envelope.go:254`), caches mutate afterwards (`internal/phonecore/snapshot.go:201`), and the cursor/ack come later still — so a crash between them either loses a frame forever (stale-dropped on redelivery, never applied) or, if reordered, permits replay. `{high-water, relay cursor, decoded cache mutation, stale flags}` must commit as one transaction **before** the ack, with the ack idempotent on retry. | Crash injection at every boundary in the receive sequence; no frame is both acked and unapplied, and none is applied twice. |
| PB-STATE-8 | **Phone->machine gap semantics.** PB-STATE-3 burns seqs, and the gateway currently *silently drops* input/resize frames whose `Gap` bit is set (`internal/remotegw/command_loop.go:208-216`) while ignoring `Gap` on commands — so the first post-restart keystroke can vanish with no signal. The invariant must be stated and tested: a burned gap is absorbed by the re-lease command frame, never by an input/resize frame. High-level operation gaps must trigger durable outcome reconciliation before later state is trusted; live-input gaps may be discarded, but only explicitly. | Test asserts the first post-restart input frame carries no `Gap` bit, and that an operation gap forces reconciliation. |
| PB-STATE-9 | **Which tier seals which state** is specified, not left open: state the wake path must read while locked (push token, dedup coordinate) is sealed under the wake tier; send-seq, receive high-waters and decrypted caches are sealed under the content tier (PB-KEY-2). One undifferentiated "sealed" would let the implementer pick whichever tier passes. | Test asserts the locked-device process can read only the wake-tier state. |
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
| **PB-PAIR-7** | **The QR must carry a destination — today it does not, so "pairs" has no endpoint.** `internal/skeleton/pairing.go:112` mints `EncodeQR(QRPayload{RendezvousID, PairingSecret})`: `RelayURL` is never set, although the codec reserves it (`internal/remote/pairing/qr.go:40,50`) and the URL is available two frames up (`loadRelayURL`, `pairing_config.go:66`) but is used only to build the rendezvous closure and then discarded. `MachineStaticPub` (`QRFlagMachineStaticPub`) is likewise dead. A scanning phone therefore receives a rendezvous id and a secret **and no endpoint to dial**, so it cannot claim the rendezvous — and PB-PAIR-6's whole threat model ("a malicious QR cannot silently point the phone at an attacker-chosen relay") presupposes a destination the QR does not carry. This is round 1's "no QR encoder" one layer down. Requires: `BeginPairing` populates `RelayURL`, plus an explicit decision on pinning `MachineStaticPub` in the QR (it removes a TOFU window). | Test: a decoded QR yields a dialable relay URL; a phone driven only from the QR completes pairing with no out-of-band configuration. **PB-PAIR-1(b)'s sizing must be re-derived from the real payload**: production currently emits ~81 chars (fits ~23 half-block rows), not the ~161 v3 assumed, so the "does not fit 80x24" constraint was computed from a payload the code never produces and only becomes true once the URL and static pub are added. |
| PB-PAIR-2 | **Phone-side camera capture + decode**, with the `CAMERA` runtime permission requested, and a manual-entry fallback when it is denied or permanently denied. | Tests for granted, denied, and permanently-denied paths; manual-entry encoding is specified, not improvised. |
| PB-PAIR-3 | The scanner dependency is justified under PB-SEC-14 (ML Kit pulls Google Play Services, in tension with a minimal dependency set) — the choice is explicit. | Decision recorded in the ADR with the tradeoff stated. |
| PB-PAIR-4 | A **persisted** pairing state machine: process death at any transition (Noise msg1/2/3, SAS display, machine decision wait, local pin commit, grant bootstrap) resumes or fails closed — never a half-paired device. | Kill/restart test at each transition; a machine that committed while the app died before persisting pins is detected and resolved. |
| PB-PAIR-5 | Explicit terminal states for: declined (`pairing.go:71 ErrPairingDeclined`), SAS mismatch, rendezvous timeout, expired/consumed QR, and already-paired. Abandoned device keys and partial local records are cleaned up. | Test per state; each is user-legible, not an opaque error. |
| PB-PAIR-6 | A malicious QR cannot silently point the phone at an attacker-chosen relay. **The LAN case must be resolved explicitly**, because a blanket private-address rule would reject the very handset demonstration PB-OPS-1 describes (a phone reaching the laptop over the LAN): private/LAN destinations are **allowed only after the origin is displayed and explicitly confirmed by the user**; public destinations follow the same display-and-confirm rule; no destination is joined silently. | Tests: LAN target requires confirmation and succeeds after it; a target swapped after display is rejected; nothing is joined without the origin being shown. |

### 6.4 PB-KEY — key tiers and grant recovery (NEW; §3, opus M3/H2, fable F5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-KEY-1 | The **JNI key-custody contract**: exactly which artifact crosses the boundary, in which direction, and why that is acceptable — reconciling the Go core's native-heap keys with the Java-only Android Keystore API. | Contract documented in the ADR and in the façade package doc; a test pins the crossing to that one artifact. |
| PB-KEY-2 | ADR-007 A15's **two-tier split is honored on Android**: the wake key is after-first-unlock and readable by the push path; the content key is user-authentication-gated and **not** readable by the push path or derivable from the wake key. **The enforcement mechanism must be stated**: on iOS A15 relies on NSE process isolation, but on Android `FirebaseMessagingService` runs in the app process, so enforcement is Keystore auth-gating (unwrap fails while locked) plus code discipline — *not* OS isolation. The emulator's software Keystore proves the code path, not the hardware guarantee (§10). | Tests assert the push path cannot obtain the content key and a locked device cannot decrypt session content; the test states which of the two properties it actually proves. |
| PB-KEY-5 | **Custody tier per role**, not one undifferentiated "core key". `crypto.KeyStore` is a single interface over `NoiseStatic`, `OpenSealedBox`, `SignCommand`, `SignRelayAuth` (`internal/remote/crypto/keystore.go:47-56`), and background reconnect needs `SignRelayAuth` while locked, while `OpenSealedBox` recovers **both** wake and content keys from a grant. If the recipient key is therefore after-first-unlock, then a stolen once-unlocked handset **plus the persisted sealed grant** yields the content key — falsifying ADR-007:89 in the very phase meant to implement it. Assign a tier to each of `{NoiseStatic, Recipient, CommandSign, RelayAuth}` and state whether the sealed grant blob is discarded after opening or retained under the content tier. | Test: an attacker with after-first-unlock access and everything at rest reaches no content key. |
| PB-KEY-8 | **A platform capability matrix, so the custody design is implementable on real devices.** The protocol needs X25519 raw-key operations for Noise, `nacl/box`-compatible anonymous sealed-box opening (`internal/remote/crypto/keystore.go:163`), wire-compatible Ed25519 signatures, per-use biometric authorization for some roles, and locked-device relay auth for another — but Curve25519 entered KeyMint only in Android 13 and hardware backing is device-dependent. For each of `{NoiseStatic, Recipient, CommandSign, RelayAuth}` state whether it is generated/used natively in Keystore, held as an app-format key wrapped by an authenticated Keystore AES key, or software-only with a documented residual. Bind this to PB-RUN-1's minSdk. | Wire KATs against the current Go implementation for every role; a defined refusal/fallback when the handset lacks the required algorithm or auth capability; PB-E2E-5 asserts the achieved backing via `KeyInfo`. |
| PB-KEY-6 | **`crypto.KeyStore` must become failable.** *(Sequencing: the **signature** change is hoisted into S7 because S6/S7/S8 all consume this interface — leaving it at S14 in stage 2 would guarantee rework across every Go-side slice. Only the Android **implementation** stays at S14.)* `SignCommand(msg []byte) []byte` is errorless and `NoiseStatic() *NoiseStatic` exports raw private material — neither is implementable against Android Keystore, which never exports private keys and whose every operation can fail (user-auth-required, and permanent invalidation on biometric-enrollment change, which PB-SEC-2 explicitly requires). PB-SEC-2's "Keystore-enforced sign authorization" is unimplementable until this changes. `crypto` is inside PB-BIND-0's allowlist, so this is a cross-cutting interface change that must be owned by a slice. | Interface returns errors; a test drives an auth-required failure and a key-invalidated failure through every signing path. |
| PB-KEY-7 | **Lock purges live memory.** Invalidating the biometric gate is not enough: `MailboxRouter` holds `ContentKey` by value for its lifetime and caches decrypted sessions/snapshots (`internal/phonecore/snapshot.go:88,132`), so "locked device cannot decrypt" can pass while the process still holds the key and already-decrypted content. On lock, background, or auth expiry the core must stop content operations, zeroize/discard native key custody, purge decrypted session/snapshot/reply caches and sensitive UI state, and require a fresh unwrap before restoring content. | Test asserts no content key and no decrypted session content remains reachable after lock. |
| PB-KEY-3 | **Epoch-grant recovery.** Today a grant can be lost with no recovery: the relay refuses appends past the mailbox depth cap (`server.go:743-747`) and `SweepRetention` purges items older than `RetentionCap` (**default 7 days**, `config.go:90`) **even if never acked** (`server.go:1136-1139`); no re-grant verb exists anywhere. A phone offline across a rotation is then permanently unable to decrypt, and re-pairing is refused because `BeginPairing` fail-fasts while a device is registered. | Either a re-grant request path, or a defined user-legible terminal state plus a documented machine-side unblock. A test drives the offline-across-rotation scenario to a defined, recoverable end — not an indefinite decrypt-failure loop. |
| PB-KEY-4 | Key rotation while the app is backgrounded/offline is handled without data loss or silent breakage. **It must update the device record's `GrantedEpoch`**: `reconcilePairedDevices` removes any device whose `GrantedEpoch != curEpoch` on every daemon start (`internal/skeleton/serve.go:499-505`), so a re-grant or offline-rotation convergence that does not update it **silently unpairs the only device on the next restart**. §7's deferral of "the epoch-equality reconcile revisit" is scoped to *multi-device*; single-device re-grant hits the same mechanism and is therefore in scope here. | Test: rotate while offline, reconnect, converge, **restart the daemon**, and assert the device is still paired. |

### 6.5 PB-NET — transport (§4.5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-NET-1 | The real `relay.Client` drives the core through the façade; the `phonesim` mailbox seam (`phonesim.go:42`) stays for testability. | Integration test: façade + real client + real in-process relay: pair -> read -> ack -> append. |
| PB-NET-2 | TLS verified by default; a pinned self-signed cert is an explicit opt-in for self-hosted relays; cleartext refused **except** an explicit, narrowly-scoped loopback carve-out for the in-process test relay — which is `ws://`-only today (`server.go:228`), making v1's unconditional ban self-contradictory (fable F6). The Go client's **trust-root source on Android must be stated** (embedded bundle vs pinning-only); `x509.SystemCertPool` is not usable as on desktop (opus H3). | Tests: bad cert fails closed; non-loopback cleartext rejected; the carve-out cannot be enabled in a release build; pinning accepts only the pinned cert. |
| PB-NET-3 | The transport handles only opaque sealed frames and never holds content keys. | Test asserts a known plaintext marker never appears on the wire. |
| PB-NET-4 | Resilience: automatic reconnect, bounded exponential backoff with **stated numeric** ceiling and jitter, re-auth after reconnect, connection state surfaced. **Input and resize are never queued or replayed** (ADR-007 D7 `:60-62`: live-only, "delivery unknown / not sent"); only high-level idempotent ops may queue, with a stated bound. | Tests against a flapping relay assert the retry ceiling, state transitions, re-auth, that no keystroke is ever replayed, and the idempotent-op queue's bound and drop signal. |
| PB-NET-5 | **Low-latency input across BOTH hops.** The mechanism is a stated protocol change — request-id correlation with concurrent dispatch, or an explicit server-push frame — because §4.5 proves a naive long-poll head-of-line-blocks the keystroke path and a second connection is not available. It must also drop the gateway's 500 ms command-IN poll (`service.go:27`), which ADR-007:461 calls "unusable for live typing"; a phone-side-only fix passes v1's criterion while typing stays 500 ms-gated (fable F4). Interaction with presence must be stated. *(v2 described this wrongly: `SweepPresence` (`internal/remote/relay/server.go:1105-1132`) fires when the MACHINE's presence entry times out, toward paired-peer tokens — the phone's own connectivity is never consulted. The interactions actually worth stating are that a GATEWAY parked in a wait keeps the machine's presence online, and that pushes fire redundantly at an already-connected phone.)* | **Acceptance is end-to-end and bidirectional, against §6.0's numbers**: (a) with a wait outstanding, a keystroke append from the same client completes within 50 ms; (b) phone `Type` -> PTY write measured end-to-end at p50 <= 150 ms / p95 <= 400 ms / p99 <= 800 ms over n >= 200; (c) cancellation, max pending waits, quota accounting, and reconnect behavior each tested; (d) the Phase A per-source connection cap and cumulative handshake deadline still hold, and the newest-wins takeover property is not weakened. |
| PB-NET-6 | Phase A's relay-adversary properties hold through the real client **across process restarts** — seq gating, replay/reorder/dup rejection, mailbox cap, hostile-pagination termination. (v1's single-process criterion was satisfiable while the property was false on a handset — opus M1.) | The Phase A adversarial suite runs against the real client, **plus** the PB-STATE-2 restart case. |
| PB-NET-7 | Hygiene: timeouts everywhere, cancellation honored, no goroutine leaks across connect/disconnect cycles. | `-race` + goroutine-leak assertion over repeated Start/Stop. |

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
| PB-SYNC-1 | **Staleness is tracked per SEQ BUCKET; repair is per CHANNEL.** v2 required "a gap in one stream marks only that stream stale", which round 2 proved **impossible**: journal and terminal frames share one `(sender, epoch)` sequence space, and `MailboxResult` carries only `{Plaintext, Gap bool}` (`internal/remote/crypto/envelope.go:195-200`) — a bare boolean with no frame kind — so a skipped seq cannot be attributed to journal-vs-terminal. There are **three buckets** (shared journal+terminal, command-reply via the deliberate `SenderKeyID=0` split, grant) and **four repair channels**. A gap in the shared bucket must conservatively stale **both** journal and terminal. | Test: a gap in the shared bucket marks journal AND terminal stale; a command-reply gap marks neither. Attributing a shared-bucket gap to one channel is a failing implementation. |
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
| PB-INPUT-3 | Lease TTL expiry mid-use has defined UX. **The 30-minute figure is not the operative one**: the lease is the *earliest* of `now+maxControlSessionTTL`, `now+TTLSeconds`, and the device-signed `ExpiresAt` (`internal/protocol/server.go:1500-1504`), so with PB-TIME-1's "phone signs `now + 1 minute`" the real lease is **60 s** — and PB-INPUT-5's ">= 60 s sustained typing" test sits exactly on it, as does §6.0's 60 s biometric freshness. Three independent 60-second walls collide. §6.0 now sets the signed `ExpiresAt` to 15 min for take_control so the lease is not the binding constraint, while command TTL stays 60 s. | Test asserts a typing session survives well past 60 s, and that expiry when it does arrive has defined UX rather than silent keystroke loss. |
| PB-INPUT-4 | Retry policy is keyed on stable server error codes, never blind resend. | Test maps each error class to its policy. |
| PB-INPUT-6 | **Coalescing must preserve ordering and flush at every boundary.** A sustained-rate test alone would pass while the last buffered keystrokes are lost whenever the user releases control. Required: byte-order preservation across frames; flush before resize; flush before release/take_control_end; defined handling of buffered input on background, auth expiry and disconnect (flushed or explicitly reported as "delivery unknown" per PB-INPUT-1, never silently dropped); a max coalesced payload (§6.0); and stated treatment of paste and IME composition, which are not keystroke streams. | A test per boundary asserts no reordering and no silent loss. |
| PB-INPUT-5 | **Input must be coalesced to stay under the relay's quotas.** `MailboxAppendPerMin: 600` (`internal/remote/relay/config.go:99`) allows 10 appends/s — `OpsPerMin` does **not** apply, since `handleMailboxAppend` never calls `meterOp`, so a ~30 Hz key-autorepeat or fast interactive typing trips `codeQuotaExceeded` mid-lease after roughly 20 s — while short-burst latency tests still pass. Coalescing bound per §6.0 (<= 8 frames/s sustained, one frame per 125 ms). | **A sustained-typing acceptance test** (not a burst): continuous input for >= 60 s at autorepeat rate stays within quota and loses no keystrokes. |

### 6.8 PB-TIME — clock skew (NEW; codex, opus Md1, fable F7)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TIME-1 | The phone signs `ExpiresAt = now + 1 minute` and the daemon rejects expired commands (`internal/skeleton/deviceauth.go:74-76`) and also `ExpiresAt > now + maxCommandValidity` (1 h; const at `internal/protocol/server.go:164`, enforced in `requireRemoteAuthz`). The usable window is therefore roughly "phone ≤1 min slow" — a handset two minutes behind fails **every** command with an opaque "not authorized". Skew must be detected, bounded (§6.0: ±30 s), and surfaced distinctly. | Test with a skewed phone clock asserts a distinct, user-legible error (not the generic authorization failure). |
| PB-TIME-3 | **A skew-detection protocol, since neither the relay nor an unauthenticated wall clock may be the authority.** A two-minute-slow phone is inferable from an expired command, but a 31-second skew is not measurable from `ExpiresAt` alone. Requires: an authenticated machine-time exchange, an RTT allowance, a stated monotonic-vs-wall-clock split, and defined offline behavior. | Boundary tests at exactly +/-29, 30 and 31 s; a test that the relay cannot influence the phone's notion of machine time. |
| PB-TIME-2 | **Every security-relevant timestamp** has a stated authoritative clock and skew behavior, not just command expiry: envelope `IssuedAt` (and the PB-GW-2 bounded-age check that consumes it), push expiry/replay, QR/rendezvous expiry display (`RendezvousTTL` 60 s), lease TTL display (`maxControlSessionTTL` 30 m), reconnect timers, and cached-state freshness. | Each timestamp's authority and tolerance recorded and tested. |

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

### 6.11 PB-PUSH — push, end to end (fable F2)

Verified: **no APNs implementation exists** (`apns.go`, 22 lines, interface-only), and
**nothing machine-side ever calls `PushTrigger` or `TokenRegister`** — the only push that fires
today is the presence-timeout sweep. FCM will be the first real backend *and* needs a producer.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-PUSH-0 | **A gateway-side trigger**: which journal transitions fire a push, with coalescing/debounce (ADR-007 D6's "push-wakes + coalesced snapshots"), sealed under the **wake key** (PB-KEY-2). **The wake key must first reach the gateway**: `gatewayParams` carries only `ContentKey`, and `WakeKey` appears nowhere in `internal/remotegw/`, `cmd/swarm-remote/`, or `internal/remote/relay/` outside tests — so this introduces a new key crossing into the sidecar that no requirement currently names, with its own custody and blast-radius consequences (the sidecar is the network-facing edge). | Tests for trigger selection, coalescing, that the content key is never used, and that the gateway holds the wake key only. |
| PB-PUSH-1 | Rename the seam transport-neutral (`PushSink`/`PushPayload`); it is already content-agnostic and keeping the APNs name for FCM is a documented landmine. | Rename lands with Phase A tests green. |
| PB-PUSH-2 | An FCM v1 sender implementing the seam. | Fake-endpoint tests: send, OAuth acquisition + refresh, 5xx retry, `UNREGISTERED` pruning. |
| PB-PUSH-3 | **A specified payload schema** — not merely "opaque fields": which key seals it, replay/expiry gating, and no session names, hostnames, agent names, or Group labels visible to the provider. | Schema pinned by test; ADR states exactly what the provider observes (token, timing, size). |
| PB-PUSH-4 | The app receives a push and renders a **content-free** notification unless the user has authenticated; it never decrypts session content with a locked device (PB-KEY-2). Lock-screen redaction and notification-channel privacy are set. | Robolectric test: locked -> generic alert only; authenticated -> content rendered. |
| PB-PUSH-5 | Missing/invalid credentials degrade gracefully and loudly; the system works without push. | Test: misconfigured sink -> no crash, explicit error, core paths unaffected. |
| PB-PUSH-6 | Push tokens survive a relay restart, or the loss is an accepted, recorded residual. Today `tokens` is an in-memory map (`server.go:173`, sole write `:830`) — a relay restart silently disables push exactly when it is needed. | Persisted and tested, or explicitly recorded with its user-visible consequence. |
| PB-PUSH-7 | The single-token-per-routing-id limitation is documented as acceptable for single-device v1 or fixed. | Decision + a test pinning the behavior. |
| PB-PUSH-8 | **The push toggles need a transport verb.** PB-APP-7 requires toggles that "demonstrably suppress delivery", but delivery is decided by PB-PUSH-0's *gateway-side* trigger, and no push-preference op exists in the signed action set (`internal/protocol/remote.go:74-79,88-89`). Local filtering is not sufficient: the push would still have been sent and the provider would still see token/timing/size, contradicting PB-PUSH-3. A device->machine preference verb is therefore required — which drags in PB-SYNC-5's problem (a new `Action*` constant, an `actionClass` mapping, and a capability-tier decision). | Verb implemented and gated; test asserts a disabled toggle means **no push is sent**, verified at the sender, not the receiver. |
| PB-PUSH-10 | **The push preference must be durable where delivery is decided.** PB-PUSH-8 puts suppression on the gateway side, but nothing says the machine-side preference survives a gateway restart, a daemon restart, an app reconnect, or a lost preference-command outcome — so a test can pass in one process while a restart silently re-enables pushes and leaks token/timing/size contrary to the setting the user is looking at. Requires a durable machine-authoritative record with acknowledged, versioned updates. | Restart test: a disabled preference still suppresses **at the sender** after gateway and daemon restarts. |
| PB-PUSH-9 | **Client-side FCM token lifecycle**: initial `getToken`, `onNewToken` rotation, re-registration on every authenticated reconnect (which also largely neutralizes PB-PUSH-6's relay-restart loss), deletion on revoke/disable, and correct behavior across process death and app upgrade. A façade method can exist while no Android code ever calls it. | End-to-end test with a fake FCM and a real relay: rotate the token and assert delivery still works; restart the relay and assert re-registration restores it. |

### 6.12 PB-SEC — handset security (expanded; §3)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SEC-1 | Key material at rest is sealed under an Android-Keystore-backed KEK per the PB-KEY-1 custody contract and the PB-KEY-2 tier split. | Persisted blob is not the raw key and does not decrypt without the keystore key. |
| PB-SEC-2 | The biometric gate is **cryptographically enforced, not cosmetic**: a stated freshness window per operation; invalidation on background, screen lock, process death, and biometric-enrollment change; defined cancel/failure/lockout and concurrent-prompt behavior; Keystore-enforced unwrap/sign authorization rather than a UI boolean; no reuse of one authentication for a different action unless explicitly allowed. | Tests per clause. A test must fail if the implementation is an in-memory `authenticated = true` flag. |
| PB-SEC-3 | No plaintext session content persisted unencrypted; no secrets or session content in logs. | Automated log scan + storage assertion (evidence artifact required, not "reviewed"). |
| PB-SEC-4 | `FLAG_SECURE` on pairing and terminal-peek screens; sensitive content excluded from recents. | Window-configuration assertion. |
| PB-SEC-5 | Cleartext traffic is disabled at the platform level **for the Java/WebView stack**. v1 wrongly claimed this backstops PB-NET-2: `networkSecurityConfig` does not govern Go's `crypto/tls` inside a native `.so` (opus H3), so PB-NET-2 is the sole control for the relay transport. | Manifest assertion, with the scope limitation stated so it is not mistaken for transport protection. |
| PB-SEC-6 | The app cannot bypass any server-side control: kill switch, lease, capability, expiry, seq gating stay authoritative server-side. | Adversarial test through the real transport: no typing without a lease or while the kill switch is engaged. |
| PB-SEC-7 | Device-loss response works end to end: revoke -> epoch rotation -> gateway severs and exits -> lost device dead. | Phase A revoke evidence re-asserted through the real transport; ADR documents threat + response. |
| PB-SEC-8 | No analytics/telemetry SDKs; dependencies minimal and justified. | Dependency inventory as an evidence artifact; assertion that no analytics dependency is present. |
| PB-SEC-10 | Excluded from Android backup and device-to-device restore (`allowBackup=false` / backup rules); state must not be extractable via ADB backup. | Manifest + rules asserted. |
| PB-SEC-11 | Exported-component hygiene: an explicit `android:exported` allowlist, validated intents/deep links, no component reachable by a third-party app that can act on the session. | Manifest assertion + intent-validation tests. |
| PB-SEC-12 | UI-redress and input-path defenses: overlay/tapjacking protection on gated actions (`filterTouchesWhenObscured` or equivalent), no sensitive clipboard use, and documented limits regarding third-party IMEs and accessibility services. | Tests where testable; documented where not. |
| PB-SEC-13 | Release builds are `debuggable=false`, non-profileable, with no debug backdoor; heap-dump/crash-report exposure considered. | Build-config assertion. |
| PB-SEC-14 | Build supply chain: dependency locking with checksum verification for Gradle/Maven and pinned gomobile/NDK. | Lockfile present and verified in the build gate. |

### 6.13 PB-TOK — design tokens (reduced per §5)

Verified: **31 distinct** `--p-*` tokens (v1 said 38 — corrected) across 4 directions in
`docs/research/remote-control-design-directions.html`; dark-only; no spacing scale.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TOK-1 | One machine-readable token source (JSON) is the single origin for the Android theme. | Theme generated from or asserted against the JSON. |
| PB-TOK-2 | Exactly one skin is chosen for v1 and recorded, and **the app is pinned to that fixed dark theme** — since §5 defers light mode, a system-light handset must not render the app unstyled or low-contrast (and PB-E2E-2's screenshots are the evidence artifact). | Decision in the ADR; a test asserts the app does not follow the system `uiMode`. |
| PB-TOK-3 | The terminal peek keeps the phosphor-green monospace treatment; purple stays retired. | Asserted against the token source + emulator evidence. |

*(v1's light-mode authoring and HTML<->JSON drift test are cut per §5; the four-direction HTML is
a prototype, and one skin has one consumer.)*

### 6.14 PB-SAS — SAS on the handset

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SAS-1 | The SAS is computed by the shared Go core (`sas.go:58`) and returned as a display string; the emoji table is never re-implemented in Kotlin — removing the cross-language failure mode rather than testing around it. | Test asserts no emoji table in Kotlin sources. |
| PB-SAS-2 | A KAT pins channel binding -> six emoji. | Go KAT; emulator evidence shows phone SAS == machine SAS. |
| PB-SAS-3 | The UI presents the SAS as a compare-both-screens confirmation, never typed. | UI test + evidence screenshot. |

### 6.15 PB-APP — the client (single-machine per §5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-APP-1 | **Pairing/onboarding** per PB-PAIR-*. | See PB-PAIR. |
| PB-APP-2 | **Triage inbox**: the four Groups as sections with one-line need summaries. *(Machine switcher cut — §5.)* | UI test covering all four Groups and the empty state. |
| PB-APP-3 | **Session detail**: journal events + snapshot cards, persistent Stop. | UI test; Stop maps to interrupt/kill with confirmation. |
| PB-APP-4 | **Terminal peek + take-control**: renders daemon-sanitized `SnapText` (no VT emulator on device — ADR-007 D2), live tail, lease acquire, on-screen keyboard, release. | UI + integration test; asserts only sanitized text is rendered. |
| PB-APP-5 | **Machine pane**: presence, paired device, revoke, kill switch, activity log. | UI test; revoke + kill switch gated per PB-SEC-2. |
| PB-APP-6 | **Launch**: submit a spec via the v1 builder/policy path; policy rejection surfaced. | UI + façade test. |
| PB-APP-7 | **Settings**: two coarse push toggles honored by PB-PUSH-0's trigger, plus the biometric gate toggle. | UI test; toggles persist and demonstrably suppress delivery. |
| PB-APP-8 | **Connection/stale UX**: offline, reconnecting, and **per-stream** stale/resyncing states visible; a stale view is never presented as live. | UI test driving each state, including PB-SYNC-1's per-stream staleness. |
| PB-APP-9 | Errors reach the user: an **exhaustive error-taxonomy mapping test** over the façade's pinned surface (v1's "test/lint; reviewed" was unenforceable). | Every façade error class maps to a rendered state; a new error class without a mapping fails the test. |
| PB-APP-10 | A revoked device shows an explicit re-pair prompt, not a failure loop; a grant-loss device shows PB-KEY-3's state. | Test per state. |

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
| PB-E2E-2 | On-emulator smoke: APK installs, pairs against a local relay + daemon, SAS matches, observes, takes control, types — **including one process death mid-session** (PB-STATE-2 on a real runtime). | Evidence (log + screenshots) + reproducible runbook. |
| PB-E2E-3 | Evidence files per repo convention with a RED-first run per slice, and each evidence file states **what it proves**, not merely that it exists. | GG-5 satisfied per slice. |
| PB-E2E-4 | No Phase A regression at any slice boundary. | Full suite + four gates green. |
| PB-E2E-5 | **A physical-handset gate.** The exit criterion says "your Android phone", and §10's honesty clause — while it does prevent a false "production-ready" label — otherwise defines that criterion away (codex#5). The gate covers: pair via the real camera, lock/unlock, process death, reboot, observe, launch, lease, type, Wi-Fi<->cellular handoff, and revoke, on hardware-backed Keystore with real biometrics — **plus the push path, which v3 omitted while §10 simultaneously listed real FCM and Doze as unverified**: real FCM registration and delivery, high-priority wake from Doze, notification behavior after reboot, token rotation, and locked-device push handling. It must also assert hardware backing through `KeyInfo`/attestation rather than asserting "hardware-backed" by assumption. | (This is why push cannot be declared done on emulator evidence alone; the alternative considered and rejected was cutting push from Phase B entirely, which would contradict roadmap B4.) | **Until this runs, Phase B is "provisionally implemented", not done** (§13). It cannot be executed on this machine (no handset), so it is an explicitly deferred gate with a named owner and a runbook — not a silently accepted limit. |

### 6.18 PB-OPS — operability

| ID | Requirement | Acceptance criteria |
|---|---|---|
**Scope correction (round 2, codex#14):** v2 silently pulled Phase C work into Phase B. The
roadmap puts relay ops — "Dockerfile / systemd unit, TLS termination runbook, VPS provisioning,
key-backup UX, onboarding docs" — in **C2** (`docs/research/remote-v1-roadmap.md:263`). Phase B
keeps only what the handset demonstration actually needs; the rest returns to Phase C.

| ID | Requirement | Acceptance criteria |
|---|---|---|
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
| Relay ops floor | codex#13 | PB-OPS-1..4 (v3's PB-OPS-5 was deleted by the §6.18 scope correction; this row pointed at a phantom id) |
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
| **No physical handset** | Hardware-backed Keystore, real biometrics, real cellular/Doze behavior unproven | Emulator exercises the code path on the shipping ABI; the hardware guarantee is **not** proven here. This gates the "production-ready" claim (§13). |
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
| S4 Gateway supervision (3 states) + release artifacts | PB-LIFE-*, PB-OPS-4 | opus | — |
| S5 Design tokens | PB-TOK-* | fable | — |
| **S6 Transport primitives**: request-id correlation, both-hop latency, TLS/resilience | PB-NET-2..7 | opus | S1 |
| **S7 Durable phone state** (Go-side; the Android *sealing* parts are **S15**) | PB-STATE-1..5, 7, 8; **PB-GW-6** (the phone `IssuedAt` seal change PB-GW-2 depends on) | opus | S0, S1, **S2, S2b** (PB-STATE-4's rollback authorities *are* PB-GW-1's inbound high-water and PB-GW-8's outbound ceilings, so neither can ship after it) |
| **S7b Gateway age check** (split out: it depends on the phone seal change) | PB-GW-2 | opus | S2, S7 |
| **S8 Façade + bind guard** | PB-BIND-1..7, PB-SAS-1, **PB-SAS-2** (sole owner; S19 contributes emulator evidence but does not own it) | opus | S6, S7 |
| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |
| S10 Per-bucket resync + grant recovery | PB-SYNC-1..6, **PB-SYNC-8**, PB-KEY-3/4 | opus | S8, S9 |
| S11 Input/lease semantics, coalescing, clock skew | PB-INPUT-*, PB-TIME-* | opus | S8 |
| S12 Push: trigger, seam rename, FCM sender, preference verb | PB-PUSH-0..3, 5..8, **PB-PUSH-10** (machine side) | opus | S0, S8 (PB-PUSH-8 needs a façade method + signed-action work) |

**Stage 2 — Android**

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| S13 Android skeleton + build + CI + runtime policy | PB-TOOL-*, PB-RUN-* | fable | S8 |
| **S14 Key custody on Android** (resolves PB-KEY-2's homelessness) | PB-KEY-1, 2, 5, 6, 7, **8**; PB-SEC-1, 2 | opus | S13, S0, S7 *(PB-KEY-6's failable-`KeyStore` **signature** lands in S7 so S6/S7/S8 build against it once; only the Android implementation is here)* |
| S15 State sealing + backup exclusion (breaks the v2 cycle) | PB-STATE-6/9, PB-SEC-10 | opus | S14, S7 |
| S16 Screens + phone-side pairing | PB-APP-*, PB-PAIR-2..6, PB-SAS-3 | fable, opus review | S13, S5, S3, S10, S11, S12 |
| S17 Push receiver | PB-PUSH-4, **PB-PUSH-9** (sole owner: token lifecycle is client-side by definition) | fable | S13, S12, S14 |
| S18 App security hardening | PB-SEC-3..8, 11..14 *(no PB-SEC-9 exists; PB-SEC-10 is owned by S15)* | opus | S16, S17 |
| **S18b Fail-closed recovery** (was homeless: needs both the fail-closed path and the unblock) | PB-STATE-10 | opus | S7, S10, S16 |

**Stage 3 — integration**

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| S19 E2E + emulator smoke | PB-E2E-1..4 | opus | S4, S7b, S9, S10, S11, S15, S16, S17, S18, S18b |
| S20 Docs / ADR / ops runbooks | PB-DOC-2, 3, 4, 7 (PB-DOC-6 withdrawn); PB-OPS-1..3 *(PB-DOC-1 is owned by S0 and PB-DOC-5 by S2 — not duplicated here)* | fable, opus review | S19 |
| S21 Physical-handset gate (deferred; no device here) | PB-E2E-5 | — | S19 |

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
  project, no deployed relay. Hardware-backed Keystore, real biometrics, Doze, cellular
  handoff, and real camera pairing remain unverified.
- **Done** — additionally PB-E2E-5 has been executed on real hardware.

Declaring "done" without PB-E2E-5 is not permitted, and neither is quietly reclassifying
PB-E2E-5 as a §10 limit: it is a deferred gate with a runbook, not an accepted gap.
