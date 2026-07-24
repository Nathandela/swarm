# Phase B requirements — Android handset (the v1 milestone)

**Status**: v2, revised after audit-committee round 1 (codex REVISE / opus REVISE / fable REVISE).
**Date**: 2026-07-24.
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
| Gradle | 9.6.1 system; wrapper pins 8.11.1 (generation verified) | checked-in wrapper |
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

Only the third and fourth rows are genuinely new. They drive PB-SEC-10..14.

---

## 4. Five blockers found by reading the tree

These reorder the phase. None was in the roadmap's Phase B plan.

### 4.1 The phone core is not bindable

`internal/phonecore/journal.go:1` documents the package as "gomobile-ready". **The claim is
unenforced — no test guards it**, and it is false. Verified failures include
`crypto.ContentKey [32]byte` (`internal/remote/crypto/epoch.go:64`, an array, in 8 exported
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
`ultraviolet`, `xo/terminfo`, `muesli/cancelreader` — **53 non-stdlib packages**. Also,
`gobind`'s generated wrapper lives outside the module's `internal/` boundary, so an
`internal/...` package cannot be bound at all.

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

### 6.1 PB-STATE — durable on-device state (NEW; the most severe gap, §4.3)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-STATE-1 | The core persists and restores everything resume-critical: device keys, pinned machine static + sign pub + routing id, epoch id + keys, **outbound send-seq**, **per-(sender,epoch) receive high-water**, relay mailbox cursor, session/snapshot caches, pending idempotent ops and their outcomes, and per-stream stale flags. | Enumerated in one persisted schema; a test asserts each field survives a restart. |
| PB-STATE-2 | **Process-death acceptance test**: kill the core process mid-session and restart. Typing, launch and kill must still succeed, and a frame captured before the kill must still be rejected as a replay. | The RED form of this test must first demonstrate today's stale-drop brick. This single test is the guard for §4.3 in both directions (liveness and replay). |
| PB-STATE-3 | Send-seq durability must not cost an fsync per keystroke. The strategy is a stated decision (reserve-a-ceiling-and-burn-the-gap, mirroring `internal/remotegw/seqstore.go`), not an accident. | Decision recorded; a test asserts no seq is ever reused across a crash at any point in the reservation window, including a crash between reservation and use. |
| PB-STATE-4 | Writes are crash-atomic; corruption or detected rollback fails **closed** (refuse to operate, prompt re-pair) rather than resetting counters. | Tests inject truncation, corruption, and a rolled-back state file; none results in seq reuse or a reset high-water. |
| PB-STATE-5 | A schema version with a forward-migration path; an app upgrade must not lose state or reset counters. | Migration test from vN to vN+1; an unknown future version fails closed. |
| PB-STATE-6 | State at rest is sealed per PB-KEY-2 and excluded from Android backup (PB-SEC-10). | Asserted jointly with those requirements. |

### 6.2 PB-BIND — bindability (§4.1, §4.2)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-BIND-0 | The bound package's dependency closure is constrained by an **allowlist**, not a denylist: closure ⊆ {crypto, wire/protocol leaf types, relay client, stdlib, x/mobile}. (v1 used a denylist that already omitted `internal/shimwire`.) | A test computes `go list -deps` and fails on any package outside the allowlist. Phase A suite green through the extraction. |
| PB-BIND-1 | A single façade package at a **non-internal** path is the only bound surface. | `gomobile bind` succeeds on the façade; nothing else is bound. |
| PB-BIND-2 | Only gomobile-legal types (no arrays, unsigned ints, maps, non-`[]byte` slices, generics, variadics, channels, or `(T, bool)`; multi-return only `(T, error)`). | **A Go test runs `gobind` over the façade and fails on any bind-illegal export** — the standing guard §4.1 showed was missing. It also emits the true legal/illegal counts. |
| PB-BIND-3 | The façade covers every capability the v1 screens need: pairing (QR decode, SAS, confirm, cancel), roster + presence, sessions with Group, journal read/subscribe, snapshot peek, take_control acquire/release, input + resize, launch, interrupt/kill, revoke, kill switch, push-token registration, connection/stale state, resync, and state lifecycle (Start/Stop/restore). | A traceability table maps every screen element to a method; a Go test exercises every method against a real in-process backend. Any screen element with no method is a coverage failure. |
| PB-BIND-4 | The JNI boundary carries no *unnecessary* secret. The one deliberate exception is the key-custody artifact defined by PB-KEY-1, which must be named, directional, and justified. (v1's absolute phrasing contradicted PB-SEC-1 — opus H2, fable F5.) | Test asserts no exported method returns raw long-term private keys; PB-KEY-1's artifact is the sole documented crossing. |
| PB-BIND-5 | No Go panic crosses the boundary (a panic through JNI kills the app). | Every entry point recovers into an `error`; a test injects a panic per entry point. |
| PB-BIND-6 | Documented threading/lifecycle contract: any-thread safe; `Start`/`Stop` idempotent; callbacks arrive on a Go goroutine (UI must marshal); a slow callback must not stall the core, with a **stated** queue bound and overflow behavior. | `-race` test hammering concurrent calls and repeated Start/Stop; a deliberately slow callback does not wedge the core and its overflow is observable. |
| PB-BIND-7 | The exported surface is pinned so a breaking change cannot land silently. | Golden-file test of the exported surface. |

### 6.3 PB-PAIR — pairing end to end (NEW; §4.4)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-PAIR-1 | **Machine-side QR rendering**: `swarm remote pair` renders a scannable QR (terminal block glyphs), not a raw string. | A test decodes the rendered output back to the exact payload (round-trip against `DecodeQR`). |
| PB-PAIR-2 | **Phone-side camera capture + decode**, with the `CAMERA` runtime permission requested, and a manual-entry fallback when it is denied or permanently denied. | Tests for granted, denied, and permanently-denied paths; manual-entry encoding is specified, not improvised. |
| PB-PAIR-3 | The scanner dependency is justified under PB-SEC-14 (ML Kit pulls Google Play Services, in tension with a minimal dependency set) — the choice is explicit. | Decision recorded in the ADR with the tradeoff stated. |
| PB-PAIR-4 | A **persisted** pairing state machine: process death at any transition (Noise msg1/2/3, SAS display, machine decision wait, local pin commit, grant bootstrap) resumes or fails closed — never a half-paired device. | Kill/restart test at each transition; a machine that committed while the app died before persisting pins is detected and resolved. |
| PB-PAIR-5 | Explicit terminal states for: declined (`pairing.go:71 ErrPairingDeclined`), SAS mismatch, rendezvous timeout, expired/consumed QR, and already-paired. Abandoned device keys and partial local records are cleaned up. | Test per state; each is user-legible, not an opaque error. |
| PB-PAIR-6 | A malicious QR cannot point the phone at an attacker-chosen relay or a local-network address without the user seeing what they are joining. | Test asserts relay URL validation//display; SSRF-style targets rejected. |

### 6.4 PB-KEY — key tiers and grant recovery (NEW; §3, opus M3/H2, fable F5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-KEY-1 | The **JNI key-custody contract**: exactly which artifact crosses the boundary, in which direction, and why that is acceptable — reconciling the Go core's native-heap keys with the Java-only Android Keystore API. | Contract documented in the ADR and in the façade package doc; a test pins the crossing to that one artifact. |
| PB-KEY-2 | ADR-007 A15's **two-tier split is honored on Android**: the wake key is after-first-unlock and readable by the push path; the content key is user-authentication-gated and **not** readable by the push path or derivable from the wake key. | Tests assert the push path cannot obtain the content key, and that a locked device cannot decrypt session content. |
| PB-KEY-3 | **Epoch-grant recovery.** Today a grant can be lost with no recovery: the relay refuses appends past the mailbox depth cap (`server.go:743-747`) and `SweepRetention` purges items older than `RetentionCap` (**default 7 days**, `config.go:90`) **even if never acked** (`server.go:1136-1139`); no re-grant verb exists anywhere. A phone offline across a rotation is then permanently unable to decrypt, and re-pairing is refused because `BeginPairing` fail-fasts while a device is registered. | Either a re-grant request path, or a defined user-legible terminal state plus a documented machine-side unblock. A test drives the offline-across-rotation scenario to a defined, recoverable end — not an indefinite decrypt-failure loop. |
| PB-KEY-4 | Key rotation while the app is backgrounded/offline is handled without data loss or silent breakage. | Test: rotate while offline, reconnect, converge. |

### 6.5 PB-NET — transport (§4.5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-NET-1 | The real `relay.Client` drives the core through the façade; the `phonesim` mailbox seam (`phonesim.go:42`) stays for testability. | Integration test: façade + real client + real in-process relay: pair -> read -> ack -> append. |
| PB-NET-2 | TLS verified by default; a pinned self-signed cert is an explicit opt-in for self-hosted relays; cleartext refused **except** an explicit, narrowly-scoped loopback carve-out for the in-process test relay — which is `ws://`-only today (`server.go:228`), making v1's unconditional ban self-contradictory (fable F6). The Go client's **trust-root source on Android must be stated** (embedded bundle vs pinning-only); `x509.SystemCertPool` is not usable as on desktop (opus H3). | Tests: bad cert fails closed; non-loopback cleartext rejected; the carve-out cannot be enabled in a release build; pinning accepts only the pinned cert. |
| PB-NET-3 | The transport handles only opaque sealed frames and never holds content keys. | Test asserts a known plaintext marker never appears on the wire. |
| PB-NET-4 | Resilience: automatic reconnect, bounded exponential backoff with **stated numeric** ceiling and jitter, re-auth after reconnect, connection state surfaced. **Input and resize are never queued or replayed** (ADR-007 D7 `:60-62`: live-only, "delivery unknown / not sent"); only high-level idempotent ops may queue, with a stated bound. | Tests against a flapping relay assert the retry ceiling, state transitions, re-auth, that no keystroke is ever replayed, and the idempotent-op queue's bound and drop signal. |
| PB-NET-5 | **Low-latency input across BOTH hops.** The mechanism is a stated protocol change — request-id correlation with concurrent dispatch, or an explicit server-push frame — because §4.5 proves a naive long-poll head-of-line-blocks the keystroke path and a second connection is not available. It must also drop the gateway's 500 ms command-IN poll (`service.go:27`), which ADR-007:461 calls "unusable for live typing"; a phone-side-only fix passes v1's criterion while typing stays 500 ms-gated (fable F4). Interaction with presence must be stated: a phone parked in a wait keeps `presence.connected = true`, suppressing the presence-timeout wake push (`server.go:1112-1132`). | **Acceptance is end-to-end and bidirectional**: (a) with a wait outstanding, a keystroke append from the same client still completes within a stated bound; (b) phone `Type` -> PTY write measured end-to-end with stated p50/p95/p99, sample count, and timeout; (c) cancellation, max pending waits, quota accounting, and reconnect behavior each tested; (d) the Phase A per-source connection cap and cumulative handshake deadline still hold. |
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
| PB-SYNC-1 | Stale state is tracked and repaired **per stream** (journal, terminal, command-reply, grant), not globally. | Test: a gap in one stream marks only that stream stale. |
| PB-SYNC-2 | Repair per stream: journal via an atomic roster+events snapshot; terminal via a fresh full snapshot (a journal reseed cannot repair a missed grid); command replies via the durable operation outcome, or the stream stays unresolved; grant via PB-KEY-3. | Test per stream, including that a journal reseed alone does **not** clear terminal staleness. |
| PB-SYNC-3 | `Stale()` clears only after a successful reseed **of that stream**, committed atomically with the matching transport watermark. Failed resync stays stale (fail-closed). | Test asserts no optimistic clearing and no watermark/coordinate confusion. |
| PB-SYNC-4 | **Authorization is specified correctly.** v1 claimed the resync rides `requireRemoteAuthz`; it does not — `handleJournalRead` gates on the negotiated `journal` capability and the kill switch only (`internal/protocol/server.go:1657-1683`), while `requireRemoteAuthz` guards the mutating ops. The requirement must state which gate applies. | The chosen gate is implemented and tested; an unauthorized resync is refused. |
| PB-SYNC-5 | If the resync is device-signed, a new `Action*` constant must be added **and mapped** in `actionClass` (`internal/skeleton/deviceauth.go:17-26`), a closed switch that fails closed on unknown actions. The capability-tier consequence must be decided: the only fitting existing class is `ActionControl`, which would make a read-repair require the control tier, and `rec.Capability` is pinned at enrollment (`pairing.go:205`) and never read from the wire — so an observe-tier device could never resync. | Decision recorded; test asserts the intended tier can resync and the unintended one cannot. |
| PB-SYNC-6 | Resync is bounded and non-amplifying; a hostile relay cannot drive unbounded work. | Non-advancing pages terminate (`errStuckPage` discipline); a **stated** rate bound on resync attempts. |

### 6.7 PB-INPUT — live-input and lease semantics (NEW; codex#10, opus Md5)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-INPUT-1 | Input/resize are live-only per ADR-007 D7: never queued, never replayed; a disconnect resolves as an explicit **"delivery unknown / not sent"** surfaced to the user. | Test asserts no replay after reconnect and that the UX state appears. |
| PB-INPUT-2 | Lease lifecycle is defined across gateway restart, daemon restart, session exit under the user, app backgrounding, and process death; input is suppressed until a new lease is visibly confirmed. | Test per event; no keystroke is ever sent without a confirmed current lease generation. |
| PB-INPUT-3 | Lease TTL expiry mid-use (`maxControlSessionTTL = 30m`, `internal/protocol/server.go:156`) has defined UX. | Test drives expiry and asserts the state. |
| PB-INPUT-4 | Retry policy is keyed on stable server error codes, never blind resend. | Test maps each error class to its policy. |

### 6.8 PB-TIME — clock skew (NEW; codex, opus Md1, fable F7)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TIME-1 | The phone signs `ExpiresAt = now + 1 minute` and the daemon rejects expired commands (`internal/skeleton/deviceauth.go:73-75`) and also `ExpiresAt > now + 1h` (`server.go:164`). The usable window is therefore roughly "phone ≤1 min slow" — a handset two minutes behind fails **every** command with an opaque "not authorized". Skew must be detected, bounded, and surfaced distinctly. | Test with a skewed phone clock asserts a distinct, user-legible error (not the generic authorization failure) and a defined tolerance or negotiated TTL. |

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
| PB-PUSH-0 | **A gateway-side trigger**: which journal transitions fire a push, with coalescing/debounce (ADR-007 D6's "push-wakes + coalesced snapshots"), sealed under the **wake key** (PB-KEY-2). | Tests for trigger selection, coalescing, and that the content key is not used. |
| PB-PUSH-1 | Rename the seam transport-neutral (`PushSink`/`PushPayload`); it is already content-agnostic and keeping the APNs name for FCM is a documented landmine. | Rename lands with Phase A tests green. |
| PB-PUSH-2 | An FCM v1 sender implementing the seam. | Fake-endpoint tests: send, OAuth acquisition + refresh, 5xx retry, `UNREGISTERED` pruning. |
| PB-PUSH-3 | **A specified payload schema** — not merely "opaque fields": which key seals it, replay/expiry gating, and no session names, hostnames, agent names, or Group labels visible to the provider. | Schema pinned by test; ADR states exactly what the provider observes (token, timing, size). |
| PB-PUSH-4 | The app receives a push and renders a **content-free** notification unless the user has authenticated; it never decrypts session content with a locked device (PB-KEY-2). Lock-screen redaction and notification-channel privacy are set. | Robolectric test: locked -> generic alert only; authenticated -> content rendered. |
| PB-PUSH-5 | Missing/invalid credentials degrade gracefully and loudly; the system works without push. | Test: misconfigured sink -> no crash, explicit error, core paths unaffected. |
| PB-PUSH-6 | Push tokens survive a relay restart, or the loss is an accepted, recorded residual. Today `tokens` is an in-memory map (`server.go:173`, sole write `:830`) — a relay restart silently disables push exactly when it is needed. | Persisted and tested, or explicitly recorded with its user-visible consequence. |
| PB-PUSH-7 | The single-token-per-routing-id limitation is documented as acceptable for single-device v1 or fixed. | Decision + a test pinning the behavior. |

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
| PB-TOK-2 | Exactly one skin is chosen for v1 and recorded. | Decision in the ADR; app implements one. |
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

### 6.18 PB-OPS — operability

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-OPS-1 | Reproducible relay deployment with TLS termination guidance; `swarm-relay` released or a pinned deploy image. | Image/unit builds; runbook executed with an artifact as evidence. |
| PB-OPS-2 | Key/identity backup and recovery documented **and exercised** (restore actually performed once). | Restore evidence artifact, not a document review. |
| PB-OPS-3 | Operator runbook: install, pair, revoke, kill switch, device loss, push configuration. | Each step executed once during verification. |
| PB-OPS-4 | Honest metadata disclosure covering relay operator and push provider. | ADR section consistent with PB-PUSH-3 and ADR-007 D11. |
| PB-OPS-5 | Relay operational floor: TLS renewal/expiry, backup/restore of relay state, resource limits, disk-full behavior, log rotation, health check, and phone/gateway/relay version compatibility. | Each addressed or explicitly deferred with its risk stated (codex#13). |

### 6.19 PB-DOC

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-DOC-1 | An ADR-007 amendment records every Phase B decision: façade + dependency surgery, durable phone state + seq strategy, the transport protocol change, per-stream resync + its authz/capability decision, JNI key custody, wake/content tiers on Android, push trigger + payload schema, supervision's three states, chosen skin, **single-machine v1**, and **light-mode deferral**. | Merged before the final audit. |
| PB-DOC-2 | Phase B exit criteria in `implementation-goals.md`; a verification file maps every PB-* ID to evidence. | Full ID coverage. |
| PB-DOC-3 | Residuals recorded in the Phase A closure style: what, why, adversary-reachable or not. | Reviewed in the final round. |
| PB-DOC-4 | `docs/research/remote-v1-roadmap.md:286` ("Implementers are sonnet/opus subagents — never fable/haiku") is amended rather than silently contradicted by §11's model assignment. | Roadmap updated. |

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
| Relay ops floor | codex#13 | PB-OPS-5 |
| Sequencing | all three | §11 |

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

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| **S0 ADR: custody, tiers, state model, transport change** | PB-KEY-1/2, PB-DOC-1 (decisions only) | opus | — |
| **S1 Dependency-edge surgery (allowlist)** | PB-BIND-0 | opus | — |
| **S2 Durable state layer** | PB-STATE-* | opus | S0, S1 |
| **S3 Transport: correlation + both-hop latency + TLS/resilience** | PB-NET-* | opus | S1 |
| **S4 Façade + bind guard** | PB-BIND-1..7, PB-SAS-1 | opus | S2, S3, S0 |
| **S5 Per-stream resync** | PB-SYNC-*, PB-KEY-3/4 | opus | S3, S4 |
| S6 Input/lease semantics + clock skew | PB-INPUT-*, PB-TIME-1 | opus | S4 |
| S7 QR: machine renderer + phone decode | PB-PAIR-1..6 | opus | S4 |
| S8 Gateway supervision (3 states) + release | PB-LIFE-* | opus | — |
| S9 Push: trigger + seam + FCM sender | PB-PUSH-0..3, 5..7 | opus | S0 |
| S10 Tokens | PB-TOK-* | fable | — |
| S11 Android skeleton + build + CI + runtime policy | PB-TOOL-*, PB-RUN-* | fable | S4 |
| S12 Handset security | PB-SEC-* | opus | S11, S0 |
| S13 Screens | PB-APP-*, PB-SAS-3 | fable, opus review | S11, S10, S5, S6 |
| S14 Push receiver | PB-PUSH-4 | fable | S11, S9, S12 |
| S15 E2E + emulator smoke | PB-E2E-* | opus | all |
| S16 Docs / ops runbooks | PB-DOC-*, PB-OPS-* | fable, opus review | all |

S0/S1/S8/S9/S10 start in parallel. Each slice gate: evidenced RED -> implementation (independent
agent) -> independent review -> `go build/vet/test -race ./...` (plus the Gradle gate once S11
lands) green before any dependent slice starts.

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

**Honesty clause.** Without a physical handset, real FCM credentials, and a deployed relay, the
strongest defensible claim at the end of Phase B is **"implementation complete and
emulator-verified; production validation pending on-device gate"** — not "production-ready".
The final audit must state which of the two was actually achieved rather than allowing the
stronger phrase by default.
