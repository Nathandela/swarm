# Swarm Remote Control — Ultra-Detailed Implementation Plan

Status: PLAN OF RECORD for the remote-control epic (`agents-tracker-5h5`). Every
implementation agent references this file. It turns the pinned design
(`docs/research/remote-control-design.md` v2) and the audit findings
(`docs/verification/audit-002-remote-control-design.md`) into per-phase, testable
requirements with explicit verification on THIS machine.

The goal is reached only when every requirement in every phase is implemented, its
acceptance criteria demonstrably met, its verification run green, and the phase gate
(section E) walked and evidenced under `docs/verification/`. Audit-committee sign-off
is required on this plan (coverage) and again on the finished implementation (fidelity).

---

## A. Ground truth: what can and cannot be verified here

This plan is honest about the build/verify environment (the audit's central demand — do
not price anything as free that is not).

- Platform: macOS arm64, Go >= 1.24, `-race` available. `go build/test/vet ./...`,
  `golangci-lint run` all run here and are the primary gates (GG-4).
- Real agent CLIs installed: `claude` and `codex` — spikes and E2E rigs drive them for
  real. `ollama` serves local models (qwen3:14b/8b/4b, qwen2.5-coder:14b, gemma3:4b) as
  cheap deterministic-ish agent stand-ins and to back a scripted fake-agent CLI.
- NO Xcode (Command Line Tools only): no iOS SDK, no simulator, no `.ipa`. Swift 5.10 CLT
  can compile pure-Swift macOS-target packages but nothing importing iOS-only frameworks.
- Consequence — the load-bearing architecture decision: ALL protocol, crypto, and state
  logic lives in Go. Both ends of every wire protocol are Go (daemon/gateway/relay and the
  gomobile-ready phone-core), so each protocol is implemented once and tested against
  itself here. The SwiftUI layer is a thin view shell over a gomobile-bound interface:
  authored and review-verified here, compiled on the user's Xcode machine later.
- The executable proof of the end-user experience on this machine is the `phonesim`
  harness (a Go CLI that drives phone-core exactly as the app would) plus a deterministic
  fake-agent CLI and, where useful, ollama-backed live agents. Anything that genuinely
  needs an iOS device is marked DEFERRED-VERIFICATION with the local proxy that stands in
  for it (phonesim scenario, macOS-target compile, or structured review against the mock).

New Go binaries/packages introduced (all in this repo, all buildable here):

- `cmd/swarm-relay` — the untrusted E2EE relay (localhost integration tests here; VPS
  deploy is a later user action).
- `cmd/swarm-remote` — the sidecar gateway (dials daemon UDS in, relay out).
- `cmd/phonesim` — the phone-core-driving harness / scenario runner.
- `cmd/fakeagent` — deterministic recorded-transcript PTY player for repeatable E2E.
- `internal/remote/...` — shared crypto, pairing, envelope, wire, journal-client packages.
- `phonecore/` — the gomobile-ready client library.
- `ios/` — the SwiftUI Void app + NSE (authored here, compiled on device later).

---

## B. Process every implementation agent follows (non-negotiable)

Derived from CLAUDE.md, `docs/specifications/implementation-goals.md` GG-4/GG-5, and the
existing evidence convention (`docs/verification/epic-13-evidence.md`,
`docs/verification/epic-08-red/`).

1. Role separation (independent agents, sonnet/opus only — never fable):
   - Spec/test agent: writes the failing tests FIRST from this plan's requirements. Does
     not write implementation.
   - Implementer agent: writes the minimum code to pass, does not edit tests to pass. If a
     test looks wrong, it STOPS and escalates.
   - Reviewer agent: independent of the implementer; verifies against requirements +
     acceptance criteria, adversarial mindset, checks the red evidence is real.
   These are different agent instances per task; the reviewer never reviews its own code.

2. TDD with evidenced failing-first run (GG-5): tests are committed/first-run RED, the red
   run captured to `docs/verification/<phase>-red/<slug>-red.txt` in the exact format of
   `epic-08-red/skeleton-red.txt` (undefined-symbol or behavioral failures, dated header).
   Implementation-first work is rejected by the reviewer.

3. Quality gates (GG-4) green before any phase closes: `go build ./...`, `go vet ./...`,
   `golangci-lint run`, `go test ./...` (with `-race` on every package that spawns
   goroutines). Swift: `swift build`/`swift test` on the macOS target for pure packages.

4. Evidence file per phase under `docs/verification/remote-phaseN-evidence.md`, matching
   the epic-13 template: What it delivers / TDD evidence (GG-5) pointing at the red file /
   Criterion walk table (requirement -> test or artifact) / Committee section / Quality
   gates (GG-4) / Notes and follow-ups.

5. Beads: one issue per requirement cluster before code; claim on start; close on green.
   No TodoWrite/markdown task lists. (Beads DB is currently write-locked by a parallel
   session; issues are created against the main checkout DB `agents-tracker-*` when the
   lock frees — tracked, not a blocker for planning.)

6. No emojis in code, comments, or docs prose. The only permitted emoji are literal data
   in the SAS wordlist table (a security/UI artifact), never in surrounding prose.

7. ADR-007 (`agents-tracker-hn8`) is authored in Phase 0 and is the authority for every
   protocol/spec change; specification docs are amended there, never silently drifted.

---

## C. Phase map (what ships when, and why)

- Phase 0 — ADR-007 + the three spikes. Decides A-vs-D transport, pins both crypto
  protocols, the journal/cursor contract, idempotency, launch authority. Spikes S-A/S-B/S-C
  return PASS/PARTIAL/FAIL verdicts with committed fixtures; their outcomes decide Phase 2
  scope. Gate: ADR merged + three spike evidence files exist with verdicts.
- Phase 1 — the provable core (ships even if every spike fails): relay, sidecar gateway,
  pairing + device registry, two-scheme crypto, daemon journal + idempotency store, inbox
  from `session_state`, remote grid attach with input via the existing lease,
  interrupt/kill with confirm, push on Group transitions (ciphertext, NSE path). One phone
  / one machine, then multi-machine. Full adversarial floor (section on R-ADV) passes.
- Phase 2 — what the spikes earned: chat transcript view (if S-A), structured expiring
  approval sheets (if S-B + S-C), launch-from-phone under the 5.6 authority model, remote
  revocation UX.
- Phase 3 — observer-attach fan-out epic, activity-feed depth, voice, quick replies, quiet
  hours, optional tsnet direct path (Option D), Live Activities.

Requirement areas and their owning phase are tagged in section D.

---

## D. Requirements

Each requirement: Statement (MUST/SHOULD, testable) / Acceptance / Verify (how, on THIS
machine) / Deps. IDs are stable and referenced by beads issues and evidence files.

IMPORTANT: every requirement below is read AS AMENDED BY SECTION D.0 (audit-003). Where D.0 and a
requirement conflict, D.0 wins. The requirements most affected carry an inline "[amended: D.0-Ax]" marker.

Area index:
- R-CRY crypto, R-PAIR pairing, R-DEV device lifecycle, R-REL relay server
  (owner: security-architecture)
- R-GW gateway, R-JRN journal, R-IDP idempotency, R-POL policy/launch, R-KS kill switch +
  activity log, R-CLI CLI, R-TUI TUI, R-PROT protocol extensions (owner: daemon integration)
- R-PHC phone core, R-SIM phonesim, R-IOS SwiftUI app, R-NSE notification extension,
  R-DSN Void design system (owner: phone client)
- R-SPK spikes, R-TDD process, R-ADV adversarial floor, R-E2E end-to-end rigs, R-GATE phase
  exit criteria (owner: verification)

### Phase tagging

- Phase 0 (design lock + spikes): all R-SPK, R-PROT.6 (ADR-007 + spec governance), R-GATE.1/.2.
- Phase 1 (provable core — ships even if every spike FAILs): all R-CRY, R-PAIR, R-DEV, R-REL,
  R-GW, R-JRN, R-IDP, R-KS, R-CLI, R-TUI, R-PROT.1-.5, R-PHC, R-SIM, R-NSE, R-DSN, all R-ADV,
  R-E2E.1-.6/.8, R-GATE.3, and the R-POL policy layer (enforcement + approval-binding
  VALIDATION). R-IOS screens are authored + review-verified in Phase 1 (compiled on device
  later). R-TDD applies to every phase.
- Phase 2 (spike-earned): live-wiring of chat view (S-A), structured approval delivery
  (S-B+S-C), launch-from-phone execution (R-POL.2-.6 live + R-PHC.10 wired), R-E2E.7,
  R-GATE.4. The builders/validators for these ship in Phase 1; only live execution is Phase 2.
- Phase 3 (deferred): observer-attach fan-out epic, activity depth, voice, Live Activities,
  optional tsnet direct path. R-GATE.5.

Critical path (build first): the unforgeable remote-origin boundary (D.0-A1: dedicated remote-tier
socket + device command-signing key) — without it R-POL and R-KS are unenforceable. Then crypto
identity (R-CRY.1-.6) and the daemon journal (R-JRN), which everything else consumes.

---

## D.0 Audit-003 amendments (AUTHORITATIVE — supersede the referenced requirement text below)

The coverage audit (`docs/verification/audit-003-remote-control-plan.md`, verdict REVISE) found the
original requirement text contradicted the source or itself in the items below. These amendments are
authoritative: where an amendment and the original requirement text conflict, the amendment wins. New
requirement IDs (R-*.16+ etc.) are added here and counted in section F. Read this section together with
every requirement it references.

### A1 (CRITICAL, 3-way convergent) — Remote-origin needs an unforgeable basis, not a self-declared capability
Supersedes/extends R-GW.2, R-POL.1, R-POL.6, R-PROT.1/.2, R-KS.1. Two layers:
- Origin by construction: the daemon serves gateway connections on a DEDICATED remote-tier listener
  (separate UDS path + 0600, e.g. `<stateDir>/remote.sock`), distinct from the owner-trusted main UDS.
  Connections on it are UNCONDITIONALLY remote-origin; the gateway dials only this socket; the daemon
  never infers trust from a hello capability. The main UDS stays owner-full-trust. (New: **R-GW.8**.)
  Verify: `TestDaemon_RemoteSocketAlwaysRemoteTier`, `TestDaemon_CompromisedGatewayCannotReachMainUDS`
  (a client offering no caps on the remote socket is still remote-tier; the gateway process cannot open
  the main UDS under its supervision model).
- Per-command device authorization the daemon verifies independently of the gateway: a device
  COMMAND-SIGNING keypair (Ed25519) minted at pairing, its public key pinned in the daemon device
  registry (R-DEV.1). Every remote mutating op carries a detached signature over the canonical tuple
  `(action, machine=endpoint id, session, operation_id, expires_at, content_hash?)`; the daemon verifies
  it against the registered device key and the device's capability grant BEFORE executing — a compromised
  gateway can neither forge a device's authority nor escalate a read-only device. (New: **R-CRY.16**
  device signing keypair via KeyStore; **R-POL.9** daemon-side signature+capability verification.)
  Verify: `TestPolicy_ForgedDeviceSignatureRejected`, `TestPolicy_GatewayCannotEscalateReadOnlyDevice`,
  `TestPolicy_ReplayedSignatureRejected` (bound to operation_id + expires_at). This replaces R-PROT.2's
  self-reported `capability_assertion` with a signature; `device_id` remains but is never trusted alone.

### A2 (CRITICAL) — Peek supersedes; snapshot is not "non-stealing + always-available"
Supersedes R-GW.6 and refines R-IOS.5. The existing attach path is close-old-then-open-new supersede
(server.go:388-394); a non-stealing snapshot silently fails under an active controller. Honest contract:
(1) when the phone is NOT controlling, it shows the LAST-KNOWN cached grid snapshot (may be stale,
labelled as such); (2) a LIVE grid requires take-control, which supersedes the current controller via the
existing S2 generation mechanism, UX-confirmed on the phone. Verify (replaces
`TestGateway_SnapshotDoesNotStealActiveController`): `TestGateway_PeekShowsLastKnownWhenNotControlling`
and `TestGateway_TakeControlSupersedesDesktop` (asserts the desktop IS superseded, matching design 7.3).

### A3 (CRITICAL) — Crash-safe exactly-once needs a two-phase intent record
Supersedes R-IDP.2/.3. Write a durable `prepared -> executing -> completed/failed` request record
(fsync'd) BEFORE the side effect, keyed by `operation_id`. On restart: a `prepared`/`executing` record
with no completion triggers per-op recovery — launch binds `operation_id` to the existing two-phase
session reservation (E5.4) so a re-run reuses the reserved session rather than spawning a second;
kill/interrupt bind to (target process identity, terminal state) so a completed kill is a no-op replay;
approval binds to a one-shot interaction state. Retention (R-IDP.4) MUST exceed the operation's
validity/retry horizon. Verify: `TestIdempotency_CrashBetweenExecuteAndCommitNoDoubleExecute` (kill -9
between side effect and record commit, restart, assert side-effect count == 1) per op.

### A4 (CRITICAL) — Input is live-only; it is NOT in the offline queue
Supersedes R-PHC.4, R-PHC.11, R-SIM.5, and clarifies R-IDP.1. Raw `input`/`resize` require a live
connection holding the CURRENT lease generation (TDataIn carries no request id and is never auto-retried,
server.go:604). On disconnect a queued keystroke resolves to an explicit "delivery unknown / not sent"
result surfaced to the user — it is never durably queued or replayed. Only high-level idempotent ops
(interrupt, kill, approve, launch) enter the offline queue (R-PHC.4) with operation_ids. Biometric: a
per-keystroke gate token is unusable; instead take-control opens a short bounded AUTHENTICATED CONTROL
SESSION (single biometric unlock, TTL + explicit end), and individual keystrokes ride that session; the
one-shot gate token (R-PHC.11) applies to discrete ops (interrupt/kill/approve/launch), not each key.
Verify: `TestInput_DisconnectYieldsDeliveryUnknown`, `TestControlSession_BoundedTTLThenReauth`.

### A5 (HIGH) — Async envelope: routing hint out of AAD; one seq coordinate; mailboxed grants
Supersedes R-CRY.9/.10/.12, R-GW.1/.5, reconciles the three cursors. (1) `recipient_key_id` is a routing
hint OUTSIDE the AEAD AAD — the ciphertext under the shared `K_epoch` is identical for every recipient;
the relay's per-device mailbox does the routing (R-REL.4). AAD = version..sender_key_id + epoch_id + seq.
(2) The async mailbox `seq` IS the durable daemon journal cursor (R-JRN.4) — one monotonic coordinate
system, so a gateway restart holds NO independent counter and cannot reset seq. `K_epoch` is persisted
machine-side 0600 (it is machine state, held by the gateway process which is the machine's crypto agent);
R-GW.1's "no state a restart loses" is corrected to "no state EXCEPT the persisted K_epoch, the current
epoch_id, and the relay-acked journal cursor". (3) EpochGrants are delivered via the relay MAILBOX
(sealed-box, async), not live-only, so a device offline during rotation receives its grant on reconnect
(R-CRY.10/R-DEV.5). (4) A detected seq gap triggers resync-from-snapshot (R-JRN.6 semantics extended to
async). (5) Defense-in-depth freshness: every async envelope carries an authenticated `issued_at`; the
phone rejects a decrypted mailbox event older than a bound (approvals additionally bound by the daemon's
authoritative expiry, A6). Verify: `TestEnvelope_IdenticalCiphertextAcrossRecipients`,
`TestGateway_RestartReusesJournalCursorAsSeq`, `TestEpochGrant_DeliveredViaMailboxToOfflineDevice`.

### A6 (CRITICAL) — Approval identity + expiry are wire-represented and daemon-enforced
Supersedes R-PROT.2, R-POL.8, R-PHC.9. Separate `operation_id` (idempotency identity of the remote
approve op) from `interaction_id` (identity of the agent interaction being approved). Add authoritative
`expires_at` (server clock) + `issued_at`; define a byte-exact content canonicalization + hash algorithm
(SHA-256 over a canonical JSON of the bound fields) for `content_hash`; add interaction supersession/
consumption state so a consumed or superseded interaction rejects a later approve. Expiry is evaluated at
the DAEMON against its own clock (phone countdowns are display-only). Phase split (see A9): the
agent-instance/session portion of the binding is Phase 1 (maps to the existing shimIdentityMatches
PID+start-time check, attach.go:37); content_hash + interaction_id capture is S-B/Phase-2-gated. Verify:
`TestApproval_DaemonEvaluatesExpiryOwnClock`, `TestApproval_ConsumedInteractionRejectsLaterApprove`.

### A7 (HIGH) — Journal hooks the choke point, not named callers; journal+meta commit is recoverable
Supersedes R-JRN.2/.3/.5. Hook the `saveMetaLocked` CHOKE POINT itself (daemon.go, the documented single
meta-writer — this covers SetStatus, finalizeTerminal, Launch, AND `reconcile.go`'s two startup saveMeta
calls that record daemon-restart Lost/Exited transitions), diffing `status.Derive(old)` vs
`status.Derive(new)` before every commit; plus a SEPARATE hook in `Delete`'s tombstone path
(lifecycle.go, which bypasses saveMetaLocked). Do not enumerate callers by name — the list drifts.
Journal+meta consistency: the journal append is a WAL-style step in the same recoverable commit as the
meta write, so a crash cannot leave meta-without-journal or journal-without-meta; crash injection between
every meta/journal/delete step. The 1s flap DEBOUNCE moves OUT of the durable journal to the push/delivery
layer (a durable-journal timer adds crash ambiguity; the journal records every distinct net transition).
(Correction: the referenced liveness ticker is `monitorPoll = 100ms`, daemon.go:30, not 200ms; the hook
is the choke point regardless.) Verify: `TestJournal_ReconcileRestartTransitionRecorded`,
`TestJournal_MetaAndJournalConsistentAcrossCrash`.

### A8 (CRITICAL) — Relay account / routing / APNs-token / de-authorization lifecycle
New requirements (R-REL.2 assumed a registered key with no lifecycle). **R-REL.11** machine registration +
routing-id derivation and proof (routing id = HKDF of the relay-auth pubkey; the machine proves control by
the R-REL.2 challenge; a device registers post-pairing and is authorized only for its paired machine's
routes). **R-REL.12** APNs push-token registration/refresh/deletion op (r_token_register) — R-REL.5's
r_push_trigger has no token source without it. **R-REL.13** device de-authorization: revocation (R-DEV.2/.3)
MUST invalidate the device's relay-auth registration AND purge its mailbox relay-side (a revoked device
keeps neither connectivity nor a drainable pre-rotation mailbox), carried by a new r_device_revoke op.
Adversarial tests: route enumeration refused, cross-route mailbox access refused, stale APNs token handled,
revoked credential refused, duplicate authenticated connection resolved (takeover or refuse, pinned).
Verify: `TestRelay_DeviceAuthorizedOnlyForPairedRoutes`, `TestRelay_RevokedDeviceDeauthorizedAndPurged`,
`TestRelay_TokenRegisterRefreshDelete`, `TestRelay_DuplicateConnectionResolved`.

### A9 (HIGH) — Phase-1 gates must not depend on Phase-2 execution
Supersedes the Phase-1 tagging of R-ADV.6, R-SIM.9, R-POL.8, R-GATE.3. Split adversarial + sim tests:
Phase-1 = validator/builder + policy-enforcement + crash-during-idempotency-record (launch AUTHORIZATION
refusal, kill/interrupt crash-recovery, approval-BINDING rejection); Phase-2 = live launch EXECUTION and
approval DELIVERY crash tests. R-SIM.9's "in-policy launch succeeds" is Phase 1 only as
builder+policy-validation against a fixture daemon; live spawn-from-phone is Phase 2. A fake backend
accepting a launch is NOT proof of live daemon enforcement and does not satisfy R-GATE.3.

### A10 (HIGH) — NSE content-free wake key; session content stays biometric-gated
Supersedes R-CRY.13, R-PHC.5, R-NSE.1/.2. Resolve the R-CRY.13-vs-R-NSE.1 contradiction AND the
content-at-rest exposure: the NSE decrypts a content-free WAKE payload with an after-first-unlock,
app-group-readable WAKE key that decrypts ONLY "activity on machine X" (no session content). Mailbox
SESSION content is encrypted under a separate biometric-gated content key and is NOT NSE-readable, so a
once-unlocked stolen phone does not yield session history from the NSE-accessible key. The device
long-term/command-signing keys remain biometric-gated. Outer APNs payload stays generic (R-NSE.2).
Verify: `TestNSE_WakeKeyDecryptsNoSessionContent`, `TestContentKey_BiometricGatedNotNSEReadable`.

### A11 (HIGH) — Connection lifecycle, error taxonomy, migrations
New: **R-PHC.13** client reconnect backoff + jitter (gateway<->relay and phone<->gateway), keepalive/
half-open bounds, duplicate-connection resolution; verified by a relay-restart R-E2E scenario (all paired
clients reconnect without a thundering herd). **R-PROT.7** a stable machine-readable refusal-reason
taxonomy (policy / kill-switch / rate-limit / stale-approval / not-authorized / invalid-field / transient
vs permanent + state effect) that every R-POL/R-KS/R-IDP/R-REL refusal uses and the phone UI renders;
string-only errors cannot drive retry policy. **R-TDD.8** versioned migration + rollback/crash tests for
every durable artifact (identity, device registry, policy, journal, idempotency, relay DB, activity log),
plus partial-`remote init` / corrupted-key / supervisor-unit-upgrade recovery.

### A12 (HIGH) — Headless-machine pairing scope
New: **R-PAIR.9**. On a headless/SSH-only machine the camera-OOB and independent-desktop-confirm pillars
collapse into one in-band channel (RCE-via-phone for anyone with shell). Phase 1 REQUIRES local-display
pairing (a local operator at a physical console); headless pairing is refused with a clear message. A
headless flow using an out-of-band confirmation code the operator already holds is a named Phase-3
follow-up, not Phase 1. Verify: `TestPairing_RefusedWithoutLocalConsole` (or an explicit documented
scope-out with the operator-presence assumption stated).

### A13 (MAJOR/MINOR, batched)
- **Split the phone X25519 keys** (opus M1): a Noise-static key and a sealed-box-recipient key, both
  pinned at pairing, rather than one key reused across both protocols (R-CRY preface, R-CRY.4/.10) — a
  clean composable argument for the near-free cost of one extra key.
- **R-POL.3** (opus m3): the RESOLVED real path (symlinks fully resolved) is what is BOTH policy-checked
  AND handed to the shim — no check-on-resolved / use-on-original gap.
- **R-POL.5** (opus m2): remote launches ALLOWLIST `Options` (and audit `InitialPrompt`), not just drop
  `Env`; a non-allowlisted Options key is refused.
- **R-ADV.8** [amended: D.0-A13] (codex): assert EVERY class R-PHC.6 promises (C0/C1/DEL, bidi overrides, zero-width,
  separators), not only control bytes.
- **R-KS.4/.5** (opus m1, codex, agy): the local activity log is a plain append-only signed log whose
  signature defends only against out-of-band edits (the key is co-located); keep the modest claim,
  off-machine anchoring optional. Do not imply on-machine tamper-proofing.
- **R-REL.1** (sonnet m1): the relay envelope is a SEPARATE structure modeled on `internal/wire` framing,
  NOT an extension of the frozen client<->daemon `wire.Type` enum (GG-7-drift-checked).
- **R-PHC.7** (sonnet M4): the `go/types` structural test additionally REQUIRES the gate token as a
  non-optional parameter on every mutating exported method, so a forgotten gate is a compile error, not a
  review miss.
- **R-CLI.2 / QR** (sonnet M3): add DEFERRED-VERIFICATION for terminal QR-glyph SCANNABILITY (font/
  contrast/aspect vary across terminals); local proxy = render the QR to known terminal profiles + a
  documented manual scan check, since the payload KAT does not prove a scannable glyph grid.
- **Clock discipline** (opus m6): every TTL is pinned to ONE authoritative clock (rendezvous relay-side,
  idempotency + approval expiry daemon-side); phone-side countdowns are display-only.
- **A-vs-D** (sonnet C2): R-CRY/R-PAIR/R-REL assume Option A and are gated on R-GATE.1 confirming A before
  Phase-1 crypto code begins (ADR-007 precedes Phase 1 in the phase map); the transport module stays
  behind an interface so Option D can slot in (design section 4). If ADR-007 picks D, these areas are
  revisited before implementation, not mid-stream.
- **APNs blind-push seam** (opus m7): explicitly a CONSCIOUS deferral (moot for a personal-only build),
  recorded in ADR-007 open question 6, not an oversight.
- **UI naming** (codex, opus M6): the phone "kill switch" control uses unambiguous labels
  (remote-access ON = enabled, OFF = severed); avoid an inverted "on = disabled" reading in R-IOS.6.

### Delta re-audit refinements (audit-003b: codex + opus verified the D.0 fixes; both PASS coverage and green-light Phase 0). Four items are pinned in ADR-007; three propagate into requirement bodies before the Phase-1 crypto slice.

- **A1-R (threat-model scope, ADR-007 decision).** A `0600` socket does NOT isolate two processes running
  as the same owner uid, and the gateway MUST run as the owner (it holds the machine identity key R-CRY.1
  and reads the 0700 owner state dir), so SO_PEERCRED cannot distinguish a compromised gateway from the
  local TUI. Honest boundary, pinned in ADR-007: the cryptographic containment boundary is the UNTRUSTED
  RELAY and the SEMI-TRUSTED PHONE — device signatures (R-CRY.16/R-POL.9) mean a compromised relay cannot
  forge commands and only paired, unrevoked devices can issue them; the kill switch + revocation defend a
  stolen phone. A process compromised while running AS THE OWNER (gateway included) already holds the
  machine identity key and can act as the owner directly WITHOUT the daemon, so it is OUTSIDE the
  cryptographic boundary by construction — the same status as a compromised shell on a single-owner
  machine. Sidecar isolation (5.7) limits blast radius on daemon/PTY state (defense-in-depth), it is not
  a cryptographic barrier. ADR-007 EITHER pins a stronger isolation mechanism (dedicated service uid with
  its own key custody, or an OS sandbox/MAC profile denying the gateway the main-socket path) OR adopts
  this scoped threat model and downgrades `TestDaemon_CompromisedGatewayCannotReachMainUDS` to
  `TestDaemon_RemoteSocketRequiresDeviceSignature` (a compromised gateway cannot forge a device signature;
  local-tier bypass is out of scope on a single-owner box). No remote-class mutating op executes on ANY
  listener without a valid device signature.
- **A3-R (interrupt semantics).** SIGINT delivery is not verifiable from terminal state, so `interrupt`
  is AT-MOST-ONCE: its two-phase record resolves to `completed` or, after a crash mid-interrupt, to a
  terminal `outcome-unknown` state the phone surfaces (never a claimed exactly-once). `kill` (SIGKILL +
  terminal-state-verifiable via shimIdentityMatches) stays exactly-once-verifiable. `operation_id` is
  persisted inside the reservation's fsync (R-IDP.2, already corrected).
- **A4-R (signed take-control session).** A signed one-shot `take_control` op (device signature R-POL.9 +
  a single biometric gate token R-PHC.11) establishes a bounded authenticated control session (TTL +
  explicit end). Raw `input`/`resize` within that session require only the live session + current lease
  generation (server.go:604) — NOT a per-keystroke signature or token. Discrete ops
  (interrupt/kill/approve/launch) each carry their own signature + gate token. This reconciles A1
  (ops signed), A4 (input is session data, live-only, never queued), and A13 (gate token on discrete ops).
- **A5-R (EpochGrant anti-replay + debounce boundary).** EpochGrants are NOT in the session-event
  journal-seq stream; they are per-device rotation artifacts delivered via the relay mailbox with their
  own authenticated anti-replay coordinate `(epoch_id, grant_seq)` monotonic per device, so a device
  rejects a replayed/stale grant independently of the session-event seq (no journal record type needed for
  grants). And the A7 flap-debounce applies ONLY to push-wakes + coalesced snapshots (R-GW.4), NEVER to
  the never-drop durable journal mailbox (R-GW.5) — otherwise R-CRY.12's consecutive-seq gap detection
  would false-positive into spurious resyncs.
- **A14 (propagate the two-key crypto split — Phase-1 crypto-slice gate).** The A13 phone-key split MUST be
  written into the requirement bodies before crypto TDD: the R-CRY preface + R-CRY.4/.10 use TWO X25519
  keys (Noise-static `s`; sealed-box recipient); R-PAIR.3 handshake payloads carry BOTH; R-PAIR.7 pins
  BOTH; R-DEV.1 registry stores BOTH; R-CRY.10 seals the EpochGrant to the RECIPIENT key, not the Noise
  static. Neither key is TOFU.
- **A15 (two-key EpochGrant: wake vs content — Phase-1 crypto-slice gate).** A10's separation MUST be
  written into R-CRY.10/.11/.13: an EpochGrant delivers TWO independent keys per epoch — a WAKE key
  (after-first-unlock, app-group-readable by the NSE, decrypts ONLY content-free "activity on machine X"
  type-0x02 wake payloads) and a CONTENT key (biometric-gated, NOT NSE-readable and NOT derivable from the
  wake key, decrypts type-0x01 mailbox session content). Push wakes are wake-key-AEAD; mailbox session
  content is content-key-AEAD. A once-unlocked stolen phone yields only the wake key (no session content).
- **Bookkeeping (opus):** the audit-003 report's Disposition mentioned working IDs D.22/R-IDP.5/R-CRY.17;
  the plan implements the amendments in THIS section D.0 (R-IDP.2/.3 and R-CRY.13 refined in place +
  R-CRY.16 added). The audit-003 report is corrected to match.

---

## D.1 Crypto (R-CRY) — owner: security-architecture — Phase 1

Pinned crypto architecture (instantiates the pinned decisions; recorded in ADR-007):
- One X25519 identity key per party (machine at `swarm remote init`; phone device key), used
  BOTH as the Noise static `s` AND as the async sealed-box recipient key — pinning the Noise
  static authenticates the async recipient key. DH-only uses, curve-domain-separated (Noise
  name/prologue vs nacl sealed-box); no signature oracle.
- Separate Ed25519 relay-auth key (stdlib), used ONLY to authenticate to the relay; the relay
  never sees the X25519 identity key.
- Async = machine epoch content key: machine holds `(epoch_id uint32, K_epoch [32]byte)`;
  `K_epoch` is sealed (nacl anonymous sealed-box) to each active device's long-term X25519
  pubkey; mailbox/push payloads are AEAD-encrypted once under `K_epoch` and fanned out;
  revocation rotates to `(epoch_id+1, K_epoch')` and re-grants only to survivors.
- New deps (justified in ADR-007): `github.com/flynn/noise` (Noise XX+PSK, ChannelBinding for
  SAS), `golang.org/x/crypto` (nacl/box, curve25519, chacha20poly1305 XChaCha, hkdf),
  `crypto/ed25519` (stdlib), `go.etcd.io/bbolt` (relay store, pure-Go), `github.com/coder/websocket`.
- Packages: `internal/remote/crypto`, `internal/remote/pair`, `internal/remote/device`,
  `internal/remote/relay`, `cmd/swarm-relay`; reuse `internal/wire` framing.

- **R-CRY.1 Machine identity keypair gen/storage.** `swarm remote init` generates one X25519
  keypair via crypto/rand; private key persisted 0600 under state dir, never logged/printed/
  transmitted; macOS Keychain optional behind the same interface.
  Verify: `TestIdentity_KeyfilePerms0600`, `TestIdentity_NoPrivateKeyInLogs`; restart reloads
  same pubkey. Deps: `internal/remote/crypto`; gateway owns the loaded key.
- **R-CRY.2 Device key via KeyStore interface (file impl now).** Phone-core reaches its X25519
  device key through a `KeyStore` interface exposing DH / sealed-box open / Noise-static handle
  but NOT raw private-key export; a file-backed 0600 impl exists now; a hardware-gated impl
  drops in with identical wire output. Verify: `TestKeyStore_NoPrivateExport`,
  `TestKeyStore_FileImplConformance` (KAT). Deps: phone-client (R-CRY.15).
- **R-CRY.3 Separate relay-auth keypair.** Each machine and device holds an Ed25519 relay-auth
  keypair distinct from identity; only the relay-auth PUBLIC key + derived opaque routing id is
  disclosed to the relay. Verify: `TestRelayAuth_DistinctFromIdentity`,
  `TestRelayAuth_IdentityNeverOnWire`. Deps: R-REL.2.
- **R-CRY.4 Noise XX live suite.** Live phone<->gateway uses `Noise_XX_25519_ChaChaPoly_SHA256`
  via flynn/noise, static `s` = X25519 identity (SHA-256 for Swift/CryptoKit interop).
  Verify: `TestNoiseXX_HandshakeCompletes`, `TestNoiseXX_SuiteExact`, `TestNoiseXX_KnownAnswer`.
  Deps: R-CRY.5/.6.
- **R-CRY.5 Prologue binds protocol/role/route.** Both sides set an identical prologue: live =
  ASCII `swarm-remote/1 live` + machine routing id + device routing id; pairing =
  `swarm-remote/1 pair` + 16-byte rendezvous id; any byte mismatch aborts at the first MAC
  (downgrade protection). Verify: `TestNoise_PrologueMismatchAborts`, `TestNoise_RouteBindingAborts`.
- **R-CRY.6 Static-key pinning on live handshake.** After each live XX handshake each side
  compares peer static to the value pinned at pairing and aborts before any transport byte if
  different (XX -> authenticated, not TOFU). Verify: `TestLive_PinnedStaticMitmRejected`,
  `TestLive_UnknownPeerRejected`. Deps: R-CRY.4, R-PAIR.7, R-DEV.1.
- **R-CRY.7 In-session replay/reorder + rekey.** Rely on Noise per-CipherState monotonic nonce
  (replayed/reordered transport frame fails AEAD); never manually assign nonces; Rekey() each
  direction after 15 min or 1 GiB. Verify: `TestTransport_ReplayFrameRejected`,
  `TestTransport_ReorderRejected`, `TestTransport_RekeyThreshold`.
- **R-CRY.8 No 0-RTT; fresh handshake per connection.** Every connection (incl. every reconnect)
  does a full fresh XX with new ephemerals (forward secrecy at each boundary); no resumption
  secret persisted. Verify: `TestTransport_FreshEphemeralsPerConn`, `TestTransport_NoResumptionState`.
- **R-CRY.9 Async envelope wire format (byte-exact).** [amended: D.0-A5] Header big-endian: `version:u8=0x01 |
  type:u8 (0x01 mailbox / 0x02 push) | epoch_id:u32 | seq:u64 | recipient_key_id:8 | sender_key_id:8
  | nonce:24 | ciphertext:N` (XChaCha20-Poly1305, incl 16-byte tag); key_ids = first 8 bytes
  SHA-256 of the respective pubkey; unknown version rejected. Verify: `TestEnvelope_RoundTripKAT`,
  `TestEnvelope_TruncatedRejected`, `TestEnvelope_UnknownVersionRejected`. Deps: R-CRY.10/.11/.12.
- **R-CRY.10 Epoch content key + EpochGrant.** Machine seals `K_epoch` to each active device via
  `box.SealAnonymous` (libsodium crypto_box_seal-compatible), delivered over authenticated Noise
  as first post-handshake message, carrying `epoch_id`; a device never gets a grant for an epoch
  it was not active in. Verify: `TestEpochGrant_SealOpenRoundTrip`, `TestEpochGrant_LibsodiumKAT`,
  `TestEpochGrant_WrongKeyFails`. Deps: R-CRY.2, R-PAIR.7, R-DEV.4/.5.
- **R-CRY.11 Async AEAD, AAD binding, nonce rules.** [amended: D.0-A5 — AAD EXCLUDES `recipient_key_id`
  (and the AEAD nonce), so the ciphertext is identical for every recipient.] XChaCha20-Poly1305 under
  `K_epoch`, 24-byte fresh crypto/rand nonce per envelope; AAD = `version || type || epoch_id || seq ||
  sender_key_id` (the header EXCEPT `recipient_key_id`, which is a routing hint outside the AEAD) so any
  header tamper fails the tag while recipient routing stays fan-out-identical. XChaCha mandated (K_epoch
  reused across events; 96-bit random would risk collision). Verify: `TestEnvelope_TamperRejected`,
  `TestEnvelope_NonceUniqueAndXChaCha`, `TestEnvelope_IdenticalCiphertextAcrossRecipients`.
- **R-CRY.12 Authenticated per-epoch sequence numbers.** Each mailbox event carries strictly
  increasing `seq` per `(machine, epoch_id)`, authenticated as AAD; device tracks highest seq per
  `(sender_key_id, epoch_id)`, rejects dup/lower, surfaces gaps — end-to-end relay reorder/dup/
  forgery detection. Verify: `TestMailbox_ReplaySeqRejected`, `TestMailbox_ReorderDetected`,
  `TestRelay_CannotForgeEvent`, `TestMailbox_GapSurfaced`. Deps: R-REL.4/.6.
- **R-CRY.13 Push envelope + NSE-reachable key.** [amended: D.0-A10] Push = type 0x02 envelope under `K_epoch` (no
  sensitive plaintext outer); `K_epoch` storable where a background NSE reads it after first
  unlock, while the device long-term private key + command-authoring keys stay biometric-gated;
  stale epoch -> graceful generic fallback. Verify: `TestPush_DecryptsWithEpochKeyOnly`,
  `TestPush_StaleEpochGenericFallback`. DEFERRED-VERIFICATION: real NSE keychain class (proxy:
  two-store fixture). Deps: R-CRY.10; phone-client NSE.
- **R-CRY.14 No hand-rolled primitives.** All primitives from flynn/noise, x/crypto, or stdlib;
  HKDF-SHA256 for any derivation; no bespoke cipher/KDF/MAC/curve. Verify:
  `TestDeps_NoHandRolledCrypto` (import allowlist) + ADR review.
- **R-CRY.15 Secure-Enclave-wraps-X25519 (interop-preserving).** Device key stays X25519 on the
  wire; hardware backing = biometric-gated Keychain item (or SE P-256 wrapping a stored X25519),
  NOT SE-native P-256 identity (SE cannot do X25519; changing the KEM would fork the wire).
  Verify: DEFERRED-VERIFICATION (proxy: file + mock-hardware KeyStore produce identical X25519 wire
  output under `TestKeyStore_FileImplConformance`). Deps: R-CRY.2; phone-client.

## D.2 Pairing (R-PAIR) — owner: security-architecture — Phase 1

- **R-PAIR.1 Single-use secret, never to relay.** 32-byte crypto/rand secret lives only in the QR
  (camera = OOB channel), never sent to the relay in any form, consumed on first completed
  handshake. Verify: `TestPairing_SecretNeverOnWire`, `TestPairing_SecretSingleUse`.
- **R-PAIR.2 QR payload format (byte-exact, <=200 bytes).** `swarm-pair:1:` + base64url of
  `version:u8=0x01 | flags:u8 | relay_url_len:u8 | relay_url:L | rendezvous_id:16 |
  pairing_secret:32 | machine_static_pub:32?`; rendezvous_id independent of secret (relay sees the
  former only). Verify: `TestQR_RoundTripKAT`, `TestQR_SizeBudget`, `TestQR_RendezvousIndependentOfSecret`.
- **R-PAIR.3 Noise XXpsk0 pairing handshake.** XXpsk0, suite as R-CRY.4, 32-byte secret as PSK,
  statics = identity keys, pairing prologue (R-CRY.5). msg2 payload {hostname, machine routing id,
  machine relay-auth pub, epoch_id}; msg3 {device name, device routing id, device relay-auth pub}.
  Verify: `TestPairing_XXpsk0Completes`, `TestPairing_PayloadFields`.
- **R-PAIR.4 SAS from handshake hash + fixed emoji table.** `okm =
  HKDF-SHA256(salt="swarm-remote/1 sas", ikm=ChannelBinding(), L>=3)`; first 3 bytes -> four 6-bit
  big-endian indices into a fixed 64-entry emoji table committed identically in Go and Swift; a
  tampered transcript yields different SAS on the two ends. Verify: `TestSAS_MatchOnCleanHandshake`,
  `TestSAS_MismatchOnTamper`, `TestSAS_KnownAnswer`. The canonical 64-entry table is pinned in
  ADR-007 (animals 0-23, fruit/plant 24-47, symbols/objects 48-63); emoji permitted as security-UI
  data only.
- **R-PAIR.5 Mandatory desktop confirm (fail-closed).** No static pinned / no EpochGrant sent
  until the machine TUI shows the SAS and the operator answers `Allow "<device>"? [y/N]`
  affirmatively; a no/absent/TTL aborts pinning nothing — the anti-photographed-QR gate. Verify:
  `TestPairing_PhotographedQRFailsAtConfirm`, `TestPairing_DeclineLeavesNoState`,
  `TestPairing_ConfirmTimeoutFailsClosed`. Deps: daemon/gateway TUI prompt.
- **R-PAIR.6 Rendezvous create/claim/burn, 60s TTL.** Relay rendezvous keyed by rendezvous_id:
  machine-created, at most two participants, opaque-byte forward only, hard 60s relay-side TTL,
  burned on first completion / TTL / cancel; claim on burned/expired/unknown rejected; relay never
  sees secret or plaintext. Verify: `TestRendezvous_TwoPartyForward`, `TestRendezvous_ThirdPartyRejected`,
  `TestRendezvous_ExpiredClaimRejected`, `TestRendezvous_BurnedAfterUse`. Deps: R-REL.1/.8.
- **R-PAIR.7 Outcome — mutual pinning + routing exchange.** On confirm both persist peer static as
  pinned identity + record peer routing id + relay-auth pub (from encrypted handshake payloads);
  device stores hostname + initial epoch_id; pinned identity is the authority for later live static
  verification. Verify: `TestPairing_PinsAndStoresRouting`, `TestPairing_ThenLiveHandshakeUsesPin`.
- **R-PAIR.8 Lifecycle + dual-side rate limits.** Pairing mode listens only while `swarm remote
  pair` runs, auto-exits on first completion/TTL, rate-limited both gateway- and relay-side; no
  standing listener between invocations. Verify: `TestPairing_NoStandingListener`,
  `TestPairing_RateLimitedBothSides`. Deps: R-PAIR.6, R-REL.8.

## D.3 Device lifecycle (R-DEV) — owner: security-architecture — Phase 1

- **R-DEV.1 Registry + per-device capability.** Durable registry per device: {device id, pinned
  static, relay-auth pub, routing id, name, capability policy (>= read+approve vs full), paired-at,
  granted epoch}; readable in TUI and (over E2EE) in app. Verify: `TestRegistry_Persist`,
  `TestRegistry_CapabilityEnforced`. Deps: R-PAIR.7; daemon/gateway enforces.
- **R-DEV.2 Local revocation (TUI, offline).** Machine TUI revokes any device without relay/phone;
  removes from registry + triggers epoch rotation. Verify: `TestRevoke_LocalRemovesAndRotates`.
  Deps: R-DEV.1/.4.
- **R-DEV.3 Remote revocation + biometric gate.** A paired device revokes another via
  relay-mediated E2EE-authenticated command; initiator passes an on-device biometric gate before
  authoring; relay cannot forge it. Verify: `TestRevoke_RemoteE2EE`, `TestRevoke_RelayCannotForgeRevocation`.
  DEFERRED-VERIFICATION biometric (proxy: stub gate, no command on deny). Deps: R-DEV.4/.6, R-REL.6.
- **R-DEV.4 Revocation -> epoch rotation.** Rotate `(epoch_id, K_epoch)` -> `(epoch_id+1, K_epoch')`
  (fresh crypto/rand); new EpochGrants only to survivors; SHOULD re-seal undelivered old-epoch
  mailbox items under the new epoch + purge old; revoked device never gets `K_epoch'`. Verify:
  `TestRevocation_NoPostEpochDecrypt`, `TestRotation_GrantsOnlySurvivors`, `TestRotation_ReSealsUndelivered`.
- **R-DEV.5 History-across-rotation.** Each device keeps a keyring `{epoch_id -> K_epoch}` and
  selects by envelope epoch_id; a device paired at N decrypts epochs >= N it holds, never < N (no
  grant); a revoked device retains only old epochs it already had. Verify: `TestKeyring_SelectsByEpoch`,
  `TestNewDevice_CannotReadPrePairingHistory`.
- **R-DEV.6 Biometric gate on mutating ops.** On-device biometric/passcode gate before authoring any
  mutating op (input/interrupt/kill/launch/approve/revoke); read ops ungated; phone-core interface
  with a stub now. Verify: `TestGate_BlocksMutatingOnDeny`, `TestGate_ReadOpsUngated`.
  DEFERRED-VERIFICATION real biometrics (proxy: stub). Deps: phone-client + daemon/gateway.

## D.4 Relay server (R-REL) — owner: security-architecture — Phase 1

- **R-REL.1 Wire protocol.** [amended: D.0-A13] WebSocket/TLS-443 via coder/websocket, one relay message per WS binary
  message, reusing `internal/wire` framing (4-byte BE length + 1-byte tag, MaxFrame 1 MiB) with a
  relay tag set; JSON control payloads, snake_case, unknown-field-tolerant, version/capability
  negotiated on `r_hello`. Types >= r_hello, r_relay, r_mailbox_append/read/ack, r_push_trigger,
  r_presence, r_ok, r_error. Verify: `TestRelay_FrameCapEnforced`, `TestRelay_MalformedControlSurvives`,
  `TestRelay_VersionCapabilityNegotiation`.
- **R-REL.2 Connection auth without learning identity.** [amended: D.0-A8] Ed25519 relay-auth signed-challenge
  (nonce||ctx) verified against the registered relay-auth pub bound to the claimed routing id; relay
  never requires/stores/learns any X25519 identity key, pairing secret, or plaintext. Verify:
  `TestRelayAuth_ChallengeResponse`, `TestRelayAuth_BadSignatureRefused`, `TestRelay_StoresNoIdentityKeys`.
  Deps: R-CRY.3, R-REL.7.
- **R-REL.3 Presence + machine-went-silent.** Per-routing-id presence via keepalive, exposed to the
  paired peer; a gateway drop (laptop sleep) transitions to offline within a bound and triggers the
  silent-push path (design 5.10); no long-term presence history retained. Verify:
  `TestPresence_TransitionsAndSilentPush`, `TestPresence_NoHistoryRetained`. Deps: R-REL.5/.10.
- **R-REL.4 Mailbox append/read-from-cursor/ack.** Per-device mailboxes of opaque envelopes; relay
  assigns its own monotonic STORAGE cursor (untrusted ordering) kept distinct from the authenticated
  `seq` (R-CRY.12) the device trusts; ack advances a durable consumed cursor for compaction. Verify:
  `TestMailbox_AppendReadAckCursor`, `TestMailbox_RelayReorderStillDetected`. Deps: R-CRY.12, R-REL.6/.7.
- **R-REL.5 Push trigger forwarding (ciphertext).** Gateway triggers device wake via `r_push_trigger`
  carrying an opaque push envelope; relay (holding APNs creds for a personal build) forwards to APNs
  with only a generic outer alert + ciphertext for the NSE; relay cannot read push content. Verify:
  `TestPush_OuterPayloadGeneric`, `TestPush_RelaySeesOnlyCiphertext`. DEFERRED-VERIFICATION live APNs
  (proxy: mock APNs sink). Deps: R-CRY.13, R-REL.10.
- **R-REL.6 Untrusted-relay invariants (forward-only).** Relay can only drop/delay; cannot read/forge/
  reorder undetectably (AEAD blocks forgery, seq blocks reorder/dup, static pinning blocks live MITM,
  PSK+confirm blocks pairing MITM). Verify: `TestRelay_CannotForgeEvent`, `TestMailbox_ReorderDetected`,
  `TestLive_PinnedStaticMitmRejected`, `TestPairing_MITMWithoutPSKFails`.
- **R-REL.7 Persistence store.** Embedded transactional store (go.etcd.io/bbolt default, no cgo):
  registry, per-device mailbox log (monotonic keys), cursors, quotas; holds only ciphertext + routing
  metadata, never plaintext or identity keys; survives restart. Verify: `TestRelayStore_SurvivesRestart`,
  `TestRelayStore_NoPlaintextAtRest`.
- **R-REL.8 Rate limits/quotas/abuse controls (day one).** Per-IP + global limits on rendezvous
  create/claim; concurrent-rendezvous cap; per-rendezvous byte/message caps during pairing; per-device
  mailbox append rate + stored-bytes quota; per-device push rate; per-routing-id conn rate + max
  concurrent; registry-size cap; each over-limit is a clean refusal, not exhaustion. Verify:
  `TestRelay_RendezvousFloodBounded`, `TestRelay_MailboxQuotaEnforced`, `TestRelay_PushRateLimited`,
  `TestRelay_ConnRateLimited`.
- **R-REL.9 Relay ops (lean).** `cmd/swarm-relay` reads one config file (listen, TLS mode, APNs creds
  path, bbolt path, quota knobs), runs non-root, ships a sample systemd unit; E2EE confidentiality does
  NOT depend on TLS (TLS = metadata defense only); localhost tests may use plain ws://. Verify:
  `TestRelay_BootsFromConfigLocalhost`, `TestRelay_E2EEOverPlainWS`. DEFERRED-VERIFICATION systemd/real-TLS/
  VPS (proxy: config parse + localhost boot).
- **R-REL.10 Metadata retention + log scrubbing.** No plaintext/ciphertext bodies in logs; routing
  ids/timestamps/sizes at debug only, production truncates; mailbox items purged after ack + retention
  cap (default 7 days); presence not persisted beyond R-REL.3; ADR-007 documents the accepted metadata
  exposure and withdraws any "leaks nothing" claim. Verify: `TestRelay_LogsNoBodies`,
  `TestRelay_RetentionPurge` + ADR review.

## D.5 Gateway sidecar (R-GW) — owner: daemon-integration — Phase 1

- **R-GW.1 Standalone supervised process, never a daemon child.** [amended: D.0-A5] `cmd/swarm-remote` runs under an
  external supervisor (macOS launchd LaunchAgent installed by `swarm remote init`; Linux systemd user
  unit), NOT spawned by the daemon; dials daemon UDS as an ordinary client; holds no state a restart
  loses except live connections; `kill -9` leaves daemon+sessions untouched (S1) and it resumes from
  its last durable journal cursor. Verify: `TestGateway_RestartResumesFromCursor` (new `internal/remotegw`).
- **R-GW.2 (CRITICAL PATH) Gateway reaches the daemon via the remote-tier socket.** [SUPERSEDED by
  D.0-A1 — read A1, not this body.] Origin is established by which socket a connection reached
  (dedicated remote-tier `remote.sock`, R-GW.8), NOT by a self-declared `remote-gateway` capability: a
  capability offer is negotiation, not authentication. Any `remote-gateway` capability that survives is a
  non-trust feature FLAG only (protocol-feature discovery), never the trust basis. Every remote mutating
  op additionally carries a daemon-verified device signature (R-CRY.16/R-POL.9). Verify (replaces
  `TestServer_RemoteGatewayCapMarksConn`): `TestDaemon_RemoteSocketAlwaysRemoteTier`,
  `TestPolicy_ForgedDeviceSignatureRejected`. Deps: R-GW.8, R-CRY.16, R-POL.9.
- **R-GW.3 Translate protocol <-> wire product.** daemon event/journal -> `session_state` + journal
  events; on-demand attach -> `grid_snapshot` (forwarded exactly from `vt.RenderSnapshot`, never raw
  PTY bytes, G-2); phone input/interrupt/kill/launch/approve -> daemon ops; transcript_delta +
  interaction_request out of scope. Verify: `TestGateway_SnapshotPassThroughIsSanitized` (reuse
  `internal/vt/render_audit_test.go` corpus).
- **R-GW.4 Snapshot backpressure — coalesce/drop, latest-wins.** On slow relay, drop stale
  grid_snapshot/session_state (keep latest per session), mirroring the shim bounded-queue discipline;
  export a drop counter. Verify: `TestGateway_SlowRelayDropsSnapshots`.
- **R-GW.5 Journal backpressure — never drop, gate cursor on relay ack.** Never drop journal events;
  do not advance the relay-acked cursor past relay acks; on outage stop advancing + re-read from last
  acked cursor on reconnect (at-least-once + daemon dedupe = exactly-once). Verify:
  `TestGateway_RelayOutageNoJournalLoss`. Deps: R-PROT.3, R-JRN.
- **R-GW.6 Peek: last-known cached snapshot; live grid requires take-control.** [SUPERSEDED by D.0-A2 —
  read A2, not this body.] When NOT controlling, the phone shows the last-known cached grid snapshot
  (labelled possibly-stale); a LIVE grid requires take-control, which supersedes the current controller
  via the existing S2 generation mechanism (close-old-then-open-new, server.go:388-394), UX-confirmed.
  A non-stealing snapshot under an active controller would only "succeed" by silently timing out
  (serve.go:280-292) — so that path is NOT relied upon. NO observer/multi-subscriber fan-out (Phase 3).
  Verify (replaces `TestGateway_SnapshotDoesNotStealActiveController`):
  `TestGateway_PeekShowsLastKnownWhenNotControlling`, `TestGateway_TakeControlSupersedesDesktop`.
- **R-GW.7 Remote input rides the existing lease + supersede.** Phone input/resize flows through the
  existing controller-lease path under generation-supersede (S2); gateway holds the lease while phone
  is in take-control; concurrent desktop attach supersedes phone and vice-versa, unchanged. Verify:
  `TestLease_RemoteControllerSupersededByDesktop` + reverse.

## D.6 Journal (R-JRN) — owner: daemon-integration — Phase 1

- **R-JRN.1 Daemon-owned append-only versioned records.** Fields: `schema_version`, monotonic `cursor`
  (uint64), `ts`, `session_id`, `type` (group_transition/launched/exited/lost/deleted/presence), typed
  `payload`; JSON one-per-line, `persist.Meta`-style discipline; future schema rejected loudly, older
  migrated forward; truncated final line tolerated. Verify: `TestJournal_SchemaRoundTripAndReject`,
  `TestJournal_TruncatedTailTolerated` (new `internal/journal`).
- **R-JRN.2 Written at the single meta-writer choke point (real seam).** [amended: D.0-A7] Records appended inside
  `saveMetaLocked`/`SetStatus`/`finalizeTerminal` and launch reservation/Delete — NOT the 200ms poller;
  a group_transition emitted when `status.Derive(old) != status.Derive(new)`; exactly one ordered record
  per persisted change under `writeMu`; Group derived with `status.Derive`, never on the phone. Verify:
  `TestDaemon_StatusChangeAppendsGroupTransition`, `TestDaemon_LifecycleAppendsRecords`.
- **R-JRN.3 Group-transition debounce.** [amended: D.0-A7] Flapping within a window (default 1s, configurable) collapses to
  the net transition; never drops a distinct net transition; never delays a terminal lifecycle record.
  Verify: `TestJournal_DebounceCollapsesFlap` (mock clock).
- **R-JRN.4 Cursor + atomic resume contract.** `journal_read(from_cursor)` returns the current roster
  snapshot + all records `cursor > from_cursor` captured under one lock (no event straddles the boundary);
  `from_cursor=0` = full history; cursors monotonic for the journal lifetime, never reused. Verify:
  `TestJournal_ResumeBoundaryAtomic`, `TestJournal_CursorMonotonicAcrossRestart`.
- **R-JRN.5 Append durability (fsync) surviving crash/upgrade (D-5).** Append fsync'd before its cursor
  is acked to any reader; ADR-003 tier (process-crash safe, power-loss out of scope), matching
  `persist.Save`. Verify: `TestJournal_CrashAfterAckSurvives` (subprocess daemon, kill -9), `-race`.
- **R-JRN.6 Retention/compaction.** Bounded via transcript-style rotation (MaxBytes/MaxFiles); a
  `journal_read` below the floor returns a full-snapshot resync signal, never a silent omission. Verify:
  `TestJournal_CompactionBoundsDisk`, `TestJournal_StaleCursorReturnsFullResync`.
- **R-JRN.7 Placement, presence, deleted-session lifecycle.** ONE daemon-wide journal under
  `<stateDir>/journal/` (each record carries `session_id`) — a single merged inbox needs one cursor, and
  `deleted`/`presence` must outlive a session dir removed by `Store.Delete`; gateway connect/disconnect
  appends `presence` (daemon-side liveness proxy; true online/asleep/offline is relay-derived). Verify:
  `TestJournal_DeletedRecordOutlivesSessionDir`, `TestDaemon_GatewayPresenceRecorded`.

## D.7 Idempotency (R-IDP) — owner: daemon-integration — Phase 1

- **R-IDP.1 request_id on every remote mutating op.** [amended: D.0-A4] interrupt/kill/launch/approve carry a durable
  `request_id` (additive Control field); `input` exempt (raw frames never auto-retried); a remote
  mutating op lacking one is refused. Verify: `TestServer_RemoteMutatingOpRequiresRequestID`.
- **R-IDP.2 Two-phase durable record surviving restart.** [SUPERSEDED by D.0-A3 — a cached outcome
  written AFTER execution leaves the execute -> crash -> re-execute window.] A durable
  `prepared -> executing -> completed/failed` record keyed by `operation_id` is fsync'd BEFORE the side
  effect; for launch the `operation_id` is persisted AS PART OF the two-phase session reservation
  (launch.go phaseReserved, same fsync — not a separate post-side-effect file), so a crash between spawn
  and commit is resolved by reconcile against the reserved id rather than re-spawning. Key =
  `<device_id>:<client-ULID>` (opaque, <=128 bytes). Verify:
  `TestIdempotency_CrashBetweenExecuteAndCommitNoDoubleExecute`, `TestIdempotency_OutcomeSurvivesRestart`.
- **R-IDP.3 Replay returns cached outcome, executes nothing.** A duplicate request_id returns the cached
  outcome with no second side effect. Verify: `TestIdempotency_ReplayLaunchNoSecondSession`,
  `TestIdempotency_ReplayKillNoSecondSignal` (side-effect count == 1).
- **R-IDP.4 Retention (TTL).** Bounded by TTL (default 24h) and/or max-entries; expiry never resurrects
  double-execute within a realistic reconnect window. Verify: `TestIdempotency_TTLCompaction` (mock clock).

## D.8 Policy / launch authority (R-POL) — owner: daemon-integration — enforcement Phase 1, launch execution Phase 2

- **R-POL.1 Remote-origin enforcement tier.** [amended: D.0-A1] Any remote-origin connection (R-GW.2) is a lower-trust tier;
  the daemon applies R-POL.2-.6 to its launch/kill/delete/interrupt regardless of what the gateway claims
  (daemon enforces independently, design 5.6); local connections keep today's behavior. Verify:
  `TestPolicy_RemoteLaunchChecked_LocalLaunchNot`.
- **R-POL.2 Authorization BEFORE argv validation.** For a remote launch, evaluate authz (kill-switch?
  cwd in an allowed root? device capability?) before any argv composition, cwd stat, or side effect;
  errors distinguish "not authorized" from "invalid field". Verify: `TestPolicy_AuthzPrecedesArgvValidation`.
- **R-POL.3 Allowed-cwd roots, machine-configured.** [amended: D.0-A13] Remote launches confined to configured roots;
  resolved symlink-hardened cwd outside a root refused; empty roots = no remote launch (fail-closed).
  Verify: `TestPolicy_LaunchOutsideAllowedRootsRefused`, `TestPolicy_SymlinkEscapeRefused`.
- **R-POL.4 Hard-coded refusal of dangerous options from remote.** `dangerously-skip-permissions` and
  full-access/full-access-sandbox refused from remote, hard-coded (not config-overridable), via an
  `Options` denylist; local unaffected. Verify: `TestPolicy_RemoteForbiddenOptionsRefused`.
- **R-POL.5 No phone-supplied env; worktree default (fixes ADR-006 billing-env).** [amended: D.0-A13] For a remote launch,
  ignore `LaunchReq.Env` entirely (source env solely from daemon policy); default worktree isolation
  unless policy opts out. Verify: `TestPolicy_RemoteLaunchDropsClientEnv`, `TestPolicy_RemoteLaunchDefaultsWorktree`.
- **R-POL.6 Per-device capability policy.** [amended: D.0-A1] Enforce read-only / read+approve / full daemon-side via
  `device_id` + capability assertion (or signed pairing token); a read+approve device's input/launch is
  refused, its approve/reads succeed; a read-only device's approve is refused. Verify:
  `TestPolicy_ReadApproveDeviceCannotLaunch`, `TestPolicy_ReadOnlyDeviceCannotApprove`.
- **R-POL.7 Policy config file + reload.** `<stateDir>/remote-policy.json` (0600, versioned); loaded on
  start; malformed file fails closed to prior/empty-allowed config, never fail-open; hot-reload if offered
  is atomic. Verify: `TestPolicy_MalformedConfigFailsClosed`, `TestPolicy_ConfigRoundTrip`.
- **R-POL.8 Approval-binding VALIDATION layer (Phase 1, delivery spike-gated).** [amended: D.0-A6/A9] An `approve` references
  an immutable tuple `(machine=endpoint id, session=namespaced id, agent-instance=(session_id, ShimPID,
  ShimStartTime), request-id, content-hash)` and is REJECTED if stale/mismatched; never translated to blind
  keystrokes; delivery/one-tap UX is S-C-gated (Phase 2), the reject-stale check is Phase 1. Verify:
  `TestApproval_StaleAgentInstanceRejected`, `TestApproval_ContentHashMismatchRejected`,
  `TestApproval_ValidTupleAccepted`.

## D.9 Kill switch + activity log (R-KS) — owner: daemon-integration — Phase 1

- **R-KS.1 Persisted kill-switch, independent enforcement.** [amended: D.0-A1] Durable enabled/disabled flag
  (`<stateDir>/remote-state.json`, 0600); when disabled the daemon refuses EVERY remote-origin op at the
  daemon boundary (even if the gateway is alive/compromised), needing neither phone nor relay; local TUI
  ops keep working; survives restart. Verify: `TestKillSwitch_OffRefusesRemoteOps`, `TestKillSwitch_StateSurvivesRestart`.
- **R-KS.2 Auto-off at zero paired devices.** Revoking the last device flips the switch off atomically
  (device loss without revocation = RCE). Verify: `TestKillSwitch_AutoOffOnZeroDevices`.
- **R-KS.3 `swarm remote off` severs the gateway.** Sets the daemon kill-switch AND stops the gateway via
  the supervisor; independent defenses (daemon refuses remote ops even if the sever fails). Verify:
  `TestRemoteOff_SetsSwitchAndStopsGateway`.
- **R-KS.4 Signed hash-chained activity log.** [amended: D.0-A13] Every remote-originated mutation appends a signed record
  (`<stateDir>/activity/`, 0600) {device_id, action, session_id, request_id, outcome, ts, sig chained to
  prev hash}; Ed25519 machine-local key generated at init, 0600, never leaves; distinct from journal +
  idempotency. Verify: `TestActivityLog_AppendSignedChained`, `TestActivityLog_TamperBreaksChain`.
- **R-KS.5 Activity-log verify command.** `swarm remote log [--verify]` prints the log and validates the
  signature chain offline, naming the first broken link, nonzero exit on failure. Verify:
  `TestRemoteLogVerify_DetectsTamper`.

## D.10 CLI verbs (R-CLI) — owner: daemon-integration — Phase 1

- **R-CLI.1 `swarm remote init`.** Generates machine identity keypair + activity-log signing key,
  installs the gateway supervisor unit, leaves remote `off` until a device pairs; idempotent (re-run does
  not rotate identity). Verify: `TestRemoteInit_CreatesKeysAndUnitIdempotent`.
- **R-CLI.2 `swarm remote pair`.** Prints a single-use QR (60s TTL), drives the relay-mediated handshake,
  displays the 4-emoji SAS, prompts `Allow "<device>"? [y/N]` (inline + TUI if active); records the pairing
  outcome; first device allows the switch to move `on`. Verify: `TestRemotePair_ConfirmRegistersDevice`,
  `TestRemotePair_RejectFailsClosed`.
- **R-CLI.3 `swarm remote devices`.** Lists paired devices (name, short id, capability, paired-at,
  last-seen) as a stable scriptable table; empty = clear line, exit 0. Verify: `TestRemoteDevices_ListsRegistry`.
- **R-CLI.4 `swarm remote revoke <device>`.** Removes a device + rotates the epoch key; writes an activity
  record; revoking the last device flips the switch off. Verify: `TestRemoteRevoke_RemovesAndMaybeAutoOff`.
- **R-CLI.5 `swarm remote off` / `on`.** `off` = R-KS.3; `on` re-enables only if >=1 device paired (else
  errors, pointing to pair) and (re)starts the gateway. Verify: `TestRemoteOnOff_Transitions`.
- **R-CLI.6 `swarm remote status`.** Read-only: kill-switch state, gateway up/down, relay reachable,
  paired-device count, machine fingerprint, last activity cursor. Verify: `TestRemoteStatus_ReportsState`.

## D.11 TUI surfaces (R-TUI) — owner: daemon-integration — Phase 1

- **R-TUI.1 Pairing-confirm prompt with SAS.** A pending pairing surfaces a confirm prompt showing device
  name + 4-emoji SAS, `Allow "<device>"? [y/N]`, reusing the existing confirm sub-state; `y`/`n` routes back
  to the daemon (R-PROT.5); fails closed on quit/timeout; cannot be dismissed into an accidental accept.
  Verify: `TestTUI_PairingConfirmPromptFlow`.
- **R-TUI.2 Paired-devices pane.** New screen listing devices (capability, last-seen) with a revoke action
  behind a confirm gate (triggers auto-off if last device). Verify: `TestTUI_DevicesPaneListsAndRevokes`.
- **R-TUI.3 Remote indicator on the status bar.** Compact `off` / `on (N devices)` / `on - paused` in the
  persistent bottom bar via `composeBoard`, updating live off the subscribe stream; never wraps the
  fixed-height bar. Verify: `TestTUI_RemoteIndicatorReflectsState`.
- **R-TUI.4 `Client` interface extension.** Add minimal remote methods (`RemoteStatus`, `Devices`,
  `Revoke`, `ConfirmPairing`, remote-state event) keeping every method stub-friendly (E7.7); existing
  `tui.New` path + tests stay green. Verify: R-TUI.1-.3 all run against the fake; `go test ./internal/tui/...`.

## D.12 Protocol extensions (R-PROT) — owner: daemon-integration — .1-.5 Phase 1, .6 Phase 0

- **R-PROT.1 New negotiated capabilities.** Add `remote-gateway`, `journal`, `activity`, `policy`,
  `pairing` to the hello intersection (F-1); an un-negotiated op is refused with `error`, never actioned;
  existing attach/subscribe clients unaffected. Verify: `TestHello_NegotiatesRemoteCaps`,
  `TestServer_UnnegotiatedOpRefused`.
- **R-PROT.2 Additive `Control` fields (omitempty).** [SUPERSEDED by D.0-A1/A6 — `capability_assertion`
  is NOT a trust field; replaced by a device signature.] Additive omitempty fields: `operation_id` and
  `interaction_id` (separated per A6), `device_id`, a detached `device_sig` (Ed25519 over the canonical
  op tuple, verified by R-POL.9 — replaces the self-reported `capability_assertion`), `cursor`,
  `issued_at`/`expires_at` (daemon-authoritative), and an `approve` sub-struct binding
  `(session, agent_instance{shim_pid, shim_start_time}, interaction_id, content_hash, expires_at)` with
  a byte-exact content canonicalization (A6). Existing messages serialize byte-identically; `protocol.md`
  amended in lockstep (GG-7). Verify: `TestControl_AdditiveFieldsOmitEmpty`,
  `TestPolicy_ReplayedSignatureRejected` + existing codec/drift tests green.
- **R-PROT.3 Journal ops.** `journal_subscribe` (stream from cursor), `journal_read` (snapshot+range,
  atomic per R-JRN.4), `journal_event` (daemon->gateway push); fan-out reuses the bounded-queue
  evict-the-wedged-subscriber discipline (S9/L1). Verify: `TestProtocol_JournalSubscribeOrderedAndEvictsWedged`,
  `TestProtocol_JournalReadFromCursor`.
- **R-PROT.4 Activity + policy/device ops.** `activity_append` (or folded into mutating handlers),
  `policy_query`, `device_list`/`device_revoke`; each capability-gated; device_revoke triggers auto-off when
  it empties. Verify: `TestProtocol_PolicyQuery`, `TestProtocol_DeviceRevokeAutoOff`.
- **R-PROT.5 Pairing-confirm event + response.** `pair_pending` (daemon->client: device name + SAS) and
  `pair_confirm` (client->daemon: allow/deny), failing closed on disconnect/timeout; only explicit local
  `allow=true` completes pairing + burns the secret. Verify: `TestProtocol_PairPendingThenConfirm`,
  `TestProtocol_PairFailsClosedOnDisconnect`.
- **R-PROT.6 Spec-amendment governance (Phase 0).** All R-PROT changes documented in `protocol.md`
  (GG-7 drift-checked) and recorded in ADR-007; `system-spec.md` gains the remote-origin trust tier +
  journal/idempotency/kill-switch/activity artifacts + new invariants; no silent drift. Verify: GG-7 drift
  check passes; ADR-007 present, cross-linked from `docs/INDEX.md` and `docs/adr/README.md`.

## D.13 Phone core (R-PHC) — owner: phone-client — Phase 1

- **R-PHC.1 Pairing state machine.** States {Unpaired, ScanningQR, AwaitingSAS, AwaitingDesktopConfirm,
  Paired, PairingFailed(reason)}; drives Noise XX (32-byte secret as PSK); no "paired" transition until
  desktop confirm observed; SAS exposed before confirm; secret single-use in-process; 60s TTL ->
  PairingFailed(timeout) via fake clock; machine static pinned on success, never silently repinned.
  Verify: pairing-package go test (fake transcript/mailbox/clock) + phonesim `pair`.
- **R-PHC.2 Machine registry + presence.** Keyed by endpoint id; two independent dimensions: phone link
  state {Connected/Degraded/Offline} and machine presence {Online/Asleep/Offline}; Asleep != Offline (N-7);
  survives restart; no retry storm on Asleep; revoke() discards pinned material immediately. Verify: go
  test (fake relay) + phonesim `machine-asleep`/`revocation`.
- **R-PHC.3 Session cache + cursor resume.** Cache SessionView merged across machines keyed by namespaced
  id; Group verbatim from wire (never derived); cursor advances only after durable local apply; a "cursor
  too old" response surfaced as "history gap", not dropped. Verify: go test (crash/restart fake journal) +
  phonesim `receive-events`.
- **R-PHC.4 Offline op queue with idempotency keys.** [amended: D.0-A4] Queue input/interrupt/kill/approve/launch with a
  durable client-generated idempotency key made at creation, replayed in order on reconnect, never
  regenerated; superseded-generation op surfaces a distinct failure; bounded FIFO per session with defined
  overflow. Verify: go test (echo daemon stub) + phonesim `send-input` offline gap.
- **R-PHC.5 Decrypt + auth pipeline (single choke point).** [SUPERSEDED by D.0-A5/A10 — the crypto layering
  is: Noise XX live; K_epoch-AEAD for mailbox events + push; sealed-box ONLY for EpochGrant delivery.]
  One decrypt-and-verify point handles: Noise XX for the live stream; K_epoch-AEAD (XChaCha20-Poly1305,
  R-CRY.11) for offline mailbox events; the content-free WAKE key for push (A10); and sealed-box open
  ONLY for an EpochGrant (which carries a new K_epoch/content-key). Reject (not log-and-continue) any auth
  failure before cache/UI; unpinned-machine traffic rejected; rotated-out epoch rejected. Verify: KAT +
  negative tests (wrong key, rotated epoch, truncated, replayed nonce).
- **R-PHC.6 Snapshot renderer + sanitizer conformance.** Decode wire `vt.Snap/Line/Run` (version-checked
  vs `vt.SnapshotVersion`) into a cell grid; re-sanitize every `Run.Text`/`Snap.Title` on-device with the
  same blocked classes as `internal/vt` (C0/C1/DEL, bidi overrides, zero-width, separators) before any text
  reaches a view (re-enters N-6, G-2); Width==2 spacer modeled; clamps (never panics) on malformed. Verify:
  sanitizer conformance suite (same adversarial fixtures as internal/vt) + decode fuzz + phonesim
  `render-snapshot-to-text`.
- **R-PHC.7 gomobile API surface rules.** [amended: D.0-A13] No channels, no struct slices, no bound structs beyond opaque
  handles; events via single-method-per-class callbacks (`OnSessionsChanged(json []byte)`,
  `OnSnapshot(id string, json []byte)`, `OnConnectionState(id string, state int)`); calls take
  strings/ints/bools/byte-slices; every mutating call takes a phone-core-generated idempotency key. Verify:
  `go/types`-based structural test rejecting disallowed shapes; DEFERRED-VERIFICATION actual `gomobile bind`.
- **R-PHC.8 On-device persistence + encryption-at-rest.** Persist registry/cache/cursors/queued-ops/pinned
  keys across relaunch; device long-term private key in Secure Enclave (never exported to Go — a `KeyStore`
  interface with sign/decrypt callbacks); explicit "not stored" list (raw PTY bytes, payload plaintext);
  revoke/sign-out wipes all persisted state for that pairing. Verify: go test (fake KeyStore) + design
  review; DEFERRED-VERIFICATION real Secure Enclave.
- **R-PHC.9 Approval request-binding validation.** Validate the bound tuple (machine, session,
  agent-instance, request-id, content-hash) vs current state before Approve sends; resync-before-enable
  re-fetches authoritative state; NO approve-and-remember API exists; fixture interaction_request shape so
  tests survive the real wire type landing. Verify: go test (match/mismatch/expired/missing) + phonesim
  `approve`; DEFERRED-VERIFICATION live wire until S-B/S-C.
- **R-PHC.10 Remote-launch builder under 5.6.** Enforce 5.6 client-side as defense in depth: refuse cwd
  outside allowed roots, NO field for dangerous flags (structurally absent, not validated-away), never
  accept phone env; worktree default true; capability (R-PHC.12) gates app reachability. Builder exists +
  tested in Phase 1, live-wired Phase 2. Verify: go test (in/out-of-root; Env structurally absent) +
  phonesim `launch`.
- **R-PHC.11 Mutating-op gate token.** [amended: D.0-A4] Every mutating call takes a short-lived single-use gate token from
  a native biometric check (R-IOS.10); phone-core owns freshness/consumption (caller can't bypass); read
  ops take none; missing/expired/reused -> distinct "re-authenticate" error. Verify: go test
  (fresh/expired/reused) + phonesim negative paths.
- **R-PHC.12 Capability negotiation / feature flags.** Expose gateway/daemon-advertised capabilities
  (transcript_delta, interaction_request, launch-from-phone) as a per-machine flag set from the hello
  Capabilities field, re-evaluated every reconnect; absence fails closed (journal+snapshot / deep-link);
  settable as phonesim fixtures. Verify: go test (fake hello) + phonesim `multi-machine-merge`.

## D.14 phonesim harness (R-SIM) — owner: phone-client — Phase 1

- **R-SIM.1 Scenario DSL + architecture.** `cmd/phonesim` drives the real phone-core library (same
  exported API gomobile binds) against a fixture relay-daemon backend; named scenarios via a small DSL
  (pair / inject events / assert state / send op + assert idempotency / render snapshot to text);
  independent runs (`phonesim run <scenario>`) with pass/fail exit + trace; faults are first-class DSL steps.
  Verify: `go build`/`go test` on phonesim; `phonesim run <every scenario>` in CI.
- **R-SIM.2 pair** (R-PHC.1/.5): happy path, desktop-reject, TTL-timeout, photographed-QR (secret reused +
  confirm mismatch). `phonesim run pair`.
- **R-SIM.3 receive-events** (R-PHC.3/.5): mid-stream disconnect + reconnect-from-cursor + a process
  crash-restart proving cursor persistence; no dup/skip. `phonesim run receive-events`.
- **R-SIM.4 render-snapshot-to-text** (R-PHC.6): feed a fixture real `vt.Snap` incl. adversarial (control
  runes, bidi, oversized grapheme); golden diff for clean; explicit assertion dangerous runes neutralized.
  `phonesim run render-snapshot-to-text`.
- **R-SIM.5 send-input** [amended: D.0-A4] (R-PHC.4/.11): input under current lease gen incl. queue-while-busy +
  offline-reconnect; exactly-once via idempotency; stale-generation surfaced. `phonesim run send-input`.
- **R-SIM.6 approve** (R-PHC.9/.11): matching tuple succeeds; stale/expired/mismatch refused locally;
  resync-before-enable proven; no approve-and-remember path. `phonesim run approve`.
- **R-SIM.7 interrupt** (R-PHC.4/.11): Group transition correct; gate/idempotency apply; offline-reconnect +
  desktop-race. `phonesim run interrupt`.
- **R-SIM.8 kill** (R-PHC.4/.11): confirm-step modeled; terminal state; op against dead session rejected
  cleanly; kill-while-desktop-take-control. `phonesim run kill`.
- **R-SIM.9 launch** [amended: D.0-A9] (R-PHC.10/.12): in-policy succeeds; out-of-root refused pre-network; Env never
  populated; capability-off = call path unreachable. `phonesim run launch`.
- **R-SIM.10 multi-machine-merge** (R-PHC.2/.3/.12): two machines, differing capabilities, one Asleep
  mid-scenario; merged Group-sectioned inbox, correct attribution, no id collisions. `phonesim run
  multi-machine-merge`.
- **R-SIM.11 machine-asleep** (R-PHC.2): daemon goes silent -> Asleep (not Offline/error), sessions freeze
  last-known, wake resumes via cursor; Asleep distinct from link Offline. `phonesim run machine-asleep`.
- **R-SIM.12 revocation** (R-PHC.2/.5/.8): revoke a machine AND simulate this device being remotely revoked;
  pinned keys discarded, subsequent traffic fails decryption cleanly, no pre-rotation-epoch payload decrypts
  after; remote self-revocation wipes local. `phonesim run revocation`.

## D.15 SwiftUI app (R-IOS) — owner: phone-client — authored Phase 1, device-compiled later

Verification for all R-IOS is structured code review against `remote-control-mock.html` + the phone-core
state shape, plus `swift build` on the macOS target for non-UIKit portions; live iOS behavior is
DEFERRED-VERIFICATION with the named proxy.

- **R-IOS.1 Module structure.** DesignSystem (Void tokens) / AppCore (screens) / thin per-screen ViewModel
  translating gomobile callbacks into `@Published` state; screens never call phone-core directly or hold
  protocol/crypto logic; DesignSystem compiles standalone; ViewModels testable against a fake phone-core
  stub. Verify: `swift build` (macOS) for buildable packages + review.
- **R-IOS.2 Pairing screen.** QR scan + SAS display + waiting-for-desktop-confirm as three states 1:1 with
  R-PHC.1. **R-IOS.3 Triage inbox.** Four Group sections verbatim from SessionView.Group + machine switcher
  (R-PHC.2 presence, Asleep visually distinct, sessions not hidden) + one-line summaries. **R-IOS.4 Session
  detail.** Journal + grid-snapshot cards (chat gated on transcript_delta cap), composer queue-while-busy,
  persistent Stop; fallback is a designed state. **R-IOS.5 Terminal peek.** Decoded re-sanitized grid (R-PHC.6)
  + live tail; take-control = lease supersede; held-vs-watching indicator; no raw wire string reaches a Text
  view. **R-IOS.6 Machines & security.** Presence (incl Asleep), per-device revoke (gate token), kill switch,
  activity log; kill-switch-on disables mutating actions app-wide. **R-IOS.7 Approvals sheet.** Built to the
  mock (bound command/context, hash, countdown from server TTL), driven by R-PHC.9, gated behind a capability
  flag until S-B/S-C. **R-IOS.8 Settings.** Exactly two coarse push toggles + quiet hours + biometric-gate
  toggle; persists via R-PHC.8.
- **R-IOS.9 Accessibility floor.** Every screen supports Dynamic Type + VoiceOver labels on every
  status-bearing element; status never conveyed by color alone (color pairs with text/shape). Verify: per-screen
  checklist review; DEFERRED-VERIFICATION live VoiceOver/Dynamic Type.
- **R-IOS.10 Biometric gate integration.** LocalAuthentication (Face ID/Touch ID, passcode fallback) before
  any mutating ViewModel call, producing the R-PHC.11 gate token; every mutating call site reviewed for no
  bypass. Verify: call-site review; DEFERRED-VERIFICATION live biometric.

## D.16 Notification Service Extension (R-NSE) — owner: phone-client — authored Phase 1

- **R-NSE.1 Decrypt pipeline + key access.** [amended: D.0-A10] NSE decrypts the sealed-box push (R-PHC.5) with the device key
  shared via an app-group Keychain access group (never duplicated to extension-local storage); visible
  title/body only from successfully decrypted content; no independent key-gen path.
- **R-NSE.2 Ciphertext-only payload.** [amended: D.0-A10] Outer APNs payload carries no sensitive fallback text — only routing
  metadata for a generic OS placeholder ("swarm: new activity") on NSE timeout (push is an untrusted hint).
- **R-NSE.3 Resource budget + failure fallback.** Complete within Apple's NSE time/memory budget; decrypt
  failure (revoked key, corrupt, duplicate) falls back to the generic placeholder, never crashes/hangs.
- **R-NSE.4 Actionable categories deferred.** No inline Approve/Deny buttons in Phase 1 (would bypass
  resync-before-enable; approval mechanism spike-gated); every push opens the app to a re-synced view; only
  the two coarse categories exist.
  Verify (all R-NSE): structured review of entitlements/payload schema/error path against Apple NSE
  constraints; DEFERRED-VERIFICATION live push (proxy: dev push provider R-E2E.5).

## D.17 Void design system (R-DSN) — owner: phone-client — Phase 1

- **R-DSN.1 Color token fidelity.** Swift color constants hex-for-hex with Void: `--p-bg #000000`,
  `--p-card #0a0f0c`, `--p-elev #101613`, `--p-well #050807`, `--p-hair #1c231e`, `--p-ink #e9fbef`,
  `--p-ink2 #8fa398`, `--p-ink3 #5c6b62`, `--p-hero #42dd82`, `--p-hero-ink #03130b`, `--p-att #ffb224`,
  `--p-work #00c2d7`, `--p-ok #4cc38a`, `--p-err #f2555a`, `--p-cta-bg #ffffff`, `--p-cta-ink #000000`;
  one token file is the only place hex appears. Verify: swift unit test diffing hex + review.
- **R-DSN.2 Typography.** SF Pro Display-equivalent (display weight 500, -0.034em tracking) + SF Mono for
  code/session/mono; body tracking -0.012em; token constants per role. Verify: swift unit test on token values.
- **R-DSN.3 Grain/radii/spacing.** Grain overlay at intensity 0.035 (named constant); radii card 18 / sheet 22
  / pill buttons+chips; named constants everywhere, no literals in view code. Verify: swift unit test on
  constants + review.
- **R-DSN.4 Status semantics + single fixed theme.** Group/connection/presence map to Void semantics (`att`
  amber = Needs input, `work` cyan = Working, `ok` green = Ready/healthy, `err` red = error/revoked/offline)
  via one mapping table; app ships Void as a single fixed dark theme, forced-dark regardless of system.
  Verify: review of the mapping table + swift build of the forced-dark config.
- **R-DSN.5 Mock-parity process.** `remote-control-mock.html` stays authoritative; any UX divergence found
  during iOS work is written back into the mock in the same change (closes audit blind spot 3); the mock
  carries a last-synced marker. Verify: per-screen review that the mock was updated for any divergence.

## D.18 Spikes (R-SPK) — owner: verification — Phase 0

S-B key finding: swarm-char already has the hook-capture plumbing (`$SWARM_CHAR_HOOK_SINK`,
`Fixture.HookPayloads`) but wired to the canned `hookprobe`; `parseHookStdin` (cmd/swarm/main.go:516)
discards `tool_input`. Codex approval is JSON-RPC `requestApproval` (app-server), not a settings hook, and
the live app-server producer is deferred per `codex.go` — S-B/S-C may need to stand up that wiring
themselves (dependency risk flagged, not a blocker).

- **S-A transcript derivation** (gates the Phase 2 chat view):
  - R-SPK-A.1 reuse swarm-char `characterize()` verbatim, add a periodic snapshot-diff recorder capturing
    grid deltas after each PTY-quiescence window across a multi-turn conversation for both CLIs.
  - R-SPK-A.2 prototype + score a grid-to-text diff heuristic per CLI: PASS (byte-exact) / DEGRADED / FAIL
    across >=3 scenarios each (plain, tool-use scrolling, one alt-screen), raw diff attached for every non-PASS.
  - R-SPK-A.3 cross-version spot-check (or explicit NOT-RUN + reason, never silently skipped).
  - R-SPK-A.4 single VERDICT line (PASS ships chat / PARTIAL ships with a specified degrade-to-journal rule /
    FAIL keeps journal+snapshot permanently) naming the exact condition that would flip it.
  - R-SPK-A.5 fixtures use the `adapter.Fixture` JSON shape (round-trip `Validate()`).
- **S-B interaction capture** (gates structured approvals):
  - R-SPK-B.1 run real claude with `--settings` hook pointed at a raw-capture relay (forks hookprobe, forwards
    raw stdin) to record the real `PermissionRequest` incl. `tool_input` for Bash + Edit/Write.
  - R-SPK-B.2 capture the real Codex app-server `requestApproval` JSON-RPC request (building the minimal
    standalone client if the wiring gap exists).
  - R-SPK-B.3 check any proposed production capture-path change against the E9.2 frozen-boundary grep ban +
    write a "production integration shape" recommendation (additive field vs new interface method vs ADR-required).
  - R-SPK-B.4 nested-payload taxonomy table (CLI | event | field path | example | stable across runs).
  - R-SPK-B.5 VERDICT (PASS additive-safe / PARTIAL one-CLI-or-ADR / FAIL).
- **S-C approval mechanism** (highest-consequence, opus C3):
  - R-SPK-C.1 empirically probe whether Claude Code's PermissionRequest hook can block the turn for a real
    approval window (staged 5s/30s/120s delays) without timeout/error/auto-deny -> delay|behavior table.
  - R-SPK-C.2 probe an MCP permission-prompt tool path (same staged method), or explicit NOT-APPLICABLE + version.
  - R-SPK-C.3 write the fallback decision rule (deep-link-to-peek) BEFORE C.1/C.2 results are known, timestamped,
    falsifiable, applied mechanically.
  - R-SPK-C.4 same staged-delay probe against Codex `requestApproval`.
  - R-SPK-C.5 VERDICT per-CLI (which CLI gets which mechanism), unambiguous.
  All spike outputs: `docs/verification/spike-S{A,B,C}.md` with `VERDICT:` lines + fixtures under
  `docs/verification/fixtures/spike-*/`.

## D.19 TDD process (R-TDD) — owner: verification — all phases

- **R-TDD.1** Role separation per task cluster: independent test-designer / implementer / reviewer agents
  (sonnet/opus only, never fable); no agent plays two roles on one cluster; the evidence file names who filled
  which role. **R-TDD.2** Failing-first captured to `docs/verification/vN-red/<name>-red.txt` in the exact
  `audit-fixes-red.txt` format, committed before/with the greening implementation (git-order checkable).
  **R-TDD.3** `go build ./... && go vet ./... && golangci-lint run && go test -race ./...` green at every
  cluster merge AND epic close. **R-TDD.4** Never modify a test to pass — stop and escalate; a test diff inside
  an implement cluster is reviewer-rejectable. **R-TDD.5** Cross-model reviewer (codex) mandatory on pairing,
  both crypto protocols, revocation, approval integrity, launch authority, gateway isolation. **R-TDD.6**
  ADR-gated boundary changes (adapter interface, wire envelope, persistence schema) land an ADR in the same
  change set. **R-TDD.7** Per-epic evidence file uses the epic-14 `| Criterion | Evidence |` walk-table; every
  row names a concrete test/CI-job/fixture; spike evidence files match the same standard.

## D.20 Adversarial floor (R-ADV) — owner: verification — Phase 1 acceptance floor

- **R-ADV.1** Replay/reorder/dup of mutating ops -> cached outcome, one execution, survives restart
  (`idempotency_test.go`). **R-ADV.2** Stale approval (expired / content-hash mismatch / superseded
  agent-instance) rejected, never a blind keystroke. **R-ADV.3** Revocation rotates the epoch (revoked device
  decrypts nothing new) AND force-releases its in-flight lease. **R-ADV.4** Photographed-QR: correct PSK but
  denied/timed-out desktop confirm pins no device key; two concurrent attempts, SAS matches one. **R-ADV.5**
  Cross-machine substitution: a malicious relay misrouting a phone op to the wrong machine is rejected
  (verified against the target's pinned device key), not silently dropped. **R-ADV.6** [amended: D.0-A9] Daemon/shim crash mid
  remote input/approve/launch leaves no orphan, recovers consistently (extend E14.3 kill -9 pattern). **R-ADV.7**
  Journal compacted past cursor N -> typed "cursor too old, resync" + snapshot fallback. **R-ADV.8** Hostile PTY
  content at the phone renderer: same adversarial fixtures replayed through the phone consumption path (or its
  Go stand-in); no C0/C1/DEL byte reaches output un-stripped (mirror `render_audit_test.go`). **R-ADV.9** APNs
  duplicate -> single logical action; expired push -> resync signal, not a stale enable-able Approve
  (`push_dedupe_test.go` + dev provider R-E2E.5). **R-ADV.10** Concurrent desktop/phone control resolves via the
  existing S2 generation-supersede in both orders; the gateway is not special-cased or lease-bypassing.

## D.21 End-to-end rigs (R-E2E) — owner: verification — Phase 1 (E2E.7 Phase 2)

- **R-E2E.1** Extend `swarm-fake-agent` with a `replay` mode streaming a captured `Fixture.PTYCapture` verbatim
  through a real PTY (deterministic; round-trip byte-equality test); spike fixtures become regression inputs.
  **R-E2E.2** ollama-backed agents ONLY where non-deterministic live turn-taking is needed AND a real CLI is too
  slow/expensive; never as a stand-in in S-A/S-B/S-C or in adapter-hook-shape assertions; each use names the
  behavior it stands in for. **R-E2E.3** 8 phonesim full-stack scenarios (Phase 1 floor) against a real
  daemon + gateway + phonesim (not stubs at every layer): pairing, triage inbox, remote attach+input,
  interrupt/kill+confirm, push on Group transition, machine-asleep, revoke-ends-session, concurrent
  desktop+phone (`e2e/remote_*_test.go`, one file per scenario). **R-E2E.4** Fail-closed latency gate: status
  change -> phonesim receipt p95 < 200ms fully local (extend the E14.8 composite-latency pattern one hop;
  `phonesim_latency_gate_test.go`; tighten once measured). **R-E2E.5** Dev push provider stand-in (at-least-once,
  duplicate-capable, delay-injectable) with a self-test before it's trusted; reused by R-ADV.9 + R-E2E.3-5.
  **R-E2E.6** Multi-machine inbox merge: two daemon+gateway stand-ins, one phone identity, merged namespaced
  inbox + independent op routing (`multimachine_inbox_test.go`). **R-E2E.7** Approval-flow scenario written AFTER
  the S-C verdict (verdict-driven not assumption-driven; its doc comment cites `spike-SC.md`'s VERDICT). **R-E2E.8**
  E2E evidence recorded in `docs/verification/remote-control-e2e-evidence.md` in the epic-14 walk-table format
  (E2E.4 row names the measured p95).

---

## E. Phase exit gates (the checklist that makes a phase "done")

A phase is "done" only when its gate below is walked and evidenced. Gates are cumulative.

- **R-GATE.1 Phase 0 exit.** ADR-007 exists and covers every design-doc section-10 item (A-vs-D
  transport, sealed-box scheme details, journal format/retention, S-C-informed approval UX shape,
  relay-vs-happy-server choice, distribution/APNs-custody); the Apple developer account is active;
  all three spikes (S-A/S-B/S-C) have a committed `VERDICT:` per R-SPK-A.4/B.5/C.5. A single checklist
  in ADR-007 ties each item to a specific artifact.
- **R-GATE.2 Spike-phase exit (parallel with Phase 0).** Every R-SPK requirement (15) has its Verify
  artifact committed under `docs/verification/` regardless of PASS/PARTIAL/FAIL — a FAIL with full
  evidence is complete; a missing evidence file is not. `spike-S{A,B,C}.md` all exist with VERDICT lines
  + linked fixture dirs.
- **R-GATE.3 Phase 1 exit ("provable core").** Sidecar gateway, relay, pairing + device registry, both
  crypto schemes, daemon journal + idempotency store, inbox (session_state), remote grid attach + input
  (existing lease), interrupt/kill + confirm, and push on Group transitions each pass their own epic
  evidence file (R-TDD.7 format); ALL R-ADV.1-10 pass (the design-doc section-9 acceptance floor); all 8
  R-E2E.3 scenarios pass against a real (non-stub) stack; GG-4 gates green on the whole module including
  the new packages. A Phase-1 rollup evidence file cross-references every constituent epic + every
  R-ADV/R-E2E row. Per D.0-A9, only the Phase-1 validator/builder/policy/crash-recovery split of R-ADV/
  R-SIM counts here; live launch execution + approval delivery are Phase 2. Per D.0-A13 (iOS), the Go
  core + uncompiled iOS source being "complete" is NOT "shipped": a mandatory pre-production Xcode/device
  gate (archive, gomobile bind, entitlements, killed-app push, NSE timeout, biometric cancel,
  Keychain-after-reboot) precedes any real-world use, and R-GATE.3 does NOT count DEFERRED-VERIFICATION
  proxies toward the "extremely safe" bar — that on-device key-custody + biometric surface is an
  explicit aggregated residual risk retired only at the device gate.
- **R-GATE.4 Phase 2 exit ("what the spikes earned").** Chat view ships only if S-A PASS/PARTIAL (with
  PARTIAL's degrade rule implemented); structured approval sheets ship only if S-B PASS/PARTIAL AND S-C
  names a workable blocking mechanism for that CLI; launch-from-phone ships under the full 5.6 model with
  each sub-item (allowed roots, no dangerous flags, no phone env, worktree default, per-device policy,
  phone confirm) having its own test, not one bundled "launch works". The evidence file cites the spike
  verdict each conditionally-scoped feature is built on; R-E2E.7 is the approval proof point.
- **R-GATE.5 Phase 3 exit (deferred).** Phase 3 (observer-attach fan-out epic, activity depth, voice,
  quick replies, quiet hours, optional tsnet direct path, Live Activities) MUST NOT begin until R-GATE.3
  AND R-GATE.4 are both closed (observer attach needs new fan-out/backpressure/eviction invariants that
  would compound with any open Phase 1/2 gap). A dated go/no-go note confirms both.

## F. Requirement count and coverage

183 requirements: the original 173 plus 10 added by audit-003 (section D.0): R-GW.8 (remote-tier
socket), R-CRY.16 (device command-signing key), R-POL.9 (daemon signature+capability verification),
R-REL.11/.12/.13 (relay account-routing / APNs-token / device-de-authorization lifecycle), R-PROT.7
(error-code taxonomy), R-PHC.13 (client reconnect backoff), R-TDD.8 (durable-artifact migrations),
R-PAIR.9 (headless-pairing scope). Originals by area: crypto/relay 38 (R-CRY 15, R-PAIR 8, R-DEV 6,
R-REL 10); daemon-integration 47 (R-GW 7, R-JRN 7, R-IDP 4, R-POL 8, R-KS 5, R-CLI 6, R-TUI 4, R-PROT 6);
phone-client 43 (R-PHC 12, R-SIM 12, R-IOS 10, R-NSE 4, R-DSN 5); verification 45 (R-SPK 15, R-TDD 7,
R-ADV 10, R-E2E 8, R-GATE 5).

Audit-003 disposition (`docs/verification/audit-003-remote-control-plan.md`, verdict REVISE): all
consensus findings, accepted divergences, and blind spots are addressed in D.0 A1-A13. The three-way
convergent CRITICAL (self-declared remote-origin) is closed by D.0-A1 (dedicated remote-tier socket +
device command signatures). One reviewer finding was rejected on cross-examination: `box.SealAnonymous`
exists in `x/crypto/nacl/box` (verified), so R-CRY.10 needs no hand-rolled primitive.

Every design-doc section 5.x security rule maps to a requirement + test target; every audit-002
consensus item (1-14) and blind spot is covered: item 1 (observer fan-out) deferred to Phase 3 with
R-GW.6 taking the lease-attach path meanwhile; 2/3 spike-gated (S-A/S-B); 4 = R-POL.8 + R-ADV.2; 5 =
R-JRN + R-IDP; 6 = R-POL.2-.6; 7 = ADR-007 A-vs-D; 8 = two schemes R-CRY.4/.9; 9 = R-NSE.2 + R-REL.5;
10 = R-REL.10; 11 = R-PAIR + R-DEV; 12 = phase map; 13 = R-KS.4 signed local log; 14 = R-GW.1 sidecar.
Blind spots: laptop sleep = R-REL.3 + R-PHC.2 + R-SIM.11; relay DoS = R-REL.8; mock sync = R-DSN.5.
