# Phase B requirements — Android handset (the v1 milestone)

**Status**: DRAFT, pending audit-committee validation of definition quality + coverage.
**Date**: 2026-07-24.
**Binds**: the Phase B implementation. Refines `docs/research/remote-v1-roadmap.md` §"Phase B"
into testable requirements.
**Predecessor**: Phase A is closed (`docs/verification/remote-phaseA-committee-closure.md`,
committee-validated single-device v1 after seven rounds).

Every claim about existing code in this document was verified against the tree at `a2b6397`
and carries a `file:line`. Requirements are written so that a reviewer can disagree with a
specific, checkable statement rather than with a vibe.

---

## 1. The binding exit criterion

From the roadmap, verbatim:

> **Phase B exit:** your Android phone pairs, observes, launches, and types into a real
> session over the untrusted relay.

Every requirement below exists to make that sentence demonstrably true, safely. A requirement
that does not serve it is scope creep and belongs in §7 (non-goals).

| Verb | Means | Phase A status (verified) |
|---|---|---|
| **pairs** | QR -> Noise XXpsk0 -> SAS six-emoji compare on both screens -> enrolled, grant delivered | phone-side driver EXISTS: `pairing.RunDevice` (`internal/remote/pairing/pairing.go:362`), SAS at `internal/remote/crypto/sas.go:58`, QR decode at `qr.go:86`. No UI, no bound surface. |
| **observes** | machine roster, sessions in the four Groups, journal events, terminal peek snapshots | core EXISTS (`internal/phonecore`), driven only by `phonesim` from Go tests. |
| **launches** | submit a launch spec (builder + policy path; live execution is Phase 2 per ADR-007) | `phonesim.DriveLaunch` (`internal/phonesim/phonesim.go:359`). |
| **types** | `take_control` lease -> sealed, seq-gated keystrokes | `phonesim.TakeControl` (:404), `Type` (:436). |
| **over the untrusted relay** | a real relay client on the phone | WebSocket client EXISTS (`internal/remote/relay/client.go`) and is what `phonesim` uses in production; it is **not reachable from Android** and **not resilient** (see PB-NET). |

---

## 2. Environment ground truth (verified 2026-07-24 by building, not assumed)

Phase A deferred the mobile app partly because no mobile toolchain existed here. That is no
longer true. Installed and **proven by producing a real AAR containing `jni/arm64-v8a/libgojni.so`**:

| Component | Version | Location |
|---|---|---|
| JDK | OpenJDK 17.0.20 (Homebrew `openjdk@17`) | `$(brew --prefix openjdk@17)/libexec/openjdk.jdk/Contents/Home` |
| Android cmdline-tools | 14742923 | `/usr/local/share/android-commandlinetools` |
| Android platform | android-35 | `$ANDROID_HOME/platforms` |
| Build tools | 35.0.0 | `$ANDROID_HOME/build-tools` |
| NDK | 27.2.12479018 | `$ANDROID_HOME/ndk` |
| gomobile / gobind | installed 2026-07-24 | `~/go/bin` |
| Gradle | system (wrapper to be checked in, PB-TOOL-4) | Homebrew |
| Go | 1.26.1 darwin/amd64 (module declares `go 1.24.2`, `go.mod:3`) | system |

Two toolchain facts learned by doing, which the build scripts must encode:

- `gomobile bind` requires `golang.org/x/mobile` **in the module dependency graph**
  (`go get -tool golang.org/x/mobile/cmd/gobind`). It is a Go 1.24+ tool directive and does
  not link into the daemon binaries.
- NDK 27 supports API 21..35 but gomobile defaults to API 16 and **fails**; every build must
  pass `-androidapi >= 21`.

**Not available, bounding what "verified" can mean (§8):** no Xcode/Apple account (iOS is
Phase C by design), no physical Android handset, no Firebase project, no provisioned VPS relay.

---

## 3. Threat-model delta: Phase B adds a second adversary

Phase A's model was **the relay is the adversary**; the phone was trusted because it was a Go
test process. A real handset introduces an adversary Phase A never had to answer. This is the
main reason Phase B is not "just write a UI".

| Actor | Phase A | Phase B |
|---|---|---|
| Relay | adversary (E2E sealing + per-(sender,epoch) seq gating) | unchanged |
| Gateway | owner-uid, trusted (documented residual) | unchanged |
| Daemon | trusted | unchanged |
| **Phone at rest / lost / stolen** | not modeled | **new adversary**: an attacker holding the device |
| **Other apps on the handset** | not modeled | **new adversary**: another app reading our storage, logs, or screen |

This drives PB-SEC-*. It also has a **hard architectural consequence** (PB-BIND-0): today the
phone core's dependency closure drags in the entire daemon, shim, PTY and VT emulator. Shipping
that to a device the adversary may physically hold is both attack surface and a Phase A
invariant violation in spirit (ADR-007 Decision 2 deliberately keeps the VT emulator and raw
PTY bytes off the network-facing edge).

---

## 4. Two blockers that must be solved before any UI work

These were found by reading the tree, not by planning, and they reorder the whole phase.

### 4.1 The current phone core is not bindable (34 of 48 exported symbols fail)

`internal/phonecore/journal.go:1` documents the package as "gomobile-ready phone-side client
logic". **That claim is aspirational and unenforced** — no test guards it. Verified failures
include:

- `crypto.ContentKey` is `[32]byte` (`internal/remote/crypto/epoch.go:64`) — arrays are not
  bindable; it appears in 8 exported signatures.
- Unsigned integers (`uint32` epoch, `uint64` seq) are unsupported; they appear throughout.
- `AcceptGrant` (`accept.go:21`) returns **four** values; gomobile allows `(T, error)`.
- `SessionCache.List` returns `[]CachedSession`, `Snapshot.Lines` is `[]string` — slices of
  non-`byte` are unsupported.
- `(T, bool)` returns (`SessionCache.Get`, `ReplyCache.Take`, `SnapshotCache.Get`,
  `MailboxRouter.TakeGrant`) are unsupported; only `(T, error)` is.
- Cross-package types from unbound packages: `crypto.KeyStore`, `protocol.DeviceCommandAuth`,
  `protocol.Control`, `protocol.JournalRecord`, `status.Group`, `time.Time`.
- `crypto.SAS` returns `[6]string` (`sas.go:58`) — an array, unbindable.

Only 14 symbols bind as-is. **Conclusion: a façade is mandatory** — this is not a retrofit of
`phonecore`, it is a new layer. Consequence for the ADR's "designed bind-safe from the first
line" intent: that intent was not achieved, and PB-BIND-2 exists to make it enforced from now on.

### 4.2 The bound package would ship the entire daemon into the app

Verified: `internal/phonecore` -> `internal/protocol` -> `internal/daemon`, dragging in
`internal/shim`, `internal/engine`, `internal/vt`, `internal/transcript`, `internal/persist`,
plus `github.com/creack/pty`, `charmbracelet/x/vt`, `ultraviolet`, `xo/terminfo`,
`muesli/cancelreader`, `x/term`, `x/termios`.

Additionally, `gobind`'s generated wrapper package lives outside the module's `internal/`
boundary, so an `internal/...` package cannot be bound directly.

**Both must be fixed before an AAR is meaningful**, and the fix (a wire-types-only package
that breaks the `phonecore -> protocol -> daemon` edge) touches Phase A code, so it must be
done with Phase A's regression suite as the guard.

---

## 5. Architecture delta over Phase A

```
  [ NEW: Android app (Kotlin/Compose) ]              <- PB-APP-*, PB-SEC-*
                |  JNI (gomobile)
  [ NEW: bindable facade, non-internal path ]        <- PB-BIND-*
                |
  [ EXISTING: internal/phonecore ]  + dependency-edge surgery (PB-BIND-0)
                |
  [ EXISTING: relay.Client (WebSocket) ] + resilience + low-latency read  <- PB-NET-*
                |  WSS
  [ EXISTING: relay ] + long-poll read (PB-NET-5) + FCM sender (PB-PUSH-*)
                |
  [ EXISTING: gateway ] + journal_resync handler (PB-SYNC) + supervision (PB-LIFE)
                |
  [ EXISTING: daemon ]  (journal_read already exists: internal/protocol/types.go:44)
```

---

## 6. Requirements

Each has an ID, a statement, and **testable** acceptance criteria. "Test" means automated
unless stated. TDD is mandatory with an evidenced RED run (implementation-goals GG-5).

### 6.1 PB-BIND — bindability (the blocking slice)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| **PB-BIND-0** | The bound package's dependency closure contains **no** daemon, shim, engine, VT-emulator, PTY, or persistence code. The `phonecore -> protocol -> daemon` edge is severed (wire types extracted to a leaf package). | A test asserts `go list -deps` of the façade contains none of: `internal/daemon`, `internal/shim`, `internal/engine`, `internal/vt`, `internal/transcript`, `internal/persist`, `github.com/creack/pty`, `charmbracelet/x/vt`. Phase A suite stays green through the extraction. |
| PB-BIND-1 | A single façade package at a **non-internal** import path is the ONLY bound surface; `internal/phonecore` is never bound directly. | `gomobile bind` succeeds on the façade; no other package is bound. |
| PB-BIND-2 | The façade uses only gomobile-legal types (no arrays, no unsigned ints, no maps, no non-`[]byte` slices, no generics, no variadics, no channels, no `(T, bool)` returns; multi-return only as `(T, error)`). | **A Go test runs `gobind` over the façade and fails if any exported symbol is bind-illegal.** This is the standing regression guard §4.1 showed was missing — it must exist, not be a one-time manual check. |
| PB-BIND-3 | The façade covers every capability the §8 v1 screens need: pairing (start/QR decode/SAS/confirm), machine roster + presence, session list with Group, journal read/subscribe, snapshot peek, take_control acquire/release, keystroke input + resize, launch submit, interrupt/kill, device revoke, kill switch, push-token registration, connection + stale state, resync. | A traceability table maps every v1 screen element to a façade method; a Go test exercises every façade method against a real in-process backend. A screen element with no method is a coverage failure. |
| PB-BIND-4 | No private key material or unsealed secret crosses the JNI boundary (JNI-visible bytes land in the Java heap and in heap dumps). | Test asserts no exported method returns or accepts raw secret key bytes; explicitly reviewed. |
| PB-BIND-5 | No Go panic may cross the boundary (a panic through JNI kills the app process). | Every façade entry point recovers and converts to `error`; a test injects a panic per entry point and asserts an error rather than a crash. |
| PB-BIND-6 | A documented threading/lifecycle contract: methods safe from any thread; `Start`/`Stop` idempotent; callbacks documented as arriving on a Go goroutine (UI must marshal); a slow callback must not stall the core. | Package doc states it; a `-race` test hammers concurrent calls and repeated Start/Stop; a deliberately slow callback does not wedge the core (bounded, with a surfaced signal). |
| PB-BIND-7 | The exported surface is pinned so a breaking change to the UI contract cannot land silently. | Golden-file test of the exported surface; changing it requires updating the golden. |

### 6.2 PB-NET — transport: reachability, resilience, latency

The WebSocket client exists (`internal/remote/relay/client.go`, ops table verified: `hello`,
`auth_init`/`auth_resp`, `mailbox_read`, `mailbox_ack`, `MsgMailboxAppend`, `token_register`,
`presence`, `push_trigger`, `device_revoke`, `rendezvous_*`). What is missing is everything a
phone on a mobile network needs.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-NET-1 | The real `relay.Client` (not an injected fake) drives the phone core through the façade. The `phonesim` `mailbox` seam (`internal/phonesim/phonesim.go:42`) is honored so both paths stay testable. | Integration test: façade + real client + real in-process relay performs pair -> read -> ack -> append. |
| PB-NET-2 | TLS verified by default; a pinned self-signed certificate is an explicit opt-in (self-hosted relay reality); cleartext `ws://`/`http://` refused unconditionally. | Tests: bad certificate fails closed; cleartext URL rejected; pinning accepts only the pinned cert. |
| PB-NET-3 | The transport handles only opaque sealed frames; it never has access to content keys. | Test asserts a known plaintext marker never appears in bytes written to the wire. |
| PB-NET-4 | Resilience for a real network: automatic reconnect, bounded exponential backoff with jitter, connection state surfaced to the UI, no unbounded growth while offline, and re-auth after reconnect. | Tests against a flapping/erroring relay assert a retry ceiling, correct state transitions, a bounded queue (drop-with-signal, never OOM), and that the resumed connection re-authenticates. |
| **PB-NET-5** | **The keystroke path must not be gated by a polling interval.** Verified today: the relay is strictly request/response — it never sends an unsolicited frame to a connected client (only `writeFrame` in reply, `server.go:458`), so the phone must poll `mailbox_read`, making round-trip latency equal to the poll period. ADR-007 explicitly deferred this decision to Phase B. Resolve it with a **long-poll `mailbox_read` (a bounded server-side wait for the next item)** — chosen over server-initiated push because it needs no client demux change and no new server push machinery. | Test: with take_control held, a frame appended for the phone is observed within a low bound (target p50 < 200 ms in-process). The long wait is bounded, cancellable, quota-respecting, and cannot pin an unbounded number of server goroutines (interaction with the Phase A per-source connection cap explicitly tested). A fixed poll interval > 1 s on the input path fails this requirement. |
| PB-NET-6 | Phase A's relay-adversary properties still hold through the real client: per-(sender,epoch) seq gating, replay/reorder/dup rejection, mailbox cap, hostile-pagination termination. | The Phase A adversarial suite runs against the REAL client, not only the fake. |
| PB-NET-7 | Hygiene: timeouts on every call, context cancellation honored, no goroutine leaks across connect/disconnect cycles. | `-race` plus a goroutine-leak assertion around repeated Start/Stop. |

### 6.3 PB-SYNC — gap-resync (closes a Phase A deferral; machine-side, not relay-side)

Verified: the relay **cannot** serve a resync. Its cursor is a storage cursor explicitly
distinct from the authenticated seq (`client.go:15-21`), and `ackItems` **deletes** acked
items (`store.go:131,143`). The repair channel is the daemon's already-existing
`journal_read(from_cursor)` (`internal/protocol/types.go:44`, handler `server.go:1654`,
returning `protocol.JournalResume{Cursor, Roster, Events, FullResync}` at `remote.go:52`).
The phone-side seed hooks already exist (`snapshot.go:183`, `journal.go:78`); the missing
piece is a phone->gateway request verb.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SYNC-1 | A phone->gateway resync action carried in the existing sealed `RemoteCommand` envelope, handled by the gateway calling `JournalReadFrom` (`internal/skeleton/api.go:418`) and re-sealing the `JournalResume` back to the phone mailbox as a new frame kind. | RED test: inject a gap; the phone requests resync; the resume frame is delivered, seeded, and the view is correct. |
| PB-SYNC-2 | `Stale()` stops being permanently sticky: it clears **only** after a successful reseed. | Test asserts sticky-until-reseed, and that a failed resync leaves it stale (fail-closed, never optimistic clearing). |
| PB-SYNC-3 | Resync is bounded and non-amplifying: a hostile or broken relay cannot drive unbounded work. | Test: non-advancing pages terminate (reuse the Phase A `errStuckPage` discipline); resync attempts are rate-bounded. |
| PB-SYNC-4 | The new action does not weaken sealing, authorization, or seq gating: it goes through `requireRemoteAuthz` like every other remote op, and the new frame kind is handled by the typed, fail-closed router (`internal/phonecore/snapshot.go`). | Phase A authz and seq-gate tests pass unchanged; a test asserts an unauthorized resync is refused. |
| PB-SYNC-5 | The design is recorded in the ADR (new action + new frame kind). | ADR amendment merged. |

### 6.4 PB-LIFE — gateway lifecycle glue (closes Phase A deferral G3)

Verified: no unit files exist anywhere (`packaging/` holds only `homebrew/swarm.rb`);
`swarm-remote` is not even in the release matrix (`.goreleaser.yaml:12-15` builds `./cmd/swarm`
only); the gateway is started by hand. ADR-007:50 requires it run "under an external
supervisor ... never spawned by the daemon" — the CLI (owner-invoked) is therefore the correct
place to hook, not the daemon.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-LIFE-1 | A supervised gateway: launchd plist (macOS) + systemd unit (Linux), generated from one source, running as the owner with restart-on-exit. | Units generated and installed by a CLI path; `plutil -lint` passes; a test asserts restart-on-exit, correct user, and no embedded secrets. |
| PB-LIFE-2 | A successful `swarm remote pair` ensures the gateway is running — no manual restart step. Hook point: `cmd/swarm/remote.go` `runRemotePair` after `res.Paired`; unit installation belongs in `runRemoteInit` which already provisions `<stateDir>/remote/`. | Integration test asserts the supervisor is engaged post-pair (through the supervisor abstraction, not by mutating the real system). ADR-007:50 respected: the daemon never spawns the gateway. |
| PB-LIFE-3 | Supervision + Phase A's exit-on-revoke together close the "live gateway epoch-reload" residual: the gateway exits on revoke, the supervisor restarts it, and it loads the rotated epoch. | Test: after revoke + restart, the gateway runs under the NEW epoch and the revoked device stays dead. Recorded in the ADR as that residual's closure. |
| PB-LIFE-4 | Units carry no credentials; installed files are owner-only. | Test asserts permissions and absence of secret material. |
| PB-LIFE-5 | Crash-looping is throttled, not spun. | Backoff/throttle present in both unit types; asserted. |
| PB-LIFE-6 | `swarm-remote` is a released artifact (today it is unreleasable). | Added to the release matrix; a build produces it for the supported platforms. |

### 6.5 PB-PUSH — FCM

Verified: `internal/remote/relay/apns.go` is 23 lines and **no APNs implementation exists at
all** — `APNsSink` (`apns.go:20`) has no production implementor. FCM will therefore be the
first real push backend. The relay stores exactly **one** token per routing id
(`server.go:898,935`), and the presence-timeout wake pushes with **no** ciphertext
(`server.go:1131`).

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-PUSH-1 | Rename the seam to be transport-neutral (`PushSink`/`PushPayload`) — it is already content-agnostic; keeping the APNs name for the FCM path is a documented landmine. Mechanical, ~9 lines. | Rename lands with Phase A tests green. |
| PB-PUSH-2 | An FCM v1 sender implementing the seam. | Unit tests against a fake FCM endpoint: send, OAuth token acquisition + refresh, retry on 5xx, and pruning on `UNREGISTERED`. |
| PB-PUSH-3 | Payloads are ciphertext-only: no session names, hostnames, agent names, or Group labels visible to Google. | Test asserts the outbound body carries only opaque fields; ADR states exactly what the push provider observes (token, timing, size) consistent with ADR-007 D11. |
| PB-PUSH-4 | The Android app receives a push, wakes the core, pulls + decrypts locally, and renders the notification from **decrypted** content — never from the payload. | Robolectric/JVM test of the messaging service. |
| PB-PUSH-5 | Missing/invalid credentials degrade gracefully: everything still works without push, and misconfiguration is loud, not silent. | Test: nil/misconfigured sink -> no crash, explicit error, core paths unaffected (matches the existing silent-drop behavior at `server.go:948` being made observable). |
| PB-PUSH-6 | The single-token-per-routing-id limitation is either documented as acceptable for single-device v1 or fixed. | Explicit decision in the ADR; a test pinning the chosen behavior. |

### 6.6 PB-TOK — shared design tokens

Verified: `remote-control-design-directions.html` defines **38 `--p-*` tokens across 4
directions** (D1 Substrate `:130`, D2 Void `:145`, D3 Signal `:163`, D4 Instrument `:186`),
with a semi-machine-readable `DIRS` JS object (`:313`, `sw` swatch arrays). **The product
tokens are dark-only** — roadmap B2's "light+dark" does not exist yet, and there is **no
spacing scale** (paddings are literal px).

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TOK-1 | One machine-readable token source (JSON) is the single origin, consumed by the Android theme. | Android theme is generated from or asserted against the JSON; a drift test fails if the HTML and JSON disagree. |
| PB-TOK-2 | Exactly one skin is chosen for v1 (Substrate or Void) and recorded; shipping four is not a v1 need. | Decision recorded in the ADR; app implements one. |
| PB-TOK-3 | Light-mode values are **authored** (new design work, not extraction) and complete — no token missing in either mode. | Parity test asserts every token exists in both modes. |
| PB-TOK-4 | The terminal peek keeps the binding phosphor-green monospace treatment; purple stays retired. | Asserted against the token source + emulator evidence. |

### 6.7 PB-SAS — SAS correctness on the handset

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SAS-1 | The six-emoji SAS is computed by the shared Go core (`crypto.SAS`, `sas.go:58`) and returned by the façade as a display string; the emoji table is **never** re-implemented in Kotlin. (This is what the cross-language KAT gate exists to prevent; keeping it in Go removes the failure mode rather than testing around it.) | Façade returns the string; a test asserts no emoji table exists in Kotlin sources. |
| PB-SAS-2 | A known-answer test pins channel binding -> six emoji. | Go KAT test; emulator evidence shows phone SAS == machine SAS for the same pairing. |
| PB-SAS-3 | The UI presents the SAS as a compare-both-screens confirmation (per the binding mock), never as something typed. | UI test + evidence screenshot. |

### 6.8 PB-APP — the Android client (design §8 v1 screens)

Binding screen set = ADR-007's adopted design, minus what that amendment explicitly defers to
Phase 2 (chat transcript, approval sheet, voice, quiet hours, activity-feed depth).

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-APP-1 | **Pairing/onboarding**: QR scan or manual entry -> SAS compare -> paired, with explicit failure states (declined, wrong SAS, timeout, already-paired). `pairing.RunDevice`'s fail-closed semantics (`ErrPairingDeclined`, `pairing.go:71`) are surfaced, not swallowed. | State tests for every path including all failures; no path leaves an undefined state. |
| PB-APP-2 | **Triage inbox**: four Groups as sections, machine switcher, one-line need summaries. | UI test covering all four Groups, empty state, multi-machine switching. |
| PB-APP-3 | **Session detail**: journal events + snapshot cards, persistent Stop. | UI test; Stop maps to interrupt/kill with confirmation. |
| PB-APP-4 | **Terminal peek + take-control**: renders the daemon-sanitized `SnapText` (the app never runs a VT emulator — ADR-007 Decision 2), live tail, take-control acquires the lease, on-screen keyboard sends keystrokes, release on exit. | UI + integration test through the façade; asserts the app renders only sanitized text and never raw PTY bytes. |
| PB-APP-5 | **Machines pane**: presence (online/asleep/offline), paired devices, revoke, kill switch, activity log. | UI test; revoke + kill switch gated per PB-SEC-2. |
| PB-APP-6 | **Launch**: submit a launch spec via the v1 builder/policy path. | UI + façade test; policy rejection surfaced, never silently dropped. |
| PB-APP-7 | **Settings**: two coarse push toggles + biometric gate toggle. | UI test; toggles persist and are honored by PB-PUSH-3. |
| PB-APP-8 | **Connection/stale UX**: offline, reconnecting, and stale/resyncing states visible; a stale view is never presented as live. | UI test driving each state including PB-SYNC-2's stale state. |
| PB-APP-9 | Every façade error reaches the user; none is silently swallowed. | Test/lint over error paths; reviewed. |
| PB-APP-10 | Revocation is handled gracefully: a revoked device shows an explicit re-pair prompt, not a failure loop. | Test asserts the re-pair state. |

### 6.9 PB-SEC — handset-side security (the §3 delta)

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-SEC-1 | Core key material is sealed at rest under an Android-Keystore-backed (hardware-backed where available), user-authentication-gated KEK. No raw key in SharedPreferences or plain files. | Test asserts the persisted blob is not the raw key and does not decrypt without the keystore key; design recorded in the ADR. |
| PB-SEC-2 | A biometric/device-credential gate guards high-blast-radius ops: take-control input, kill, launch, revoke, kill switch. (ADR-007 D7 already contemplates a biometric gate token.) | Each gated op refused without fresh auth; not bypassable by app restart. |
| PB-SEC-3 | No plaintext session content persisted unencrypted; no secrets or session content in logs. | Tests + log assertion; reviewed. |
| PB-SEC-4 | `FLAG_SECURE` on pairing and terminal-peek screens; sensitive content excluded from recents. | Asserted over window configuration. |
| PB-SEC-5 | Cleartext traffic disabled via network security config, backstopping PB-NET-2. | Manifest/config assertion. |
| PB-SEC-6 | The app cannot bypass any server-side control: kill switch, lease, capability, expiry, and seq gating remain authoritative server-side. | Adversarial test: a misbehaving client cannot type without a lease or while the kill switch is engaged — refused by the server, asserted through the REAL transport. |
| PB-SEC-7 | Device-loss response works and is documented: owner revokes; epoch rotation kills the lost device's traffic; the gateway severs and exits. | Phase A revoke evidence re-asserted through the real transport; ADR amendment documents threat + response. |
| PB-SEC-8 | No analytics/telemetry SDKs; the dependency set is minimal and justified. | Dependency list reviewed; assertion that no analytics dependency is present. |

### 6.10 PB-TOOL — toolchain and reproducible build

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-TOOL-1 | The toolchain (JDK, SDK, build-tools, NDK, gomobile, `-androidapi`) is pinned in-repo, not in shell history. | A checked-in config names every version in §2; a fresh shell sourcing it can build. |
| PB-TOOL-2 | One command builds the AAR. | Script produces an `.aar` with `classes.jar` + `jni/<abi>/libgojni.so`; non-zero exit on failure. |
| PB-TOOL-3 | One command builds the debug APK; release signing reads an operator-supplied keystore from config/env, never the repo. | `assembleDebug` produces an installable APK; no keystore or password in git. |
| PB-TOOL-4 | The Gradle wrapper is checked in (no system `gradle` dependency), with a pinned distribution. | `./gradlew --version` works without system gradle. |
| PB-TOOL-5 | No Go regression from the new packages/tool directive. | `go build ./... && go vet ./... && go test -race ./...` green. |
| PB-TOOL-6 | Android sources are linted and unit-tested in the same gate. | `./gradlew lint test` green; documented beside the Go gate. |
| PB-TOOL-7 | CI covers the new artifacts (today `.github/workflows/ci.yml` has no android lane). | A CI lane builds the AAR (and runs the Gradle gate) or the omission is explicitly justified and recorded. |

### 6.11 PB-E2E — verification

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-E2E-1 | A Go end-to-end test wiring the REAL components — real relay server, real relay client, real gateway, real daemon, real façade (no injected fake) — exercising pair -> observe -> launch -> take_control -> type -> revoke. | Passes under `-race`. This is the primary machine-checkable proof of the exit criterion. |
| PB-E2E-2 | An on-emulator smoke: the APK installs, pairs against a local relay + daemon, shows a matching SAS, observes, takes control, and types. | Evidence (log + screenshots) under `docs/verification/` + a reproducible runbook. |
| PB-E2E-3 | Evidence files follow repo convention, with a RED-first run recorded per slice. | Files exist; GG-5 satisfied per slice. |
| PB-E2E-4 | No Phase A regression at any slice boundary. | Full suite + the four gates green at every boundary. |

### 6.12 PB-OPS — minimum production readiness

"Ready for production" was required by the operator. Full relay ops depth is roadmap C2; this
is the subset Phase B genuinely needs.

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-OPS-1 | The relay is deployable by a documented, reproducible path with TLS termination guidance. | Image/unit builds; runbook executed locally at least once. |
| PB-OPS-2 | Key/identity backup and recovery documented (what to back up; what a loss costs). | Runbook section reviewed for honesty against the crypto design. |
| PB-OPS-3 | An operator runbook covers install, pair, revoke, kill switch, device loss, and push configuration. | Each step executed at least once during verification. |
| PB-OPS-4 | Honest metadata disclosure: what the relay operator and the push provider can observe. | ADR/doc section consistent with PB-PUSH-3 and ADR-007 D11. |

### 6.13 PB-DOC — decisions and traceability

| ID | Requirement | Acceptance criteria |
|---|---|---|
| PB-DOC-1 | An ADR-007 amendment records every Phase B decision: the façade + dependency-edge surgery, long-poll transport, resync action + frame kind, keystore/KEK design, push seam rename + payload shape, supervision closing the epoch-reload residual, device-loss threat model, chosen skin, and the single-device carry-over. | Amendment merged before the final audit. |
| PB-DOC-2 | Phase B exit criteria added to `implementation-goals.md`; a verification file maps every PB-* ID to evidence. | Both updated; full ID coverage. |
| PB-DOC-3 | Residuals recorded in the Phase A closure style: what is deferred, why, and whether it is adversary-reachable. | Reviewed by the committee in the final round. |

---

## 7. Non-goals (explicit, with rationale)

| Out of scope | Why |
|---|---|
| iOS client | Phase C by design; needs Xcode + Apple account, neither present. The façade is shared, so iOS is a rebind, not a rewrite. |
| Chat transcript / `transcript_delta` | ADR-007: Phase 2, gated on spike S-A (deriving clean incremental text is unproven). |
| Approval sheets / `interaction_request` | ADR-007: Phase 2, gated on spikes S-B/S-C. |
| Voice, quiet hours, Live Activities, activity-feed depth | Design §9 puts them in Phase 2. |
| Multi-device pairing + `SenderKeyID` binding, and the epoch-equality reconcile revisit riding with it | The exit criterion is ONE phone. Phase A's `AddSole` single-device invariant holds. Doing it here reopens validated Phase A security surface for no exit-criterion gain. Phase C. |
| Admin tier | Phase A deferral; no exit-criterion need. |
| ME-1 relay socket close | Non-load-bearing since the gateway exits on revoke (Phase A round 3). |
| Multi-subscriber observer fan-out | Design §7.3: Phase 3. |
| Play Store distribution | Sideload satisfies the milestone; release signing is supported (PB-TOOL-3), store submission is not a Phase B need. |
| Real APNs | No Apple account; the seam rename (PB-PUSH-1) keeps iOS a drop-in later. |

---

## 8. Verification strategy and its honest limits

**Three tiers, strongest first:**

1. **Go end-to-end with real components (PB-E2E-1)** — everything except the Kotlin UI is
   exercised for real: real relay, real transport, real crypto, real gateway, real daemon.
2. **Android JVM tests + `gradlew lint test`** — UI logic, push receiver, security config.
3. **Emulator smoke (PB-E2E-2)** — the only tier proving the built APK runs and the JNI
   boundary works on a real Android runtime.

**Limits to be stated in the final report, not papered over:**

| Limit | Consequence | Mitigation |
|---|---|---|
| No Firebase project | Real FCM delivery unverifiable here | Full implementation + fake-endpoint tests + credential runbook; PB-PUSH-5 keeps the system functional without push |
| No physical handset | Hardware-backed Keystore and real biometrics are emulator-approximated | Code path exercised; the hardware guarantee is not proven here |
| No provisioned VPS relay | Real-network latency/NAT unverified | Local relay proves protocol correctness; PB-OPS-1 makes deployment reproducible |
| Emulator is x86_64 | arm64 native lib is built but the smoke runs x86_64 | Build all target ABIs; Go code is architecture-independent |

The final audit must confirm this list is **complete**, not merely present.

---

## 9. Implementation plan — slices and the agent dance

Operator's instruction: opus and fable subagents only, **independent agents per role** (test
author, implementer, reviewer are separate agents that do not share context), TDD with an
evidenced RED run, repeated per slice, with review-and-iterate at each phase end.

| Slice | Requirements | Model | Depends on |
|---|---|---|---|
| **S0 Dependency-edge surgery** | PB-BIND-0 | opus (touches Phase A code) | — |
| S1 Façade + bind-legality guard | PB-BIND-1..7, PB-SAS-1 | opus (security-relevant) | S0 |
| S2 Transport resilience + TLS | PB-NET-1..4, PB-NET-6..7 | opus (security-critical) | S0 |
| S3 Long-poll read | PB-NET-5 | opus (relay protocol change) | S2 |
| S4 Gap-resync | PB-SYNC-* | opus | S1, S2 |
| S5 Gateway supervision + auto-start + release | PB-LIFE-* | opus | — |
| S6 Push seam rename + FCM sender | PB-PUSH-1..3, 5, 6 | opus | — |
| S7 Design tokens (incl. authoring light) | PB-TOK-* | fable | — |
| S8 Android project skeleton + build + CI | PB-TOOL-* | fable | S1 |
| S9 Android screens | PB-APP-*, PB-SAS-3 | fable, opus review | S8, S7, S1 |
| S10 Handset security | PB-SEC-* | opus | S8 |
| S11 Push receiver | PB-PUSH-4 | fable | S8, S6 |
| S12 E2E + emulator smoke | PB-E2E-* | opus | all |
| S13 ADR / docs / ops runbooks | PB-DOC-*, PB-OPS-* | fable, opus review | all |

S0 blocks S1/S2 and therefore most of the phase — it runs first, alone, with the Phase A suite
as its guard. S5/S6/S7 are independent and run in parallel with it.

Each slice gate: evidenced RED -> implementation (independent agent) -> independent review ->
`go build/vet/test -race ./...` (plus the Gradle gate once S8 lands) green before any dependent
slice starts.

---

## 10. Definition of Done for Phase B

Phase B is done when **all** hold:

1. Every PB-* requirement has passing evidence, or is explicitly recorded as a §8 limit with
   committee agreement.
2. The exit criterion is demonstrated: an Android build pairs, observes, launches, and types
   into a real session over a real relay (PB-E2E-1 machine-checkable; PB-E2E-2 on emulator).
3. `go build ./... && go vet ./... && go test -race ./...` green; `./gradlew lint test` green.
4. No Phase A regression: the Phase A suite and its committee-validated security properties
   still hold, re-asserted through the REAL transport.
5. ADR amendment + verification evidence merged.
6. The audit committee agrees the requirements are met and the system is production-ready, or
   every remaining residual is documented, non-adversary-reachable, and accepted by all members.
