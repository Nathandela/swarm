# Remote Phase A — A5 (take_control) adversarial acceptance evidence

**Slice**: A5 (`take_control` — the signed, lease-bound remote-input gate), sub-slice **A5-d**
(adversarial suite closeout).
**Status**: all 14 acceptance attacks pinned by a test that PASSES against current code. Two
gaps in the map (attacks 10 and 14) were closed by adding dedicated confirmation tests; both
pass immediately (they confirm gates A5-a/b/c already landed — no new production code). Attack
12 is documented N/A by architecture.

## What A5 defends

A signed one-shot `take_control` op establishes a bounded (TTL + explicit end),
lease-bound control session; keystrokes then ride that session with no per-keystroke
signature. Remote `OpDataIn`/`OpResize` reopen ONLY inside a live, authorized control
session and stay fail-closed everywhere else. The gates, in the order an attack meets them:

1. **Kill switch** (`requireRemoteAuthz`, first gate; re-checked per keystroke as
   `controlGateOpen` clause 1) — `internal/protocol/server.go`.
2. **`requireRemoteAuthz` choke point** — operation_id present, device identity present,
   Ed25519 signature verifies over the canonical tuple `(Action, Machine, Session,
   OperationID, ExpiresAt, ContentHash)` (`crypto.Command.Canonical`), and the device's
   capability permits `ActionControl` (`actionClass`).
3. **Gate-token present-check + content-hash binding** — `handleTakeControl` requires a
   non-empty `GateToken` and passes `ContentHash = SHA256(wire GateToken)` into the authz
   tuple (A5-c).
4. **operation_id single-use** — `ClaimOperation` (idempotency `Prepare`) refuses a replay.
5. **The four-clause input gate** `controlGateOpen` (A5-b): (1) kill switch still ON,
   (2) `cc.control != nil` (per-connection fail-closed default), (3) `now < expiry`
   (lazy expiry on the server clock), (4) `ctl.target == cc.attSession && ctl.leaseGen
   == cc.attGen` (still bound to this connection's current lease).
6. **Server-side generation gate** in `forwardInput`/`forwardResize` (stale generation is
   dropped, never reaches the shim) — the final serialization point.

## The 14-attack acceptance bar → refusal mechanism → pinning test

| # | Attack | Expected | Refusal mechanism (gate/clause) | Pinning test (file : function) | Status |
|---|--------|----------|---------------------------------|--------------------------------|--------|
| 1 | Replay of a valid `take_control` (same op_id) | refused (single-use) | operation_id single-use: `ClaimOperation`/idempotency `Prepare` returns existed → refuse (`CodeStaleApproval`), no second attach | `internal/skeleton/takecontrol_gatetoken_test.go : TestSkeleton_TakeControlReplayedOperationIDRefused` | covered (A5-c) |
| 2 | Dup / reorder of `take_control` | refused (single-use) | same op_id single-use gate — a duplicate or reordered copy carries the consumed operation_id, so `Prepare` refuses (a reorder is indistinguishable from a replay at this layer) | `internal/skeleton/takecontrol_gatetoken_test.go : TestSkeleton_TakeControlReplayedOperationIDRefused` | covered (A5-c) |
| 3 | Expired session then `OpDataIn` | keystroke dropped | `controlGateOpen` clause 3 — `now >= expiry` (lazy expiry on server clock via `serverNowNS` seam) | `internal/protocol/takecontrol_input_test.go : TestProtocol_InputAfterSessionExpiryDropped` | covered (A5-b) |
| 4 | Wrong/stale lease generation (input at gen N after supersede to N+1) | dropped | `controlGateOpen` clause 4 (`ctl.leaseGen == cc.attGen`) + server-side generation gate in `forwardInput`/`forwardResize` (stale gen never reaches the shim) | `internal/protocol/lease_test.go : TestLease_StaleGenerationInputDroppedServerSide` and `TestLease_StaleGenerationResizeDropped` | covered (E6.4 gen gate + A5-b clause 4) |
| 5 | Missing gate token | refused | `handleTakeControl` present-check — empty `GateToken` refused (`CodeInvalidField`) BEFORE authz; a hash-only check would wrongly accept SHA256("") | `internal/skeleton/takecontrol_gatetoken_test.go : TestSkeleton_TakeControlMissingGateTokenRefused` | covered (A5-c) |
| 6 | Invalid/reused gate token (swapped by relay) | refused (signature fails) | content-hash binding — daemon recomputes `SHA256(wire GateToken)`; a swapped token yields a different hash, so the Ed25519 signature (which covers it) fails to verify → `CodeNotAuthorized` | `internal/skeleton/takecontrol_gatetoken_test.go : TestSkeleton_TakeControlSwappedGateTokenRefused` | covered (A5-c) |
| 7 | Kill-switch off at `take_control` | refused (`CodeKillSwitch`, first gate) | `requireRemoteAuthz` kill-switch gate fires before authz is consulted and before any lease | `internal/protocol/takecontrol_test.go : TestProtocol_TakeControlKillSwitchOff` | covered (A5-a) |
| 8 | Kill-switch flipped off mid-session then `OpDataIn` | dropped | `controlGateOpen` clause 1 — kill switch re-checked on every keystroke | `internal/protocol/takecontrol_input_test.go : TestProtocol_InputAfterKillSwitchFlippedOffDropped` | covered (A5-b) |
| 9 | Input outside any session (`cc.control == nil`) | dropped (fail-closed default) | `controlGateOpen` clause 2 (fail-closed default) + HIGH-2 remote-tier fail-closed on raw input | `internal/protocol/takecontrol_input_test.go : TestProtocol_InputWithoutTakeControlDropped` and `internal/protocol/remote_input_refused_test.go : TestProtocol_RemoteInputFrameNotForwardedNoKeystrokeInjection` | covered (A5-b + HIGH-2) |
| 10 | Second device rides another's session (`OpDataIn` without its own `take_control`) | dropped | per-connection `cc.control`/`cc.attSession` — a separate connection that never took control hits clause 2 (`cc.control == nil`) AND the no-attach guard; its bytes never reach the controller's shim | `internal/protocol/takecontrol_seconddevice_test.go : TestProtocol_SecondDeviceRidesSessionDropped` **(NEW)** | covered (added, PASS) |
| 11 | Session end (`take_control_end`) then input | dropped | `handleTakeControlEnd` clears `cc.control` and releases the lease → clause 2 fail-closed | `internal/protocol/takecontrol_input_test.go : TestProtocol_InputAfterTakeControlEndDropped` | covered (A5-b) |
| 12 | Concurrent expiry vs keystroke | never a post-expiry shim write | **N/A by architecture** — see disposition below | (no dedicated test — vacuous; expiry drop pinned by attack 3, post-release write prevented by attack 4's generation gate) | N/A (documented) |
| 13 | Capability too low (`CapReadApprove` does `take_control`) | refused (`CodeNotAuthorized`) | `actionClass(take_control) == device.ActionControl`; `Capability.Allows(ActionControl)` false for `CapReadApprove` → authorizer rejects | `internal/skeleton/takecontrol_authz_test.go : TestSkeleton_TakeControlRefusedForLowCapability` and `TestSkeleton_TakeControlMapsToControlClass` | covered (A5-a) |
| 14 | Forged/altered target (device signs Session=A but wire SessionID=B) | refused (signature binds Session) | `handleTakeControl` passes the WIRE `c.SessionID` into `requireRemoteAuthz`; `crypto.Command.Canonical` signs `Session` (field 3), so a signature over Session=A fails to verify against wire Session=B → `CodeNotAuthorized`, no lease | `internal/skeleton/takecontrol_forgedtarget_test.go : TestSkeleton_TakeControlForgedTargetRefused` **(NEW)** | covered (added, PASS) |

## Attack 12 disposition — N/A by architecture

"Concurrent expiry vs keystroke — never a post-expiry shim write." There is **no async
expiry actor** in the design: expiry is a *lazy* comparison (`controlGateOpen` clause 3,
`!cc.srv.now().Before(ctl.expiry)`), not a timer that mutates state on a background
goroutine. Each connection is served by **one in-order goroutine** (`clientConn.serve`),
so the expiry check and the subsequent `forwardInput` execute sequentially on the same
goroutine, one frame at a time — there is no interleaving in which a keystroke is
forwarded after the gate observed expiry. Any residual cross-connection race (a supersede
from another connection while this one forwards) is a *generation* concern, not a *time*
concern, and is closed by the server-side generation gate in `forwardInput` (attack 4).

A dedicated "attack 12" test would therefore either duplicate
`TestProtocol_InputAfterSessionExpiryDropped` (attack 3, the time-expiry drop) or attempt
to construct a concurrency the single-goroutine serve loop makes unrepresentable. It adds
no coverage and is deliberately omitted — the property is structural, not test-observable.

## New tests added by A5-d (confirmation, not new gates)

Both are **confirmation** tests: they assert properties the A5-a/b/c gates already
enforce, so they PASS immediately against current code. No production code was added or
modified.

- **Attack 10** — `internal/protocol/takecontrol_seconddevice_test.go :
  TestProtocol_SecondDeviceRidesSessionDropped`. Connection A runs an authorized
  `take_control` over `sess1` (the one lease/stream); A's own keystroke reaches the shim.
  A separate connection B (no `take_control`) sends a raw input frame — dropped, because
  `cc.control`/`cc.attSession` are per-connection and B has neither. Asserts A's bytes
  present and B's bytes absent in the same (A's) shim stream. **PASS.**
- **Attack 14** — `internal/skeleton/takecontrol_forgedtarget_test.go :
  TestSkeleton_TakeControlForgedTargetRefused`. Against the real assembled daemon: a
  CapFull device signs a fully-valid `take_control` (valid gate token) binding
  Session=sessionA, and the wire carries SessionID=sessionB (a real running session). The
  daemon verifies the signature against the WIRE session, so it fails →
  `CodeNotAuthorized`, no lease. Reuses `signedTakeControl`/`sendTakeControl` from the A5-c
  gate-token E2E, changing only the target so the refusal is attributable to the session
  mismatch alone. **PASS.**

## Verification

```
go test -race ./internal/protocol/ ./internal/skeleton/ \
  -run 'TestProtocol_TakeControl|TestProtocol_InSession|TestProtocol_InputAfter|TestProtocol_SecondDevice|TestSkeleton_TakeControl' -v
```

All 15 targeted tests pass under `-race`, including the two new confirmation tests
(`TestProtocol_SecondDeviceRidesSessionDropped`, `TestSkeleton_TakeControlForgedTargetRefused`).
`go build ./...` is clean. `git status --porcelain` for the touched trees shows only the
two new test files plus this evidence doc — no production, frozen, or existing-test file
was modified.

This closes the A5 adversarial acceptance bar (all 14 attacks pinned), modulo the
cross-model committee review the orchestrator runs separately.
