# Remote Phase 1 — Relay Slice (8664f3b) — Audit Committee Review

**Slice under review**: `internal/remote/relay/` + `cmd/swarm-relay/` (commit `8664f3b`,
GREEN, 36/36 -race). Reviews R-REL.* against ADR-007 D9/D11.
**Review date**: 2026-07-20. **Beads**: epic `agents-tracker-qnx`, task `agents-tracker-7qc`.
**Verdict**: **REVISE** — the E2EE payload core is trustworthy; the availability,
control-plane-integrity, and metadata-scoping obligations are substantially unmet, two
gaps trivially exploitable pre-authentication. Do NOT build the rest of the epic on it
until the CRITICAL/HIGH items are fixed.

## Committee composition (and a gap to close)

Full-rigor cross-model review was requested. Actual panel:
- **Opus subagent** — deep crypto/concurrency pass. Delivered.
- **Sonnet subagent** — broad adversarial pass. Delivered.
- **Independent lead read** (this reviewer) — server.go, store.go, routing.go, wire.go, main.go.
- **codex (GPT-5.5)** — UNAVAILABLE: hit account usage limit (resets ~2026-07-25). No output.
- **agy (Gemini 3.5 Flash)** — UNAVAILABLE: `agy` not installed on this machine.

**Process note**: the plan (section B.3) requires cross-model review (codex + independent
opus) for security-critical slices. Two external models were down, so this review is
opus + sonnet + lead only. It should be RE-RUN with codex once credits reset (or agy
installed) before the relay slice is considered fully signed off — especially because
the slice was authored by "Claude Fable 5" (commit 8664f3b), a model the plan explicitly
forbids for this work (B.1).

## Consensus findings (raised by 2+ reviewers — treat as real)

### CRITICAL

**CR-1. No connection-admission control + no socket timeouts + a single GLOBAL auth
rate counter → one anonymous socket denies all authentication.**
`server.go:213-220` accepts unlimited concurrent websockets; `server.go:174` sets no
`ReadTimeout`/`IdleTimeout`; `readFrame` (`server.go:284-293`) has no per-read deadline;
`connRate` (`server.go:128`, consumed `server.go:422`) is ONE process-wide window, not
per-IP/per-key. An attacker opens N idle sockets (goroutine/fd exhaustion, slowloris)
and/or flaps `auth_init` (revocation check passes for random pubkeys) to burn the global
`ConnPerMin` budget, locking out every legitimate machine/phone reconnecting from
cellular — defeating the "cold-start-from-cellular / extremely safe" product bar with
~20 lines of attacker code, no credentials. (Opus C1; Sonnet C2+C3; lead #2 partial.)
*Fix*: per-IP and global concurrent-conn caps; accept/handshake + idle read deadlines;
key `connRate` per source; rate-limit at raw-accept, not just `auth_init`.

**CR-2. R-REL.8 ("rate limits on EVERY endpoint") is systemically false — only 2 of 16
ops are limited.** Enumerated dispatch (`server.go:332-382`): only `mailbox_append`
(`server.go:513-523`) and `push_trigger` (`server.go:633-644`) have any limit. `hello`,
`auth_resp`, `authorize_device`, `mailbox_read`, `mailbox_ack`, `token_register/delete`,
`presence`, `device_revoke`, and all five `rendezvous_*` have none. The relay's own
`TestRelay_ConnRateLimited` (`abuse_test.go:82-104`) inadvertently proves the global-
bucket flaw by showing three DIFFERENT keys share one counter. (Sonnet C2; Opus C1.)
*Fix*: rate-limit every state-touching op, keyed by source.

**CR-3. Retention/presence sweeps are never invoked by the shipped binary.**
`cmd/swarm-relay/main.go:21-44` starts no ticker; `SweepRetention`/`SweepPresence`
(`server.go:805,832`) are called only from tests. In production: mailbox ciphertext is
retained FOREVER (violates R-REL.10 + the D11 "retention is bounded" privacy
commitment), and the N-7 "machine went silent" push never fires (a missing product
feature). The 36/36 suite passes only because tests call the sweeps by hand. (Lead #1;
Sonnet H1; Opus C2 — Opus ranks CRITICAL as both a broken auditable promise AND the
enabler of the storage-exhaustion vector.)
*Fix*: baseCtx-guarded ticker in `Start`/`run` calling both sweeps; add a test booting
via `run` that asserts a purge with no manual sweep call.

**CR-4. Mailbox has no depth/byte cap; `mailbox_read` is unpaginated → a mailbox can be
permanently bricked.** `handleMailboxRead` (`server.go:531-549`) returns EVERY item after
`cursor` in one frame; if the JSON exceeds `MaxFrame` (1 MiB, base64 ~4/3 inflation),
`WriteFrame` (`wire.go:54-57`) returns `ErrFrameTooLarge` and writes NOTHING → the
non-nil dispatch error tears the connection (`server.go:252-259`) → the client never got
a cursor to ack → the next read re-serializes the same oversized backlog and fails
identically. No malice required (a device offline while traffic accrues), and trivially
forced: a paired-but-malicious machine floods up to `MailboxAppendPerMin` (default 600) ×
~768 KiB/frame with no depth cap and (per CR-3) no retention purge. (Sonnet C1+C4 — the
self-bricking chain is Sonnet's standout catch; Opus H1 for the flood; lead open-Q #7.)
*Fix*: paginate `mailbox_read` with `limit`/`max_bytes` + `has_more`, never exceed
`MaxFrame`; enforce a per-mailbox depth/byte quota returning clean `quota_exceeded`.

### HIGH

**HI-1. Rendezvous endpoints: unauthenticated, no participant check, no rate limit, and
`create` is a blind overwrite.** `server.go:687-790`. `rendezvous_create` (`:701`) does
`s.rendezvous[id] = ...` with NO existence check → an attacker who sees/knows an id
REPLACES the live slot (victim orphaned, legit claimer joins the attacker's slot);
`rendezvous_complete` (`:778`) burns ANY id with no participant check; `claim` occupies a
slot it didn't create; no rendezvous op consults any rate window (only the global
`MaxConcurrentRendezvous` cap). Noise XXpsk0 + SAS + desktop-confirm still fail closed
(no MITM/RCE), so impact is **pairing DoS**, not compromise. (Consensus 2a/b/c; Opus adds
2d overwrite.)
**Correction to the lead's initial framing**: the rendezvous id is an independent 128-bit
random that lives ONLY in the out-of-band QR (`pairing/pairing.go`, `qr.go`) and is never
on the wire before `create` — so a *blind anonymous internet* client CANNOT guess it
(2^128). The real exploit position is the **untrusted relay operator itself** (sees
`create{id}` directly) or an on-path attacker absent TLS (HI-3). Still in-scope for an
untrusted-relay threat model; re-ranked HIGH, not CRITICAL.
*Fix*: `create` refuses an existing/burned id; `claim`/`send`/`complete` verify the
caller is `slot.creator`/`slot.claimer`; per-IP rendezvous rate window.

**HI-2. TLS is unenforced, `TLSMode` is DEAD CONFIG, and the CONTROL PLANE (not just
metadata) depends on TLS.** `Start` always `net.Listen("tcp")` + `ws://`
(`server.go:163-177`); `TLSMode` (`config.go`) is read and round-tripped but consulted
NOWHERE → setting `"tls_mode":"on"` silently serves plaintext (a false assurance, worse
than none). Deeper: the relay-auth handshake authenticates the key holder but establishes
NO session key and NO channel binding, so on plain `ws://` an active MITM forwards the
challenge, captures the signature, and HIJACKS the authenticated control session — then
`mailbox_ack` (delete the victim's queued commands), `device_revoke` the victim's
devices, or `token_delete`. E2EE payload confidentiality survives; control-plane
integrity does not. (Lead #5; Sonnet H5; Opus H3 — Opus's channel-binding analysis is the
key escalation.)
*Fix*: bind the auth signature to a channel/transcript hash OR require+pin TLS; make the
relay fail closed when `TLSMode=="on"` and it cannot terminate TLS; correct the D11
wording to state TLS is load-bearing for control-plane integrity.

**HI-3. `authorize_device` is a UNILATERAL relay-side pairing claim → mailbox-flood /
push-spam / storage-exhaustion against any known device pubkey.** `server.go:479-497`:
any party who generates an Ed25519 key and authenticates (no machine allowlist) can
`addPair(self, arbitraryDevicePub)` with no device consent at the relay. Then (per CR-4)
flood that device's mailbox (600/min × ~768 KiB, no depth cap, no retention → multi-TB)
and `push_trigger` it (battery/notification DoS, wake oracle). The device's AEAD rejects
junk (no RCE — E2EE holds), but D9's "authorization scoped to paired routes" is a
unilateral claim, not mutual. (Opus H1; Sonnet M3; lead #6 — under-ranked LOW originally.)
*Fix*: require a device-signed pairing token / rendezvous-derived proof before `addPair`;
add the per-mailbox depth/byte quota (shared with CR-4).

### MEDIUM

**ME-1. Revocation is neither atomic nor connection-severing (R-REL.13 partially false).**
`handleDeviceRevoke` (`server.go:651-676`) runs `removePair`/`revoke`/`purgeMailbox` as
THREE separate bbolt transactions then a separate `s.mu` section. (a) **Not atomic**: a
crash between `removePair` and `revoke` leaves the device de-paired-but-not-revoked, and
`mailbox_read` gates only on `requireAuth` (no pairing check) → it can still authenticate
and DRAIN its existing mailbox (the "drainable pre-rotation mailbox" R-REL.13 forbids).
(b) **TOCTOU with append**: `handleMailboxAppend` checks `isPaired` in one txn, then
`appendItem` in another — a concurrent revoke can be straddled, RESURRECTING a revoked
device's mailbox (`MailboxDepth` > 0). (c) **Does not sever the live socket**: only
`superseded.Store(true)`, never `old.cancel()`/`CloseNow()` → zombie fd/goroutine until
self-disconnect. (d) **Wrong error code**: `superseded` is overloaded, so a revoked-but-
connected device is told `duplicate_connection`, not `revoked`. (Opus M1 for a/c/d; lead
for b the TOCTOU — a divergence neither subagent independently raised.)
*Fix*: single `db.Update` (revoke first); re-check pairing inside `appendItem`'s txn or
gate append on non-revoked; `old.cancel()` on revoke; distinct `revoked` requireAuth code.

**ME-2. Cross-tenant presence/existence oracle — no `isPaired` gate on `presence`.**
`handlePresence` (`server.go:593-604`) lets any authenticated party query the
online/offline/unknown state of ANY routing id. Since `rid = HKDF(pubkey)` is stable for
life, an attacker knowing a target's relay-auth pubkey polls to reconstruct the victim's
laptop-awake schedule and test account existence — cross-tenant metadata beyond what D11
admits (D11 scopes exposure to "the relay," not other tenants). (Sonnet H4; Opus M2.)
*Fix*: require `isPaired(sc.rid, req.Target)`; else return `unknown`.

**ME-3. Unbounded in-memory growth (monotonic slow DoS).** `burned` (`server.go:126`,
never purged), `appendRate`/`pushRate` (keyed by target rid, never reaped), `presence`
(deleted only on revoke), plus the persistent `revoked`/`pairs` buckets. Grows without
bound under device churn; compounds CR-1. (Lead #3; Sonnet M1; Opus M3.)
*Fix*: TTL-expire `burned` after `RendezvousTTL`; reap idle rate/presence entries in the
(now-wired) sweep.

**ME-4. Unthrottled silent-push spam via presence flapping.** `SweepPresence`
(`server.go:805-828`) delivers pushes with no rate check (`pushRate` guards only
`push_trigger`). A machine rid flapping every `PresenceTimeout+e` spams paired devices;
combined with HI-3, an attacker pairs+flaps to push-spam a victim. (Opus M4.)
*Fix*: subject sweep pushes to a per-target rate window.

## Divergence / per-member unique signal

- **Sonnet uniquely**: CR-4's self-bricking chain (unpaginated `mailbox_read` →
  `MaxFrame` → permanent unreadability) — the highest-leverage catch; Opus did not flag
  the self-brick specifically.
- **Opus uniquely**: HI-2's channel-binding / control-plane-MITM analysis; HI-1's `create`
  blind-overwrite (2d); ME-1's revoke non-atomicity + wrong error code; ME-4 push-spam;
  and the "you missed it entirely" GLOBAL `connRate` counter (folded into CR-1).
- **Lead uniquely**: the revoke/append TOCTOU (ME-1b); grounding the store-layer
  directionality (`addPair` is undirected both-ways, `pairedPeers` prefix-safe).
- **Both subagents corrected the lead**: the rendezvous-id "anonymous guess" framing was
  wrong (128-bit OOB secret); the per-conn lock-free fields ARE safe (dispatch is serial);
  the auth nonce IS single-use (success path); `mailbox_ack` is self-harm only.

## Blind spots (worth a follow-up look)

- Relay-restart semantics: `burned`/`rendezvous`/`presence` are in-memory only → after a
  restart a completed rendezvous id is no longer "burned" (single-use only within a
  process lifetime). Low, but note it.
- No test exercises real concurrency (all tests are sequential single-client round-trips),
  so `-race` green is weak evidence for the shared-map paths — the ME-1b TOCTOU lives
  exactly there.
- ME-5 (Opus): the durable pairing GRAPH + revocation set are unbounded-retention
  metadata slightly beyond the D11 honesty statement's wording — a spec-honesty gap, not a
  code bug.

## What the tests do NOT cover

No enumeration test asserting each op has a quota (CR-2); no `mailbox_read` × `MaxFrame`
boundary (CR-4); no connection-admission/idle-growth test (CR-1); no non-participant
rendezvous test (HI-1); no assertion that a revoked/superseded socket is actually closed
(ME-1); no test of the shipped binary's sweep wiring (CR-3, `main_test.go` checks only arg
parsing); no `TLSMode` enforcement test (HI-2); no cross-tenant presence test (ME-2); no
concurrent revoke-during-append (ME-1b). The coverage holes line up exactly with the
findings — the bugs live precisely where no test was written. What IS tested is genuinely
adversarial and trustworthy: the E2EE payload core (`untrusted_test.go`), no-plaintext /
no-identity-keys at rest (`store_test.go`, `auth_test.go`), no-bodies-in-logs
(`metadata_test.go`), and revoked-device MailboxDepth==0 + re-auth refusal
(`lifecycle_test.go`).

## Verdict and required actions

**REVISE.** The cryptographic content boundary holds and is honestly proven. The relay's
availability and control-plane-integrity obligations are not production-ready. Fix at
least CR-1..CR-4 and HI-1..HI-3 before any later phase builds on the relay, then RE-RUN
this review WITH codex (cross-model requirement) given the Fable-5 authorship.
