# Phase A — Audit-Committee Fix Cycle: Closure (2026-07-24)

Closes the required fix cycle from `remote-phaseA-committee.md` (verdict REVISE). Every
consensus blocker (C1-C8) and every hardening/deferral follow-up (F4/F5/F7, sonnet#3/#4/#6,
ME-1) is resolved below, mapped to its fix commit + the test that pins it. Standing gate re-run
after the last fix: `go build ./...`, `go vet ./...`, `go test -race ./...` -- ALL GREEN (0 fail
across the tree).

## Consensus blockers

| # | Finding | Resolution | Commit | Test(s) |
|---|---------|-----------|--------|---------|
| C1 | Device revoke does not sever a live lease/peek at the daemon | `controlSession` records the establishing `deviceID`; `severControl(pred)` releases matching leases + cancels peeks on revoke; input gate re-checks device-still-registered per keystroke | `ec1cb42` | protocol revoke-sever tests |
| C2a | `off` pauses but does not sever; journal not kill-switch-gated | `off` (and last-device revoke) proactively SEVER every lease + peek via `SeverAllRemoteControl`; journal_read/subscribe + fan-out gated on the kill switch (resume requires fresh take_control) | `e0630bc` | protocol OffSeversLiveLease / OffSeversLivePeek / OffGatesJournalSubscribe; skeleton ManualOffSeversLiveTakeControlLease |
| C2b | Outbound seq resets on gateway restart -> phone silently drops all frames | Durable batch-reserve-ahead seq (fsync file+dir, resume at ceiling+1) for journal/terminal + reply streams | `ab742ab` | remotegw OutboundSeqSurvivesRestart / ReplySeqSurvivesRestart |
| C3 | Launch idempotency never engages (OperationID dropped) | `daemonLaunchSpec` preserves `OperationID` into the LaunchSpec; remote launch replay across restart returns the cached outcome | `ec1cb42` | protocol RemoteLaunchOperationIDEngagesIdempotency |
| C4 | Idempotency log grows unbounded (R6 never wired) | `OpenWithOptions{TTL 24h, MaxEntries 100k}` + Compact-on-Open + hourly compactLoop | `5092e03` | daemon idempotency compaction tests |
| C5 | Production pairing incomplete: sealed grant discarded, never delivered | Daemon PERSISTS the sealed grant (`internal/remote/grant` sidecar); gateway `AuthorizeDevice` + MailboxAppends a tagged bootstrap frame; phone `NewFromMailbox` reads + AcceptGrants it off the mailbox (no in-process injection) | `b63a640` (machine), `7f00f29` (phone) | skeleton Pairing_PersistsSealedGrant; swarm-remote DeliverEpochGrant_AuthorizesAndAppendsBootstrap; skeleton GrantDeliveredOverMailboxBootstrapsE2E |
| C6 | Single-device assumed but not enforced (2nd pairing bricks the gateway) | BeginPairing refuses a 2nd pairing fail-fast (Count>0); multi-device (SenderKeyID binding + admin tier) formally deferred | `b63a640` | skeleton BeginPairing_RefusesSecondDevice |
| C7 | `remote pair` without a relay -> nil-rendezvous crash | Guard `cfg.NewRendezvous == nil` -> clean "relay not configured" error before the call | `b63a640` | skeleton BeginPairing_NilRendezvousFailsCleanly |
| C8 | Phone mailbox swallows command replies as journal | Typed mailbox router: explicit kind switch (snapshot/reply/grant/push/journal), unknown kind fails closed; SealControlReply stamps `command_reply` | `083b774` | phonecore MailboxDemux_CommandReplyNotJournaled / GrantAndPushNotJournaled / JournalUnaffected |

## Hardening / deferral follow-ups

| # | Finding | Resolution | Commit |
|---|---------|-----------|--------|
| F4 | Remote launch fails OPEN when the backend exposes no LaunchPolicy | Remote-tier launch refused CodePolicy when no LaunchPolicy present (owner tier untouched) | `3bc3692` |
| F5 | No server-side upper bound on device-signed ExpiresAt | `maxCommandValidity = 1h` cap in requireRemoteAuthz (applied uniformly incl. take_control) | `ec1cb42` |
| F7 | SnapText keeps U+202E / zero-width -> Trojan-Source spoofing in the peek | stripControls drops the bidi formatting/override/isolate set + zero-width set | `3bc3692` |
| sonnet#3 | No admin tier (any CapFull device can revoke any other) | Formal v1 deferral (moot under single-device C6); ADR-007 2026-07-24 | `b63a640` |
| sonnet#4 | Default pairing capability is "full", not echoed at confirm | `swarm remote pair` confirm echoes "Capability to grant: <tier>"; default "full" kept (personal single-owner tool) with the tier now visible | `3aff4cb` |
| sonnet#6 | Dead unguarded envelope openers (replay-bypass footgun) | OpenCommandEnvelope / OpenRemoteCommand marked UNGUARDED + TEST-ONLY; production uses OpenRemoteCommandGuarded | `3aff4cb` |
| ME-1 | Relay-socket close on revoke unreached from the daemon path | Formal Phase-B deferral: C1+C2a close the injection/read hole at the daemon (tested); relay close is transport hygiene with near-zero marginal security, needs disproportionate cross-process infra. Mechanism sketched (gateway holds the relay client post-C5). ADR-007 2026-07-24 | `3aff4cb` |

## Design decisions recorded (ADR-007 amendment 2026-07-24, "Phase-A audit-committee closure")
- Grant delivery mechanism (C5): out-of-band over the relay mailbox via the gateway (implements the
  2026-07-23 deferral); the bootstrap grant is recipient-sealed (delivers the ContentKey), distinct
  from the ContentKey-sealed router epoch_grant rotation frame.
- Single-device v1 (C6) + admin-tier deferral (sonnet#3); multi-device requires a frozen-crypto
  SenderKeyID-binding ADR.
- ME-1 relay-close deferral (formal ruling).

## Standing gate (post-fix-cycle)
`go build ./...` clean; `go vet ./...` clean; `go test -race ./...` -- 0 failures across the tree.
TDD failing-first evidenced per fix (RED assertion named in each commit). No existing test assertion
was weakened; several tests gained a now-required precondition (switch-on for journal, a permissive
LaunchPolicy fixture, a registered device) with the justification recorded in each commit.

## Re-audit ROUND 2 (2026-07-24) — codex REJECT / sonnet REVISE / opus REVISE

The re-audit (codex + sonnet + opus; agy errored on repo access again) confirmed the crypto core +
C1-C4/C6-C8/F4/F5/F7 sound, but found real bugs the first cycle missed or introduced. All addressed:

| Finding (reviewer) | Resolution | Commit |
|---|---|---|
| C8 not integrated in the phone path -- Observe/ReadReply swallow each other (codex#3, opus F1) | One `drain()` both call: every item through the router once, one cursor; ReadReply drains router.Replies(), OpenControlReply bypass removed | `475d1fa` |
| C5 bootstrap poison-frame DoS + single-page read (sonnet#1, opus F2/F5, codex) | NewFromMailbox loops MailboxReadPage across pages, skips frames that fail to open, returns the first that opens | `475d1fa` |
| No phone MailboxAck -> mailbox fills (codex#5) | drain() acks the consumed prefix each sweep (gap-resync deferred: needs a re-request channel) | `475d1fa` |
| off/take_control race -> silent resume (codex#2) | severGen counter: take_control captures before authz, re-checks under ctlMu, releases + fails closed if a sever advanced it | `7768a83` |
| Journal kill-switch gate not remoteTier-scoped -- owner tier wrongly gated (sonnet#2) | Gate journal ops + fan-out on cc.srv.remoteTier && remoteControlDisabled() | `7768a83` |
| off leaves journal subs silently armed (codex#7, opus F7) | SeverAllRemoteControl closes remote journal subscribers -> fresh journal_read (resync) on reconnect | `7768a83` |
| Launch check-on-resolved/use-on-original (opus F6) | Thread the resolved cwd into the launch spec (ADR-007 D8) | `7768a83` |
| C6 single-device non-atomic -> concurrent pairings brick the gateway (codex#6, sonnet#6, opus F4) | Atomic Registry.AddSole (reject a 2nd distinct device under the mutex); pairing commits via it | `e8741db` |
| Enrollment non-transactional; grant.Save no dir-fsync (codex#6, opus F8) | Roll back the device on grant-Save failure; fsync the grants dir after rename | `e8741db` |
| Grant sidecar never cleaned on revoke (sonnet#5) | grant.Delete + call from RevokeDevice | `e8741db` |
| Production PhoneTarget trusts a self-reported, untested routing id (opus F3) | Derive PhoneTarget = relay.RoutingID(rec.RelayAuthPub) (canonical); tested over real resolveGatewayParams | `e8741db` |
| Idempotency compaction not crash-safe; dead-store on failure (codex#8) | fsync the dir after rename; keep the old handle usable on any failure (never dead-store) | `082a9ba` |
| Outbound SenderKeyID asymmetry undocumented landmine (sonnet#3) | Documented the intentional sender-zero-for-replies bucket separation | `4fde9a1` |
| Race-gate flake TestRemotePeek_LargeGridClipped... under load (codex#9) | Raise the shared test recvTimeout 2s -> 5s | `cadbbbd` |
| **Revoke does not rotate the epoch key -> revoked phone reads a re-paired phone (codex#1)** | **Revoke ROTATES the machine epoch key (machineid.RotateEpoch + persist + reload pairing snapshot); revoked key dead for future traffic. Operator-directed. ADR-007 2026-07-24.** | `a653089` |

### Honest deferrals (round 2)
- **"A real phone can pair" -- lifecycle glue is Phase B.** The machine-side grant delivery + the
  phone-core mailbox bootstrap are complete and tested (`b63a640`/`7f00f29` + the routing-id fix
  `e8741db`), but there is no mobile app and no gateway auto-start/supervision post-pair (G3): the
  operator runs `swarm remote pair` then (re)starts `swarm-remote`. Phase A proves the COMPONENTS and
  the delivery/bootstrap path E2E; the production lifecycle glue + real device client are Phase B/C.
- **Mailbox gap-resync deferred** (codex#5 second half): the phone acks and detects a seq gap but does
  not yet re-request lost frames (no such channel exists; a full-resync request is a Phase-B protocol
  addition). At-least-once + monotonic seq means a dropped frame surfaces as a gap, not silent corruption.
- **ME-1 relay-close, multi-device/SenderKeyID binding, admin tier** -- unchanged Phase-B deferrals
  (ADR-007 2026-07-24), reaffirmed honest by sonnet + opus.
- **kind-string literal dedup** (sonnet#6, LOW) -- not done; C8's fail-closed default makes any drift a
  loud error, not a silent misroute, so a shared-constants refactor is deferred as non-load-bearing.

Standing gate after round 2: `go build/vet/test -race ./...` -- 0 failures. Re-audit round 3 follows.

## Re-audit ROUND 3 (2026-07-24) — codex REJECT / sonnet REVISE / opus REVISE

Round 3 confirmed the round-2 security core sound (all three reviewers: severGen race, journal
remoteTier scoping, AddSole atomicity, sequential mailbox demux, canonical routing, old ContentKey
genuinely dead, no orphaned durable state). It found that the epoch-rotation fix did not fully compose,
plus durability/DoS edges. All addressed:

| Finding (reviewer) | Resolution | Commit |
|---|---|---|
| Epoch TOCTOU: concurrent revoke rotates mid-BeginPairing -> replacement enrolled under stale epoch (codex/sonnet/opus UNANIMOUS) | Re-validate the epoch at the commit point; abort fail-closed if a.pairing.EpochID changed since the handshake snapshot | `6310280` |
| Revoke not crash-atomic: rotate-after-remove reopens the hole on a crash between (codex#3, opus#3) | Rotate BEFORE remove ("removed => rotated" invariant); a rotation fault aborts the revoke | `6310280` |
| machineid.Save / Registry.persistLocked miss dir-fsync; grant.Delete no dir-sync + error swallowed (codex#3, opus#5, sonnet#3) | dir-fsync after rename in both; grant.Delete dir-syncs the unlink; RevokeDevice surfaces the delete error | `6310280` |
| Crash between AddSole and grant.Save leaves an inert device holding the slot (codex#6, sonnet#3, opus#4) | Startup reconcile clears a registered device with no grant sidecar (machine.key-gated, fail-safe) | `6310280` |
| **Gateway keeps the old epoch key + serves the revoked device after re-pair (codex#1 CRITICAL, opus#2)** | **Gateway EXITS when its device is no longer registered (re-check on reconnect -> ErrDeviceRevoked), during the deviceless window before re-pair -- the v1 closure of codex#1 in the composed system** | `a48887c` |
| Mailbox gap silently discarded (closure overstated "detects a gap") (codex#5, sonnet#4) | drain surfaces a sticky Stale() flag + stops acking past a detected gap (full resync stays Phase B) | `58c91ef` |
| Hostile pagination spins forever on non-advancing has_more (codex#7) | Both scan loops break (errStuckPage) when a page fails to advance the cursor | `58c91ef` |
| drain not concurrency-safe (application-order under concurrent Observe/ReadReply) (sonnet#2) | drainMu serializes the whole sweep (crypto seq dedup already prevented drop/double-count) | `58c91ef` |
| Idempotency compaction loses records after a post-rename failure (old handle = unlinked inode) (codex#4) | Keep the tmp handle open through the rename; swap s.f = tmp before dir-sync (never the ghost inode) | `94d9b62` |

### Deferrals reaffirmed / re-scoped (round 3)
- **Mailbox gap RESYNC** stays Phase B, but the phone now DETECTS + SURFACES a gap (Stale()) and stops
  acking past it, rather than silently trusting a stale cache -- the round-2 wording is now true.
- **ME-1 relay-socket close** remains Phase-B, and is no longer load-bearing for the revoke
  confidentiality property now that the gateway EXITS on revoke (opus#2 noted it had become load-bearing;
  the gateway-exit fix removes that dependency).
- **A live in-place gateway epoch-reload** (fsnotify/signal) stays Phase B; exit-on-revoke is the v1
  closure. Real-phone lifecycle glue (mobile app, gateway supervision/G3), multi-device/SenderKeyID
  binding, admin tier -- unchanged Phase-B deferrals.
- **opus#6** (take_control fail-closed emits OpDetach not a distinct retry code) -- cosmetic client
  nicety, deferred.

Standing gate after round 3: `go build/vet/test -race ./...` -- 0 failures. Re-audit round 4 follows.
