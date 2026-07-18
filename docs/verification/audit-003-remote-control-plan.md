# Audit 003: Remote-Control Implementation Plan — Committee Report

Target: `.claude/tmp/remote-control-implementation-plan.md` (173 requirements, the plan of record for
epic `agents-tracker-5h5`). Committee: GPT-5.6 sol (codex, read-only repo access), Gemini 3.5 Flash
(agy, brief+plan inlined), Sonnet (source-verified), Opus (source-verified, security focus). All four
reviewed independently against the same brief. This is the cross-examined synthesis; the plan is
revised (section D.22 + surgical edits) to incorporate every accepted finding.

## Verdict

REVISE (unanimous). The direction and breadth are strong and the plan is unusually honest about the
build/verify environment, but several Phase-1 contracts are contradictory or unenforceable against the
current code. Coverage is NOT yet exhaustive: the remote authorization boundary has no unforgeable
basis, four Phase-1 requirements contradict either the source or each other, and the async-delivery /
crash-recovery / connection-lifecycle contracts are asserted rather than specified. Fixing these is the
gate before TDD begins.

## Consensus (raised independently by 2+ members; all accepted)

1. CRITICAL — Remote-origin authorization is self-declared and has no cryptographic teeth (codex, sonnet,
   opus — three-way convergent, the single most consequential finding). Two layered holes:
   (a) The remote tier is marked by the gateway voluntarily offering the `remote-gateway` capability
   (R-GW.2); `hello` caps are merely intersected (server.go:744/757), there is no client auth. The
   gateway runs as the owner uid (launchd/systemd user unit) and dials the SAME UDS as the local TUI, so
   the daemon cannot distinguish them. An UNMARKED connection = local = full trust (R-POL.1). A
   compromised gateway — exactly what 5.7 privilege-separates against — simply omits the capability and
   gets unrestricted launch/kill with no policy and no kill switch. Sidecar isolation buys process
   containment, NOT authorization containment.
   (b) Even when marked, the daemon cannot verify a device's capability independently of the gateway:
   device keys are DH-only (R-CRY.2 KeyStore exposes only DH/sealed-box/Noise-static, "no signature
   oracle"), the Ed25519 relay-auth key is scoped relay-only, and Noise terminates at the gateway.
   R-PROT.2's `device_id`/`capability_assertion` are self-reported strings with no issuer/signature/
   expiry/revocation. So R-POL.6/R-POL.1's "regardless of what the gateway claims" is unimplementable.
   Fix (both halves): origin by construction, not declaration — a dedicated, hard-wired remote-tier
   daemon listener (distinct socket path + perms) whose connections are UNCONDITIONALLY remote; the main
   UDS stays owner-trusted. Plus a device command-signing key (Ed25519) minted at pairing, pubkey pinned
   in the daemon registry; every remote mutating op carries a signature over (action, machine, session,
   request-id, expires-at, content-hash) that the daemon verifies against the registered device key,
   independent of the gateway. Test: a compromised gateway that omits caps / forges device_id / replays a
   signature still cannot obtain local privileges or impersonate a device.

2. CRITICAL — R-GW.6 "on-demand snapshot that does not steal an active controller" contradicts both the
   attach code and the approved design (codex, opus). Protocol attach is close-old-then-open-new
   supersede (server.go:388-394); the cited `sampleGrid` only "doesn't steal" because it blocks on the
   shim's single serve slot and silently times out (serve.go:276-279). So peek is non-stealing OR
   always-available, never both, and `TestGateway_SnapshotDoesNotStealActiveController` would pass
   precisely by silently failing under an active controller. The design (5.3/7.3, design.md:304) already
   says Phase-1 peek SUPERSEDES the lease. Fix: state the honest behavior — a last-known cached snapshot
   when not controlling; live grid requires take-control (existing S2 supersede), UX-confirmed; the test
   asserts supersession, not its absence.

3. CRITICAL/HIGH — Crash-safe exactly-once is unspecified (codex; agy adjacent). R-IDP.2 persists the
   outcome AFTER execution, leaving execute -> crash -> re-execute; R-ADV.1/.6 nonetheless demand one
   execution across crash. Fix: a durable prepared -> executing -> completed record written BEFORE the
   side effect; per-op recovery (launch binds to the existing two-phase session reservation, kill/
   interrupt to process-identity + terminal state, approval to a one-shot interaction state); retention
   must exceed the retry horizon.

4. HIGH — Async epoch fan-out, sequencing, and cursor ownership are internally inconsistent (agy, codex,
   opus — convergent). "One ciphertext fanned out under K_epoch" contradicts a single `recipient_key_id`
   in the AAD (different recipients -> different AAD -> not one ciphertext). Three cursor systems (journal
   R-JRN.4, relay-storage R-REL.4, authenticated seq R-CRY.12) are never reconciled; R-GW.1's
   "stateless-except-cursor" contradicts R-CRY.12's monotonic per-epoch seq that a restart would reset
   and the device would reject. EpochGrant delivery is live-only, so an offline-at-rotation device is
   locked out. Fix: recipient_key_id becomes a routing hint OUTSIDE the AEAD AAD (identical ciphertext
   under shared K_epoch; the relay's per-device mailbox does routing); async mailbox seq = the durable
   daemon journal cursor (one coordinate system, no independent gateway counter); K_epoch persisted
   machine-side 0600; EpochGrants mailboxed for async delivery; seq-gap -> resync-from-snapshot.

5. HIGH — Journal/meta atomicity + hook enumeration (codex, sonnet — convergent). R-JRN.2 names call
   sites (SetStatus/finalizeTerminal/Launch/Delete) and misses `reconcile.go`'s two startup `saveMeta`
   calls — the exact daemon-restart Lost/Exited transition the journal exists to make durable. And
   `saveMetaLocked` commits meta.json independently, so a crash yields meta-without-journal or vice
   versa. Fix: hook the `saveMetaLocked` CHOKE POINT itself (covers reconcile + every future caller) +
   a separate Delete hook; define journal-as-WAL / recoverable commit with crash injection between
   steps; move the 1s debounce to delivery, not the durable journal.

6. HIGH — Phase-1 gates depend on Phase-2 features (codex, opus, sonnet-adjacent). R-GATE.3 requires all
   R-ADV.1-10, but R-ADV.6 includes crash-mid-launch/approve while launch execution + approval delivery
   are Phase 2; R-SIM.9 says an in-policy launch "succeeds" though only the builder exists in Phase 1;
   R-POL.8's content-hash/request-id validation needs interaction content that is S-B/Phase-2-gated (only
   the agent-instance/session portion is genuinely Phase 1). Fix: split R-ADV/R-SIM/R-POL.8 into Phase-1
   validator/builder tests and Phase-2 live-execution tests; a fake backend accepting launch is not proof
   of live daemon enforcement.

7. HIGH — NSE key contradiction + content-at-rest exposure (codex, opus — convergent). R-CRY.13 says push
   decrypts with K_epoch; R-NSE.1 says the NSE decrypts a "sealed-box push" with the DEVICE key —
   contradiction. Worse, K_epoch also decrypts ALL mailbox content, so an after-first-unlock,
   NSE-readable K_epoch means a once-unlocked stolen phone yields all current-epoch session history —
   contradicting 5.1 ("a stolen phone must not become data exfiltration"). Fix: a content-free WAKE key
   for the NSE (decrypts only "activity on machine X"); session/mailbox content under a biometric-gated
   content key; generic outer payload.

8. HIGH — Connection lifecycle, error taxonomy, and migrations under-specified (codex, sonnet). No
   client-side reconnect backoff/jitter (only "no retry storm on Asleep"), no keepalive/half-open bounds,
   no duplicate-connection takeover, no machine-readable refusal-reason vocabulary (four areas invent
   ad-hoc strings), no versioned migration/rollback tests for the durable artifacts. Fix: add a stable
   error-code taxonomy (retryable vs permanent + state effect), client reconnect backoff+jitter, and
   migration tests for identity/registry/policy/journal/idempotency/relay-DB/activity-log.

9. HIGH — Headless-machine pairing voids the pairing security argument (agy, codex, opus — convergent).
   On an SSH-only box (a prime remote-control target), the QR and the "independent" desktop confirm
   collapse into one in-band channel: the camera is no longer out-of-band and the second gate is answered
   on the same channel it must be independent of -> "whoever holds SSH can pair" = RCE-via-phone. Fix:
   Phase 1 requires local-display pairing (scope headless out explicitly); a headless flow (an OOB
   confirmation code the operator already holds) is a named follow-up.

## Divergence and refinements

- Input delivery (codex, CRITICAL): R-IDP.1 exempts input from retry, but R-PHC.4/R-SIM.5 queue-and-
  replay it and TDataIn has no request id. Accepted: remove input from the offline queue — input needs a
  live lease; disconnect yields "delivery unknown/not sent"; queue only high-level idempotent ops. And a
  per-keystroke biometric token is unusable; define a bounded authenticated control session instead.
- Single-X25519-key reuse (opus M1): a proof-hygiene gap, not a live oracle (neither Noise nor sealed-box
  exposes the raw shared secret). Accepted the cheap fix: split into two X25519 keys (Noise static +
  sealed-box recipient), both pinned at pairing, rather than carry a cross-protocol-composition argument.
- Relay-side revocation (opus M3): crypto-side revocation (epoch rotation + lease release) is real, but
  the relay has no device-deregister op, so a revoked device keeps relay-auth and can drain its
  pre-rotation mailbox. Accepted: a MUST relay-side de-authorization + mailbox purge on revocation, with a
  message to carry it — folded into the relay account/routing lifecycle (finding 4 of divergence below).
- Relay bootstrap/routing/APNs registration (codex, CRITICAL): R-REL.2 assumes a registered relay-auth
  key + routing id with no requirement defining machine registration, routing-id derivation/proof, device
  authorization, credential revocation, duplicate-connection takeover, or APNs token registration/refresh/
  deletion. Accepted: add a full relay account/routing/authorization lifecycle + a push-token registration
  op + adversarial tests (route enumeration, cross-route mailbox access, stale tokens, revoked creds).
- Approval identity (codex CRITICAL, opus m4): `request_id` is overloaded for the idempotency op and the
  agent interaction. Accepted: separate `operation_id` from `interaction_id`; add authoritative
  `expires_at` + clock-skew + daemon-side expiry; byte-exact content canonicalization + hash algorithm;
  supersession/consumption state; expiry tested at the daemon, not just a phone fake clock.
- Options/env launch surface (opus m2, m3): R-POL.5 strips `Env` but passes `Options`/`InitialPrompt`
  unaudited; symlink resolution is check-on-resolved / use-on-original. Accepted: allowlist `Options` for
  remote launch; check AND hand the shim the same fully-resolved real path.

## Blind spots / over-engineering (found and accepted)

- Activity-log signing + hash chain (opus m1, codex, agy): the signing key sits beside the log under the
  same uid, so it defends against no on-machine adversary (audit-002 item 13 already downgraded the
  claim). Accepted: drop to a plain append-only log unless off-machine anchoring is added; keep the modest
  claim explicit.
- R-DSN pixel-fidelity swift unit tests (sonnet m2): proportionate cost is arguable for a personal app;
  kept (cheap as constants) but flagged as the first trim candidate under time pressure.
- R-ADV.8 asserts only C0/C1/DEL while R-PHC.6 promises bidi/zero-width/separators too (codex): accepted —
  assert every promised class.
- Clock-skew as a class (opus m6): pin every TTL to a single authoritative clock; phone countdowns are
  display-only, enforcement server-side.
- APNs blind-push-gateway seam (opus m7): audit-002 item 9 asked to design it now; the plan deferred it
  silently. Accepted as a CONSCIOUS deferral (moot for personal-only), now flagged.
- iOS "shipped" (codex, opus m5): structured review + fake APNs cannot prove compilation, entitlements,
  gomobile bind, NSE budget, or LocalAuth lifecycle. Accepted: rename the Phase-1 iOS gate to "Go core +
  uncompiled iOS source complete"; add a mandatory pre-production Xcode/device gate; R-GATE.3 must not
  count deferred proxies toward "extremely safe" — aggregate that risk explicitly.

## Corrections to the plan's source claims

- "NOT the 200ms poller" (R-JRN.2) — the actual liveness ticker is `monitorPoll = 100ms` (daemon.go:30).
  Corrected; and the hook is the choke point regardless of the poller value.
- R-REL.1 "reusing internal/wire framing with a relay tag set" — clarified to a SEPARATE relay envelope,
  NOT an extension of the frozen client<->daemon `wire.Type` enum (GG-7 drift-checked, G1 frozen).
- `box.SealAnonymous` (agy MAJOR) — REJECTED: `golang.org/x/crypto/nacl/box.SealAnonymous` exists and is
  the crypto_box_seal-compatible primitive R-CRY.10 references (verified: `go doc` confirms the
  signature). No hand-rolled primitive needed; the plan's approach stands.
- A-vs-D Option contingency (sonnet C2) — the phase map already sequences ADR-007 (Phase 0) BEFORE the
  Phase-1 crypto epics, so the A-vs-D call is made before that code is written; made explicit that
  R-CRY/R-PAIR/R-REL assume Option A and are gated on R-GATE.1 confirming A (the transport module stays
  behind an interface so D can slot in, per design section 4).

## Per-member signal

- GPT-5.6 sol (codex): deepest source-verified teardown (6 CRITICAL + 6 HIGH) — the self-asserted origin,
  the R-GW.6 impossibility, crash-safe exactly-once, the input contradiction, the missing relay lifecycle,
  and approval identity/expiry all with file:line proof.
- Gemini 3.5 Flash (agy): the gateway seq/key-persistence CRITICAL and the stale-epoch delay/replay
  freshness gap; one factual miss (SealAnonymous) caught in cross-examination.
- Sonnet: the daemon-side capability has no signing key (convergent C1), the reconcile.go journal-hook
  miss, the A-vs-D no-contingency asymmetry, and the elegant "make the biometric gate a compile error via
  the R-PHC.7 structural test" fix.
- Opus: the sharpest statement of the origin hole (unmarked = full trust; owner-uid gateway), the
  NSE-K_epoch content-at-rest exposure, relay-side revocation drain, headless-pairing collapse, and a
  disciplined fair-play list of what could NOT be broken (cross-protocol oracle, relay forgery,
  replay/reorder, revocation-drop, stale-generation input — the existing invariants hold).

## Disposition

All consensus items, accepted divergences, and blind spots are incorporated into the plan's section D.0
(authoritative amendments A1-A13, superseding the referenced requirements). New requirements added:
dedicated remote-tier socket R-GW.8; device command-signing key R-CRY.16 + daemon signature/capability
verification R-POL.9; two-phase crash-safe idempotency (folded into R-IDP.2/.3); relay account/routing/
APNs-token/de-authorization lifecycle R-REL.11-.13; error-code taxonomy R-PROT.7; client reconnect backoff
R-PHC.13; durable-artifact migrations R-TDD.8; content-free NSE wake key (folded into R-CRY.13); headless
pairing scope R-PAIR.9; approval identity/expiry wire fields (folded into R-PROT.2). The contradicted
requirement bodies carry inline `[amended/SUPERSEDED by D.0-Ax]` markers pointing at the governing
amendment (R-GW.6 peek-supersedes; R-IDP.2 two-phase; R-GW.2/R-PROT.2 socket+signature not capability;
R-PHC.5 crypto layering; and the rest).

## Delta re-audit (003b)

The D.0 amendments were re-reviewed by the two source-verified members (codex, opus). Result: every prior
finding is CLOSED or conceptually closed; NONE left unaddressed. `box.SealAnonymous` rejection confirmed
(exists in x/crypto). Verdict (both, convergent): **Phase 0 (ADR-007 + spikes S-A/S-B/S-C) may begin now —
the residuals belong exactly there.** Four residuals are pinned in ADR-007 and captured as D.0 refinements
A1-R..A5-R + A14/A15: (1) A1 same-uid gateway isolation — pin a dedicated-uid/sandbox mechanism OR adopt
the scoped threat model (a compromised owner-uid gateway holds the machine identity key and is outside the
crypto boundary; the boundary is the untrusted relay + semi-trusted phone, enforced by device signatures)
and make R-GW.8's test non-vacuous; (2) interrupt is at-most-once with an outcome-unknown crash record;
(3) a signed `take_control` op establishes the control session, keystrokes ride session+lease not
per-keystroke signatures; (4) EpochGrants carry their own `(epoch_id, grant_seq)` anti-replay coordinate,
outside the journal-seq stream. Two body-propagations gate the Phase-1 CRYPTO slice (not Phase 0): A14 the
two-X25519-key split into R-CRY preface/.4/.10 + R-PAIR.3/.7 + R-DEV.1; A15 the wake-key/content-key split
into R-CRY.10/.11/.13. These land with ADR-007 (the crypto-design authority) before any crypto TDD.
