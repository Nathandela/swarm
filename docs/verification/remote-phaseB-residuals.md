# Phase B residuals — consolidated, in the Phase A closure style

**Scope: PB-DOC-3.** *"Residuals recorded in the Phase A closure style: what, why,
adversary-reachable or not."*

Phase B's residuals were recorded honestly and recorded **scattered**: an `## Accepted residuals`
section in each of 24 slice evidence files, an open-debt ledger in
`remote-phaseB-progress.md`, and decision-level acceptances in ADR-007's B-entries. That is the
right place to *write* them and the wrong place to *read* them: no reader of any one file can
answer the question the final audit actually asks, which is E15.4 — **is there a known-open
security defect?**

This file answers that question. It is a consolidation, not a new investigation: every entry
points at where it was originally recorded, and nothing here is retired that was not retired
there.

---

## How reachability is decided here, and what the verdict is worth

Phase A's closure classified residuals by whether the **relay adversary** could reach them,
because the relay is the one party the threat model assumes is hostile. Phase B adds a handset, so
one axis is not enough. Four classes, most-serious first:

| Class | The adversary | Why it is its own class |
|---|---|---|
| **R** | The untrusted **relay**, or anyone on the network path between the phone and it | The threat model's named adversary; Phase A's whole bar |
| **D** | Someone with **write or read access to the handset's app data**, or a rooted device | New in Phase B — Phase A had no client |
| **O** | The **owner's own machine or a local fault** (I/O error, crash window, misconfiguration) | Not an attack; can still lose data or brick the product |
| **N** | **No adversary** — dead code, coverage gaps, hygiene, product correctness | Cannot be a security finding, and pretending otherwise dilutes the ones that are |

**The verdict on each line below is mine, derived from the cited evidence text plus source
checks where the entry says so.** Three entries I could not settle here and they say so at the
entry rather than defaulting to the reassuring answer. A residual's class is not its severity: an
N-class residual can be product-fatal (§4.1 is), and an R-class one can be trivial.

---

## 1. Class R — the relay adversary or the network path

### 1.1 The transport-security policy is not on the shipping dial path *(OPEN — found in S20)*

**What.** `relay.Security` — the certificate pin, the cleartext refusal, and the redirect re-check
that stops a `wss://` upgrade being answered with a 302 into `ws://` — is applied only by
`relay.DialSecure`. **No production caller calls it.** `mobile/relay.go`'s `App.dial` calls
`relay.Dial`; `mobile/pairing.go` calls `relay.DialRaw`; `cmd/swarm-remote/main.go` calls
`relay.Dial`. A repo-wide search finds no non-test construction of a `relay.Security` value at all.

**Why it matters.** On Android `TrustRootSourceFor` returns `TrustRootsPinned` precisely because
Go's root store is stale or empty there — but `ErrPinRequired` is raised inside `DialSecure`, which
the app does not reach, so the refusal never fires. A phone handed a `ws://` relay URL by its
pairing QR runs the whole session in cleartext with nothing objecting, and a `wss://` one is
verified against the root store the code documents as unusable.

**Reachable: YES, by the relay operator and by a network-path adversary** — for routing metadata,
which is exactly what PB-NET-2 exists to protect. Payloads stay sealed either way; this is a
metadata-confidentiality hole, not a content one.

**Status: OPEN.** Not fixed in S20 — the fix needs a channel to carry the pin to the handset, and
the pairing QR has no pin field and no room for one at `MaxRelayURLLen = 39`
(`docs/operations/relay-runbook.md` §8c). This is standing defect class (v), *a fence guarding a
path production does not take*, applied to the phase's transport-security requirement.
`internal/remote/transport/tls_test.go` is green and covers `DialSecure`.

### 1.2 One duplicate frame from a hostile relay shifts every reply by one *(CLOSED)*

**What.** `Conn.pending` discarded replies owed to abandoned requests, so a *duplicate* frame from
the relay was undetectable: after one duplicate, a drain returned `0 items, hasMore=false,
err=nil` against a mailbox holding one. Recorded in `remote-phaseB-s6-evidence.md`, which called
it "the load-bearing assumption of a deliberate design change, in a system whose threat model says
the relay is untrusted".

**Reachable: YES.** **Status: CLOSED by S6b's request-id correlation**, which is what the S6
evidence names as the real fix. Recorded here because the S6 file alone reads as open.

### 1.3 Append and push rate windows are per-target and shared across senders *(ACCEPTED, v1)*

**What.** `appendRate[req.Target]` and `pushRate[req.Target]` are keyed per target, so an
authorized sender can burn a target's whole `MailboxAppendPerMin` and the legitimate phone gets
`ErrQuotaExceeded` until the window rolls. ADR-007 **B29**.

**Reachable: YES, but narrowly.** You must be authorized by the target to append at all, so in
single-device v1 the only party who can do this is the owner's own phone — except in a fresh
target's **first-use window**, where a stranger holding a new phone's relay-auth public key could
burn its append budget and delay the epoch grant by up to a minute. Transient and self-healing.

**Status: ACCEPTED for v1 with an explicit trigger — multi-device, or any change that widens who
may append to a target.** B29 also records the constraint on the fix: keying the window per
`(source, target)` mints map entries under attacker-chosen keys, which is the unbounded per-key
state the auth path refuses by design. Anyone taking this must not trade a fairness bug for a
memory-exhaustion bug. **The trigger is a "when", not an "if": the relay code already supports
more than one device.**

### 1.4 Re-authenticating an already-authenticated relay socket is a legal frame sequence *(RECORDED)*

**What.** Neither `handleAuthInit` nor `handleAuthResp` checks `sc.authed`, so a client may
authenticate as A and then re-authenticate as B on the same connection; `registerSession` rewrites
`sc.rid` in place. Phase A surface, found by S6b.

**Reachable: YES** by any client that can complete one handshake. **Status: RECORDED, not urgent
— Phase B's exposure through it (a wait-slot leak) is fixed, and `s.sessions` self-heals because
`registerSession` overwrites the key.** The hazard is the *next* feature keyed on `sc.rid`. S6b's
note is explicit that this must not be fixed inside a Phase B slice without an ADR.

### 1.5 A lost mutation under a scalar per-stream high-water *(ACCEPTED)*

**What.** A `kill` at seq 1 whose forward fails, followed by a keystroke at seq 2 in the same
batch, persists `Highest=2`; on restart the retained kill is refused and never re-forwarded — an
accepted deviation from PB-GW-3's "loss forbidden" for mutations.

**Reachable: YES by a relay that fails a forward — and also with no adversary at all.** The
reviewer tested the obvious fix and it makes inputs above the hole replayable, violating the input
class's forbidden-duplication rule; a gap set would be needed.

**Status: ACCEPTED, not fixed.** Softened by the fact that against an honest (purging) relay the
mutation is already lost regardless, because `PollOnce` acks `maxCursor` unconditionally past
per-item failures — so this is a requirements-level gap that predates S2.

### 1.6 An in-place relay upgrade skips every pre-version record *(ACCEPTED, by design)*

**What.** ADR-007 **B28**: the mailbox item layout gained a version byte, and the store fails
closed on any record it did not itself write — skipped, never served.

**Reachable: not by an adversary; triggered by the operator upgrading.** A skipped record is a
lost frame, which the receive path tolerates (the phone reseeds). The alternative — reading a
legacy record as the new layout — serves 32 bytes of ciphertext as a routing id and **wedges that
mailbox for its whole retention window** while the relay reports itself healthy.

**Status: ACCEPTED and deliberate.** The operator-facing consequence is in
`docs/operations/relay-runbook.md`; without it the next operator rediscovers it as data loss.

### 1.7 Out-of-order verbatim replay can manufacture a gap *(ACCEPTED)*

A delivery-unknown record replayed after a later successful frame. **Not silent loss** — the phone
already saw `Gap: true`, so PB-SYNC-1 resyncs. **Reachable: YES; harmless.** `S2b`.

---

## 2. Class D — the handset's own data directory

### 2.1 Key material in the clear on the shipped app *(CLOSED)*

**Retired by S14 and the retirement was forced, not remembered.** `swarmmobile.NewApp` now takes a
`KeyCustody` reverse-bound interface implemented over the Android Keystore, one method per
PB-KEY-2 tier. The fence that named this defect
(`TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites`) went red on purpose when S14 deleted
both call sites, and was retargeted to a floor of zero rather than deleted. Recorded here because
several slice files still describe it as open.

### 2.2 A cheap DoS with a destructive-only remedy *(RECORDED, not fixed)*

**What.** Anyone with write access to the app data directory can stamp `schema_version: 2` plus one
key field and **permanently** prevent the app starting: `NewApp` fails with `ErrCorruptState` and
the only remedy is clearing app data (which is what the message now says — the earlier "re-pair"
advice was unreachable).

**Reachable: YES, by any party who can already write the app's private data** — which on a
non-rooted Android device is the app itself, its backups, or an attacker who has already won.
**Status: RECORDED, not fixed.** `S14a`.

### 2.3 Unsealed plaintext buffers are not zeroized after use *(RECORDED)*

Same posture as the pre-existing software store; hardware custody is the real fix. The KEK the
façade fetches *is* zeroized as soon as the cipher is built. **Reachable: only by a process that
can read another process's memory**, i.e. a rooted device or a core dump. `S14`, `S14a`.

### 2.4 `resolveSend` copies the content key into a `sendCtx` *(RECORDED — has an owner)*

An operation already in flight continues past a concurrent purge. In-memory and in-flight, never a
durable resurrection. **The one place a purged key is still used.** **Owner: whoever next touches
the façade send path.** `S14`, `S14a`.

### 2.5 The v4 → v5 migration leaves the cleartext copy on disk until the first Save *(ACCEPTED)*

Inherent to migrating a file rather than rewriting it at load, and **theoretical: no Phase B app
has shipped, so there is no installed base.** `S15`.

### 2.6 Backup exclusion covers `domain="root"`, not `device_root` *(ACCEPTED, scoped)*

Covers the app's private data directory, which is where the state dir lands today. If a later slice
moves it to device-protected storage so the wake path can run before first unlock — plausible for a
push-woken app — the rules need a `device_root` exclusion too. **Not added speculatively; AGP lint
could not be run here to confirm the domain token.** `S15`.

### 2.7 The Keystore read-back never compares `insideSecureHardware` *(OPEN — found in S20)*

**What.** `CustodyProvisioning.provision`
(`android/app/src/main/kotlin/dev/swarm/phone/keys/Provisioning.kt`) does the right thing in general
— it generates, then reads `KeyInfo` back, and throws `KeystoreDowngrade` when the achieved
parameters differ from the requested ones. But its `downgrades` list compares only
`userAuthenticationRequired`, `userAuthenticationValidityDurationSeconds`,
`invalidatedByBiometricEnrollment`, and a spuriously-reported StrongBox. **`insideSecureHardware` is
read into `KeyInfoRecord` and never compared against anything.** A handset whose Keystore returns a
purely software KEK provisions cleanly, `strongBoxBacked` is `false`, and nothing objects.

**Reachable: not by an adversary — but it silently voids PB-SEC-1's at-rest claim on any device
where it is true.** Partly a limit of the API: `KeyGenParameterSpec` has no "require secure
hardware" setter, so there is no requested value to compare against.

**Status: OPEN, and it is why the gate's step 2a is a human observation** rather than an assertion —
nothing in the product will report a software-only KEK.

### 2.8 Provisioning refuses over two capabilities no matrix row consumes *(OPEN — found in S20)*

**What.** `CustodyPlan.forDevice` returns `Refused` unless `KEYSTORE_X25519` and `KEYSTORE_ED25519`
both probe `PRESENT` (`UNKNOWN` fails closed as `ABSENT` does). But **no row in `KeyCustodyMatrix`
is `KEYSTORE_NATIVE`** — every role is `KEYSTORE_WRAPPED`, with the X25519/Ed25519 private halves in
the Go core sealed under an **AES-GCM** Keystore KEK (ADR-007 B17(a)). So the app requires two
Keystore capabilities it never uses.

**Why it may still be right:** its own comment argues the canary case — at API 33 both are meant to
be guaranteed, so a non-`PRESENT` answer means a Keystore misbehaving, and failing closed beats
downgrading silently.

**Reachable: no adversary; a plausible hard failure on a real device.** On a handset whose
Curve25519 probe answers `ABSENT` or `UNKNOWN`, the app refuses to provision at all.

**Status: OPEN, undecidable without a device**, which is exactly why it is step 2c of the gate and
deliberately first. Note also that this corrects how B31 and PB-KEY-8 frame the Curve25519 risk:
KeyMint's Curve25519 support does not gate any role's *backing*, because no role asks Keystore for a
Curve25519 key. It gates *whether the app provisions at all*.

### 2.9 The wake KEK is not user-authentication-gated *(BY DESIGN)*

ADR-007 B9/B16. `KEYSTORE_WRAPPED` is accurate for `RELAY_AUTH` — the key *is* wrapped by a
Keystore AES key — but the tier's gate is the **split**, not the KEK. Stated because PB-KEY-8's
matrix forbids a residual on a non-`SOFTWARE_ONLY` row. `S14`.

---

## 3. Class O — the owner's machine, local faults, and misconfiguration

- **`swarm remote revoke` reports success for a device id that was never paired** — prints
  `revoked device <id>` and exits 0. Verified no-op: the machine epoch does not rotate. **Found in
  S20.** During a device-loss incident a mistyped id produces exactly the output that says the lost
  phone is cut off. `swarm remote regrant` refuses the same id properly. Mitigation is in the
  operator runbook (always confirm with `swarm remote devices`); the asymmetry itself is **OPEN**.
- **`swarm remote init` warns and exits 0 when `swarm-remote` is not on `PATH`**, installing no
  supervision unit. A scripted install that checks the exit status records success for a machine
  whose gateway will never start. **Found in S20**, documented in the operator runbook. **OPEN.**
- **N1: re-`Install` never refreshes a loaded unit definition** — launchd ignores `bootstrap` on a
  loaded label and `Ensure` never runs `systemctl --user daemon-reload`, so after an upgrade
  `swarm remote init` silently leaves the old definition running until logout/reboot. `S4`.
- **N2: the unit's `SWARM_DAEMON_REMOTE_SOCK` may point at a socket nothing serves.** Symptom:
  "phone pairs, then silence". Spec/ADR follow-up. `S4`. S4b reduced the related env-inheritance
  hazard from silence to a warning, but **later drift is still unwarned**: the liveness probe runs
  only from `init`/`pair`, and `swarm remote status` is the natural home for a standing check.
- **A revoke that fails partway can leave the device live and un-severed.** `swarm remote off` is
  the working fail-safe and is independent of rotate/persist. Carried from the Phase A closure
  (round 6), and now an operator-runbook step rather than only a note.
- **Per-keystroke fsync is on the input critical path** — 13-15 ms on this M1/APFS host, so a batch
  of 8 costs ~120 ms, about 10% of the p50 ≤ 150 ms budget, worse on a network-mounted state dir.
  An invariant-preserving optimisation exists and was **recorded, not taken**. `S2`.
- **Retired-epoch entries accumulate**, one small record per revoke. Harmless and prunable. `S2`.
- **`Snapshot` charges N+1 appends to one slot**, so a reconnect loop against a severing daemon can
  exceed the append budget unthrottled. `S2b`.
- **A latent pairing race that reports itself as the wrong failure.** `relay.handleRendezvousClaim`
  refuses a rendezvous id it has never seen and `pairing.RunDevice` does not retry, so a
  `BeginPairing` that beats the machine's `Create` fails **terminally** and surfaces five seconds
  later as *"the phone never derived a SAS"*, with the real cause discarded. Reproduced 2 runs in 5
  under load. S9 fixed only its own test; `conformance_test.go`'s `runMachinePairing` still has it.
  **Anyone who sees "never derived a SAS" should suspect this first.** `S8`, `S9`.
- **A machine that publishes an empty `MachineEndpointID` still produces a paired-but-mute phone.**
  The phone does not refuse such a pairing, and refusing would be worse — the machine has already
  enrolled the device and `BeginPairing` fail-fasts while a device is registered, so a phone that
  discarded the enrollment would need physical access to the machine to recover. A sixth terminal
  pairing state is a **PB-PAIR-5 amendment, not an implementer's call**. `S19`.
- **`Core.Save` rebinds the live sequencer**, so a mid-session save jumps to the persisted block
  ceiling and sets the gap flag until a command frame is sent. `S7`.
- **Reconcile adoption is not persisted**, so every phone process death re-arms the fail-closed
  refusal of mutating ops. PB-STATE-10 territory. `S7`.
- Carried unchanged from the **Phase A closure**, all owner-side I/O-fault or degenerate-state
  edges and none relay-reachable: `machineid.Save` lacks the `(committed, err)` distinction; the
  epoch-equality startup reconcile is single-device-specific and **must be revisited in the
  multi-device ADR** (it would wrongly delete a legitimate survivor); `AddSole` committed-error
  enrolled-no-grant (self-heals on the next restart); the gateway under a *permanently* unreadable
  registry.

---

## 4. Class N — no adversary, and one of them is product-fatal

### 4.1 A phone more than 10 minutes FAST goes silently deaf to the whole machine→phone plane

**The most serious non-security residual in the phase, and it is worth reading twice.**
`phonecore/snapshot.go` enforces `InboundMaxAge` on the **phone's** clock, so a fast phone gets
`ErrStaleAge` for every reply, journal record and snapshot. `mobile/relay.go` **swallows it with no
stale mark and no event**, and `ConnectionState()` still reads **online**. The user sees a
connected phone that receives nothing.

**Reachable: by anyone who can move the handset's clock, including the user and the OS.** Not an
attack in any realistic model — but it is a total loss of function presenting as a healthy
connection. It is the **third clock wall** (30 s surfaced, 60 s refused opaquely, 10 min deaf) and
the only one that was undocumented. `S7b`, `S11`.

**Status: OPEN.** The asymmetry follows from S7b's deliberate one-sidedness (PB-GW-2 is
inbound-only), and closing it was blocked on the PB-TIME-2 reply-seal gap. S11 closed the
reply-seal half; **the phone-side bound is still not enabled.**

### 4.2 The offline op queue is safe only by accident, and nothing pins it

`OpQueue` stores **unsealed** `QueuedOp` and seals at replay time, so `IssuedAt` is stamped on
send, not on enqueue. That ordering is the only reason offline replay works at all under the
10-minute inbound age bound — **and no test asserts it**. A future refactor that sealed at enqueue
would silently brick offline replay, and the failure would look exactly like the PB-GW-6 brick S7b
was created to avoid. **Wants a test pinning seal-at-send.** Compounded by the fact that
`OpQueue.Enqueue` has **zero production callers**, so the hazard is latent in both directions.
`S7b`.

### 4.3 Fences on paths production does not take — standing defect class (v)

Each of these is fully tested and unreachable from the shipping app. None is harmful today; each is
a trap for the next slice that adds a caller.

| Symbol | Consequence | Source |
|---|---|---|
| `transport.SendLive`, `transport.RetryFor` | PB-INPUT-4's retry table is enforced **nowhere**; the façade appends through `relay.Client.MailboxAppend` directly | `S6b`, `S11` |
| `OpQueue.Enqueue` | Nothing fills the offline queue | `S7b` |
| `RelaySink.Err()` | Replay failures are stashed but unreachable and unlogged | `S2b` |
| `PushTokens.disable` (Kotlin) | "Deletion on disable" is a method with no caller; PB-APP-7's switches are where the call belongs | `S17` |
| `Sequencer.Next()` | Exported on the gomobile-bound package, issues seqs with no durable reservation | `S7` |
| `InsecureCleartextSealer` | Zero call sites; **deliberately retained** so a future test wanting unsealed custody names its choice rather than hand-rolling an identity sealer | `S14`, `S14a` |
| `relay.Security` | §1.1 — the whole transport-security policy | **S20** |

### 4.4 Measurement that does not measure the shipped path

- **PB-NET-5's latency harness never enters `mobile/commands.go`.** `phonesim.Phone.Type` seals with
  `phonecore.SealInputData` and calls `relay.MailboxAppend` directly, so the coalescer, the lease
  gate, `sendInputFrame` and S11's bucket-ordering lock are on the real app's path and on nothing
  the harness times. **The numbers are real for what they measure**, and the façade's added cost was
  measured separately as negligible (~1.9 µs/keystroke). **Read PB-NET-5's evidence as
  "phonecore → PTY", not "phone → PTY".** `S6b`.
- **Every latency number in the phase was taken through Rosetta** (`/usr/local/bin/go` is x86_64 on
  an M1), so they are **pessimistic**; a budget that passes here passes natively with margin.
  **Never loosen a bound to "correct for" Rosetta.** The probe must be `sysctl.proc_translated` read
  **in-process** — `uname -m` lies inside a translated process and a shell-level read lies the other
  way. `S6b`.
- **PB-BIND-1's literal criterion is skipped in the default gate** (needs `ANDROID_HOME`). The
  property *is* covered, by S13's `TestPBTOOL2_...`, which binds `./mobile` through the checked-in
  build command and inspects the artifact — but an auditor reading S8's green gate alone would
  over-read it. `S8`.
- **The grace-window half of PB-STATE-10's end-to-end recovery test is a sample, not a proof** —
  deleting `rearmAfterPairing` escapes it 2 runs in 5. The deterministic guard is the conformance
  test. `S18b`.
- **The Kotlin halves of S16 and S17 cannot be verified independently**: Kotlin compiles the whole
  test source set at once, so an unimplemented RED in one package blocks every Kotlin test in the
  module. `S16`.
- **Gradle caches aggressively.** A run reporting `BUILD SUCCESSFUL` with tasks `up-to-date` has you
  reading an earlier run's XML. Delete `test-results` and use `--rerun-tasks` before believing a
  count. The Kotlin baseline is **208 unique `@Test` counted from source**; a full run reports 416
  across the debug and release variants.

### 4.5 Unbounded or unpinned state

- **`ReplyCache` is unbounded** and deliberately-retained unattributable replies can accumulate;
  `TakeFor` makes selective draining possible. Needs a bound or a drain policy. `S1b`, `S7`.
- **The undelivered-input ledger is unbounded.** `S11`.
- **The `opID` invariant has no enforcement** — only a comment; a future background `replyError`
  would break correlation silently. `S1b`.
- **Nothing mechanically pins the `crypto` package's exported surface.** The freeze is
  process-enforced; the golden pin covers the mobile façade only. Pre-existing. `S14a`.

### 4.6 Gate hygiene, flakes, and known-red

- **`golangci-lint` is at 0** as of 2026-07-26 — but only under
  `~/go/bin/golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 ./...`. The default
  `max-same-issues: 3` suppresses duplicates *in proportion to how many exist*, so the report looks
  complete while hiding findings; a measured count of 25 was really 31. **The runbook line is the
  invocation, not the tool name.**
- **`gofmt` is checked by nothing.** It is not in golangci-lint's default set, and 10 files are
  unformatted at HEAD. GG-4 does not currently mean "formatted" by any mechanism — either it should,
  or GG-4 should stop implying it.
- **Five flakes**, none owned: `TestS6B_GatewayInputLatencyIsNotPollGated` (a latency assertion
  under suite load), `TestPBSAS2_...` (a temp-dir cleanup race; the test body passes),
  `TestPBPAIR5_.../different_machine` (load-dependent precondition — an epoch is not an epoch key),
  `TestPresence_TransitionsAndSilentPush`, and — **newly observed in S20** —
  `TestRunShim_LaunchesAgentPersistsAndLeadsSession` (*"shim pid N never became its own session
  leader (getsid != pid)"*, under full-suite load; `-count=3` in isolation passes). Three of the
  first four are suspected to share one cause. **A green that requires re-running trains readers to
  dismiss reds**, which is E15.6's whole point, and a fifth unowned flake is how the fourth stopped
  being read.
- **PB-E2E-2 is RED and stays RED**: `android/gate/s19_pbe2e2_test.go` reports four of five in-app
  actions with no subject (no scanner, no SAS display, no confirm control, no keyboard) and no
  instrumented source set. Assigned separately.
- **GG-5 is not satisfied for S17 or S18b** and cannot be satisfied retroactively — recorded in the
  progress ledger rather than papered over.

### 4.7 Product gaps recorded as decisions

- **A push preference the machine never received is never retried, and no façade surface reports
  it.** `SetPushPreference` advances the durable version before sealing, so a send that fails on a
  backgrounded handset — the normal state under ADR-007 B16 — burns a version the machine never
  stored. Safe, and **measured** rather than reasoned about: it works only because
  `filePushPrefs.SavePrefs` refuses anything not *strictly* exceeding what it holds. **If that rule
  ever became "consecutive", the first backgrounded toggle would deafen the machine to every later
  one.** The user sees their choice on screen while the machine keeps the old one. `S16`.
- **`App.SetPushPreference` still records a LOCAL refusal on the phone side.** `S12`.
- **PB-PAIR-4's pairing state is a façade-owned file**, not a `phonecore.State` field — a decision,
  not a constraint, and a second durability mechanism in a codebase deliberately strict about
  PB-STATE-1's one enumerated schema. `S16`.
- **PB-PAIR-3 is a decision only**: no scanner dependency, no camera code, no decode path. `S16`.
- **`PhoneSurface` acts on the first row of the triage inbox** because the surface has no
  navigation — the bounded-scope consequence of "a minimal Activity, not a finished app". `S18`.
- **The design HTML is an unversioned research artifact.** A replacement of the file would be caught
  only through the token block. `S5`.

---

## 5. What is deferred rather than residual

- **PB-E2E-5 (physical handset)** — ADR-007 **B31**, approved by enumeration: no real biometrics, no
  camera, no FCM delivery, no Doze, no hardware Keystore attestation. **An emulator is not a
  handset**, and every Android claim in this phase is *what the app asks the platform for*, never
  what the platform then does. The gate's runbook is
  `docs/operations/physical-handset-gate.md`, **every step of it marked UNRUN**; writing it
  surfaced the two entries below, which are residuals rather than deferrals because they are
  properties of the shipped code and not of the missing device.
- **Nothing has ever run against Google.** No account, no Firebase project, no
  `google-services.json`. The FCM sender is complete and tested against a fake endpoint; **no test
  in this repository is evidence that a wake would be delivered.**
- **Multi-device, multi-machine, the machine switcher, admin tier, live gateway epoch-reload, ME-1
  relay-close** — Phase C, unchanged from the Phase A closure. Multi-device is the trigger for §1.3
  and for revisiting the epoch-equality reconcile in §3.
- **Light mode** — Phase C (§5), which is why the token source pins `mode: "dark"`.
- **Production relay operations** — deployment, VPS provisioning, image publishing, TLS renewal
  automation, backup/restore, disk-full behaviour, log rotation, health checks, resource limits,
  cross-version compatibility. Phase C by the §6.18 scope correction.

---

## 6. The three I could not settle

Stated rather than defaulted to the reassuring answer:

1. **§1.1's blast radius on a real handset.** I established that no production caller reaches
   `DialSecure`. What a `wss://` dial from `relay.Dial` actually *does* on Android 14 — whether it
   fails closed against an empty root pool or succeeds against a stale one — needs a device, and
   PB-E2E-5 is deferred. The metadata exposure under a `ws://` URL is certain either way.
2. **Whether `sealAtSeq`'s caller-supplied closure under `s.mu` is reachable.** `S1b` records that a
   future `build` calling back into a locking method self-deadlocks, and documents it in the doc
   comment. I did not enumerate today's callers.
3. **Whether the four flakes are one bug.** The progress ledger suspects a shared cause for three of
   them and narrowed the fourth without closing it. I did not reproduce them.

## 2.9 THE APP CANNOT OBSERVE — three more bound verbs with no caller (found 2026-07-26)

Found by running, against the finished tree, the same search I had just written into the audit
committee's brief: *exported symbols with no non-test caller*. It is the class this phase has found
five times, and it is present **in the surface built to close the previous instance of it**.

**Measured.** Of 45 bound `App` verbs, 17 have no production-Kotlin caller. Three of those are
load-bearing for observation, and appear **zero times in ALL Kotlin — `main`, `test` and
`androidTest` alike**:

- `SetEventListener` — no listener is ever installed, so no journal event can reach the app.
- `SubscribeJournal` — journal delivery is never started.
- `TerminalWatch` — the machine is never asked to send terminal frames. `TerminalWatch` also has no
  non-test **Go** caller: only its own declaration at `mobile/commands.go:275`.

**Why the peek looks wired but is not.** `PhoneSurface` calls `bridge.terminalPeek(...)`, which calls
`app.peek(session)`, which reads `core.Router().Snapshots().Get(session)` — **a local cache**.
`TerminalWatch` is the verb that asks the machine to populate it. With nothing subscribing, `Peek`
returns `no terminal snapshot for ...` forever, and the failure reads as "nothing is happening on the
machine" rather than as a missing subscription.

**Why the green gates did not catch it.**
- PB-E2E-1 passes because the **Go** chain calls `TerminalWatch` directly. The Go core is fully
  exercised; the app's use of it is not.
- The instrumented `PbE2E2PairAndTypeTest` would exercise it, but it requires an emulator **and** a
  session, and the smoke script stops at session creation (no CLI can create one). So it has never
  run.
- The screen-coverage artifact traces entry points, not reachability from them.

**Consequence: PB-APP-3/4/5 are non-functional in the shipping app**, in the same way PB-APP-1/6 were
before `App.Start` was found. A user pairs, sees a roster that never updates, and opens a terminal
peek that is permanently empty.

**The general lesson, which is now the most important thing this phase has produced.** Five separate
instances, each found later than the last, each in code that was unit-tested and traced in a coverage
artifact. **A gomobile facade makes "exists" and "is used" independent by construction** — the Go side
compiles and tests green with no caller at all, because its callers are in another language the Go
toolchain never sees. Every gate this phase built was one-sided until the S17 reachability walk, and
that walk is still bounded (it cannot cross property initialisers). **The generalisable control is a
bidirectional one: every bound verb must be either called from production Kotlin or explicitly listed
as deliberately unbound, with the list checked.** Without it, the next verb added is uncalled by
default and nothing says so.

## 2.10 Corrections to §2.7, §2.8 and §3 — two of my own claims were wrong

**§2.7, §2.8 and §3 are FIXED** (`0b8d73b`, `94f157f`, `083ec6e`). Three corrections stand with them.

**(a) §2.8's severity claim was FALSE, and the truth is a sixth instance of the uncalled-symbol
class.** I recorded that "on a handset whose Curve25519 probe answers ABSENT or UNKNOWN, the app will
not provision at all". **`CustodyPlanner.forDevice` has no production caller** — verified: it appears
only at its own declaration, and `PhoneRuntime.construct()` goes straight to
`KeystoreCustodyBootstrap` without ever building a capability map.
`KeyCustodyException.PlatformCapabilityMissing` is declared and routed and **never thrown**. So the
shipped app never refuses over Curve25519, because the gate that would refuse is never invoked — and
**physical-handset runbook step 2c is inert** and must be corrected before a device session.

The fix is still right: it is what a wiring slice would turn on, and the planner is the intended gate.
But this is arguably a **larger** finding than the defect it was dispatched to fix, and it is the
same class again — the sixth.

**(b) §3's "verified benign" was WRONG.** I recorded that revoking an unpaired id was harmless
because the epoch does not rotate. `runRemoteRevoke` calls `purgeOutboundCustody` **unconditionally**
once the daemon reports no error (`cmd/swarm/remote.go:540`, before the success message), so a
mistyped id **also emptied the machine's outbound journal** — the undelivered frames queued for the
handset that IS still paired. Not a no-op, and worse than the misleading output I filed it under. The
refusal now lands before all three purge steps. (`stopGatewayIfQuiescent` and `purgeRelayState` were
genuinely inert on that path.)

**(c) The remote tier keeps the asymmetry deliberately.** A phone-sealed `device_revoke` naming an
unknown id still returns OK, because over an at-least-once relay a retry of an already-successful
revoke legitimately removes nothing, and that layer cannot distinguish the two. The owner-tier guard
is what changed. Dropping the tier split fails a **pre-existing** fence
(`internal/skeleton/s18_revokeverb_test.go:127`), which is independent confirmation the split is on
the right axis.

## 2.11 A gate that reads green when it did not run

`android/app/libs/swarm.aar` is absent in a fresh worktree, so `./gradlew` fails at
`:app:requireSwarmAar` — and a command whose output is piped through `tail` **swallows the nonzero
exit**, so the run reads as green to anyone not checking the status. `./android/build-aar.sh` must be
run first. Recorded because "the gate passed" and "the gate ran" are different claims, and this phase
has now confused them twice: here, and with Gradle's cached `up-to-date` results reporting BUILD
SUCCESSFUL while an earlier run's XML was read.
