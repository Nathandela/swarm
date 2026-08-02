# A5 take_control — Cross-Model Review Evidence (2026-07-23)

Per DoD §0, the two security-critical slices (A5 input backend, A7) require cross-model
review (codex + independent opus) recorded as an evidence file. This is A5's.

**Panel:** codex (GPT-5.6 sol, read-only) + an independent opus agent, both given the same
threat model + the full A5-a..A5-d production diff, both told to assume the work is flawed.

## Verdict

**The A5 daemon-side gate is SOUND.** Both models independently confirmed, and I concur after
adjudicating: no authz bypass, no establishment replay, no cross-device injection, no
stale-generation keystroke landing, kill-switch is the first gate inside requireRemoteAuthz
and fail-closed there, the raw `SHA256(GateToken)` is genuinely bound into the signed tuple
(swapped/absent token refused), capability is read from the pinned registry (CapFull-only,
never the wire), and single-use is durable across a crash. Opus: "No CRITICAL, demonstrable
bug exists in the A5 diff as written." The 14-attack suite pins the right refusals.

The findings below are **hardening + one required design decision**, not a demonstrated
break of the code that exists.

## Adjudicated findings

### R1 (HIGH, DESIGN DECISION — not an A5 code bug): keystroke-transport trust boundary
Both models flagged that keystrokes are unsigned and "ride the session." Opus established
why this is NOT a live A5 bug: there is **no `TDataIn` producer anywhere in `internal/remotegw`
or `internal/phonecore`** — the input data-plane does not exist yet; A5 built only the
daemon-side session gate. The **relay** cannot forge keystrokes (it lacks the ContentKey; the
GW-M2 MailboxReceiver seq-gates the command-IN path). The open question is the **gateway's**
data-plane integrity — which ADR-007 already documents as the owner-uid residual (a compromised
owner-uid gateway is outside the crypto boundary; dedicated-uid/sandbox hardening deferred).
My review brief overstated "the gateway is untrusted for integrity," which inflated codex's
severity to CRITICAL; opus's HIGH-with-context framing is the accurate one.
**REQUIRED BEFORE A7 WIRES REAL INPUT:** decide + document the keystroke trust boundary —
either (a) treat the co-located gateway as trusted for data-plane integrity (the relay is the
real adversary and cannot forge), or (b) authenticate the data plane end-to-end to the daemon
(keystrokes sealed under a key the gateway does not hold, or a per-session MAC keyed by material
bound into the signed take_control). Do NOT ship a keystroke transport that reuses the command
envelope pattern without resolving this. Tracked as an A7 blocker + an ADR-007 decision.

### R2 (FIX NOW): fail-OPEN on absent OperationClaimer / KillSwitch optional interfaces
Both models: the gate-token present-check, single-use claim, and kill-switch checks use
`if impl, ok := cc.srv.d.(X); ok && !allowed` — a MISSING backend reads as "allowed," not
"deny." Production `coreAPI` implements all of them, but the `var _ protocol.X = (*coreAPI)(nil)`
pin only proves coreAPI IMPLEMENTS the interface, not that the remote Server is CONSTRUCTED with
it — and `protocol.FromDaemon` is exactly a DaemonAPI adapter that could forward
`DeviceAuthenticator` while dropping `OperationClaimer`/`KillSwitch`, silently yielding
authorized-but-replayable-and-unkillable take_control with no compile error or test failure.
`DeviceAuthenticator` already fails CLOSED; these must too.
**FIX (slice A5-e):** on the remote tier, take_control refuses fail-closed when a required
guard (OperationClaimer, KillSwitch) is absent — the remote server must not grant control it
cannot make single-use or cannot kill. Updates the protocol test stubs to implement the
now-required interfaces (test-infra, assertions preserved).

### R3 (FIX NOW): replayed old take_control_end desyncs state (codex F7)
`handleTakeControlEnd` clears `cc.control` BEFORE checking the session/generation match, so a
delayed end for gen N replayed after the same connection establishes N+1 shuts the N+1 input
gate (while releaseLease correctly refuses to release N+1) — leaving lease-owned-but-input-dead.
**FIX (A5-e):** clear `cc.control` only when its target+generation match the end request.

### R4 (FIX NOW): handleResize forwards on wire identity, not server identity (opus LOW-3)
`handleResize` gates on `cc.attSession/attGen` but forwards on wire `c.SessionID/c.Generation`
(unlike `handleDataIn`, which uses the server identity). Safe today only because forwardResize
backstops it. **FIX (A5-e):** forward resize on `cc.attSession/attGen` for gate/forward identity
consistency.

### R5 (FIX NOW, fail-safe): TTLSeconds int64 overflow (opus LOW-1)
`time.Duration(c.TTLSeconds) * time.Second` overflows for TTLSeconds ≳ 9.2e9 → negative ttl →
immediately-expired session (fails safe, but violates the "never immediately-expired" invariant).
**FIX (A5-e):** clamp the lower bound (`if ttl <= 0 { ttl = default }`) after the multiply.

### R6 (RECORD for the deferred idem-GC L3/DME-1): GC TTL must exceed max command validity
Opus MED-2: the single-use `prepared` records never Compact (unbounded disk/mem/startup growth),
and a naive TTL Compact keyed on UpdatedAt could drop a record before its command ExpiresAt and
REOPEN the replay hole (ExpiresAt is not stored in the record). **CONSTRAINT on L3/DME-1:** any
idempotency GC must use a TTL comfortably larger than the maximum accepted command ExpiresAt
horizon, and should persist ExpiresAt so the safe-GC horizon is explicit. Not an A5 blocker
(growth is slow; each record needs a CapFull-signed op).

### R7 (A7 REQUIREMENT): bind the control-session lifetime to the signed ExpiresAt (codex F4 / opus MED-1)
TTLSeconds is unsigned wire data; the session lifetime should be bound to what the device
signed. The clean design is expiry = min(signed command ExpiresAt, now + server-max), which
uses the ALREADY-signed ExpiresAt — but that requires the phone to sign take_control with an
ExpiresAt = desired session end (a phone-core/A7 decision). **Recorded as an A7 requirement**;
the A5-e cap can additionally bound the session at the signed ExpiresAt where present without
removing the TTL hint (preserving the committed A5-b expiry test).

### R8 (documented narrow residual): one-frame TOCTOU after emergency kill-off (codex F6 / opus LOW-2)
`controlGateOpen` checks kill/expiry, releases locks, then forwardInput re-checks only
controller/generation. A kill-switch flip from ANOTHER connection between the gate check and the
shim write could let ONE in-flight keystroke land post-off (sub-ms window; every subsequent
keystroke dropped). Both models rate this low/negligible. The fix (re-check kill/expiry under
ls.inMu) complicates the owner-shared forward path, so it is recorded as an **accepted narrow
residual**: at most one in-flight keystroke after an emergency off. Revisit if the kill switch
must be authoritative to the last in-flight byte.

## Actions
- A5-e hardening slice: R2 (fail-closed guards), R3 (end gen-match), R4 (resize identity),
  R5 (TTL lower clamp), and the R7 ExpiresAt cap (additive). Cross-model-reviewed changes to a
  security-critical path.
- A7 blockers recorded: R1 (keystroke transport decision + ADR), R7 (phone signs the lifetime).
- L3/DME-1 constraint recorded: R6 (GC TTL > max command validity; persist ExpiresAt).
- Verdict: A5 gate TRUSTED for the daemon-side contract; the end-to-end keystroke-injection
  property is undecidable from this slice and gated on the R1 decision before real phones type.
