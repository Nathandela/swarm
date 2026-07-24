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

## Re-audit
The full `/audit-committee` is re-run against this closure; iterate until a clean verdict.
