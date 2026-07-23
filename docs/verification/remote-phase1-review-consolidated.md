# Remote Control — Consolidated Review + Remaining-Work Map (2026-07-22)

Four-agent independent review of the full remote-control state at HEAD `b9cee19`
(37 commits landed 2026-07-20 on top of the prior handoff at `2aa8187`). Panel:
opus (relay hardening re-audit + first gateway review), opus (pairing / device /
enroll / phonecore / device-auth chain), sonnet (daemon-review findings status),
sonnet (plan-of-record gap analysis). Baseline verified before review: clean pull,
`go build` / `go vet` clean, all nine remote packages green under `-race`.

Plan of record: `.claude/tmp/remote-control-implementation-plan.md`. Prior reviews:
`remote-phase1-relay-review.md`, `remote-phase1-daemon-review.md` (both REVISE).

## 1. Verdict summary per slice

| Slice | State | Verdict |
|---|---|---|
| Crypto foundation (`internal/remote/crypto`) | GREEN, reviewed, frozen | TRUSTED (unchanged) |
| Relay (`internal/remote/relay`, `cmd/swarm-relay`) | GREEN + 3 hardening rounds | E2EE payload core trustworthy; NOT yet safe as an internet-facing service (HI-2/HI-3/CR-1 residual/CR-4 default-off/ME-1..4 open) |
| Daemon foundation (journal/idempotency/protocol/daemon) | GREEN | Journal reachability + roster fixed (DHI-1/2); the A3 launch crash table (DCR-1/2) and kill/delete idempotency (DHI-3) UNTOUCHED |
| Pairing / device registry / enroll | GREEN | Trustworthy as wired; SAS is 24-bit and grindable by a PSK-holding MITM (MED) |
| Phonecore (`internal/phonecore`) | GREEN, ~4/13 of R-PHC | Command signing solid; journal receive has NO replay/seq/gap enforcement (HIGH, latent) |
| Gateway (`internal/remotegw`) | GREEN, first review | E2EE + command-authz sound; journal delivery can silently LOSE events (HIGH); no binary (`cmd/swarm-remote` missing) |
| Device-auth choke point (`internal/skeleton`) | GREEN | Fail-closed and E2E-proven for kill/delete/launch; attach/resize/input are UNGATED on the remote tier (HIGH, latent) |
| Policy (R-POL.2-.8), kill switch (R-KS), CLI, TUI, revocation orchestration, phonesim, iOS/NSE/DSN, ops | — | NOT STARTED |

No slice regressed; everything green is genuinely green under `-race`. The risk is
concentrated in what is MISSING or UNGATED, not in what is built.

## 2. Consensus findings (2+ independent reviewers — treat as real)

1. **No kill switch anywhere on the remote path** (gap analysis: R-KS zero code;
   gateway review GW-M3; phone-chain review MED-2). `CodeKillSwitch` is a dead enum.
   There is no durable way to disable remote access short of deleting keys. ADR-007
   D10 makes this a Phase-1 invariant.
2. **Remote launch executes with no policy layer** (gap analysis: R-POL.2-.8 zero
   code; phone-chain HIGH-1). No allowed-cwd-roots, no dangerous-options denylist,
   and env is FILTERED, not dropped — sharpened by: `LaunchContentHash` EXCLUDES
   `Env` on the premise remote drops it, so env is an unauthenticated channel a
   compromised gateway can inject under a valid device signature
   (`internal/protocol/launchhash.go:20-24`, `server.go:36-46`).
3. **Remote input is both missing and ungated** (gap analysis: R-GW.7 — a phone
   cannot type into a session today; phone-chain HIGH-2 — `OpAttach`/`OpResize`/
   `OpDataIn` dispatch on the remote tier with NO `requireRemoteAuthz`,
   `server.go:826-831`). A compromised gateway dialing `remote.sock` can inject
   keystrokes with no device signature. Fail-close now; signed `take_control` later.
4. **Journal receive chain has no replay/seq/gap enforcement end-to-end**
   (gateway review GW-H2/GW-M2; phone-chain HIGH-3). `crypto.MailboxReceiver`
   exists, is reviewed, and is used ONLY in tests. Phonecore applies replayed older
   envelopes unconditionally (`phonecore/journal.go:65-86`); the gateway seals with
   a per-process seq counter that resets on restart (`relaysink.go:82-84`),
   violating ADR-007 D6 (seq must BE the journal cursor) and guaranteeing a
   post-restart wedge the moment gap detection is wired.
5. **Launch-from-phone reliability is broken across daemon crashes** (daemon-status:
   DCR-1/DCR-2 OPEN, unchanged since the review that rejected the deviation).
   Kill -9 windows W1/W2 permanently poison an `operation_id` (phone can never
   retry); W3/W4 report a dead or ORPHANED session as a successful launch. No
   compaction is wired (poison is forever, log grows unbounded). Kill/delete bypass
   idempotency entirely despite requiring `operation_id` on the wire (DHI-3).

## 3. Notable single-reviewer findings

- **GW-H1 (HIGH)**: gateway advances its journal cursor BEFORE the relay acks the
  append (`gateway.go:198-202`, `relaysink.go:99-101`) — one transient relay
  failure = permanent silent event loss. Violates R-GW.5.
- **Relay CR-4 residual**: mailbox depth/byte quota is OFF BY DEFAULT in the shipped
  binary (`config.go:86-93`, guard at `server.go:719`); storage exhaustion open.
- **Relay CR-1 residual**: one IP can fill the 4096-conn global pool and hold it
  with unmetered `hello` trickle (per-read deadline re-arms, `server.go:415,556`).
- **Relay HI-2**: `TLSMode` is consulted nowhere — `tls_mode:"on"` silently serves
  plaintext ws://; no channel binding in auth.
- **Relay HI-3**: `authorize_device` is unilateral (no device consent proof) and any
  self-generated key can authenticate (no machine allowlist) → pair-and-flood.
- **Relay ME-1**: revoke is 3 non-atomic txns, never closes the live socket, TOCTOU
  vs append resurrects the mailbox.
- **SAS MED-1**: 24-bit SAS (`sas.go:52`) + no ephemeral commitment → a
  PSK-holding MITM can grind ~2^24 keypairs to force matching SAS on both legs and
  substitute its own command-signing key in msg3. Preconditions are narrow (leaked
  QR + MITM position) but this is exactly the case SAS exists for. Widen to 5-6
  emoji or add an initiator ephemeral commitment.
- **GW-M1**: command mailbox never acked; restart replays up to 7 days of commands
  (bounded only by daemon idempotency + expiry).
- **DME-2**: `journal_subscribe` returns no cursor and does no backfill — a phone
  doing read-then-subscribe can silently miss events in the gap.

## 4. What is genuinely solid (verified, one line each)

- Crypto foundation: frozen, reviewed, correctly used everywhere it IS used.
- Device-signature choke point for kill/delete/launch: content-binding, expiry,
  capability from registry (never the wire), fail-closed on every unknown; proven
  by real-stack E2E including a tampering-gateway refusal.
- Enrollment keystone: sealed epoch grant reaches only the paired device;
  unpaired/ungrated devices cannot read journal traffic.
- E2E tests (`internal/skeleton/*_e2e_test.go`, `remotegw/relay_e2e_test.go`)
  exercise the REAL relay + crypto + gateway + daemon, no security stubs.
- Assembled daemon now really serves `journal_read`/`journal_subscribe` on a
  dedicated opt-in remote socket with an atomic roster snapshot (DHI-1/2 closed).

## 4b. Fix-pack progress log (updated as slices land)

TDD, independent roles (opus test-writer + separate opus implementer, Fable orchestrates).

- **Item 3 (HIGH-2) fail-close remote-tier attach/resize/input** — DONE. RED `eb4cec0`,
  GREEN `3442322`. Three tier-scoped guards in server.go; owner tier unchanged; `-race` green.
- **Item 6 (HIGH-3) phone journal replay/reorder/gap guard** — DONE. RED `53732a3`,
  GREEN `b8bdcec`. `JournalReceiver` over the frozen `MailboxReceiver` + monotonic-cursor
  guard on `SessionCache.Apply`; `-race` green.
- **Item 2a (R-KS.1) kill-switch enforcement at the protocol choke point** — DONE. RED
  `bb9dfed`, GREEN `3958c13`. Optional `KillSwitch` interface consulted as the FIRST gate in
  `requireRemoteAuthz`; disabled => `CodeKillSwitch` before signature work. `-race` green.
  2b (durable remote-state.json + enroll wiring + R-KS.2 auto-off) still to do.
- **Item 4a (CRITICAL DCR-1/DCR-2) launch crash-window fix** — DONE (independent
  adversarial re-derivation of the crash table in progress as a gate). RED `ed17303`, GREEN
  `a39cfc7`. Liveness-based replay (return recorded session only if present-and-not-Lost;
  else Redrive under the same operation_id) + Open-time resolver failing stale
  prepared/executing launch records. W1 poison and W3 silent-corpse closed; W4 live-orphan
  double-spawn is the documented ceiling (skipped test). `-race` green.
- Remaining fix-pack: 4b kill/delete/interrupt idempotency (DHI-3); 2b kill-switch durable
  state + auto-off; item 1 launch policy (R-POL.2-.8 — the biggest safety gap); item 5
  gateway reliability (GW-H1/H2/M1/M2 — note GW-H2 changes RelaySink seq to the journal
  cursor, which must reconcile with item 6's test setup); item 7 relay round 3; item 8 SAS
  widening (needs an ADR — crypto is frozen).

## 5. Remaining work to "usable from a phone, daily" (dependency-ordered)

Sizes: S = day, M = few days, L = 1-2 weeks, XL = multi-week.

**FIX PACK (safety + reliability, before any real phone connects)**
1. R-POL.2-.8 launch policy: drop env on remote tier (or bind it into the content
   hash), allowed-cwd-roots fail-closed, dangerous-options denylist, policy config
   file. (L) — consensus #2.
2. R-KS kill switch + activity log, checked fail-closed in `requireRemoteAuthz`
   before signature work; `swarm remote off` severs. (M) — consensus #1.
3. Fail-close `OpAttach`/`OpResize`/`OpDataIn` on the remote tier NOW. (S) — #3.
4. Daemon idempotency: atomic opID+meta durability, phase-aware replay (never
   return prepared/executing as success), Open-time resolver for stale records,
   TTL/Compact wired from config, kill/delete through the store. (M-L) — #5.
5. Gateway reliability: ack-gated cursor (GW-H1), Seq = journal cursor (GW-H2),
   MailboxAck + durable cursor (GW-M1), inbound envelopes through
   `MailboxReceiver` (GW-M2). (M)
6. Phone receive integrity: `MailboxReceiver` on the journal path; `SessionCache`
   rejects non-monotonic cursors, gap triggers resync. (S-M) — #4.
7. Relay hardening round 3: depth cap ON by default, per-source concurrent-conn
   cap + cumulative handshake deadline, enforce TLSMode (or remove it) + channel
   binding, device-consent pairing proof + machine allowlist, atomic revoke that
   closes the live socket. (M)
8. SAS widening / ephemeral commitment. (S)

**PHASE-1 PRODUCT COMPLETION**
9.  R-PROT.4/.5 ops: `device_list`, `device_revoke`, `policy_query`,
    `pair_pending`/`pair_confirm` events. (S-M) — prerequisite for 10-12.
10. R-CLI: `swarm remote {init,pair,devices,revoke,off,on,status}`. (M)
11. R-TUI: pairing confirm prompt (required by R-PAIR.5), devices pane, remote
    status indicator. (M)
12. R-DEV.3-.5: revocation -> epoch rotation -> re-grant survivors -> purge
    undelivered; multi-epoch keyring. (M) — no `Rotate*` exists anywhere yet.
13. R-GW.6/.7: signed `take_control` + remote input riding the existing lease. (L)
14. `cmd/swarm-remote` binary + launchd/systemd unit + `swarm remote init`. (M)
15. R-PHC completion: snapshot renderer/sanitizer (a phone cannot render a
    terminal today), pairing state machine, machine registry/presence, on-device
    persistence, gomobile surface rules + structural test, approval binding,
    launch builder, biometric gate token, capability negotiation, reconnect
    backoff+jitter. (L)
16. R-SIM: `cmd/phonesim` driving the real phone-core; the 8 R-E2E.3 scenarios.
    (M-L)

**THE APP + RELEASE**
17. R-IOS/R-NSE/R-DSN: SwiftUI app, notification service extension, design system.
    ZERO Swift code exists. Requires Xcode + Apple developer account (not on this
    machine). (XL)
18. On-device cross-language KAT gate (Swift/CryptoKit interop). (S, on-device)
19. Ops: relay Dockerfile/systemd, TLS termination runbook, VPS provisioning,
    key-backup UX, onboarding docs. ZERO artifacts exist. (M)
20. R-ADV floor as an evidenced suite + R-E2E rollup evidence + R-GATE.3. (S-M
    after the above; several R-ADV items unimplementable until 2/4/12 land.)

**PHASES 2-3** (spike-gated, per plan): chat/transcript on phone, approvals
(`interaction_request` one-tap), launch-from-phone polish, Android/Live
Activities. Note: an `approve` capability class exists but NO approve wire op or
daemon workflow — Phase-2 scope, do not mistake the scaffolding for the feature.

## 6. Decisions (user, 2026-07-22)

1. **Scope order: SAFETY FIX PACK FIRST** (items 1-8 above), then Phase-1
   completion in plan order.
2. **Client strategy: iOS AND Android, both first-class.** This AMENDS the
   plan's iOS-first/Android-Phase-3 stance (ADR-007 D12) — write the ADR
   amendment when client work starts. Consequence: the gomobile-bound Go
   phone-core becomes the single shared core (it binds to both an iOS
   xcframework and an Android AAR), with two thin native UIs. Android is
   buildable and testable on THIS machine (no Apple account needed), so an
   Android build is likely the first real on-phone artifact; iOS follows when
   an Xcode + Apple developer account environment is provided.
3. **Relay hosting: VPS + reverse proxy** (Caddy/nginx terminates TLS in front
   of swarm-relay). Ops item 19 delivers Dockerfile/systemd + runbook for this
   shape; relay round 3 removes the dead TLSMode flag and adds channel binding
   rather than in-process TLS.
4. **ADR-007 status**: still "Proposed"; ratification language says Phase-1
   close — ratify (with the D12 amendment) at R-GATE.3.
