# Phase A — Audit Committee Report (2026-07-24)

Convened per DoD §3.3: codex (GPT-5.6 sol), sonnet, opus (agy/Gemini errored on repo access —
noted, excluded). Brief = the DoD + the Phase A diff + evidence. All read the actual code.

## Verdict: REVISE (sonnet, opus) / REJECT (codex) -> **REVISE with a required fix cycle.**

**All three confirmed the security CORE is sound:** no relay-driven forgery (no ContentKey),
replay/reorder/dup killed by the single-Accept per-(sender,epoch) seq gate + the daemon idempotency
backstop, no new cross-session keystroke misroute (the A7 sealed-session-id fix holds), the
take_control four-clause gate re-checks per keystroke, pairing is Noise XXpsk0 + 36-bit SAS +
mandatory fail-closed desktop confirm, SnapText strips C0/C1/DEL, the remote tier refuses to start
without KillSwitch+OperationClaimer. A5/A7 hold up under independent tracing.

## Consensus findings (2+ members)

### C1 [BLOCKER, unanimous] Device revoke does not sever a live lease/peek at the daemon
codex#3 + sonnet#1 + opus F1. `coreAPI.RevokeDevice` is only `devices.Remove(id)` (skeleton/api.go:159);
`controlGateOpen` clause 1 checks the GLOBAL kill switch, never whether the lease-establishing device
is still registered; `controlSession` records no deviceID (server.go:865); the peek path carries no
device identity. So after `swarm remote revoke X`, if any other device keeps the global switch on, X
keeps keystroke injection into a session it took control of for up to maxControlSessionTTL (30 min) +
keeps reading terminal peeks. The relay's ME-1 live-close is NEVER reached (the daemon-hosted revoke
doesn't call it; and ME-1 purges the device inbox, not the machine inbox where X's queued keystrokes
sit). No epoch rotation on revoke (X keeps K_content). Tests only cover removal + auto-off-on-last-
device. **Fix:** record the establishing deviceID on the lease + peek; on RevokeDevice force-release
every lease/peek whose device is no longer registered (coarse v1 acceptable: drop ALL remote leases +
peeks on any revoke). Consider epoch rotation on revoke.

### C2 [BLOCKER] "off" pauses, does not sever; gateway-restart freezes the phone; overstated claims
codex#4 + opus F2 + sonnet#2. (a) `off` re-checks per keystroke (drops input) but never tears down
the lease/connections or clears cc.control, so ON before the signed expiry reactivates the lease
without a fresh take_control/biometric — contradicts the DoD "off severs the gateway". (b) The
OUTBOUND seq counters (RelaySink.seq journal+terminal, CommandBridge.replySeq) are in-memory and
reset to 0 on gateway restart while EpochID + the phone's per-stream high-water are durable -> the
phone silently drops ALL journal + terminal + reply frames after any gateway restart until the
counter climbs back (or re-pair). The GW-H2 deferral justification only covered the INBOUND cursor;
it does nothing for this outbound reset -> the A2 "resume from the last durable cursor" claim is
false for the phone. (c) journal_subscribe is not kill-switch-gated (unlike peek), so a still-open
phone keeps getting session lifecycle events after off — "off severs everything" overstates it.
**Fix:** OFF force-releases leases/peeks (folds into C1's release path); seed RelaySink.seq +
replySeq from durable state on start (GW-H2 outbound-seq = journal cursor) OR consciously re-scope
the A2/off claims + document phone-freeze-on-restart; gate journal on the kill switch OR narrow the
"off severs everything" wording.

### C3 [BLOCKER, codex] Launch idempotency never engages + post-restart launch replay
codex#2. `daemonLaunchSpec` (server.go:51) never sets `LaunchSpec.OperationID`, so the daemon's
launch idempotency is not engaged for remote launches; combined with the gateway's in-memory seq
receiver resetting on restart (opus F6), a malicious relay can replay a signed launch (ExpiresAt
still valid) after a gateway restart and spawn a duplicate session. **Fix:** preserve OperationID
into LaunchSpec; seed the command receiver's high-water on resume (F6); add a remote-stack duplicate-
launch-across-restart test.

### C4 [BLOCKER, opus F3] Idempotency log grows unbounded (the R6 constraint, never implemented)
opus F3. `daemon.go:196` opens the idempotency store with no TTL/MaxEntries and `Compact()` is never
called, so every remote kill/delete/take_control/launch appends a permanent fsync'd record, replayed
in full on every restart — slow disk exhaustion + O(all-ops) restart replay. This is exactly the R6
constraint recorded in the A5 review (GC TTL > max command validity) that was never wired. **Fix:**
OpenWithOptions{TTL >= max command-validity horizon, MaxEntries} + schedule Compact().

## Codex-unique (adjudicated as real)

### C5 [BLOCKER for "test from phone"] Production pairing is incomplete
codex#1. `BeginPairing` creates an EpochGrant then DISCARDS it — no grant DELIVERY to the phone (over
the relay mailbox) and no relay AUTHORIZATION of the phone; the phonesim E2E injects both in-process.
So `swarm remote pair` enrolls a registry record but a REAL phone cannot obtain ContentKey or exchange
mailbox traffic. This is the "sealed EpochGrant delivery ... belongs to A2/A7" deferral — but it is
the missing call site that makes the phone usable. **Fix:** wire transactional pairing completion:
relay authorize_device + sealed grant delivery over the mailbox + ack, then registry commit/result.

### C6 [BLOCKER] Single-device v1 is assumed but not enforced
codex#6. Pairing adds records freely, but `cmd/swarm-remote/config.go:35` rejects != 1 device at
startup — so a 2nd pairing BRICKS the gateway on restart. And SenderKeyID stays zero on input frames
(the A7 cross-device residual), so a 2nd enrolled device is not cryptographically attributable and
could target another device's lease past the seq high-water. **Fix:** reject a 2nd pairing
transactionally OR bind every envelope + lease to a nonzero device sender id.

### C7 [MEDIUM] `remote init` without a relay -> `remote pair` nil-rendezvous crash
codex#8. `BeginPairing` checks config nil but calls the nil NewRendezvous unconditionally. **Fix:**
return a clean "relay not configured" error.

### C8 [MEDIUM] Phone mailbox can consume+lose command replies
codex#7. `MailboxRouter` treats every non-snapshot plaintext as a journal record; command replies have
no discriminator, so Observe can swallow a reply before ReadReply. **Fix:** one typed mailbox router
covering journal + terminal + reply + grant + push kinds.

## Hardening / design notes (follow-ups, not blockers)
- opus F4: LaunchPolicy fail-OPEN on backend misassembly (not in the construction guard) — add it or
  make the remote-tier missing-policy branch DENY.
- opus F5: no server-side upper bound on device-signed ExpiresAt (a command can be signed valid for
  years; seq + operation_id are the sole replay defense). Add a freshness cap.
- opus F7 + sonnet: read-plane has no per-device authz (shared root with C1); SnapText keeps
  U+202E/zero-width -> Trojan-Source visual spoofing in the peek. Strip bidi/zero-width in SnapText.
- sonnet#3: no admin tier (any CapFull device can revoke any other) — make it a FORMAL v1 deferral,
  not an inline code comment.
- sonnet#4: default pairing capability is "full" — echo the granted tier at the confirm prompt /
  consider a lower default.
- sonnet#6: dead unguarded envelope openers (OpenCommandEnvelope/OpenRemoteCommand) — delete/mark
  test-only.

## Divergence
codex REJECT vs sonnet/opus REVISE: codex weighted the incomplete/deferred production compositions
(C5 pairing completion, C2b GW-H2) as hard blockers; sonnet + opus weighted the shipped-code authz
gap (C1) and accepted more deferral. All three agree the core is sound and A5/A7 hold.

## Required fix cycle (before re-audit)
1. **C1** daemon-side lease/peek severance on revoke (+ deviceID on lease/peek) — unanimous top blocker.
2. **C4** wire idempotency compaction (TTL + Compact) — reliability.
3. **C3** preserve launch OperationID + seed the command receiver on resume.
4. **C2** OFF force-releases leases/peeks; fix outbound-seq durability OR consciously re-scope the
   A2/off claims (honesty); gate journal or narrow wording.
5. **C5** production pairing completion (relay auth + grant delivery) — required to actually test from
   a phone.
6. **C6** enforce single-device OR bind sender id.
7. **C7/C8** nil-rendezvous guard; typed mailbox router.
8. Hardening follow-ups + formal deferral rulings (admin tier).
Then re-run the committee; iterate until a clean verdict.
