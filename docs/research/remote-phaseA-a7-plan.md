# Phase A — A7 combined implementation plan (input channel + terminal renderer)

Locked 2026-07-24. Synthesises the two code-grounded plans for the two operator decisions
recorded in ADR-007 "Amendment 2026-07-24" (sealed+seq'd lease input channel; server-side VT
render). Both are the SECURITY-CRITICAL A7 surface -> cross-model review per DoD §0 on the
security-relevant slices. Frozen crypto (`internal/remote/crypto`) reused unchanged.

## 0. The unifying architecture (why the two sets decouple)

The renderer does NOT share the gateway's lease-connection reader. Raw VT bytes for the phone
come from a **daemon-side persistent read-only fan-out tap** per session, NOT the gateway,
for two hard reasons: (a) terminal *peek* must work BEFORE `take_control` (no lease/pump
exists then); (b) placement — the daemon renders so only *sanitized text* crosses the
daemon->gateway socket, keeping the VT emulator + raw hostile PTY bytes off the network-facing
sidecar (ADR-007 Decision 2). The shim is single-consumer, so the daemon must multiplex
anyway. Clean shape:

    daemon per-session tap (keeps the upstream shim stream alive)
      -> controller pump  (raw TDataOut, to the active LOCAL controller only)
      -> render loop       (vt.Emulator.Feed -> debounce -> SnapText -> sealed snapshot)

**The invariant both sets honor:** the gateway NEVER forwards raw pump output to the phone;
the phone's terminal view is EXCLUSIVELY the daemon-rendered, sealed snapshot mailbox stream.

**Unifier decision (adopted):** the remote-tier pump SUPPRESSES raw output frames to a REMOTE
controller (OpLease + input acks only), since the phone never consumes raw `TDataOut`. This
removes the gateway lease connection's drain-or-die obligation entirely (no `evictPump` risk
from an undrained remote lease). It is a remote-tier pump change in the daemon (server.go),
security-relevant -> folded into the fan-out slice + cross-model reviewed.

## 1. Default decisions (engineering calls; flag-for-review, not blockers)

- **Slice F architecture = daemon-side observer/fan-out, shim FROZEN** (recommended over a
  read-only shim peek subscriber, which would touch the survival-tested shim + need concurrent
  serveConn). Extend the Server lease/pump to keep the upstream shim stream alive while a
  remote observer wants the session and tee frames to the render loop; local attach untouched.
- **Adopt the pump-suppression unifier** (§0) — removes the gateway drain-or-die failure mode.
- **Styling OUT of v1** — plain text only (matches the binding phosphor-green mono design).
- **Live tail = full snapshot re-sent per debounce** — incremental diff is a later
  optimization (YAGNI until measured).
- **Frame size** — clip the rendered snapshot to a cols x rows cap (phone viewport), well
  under `wire.MaxFrame` (1 MiB).
- **take_control_end** — gateway closes the lease conn (cleanup->releaseLease); explicit end
  frame is a nicety, not required.
- **Cross-device input** — single-device v1 makes it moot; optional `SenderKeyID` lease
  binding is the hardening if multi-device is added (ADR residual).

## 2. Build order

Renderer peek path FIRST (independently valuable — the v1 "terminal peek" screen — and it
establishes the daemon fan-out tap), THEN the input channel on top.

    Renderer:  A -> B -> C -> D -> E -> F        (F = fan-out tap + pump-suppression: sec-crit)
    Input:     1 -> 2 -> 3 -> 4 -> 5 -> 6 -> 7   (3-7 ride the fan-out; 7 = E2E + adversarial)

Slice F and input slices 3-7 (+ daemon R7) are the security-critical set -> cross-model review.

## 3. Renderer slices (source: `internal/vt` ADR-005; raw output does NOT reach the remote tier today)

- **A — Snap -> sanitized plain text.** RED (`internal/vt`): `TestSnapText_StripsAllControlBytes`
  (hostile Snap -> every line has no byte <0x20, no 0x7f, no 0x80-0x9f, no embedded `\n`),
  `TestSnapText_RowCountMatchesGrid`. Prod: `SnapText(*Snap) []string` in `render.go` (flatten
  the ALREADY-sanitized `Run.Text`; `sanitizeText` at emulator.go strips ESC/C0/C1/DEL +
  Trojan-source). No new deps. **The security choke point.**
- **B — wire type + ops + GG-7.** RED (`internal/protocol`): `TestTerminalSnapshot_RoundTrip`;
  registering the type reddens `TestProtocolMDBidi_FieldSetMatchesStructs` until protocol.md
  rows exist. Prod: `OpTerminalSubscribe`/`OpTerminalSnapshot`, `TerminalSnapshot` struct,
  `Control.Terminal *TerminalSnapshot json:"terminal,omitempty"`. protocol.md: add `terminal`
  to the Control table + a new `TerminalSnapshot` field table + the two op docs + `allOps()`;
  register `reflect.TypeOf(TerminalSnapshot{})` in `wireJSONTags()`.
- **C — phone decoder (thin, NO `internal/vt` import).** RED (`internal/phonecore`):
  `TestSnapshotReceiver_DecodesSealedFrame`, `TestMailboxDemux_JournalUnaffected`,
  `TestSnapshotCache_LatestPerSession`, `TestMailboxSeqGate_SharedJournalAndSnapshot`. Prod:
  demux mailbox plaintext by a `kind` field through the ONE `MailboxReceiver` (empty->journal
  unchanged; `"terminal_snapshot"`->snapshot); `SnapshotCache` map[session]->[]string.
- **D — gateway seal + forward.** RED (`internal/remotegw`): `TestRelaySink_ForwardsTerminalSnapshot`
  (shares journal seq), `TestTerminalSink_OpaqueToRelay`. Prod: `RelaySink.Terminal(...)`
  sharing `s.seq`; `Gateway.RunTerminal` mirroring `RunJournal`.
- **E — daemon render loop (stub stream).** RED (`internal/daemon`):
  `TestRenderLoop_HostilePTYCannotEscape` (stub SessionStream emits hostile Frames -> emitted
  lines printable-only — the e2e security assertion), `TestRenderLoop_CoalescesBurst`,
  `TestRenderLoop_InitialSnapshotFromStream`. Prod: `Frames()` -> `vt.Emulator.Feed` ->
  debounce (reuse `journal/debounce.go`) -> `SnapText` -> push. File: `daemon/terminalrender.go`.
- **F — SPLIT into F1/F2/F3 (grounded 2026-07-24; SECURITY-REVIEWED as a set).** Correction to
  §0/§1: the shim is strictly single-consumer, and the local controller (owner-tier `d.srv`) and
  remote peek (remote-tier `d.remoteSrv`) are DIFFERENT `protocol.Server` instances sharing ONE
  `coreAPI` backend. So the fan-out tap CANNOT live in the pump — it must live at the single
  convergence point, `coreAPI.Attach` (package skeleton), teeing one shared upstream to N
  subscribers (mirror `vt.Emulator` seeds late joiners; per-sub bounded channel evicts only the
  stalled sub; refcount opens/closes the single upstream). Shim stays frozen.
  - **F1 — shared per-session tap** (skeleton/sessiontap.go + `coreAPI.Attach` routes through it).
    The risky keystone. RED: `TestSessionTap_SecondSubscriberSharesOneUpstream`,
    `_LateJoinerSeededFromMirror`, `_LastCloseClosesUpstream`, `_OverflowEvictsThatSubOnly`.
    Regression guards that MUST stay green: protocol lease_test (supersede/detach/EOF),
    daemon TestSurvival_KillDashNineReconnectsAll, skeleton gridtap_test.
  - **F2 — terminal_subscribe peek handler** (export daemon RenderTerminal/TerminalStream/
    TerminalRender; new optional `protocol.TerminalTapper`; handler + `case OpTerminalSubscribe`
    in server.go; `coreAPI.TerminalTap` = readOnly subscribe). Read-only via 3 independent layers
    (never routes to forwardInput; renderTerminal only reads; readOnly tapSub Input/Resize no-op).
    **Kill-switch: peek REFUSES when RemoteControlEnabled()==false** (terminal content is more
    sensitive than journal metadata; `swarm remote off` blanks the phone — fail-closed). RED:
    `TestRemotePeek_WorksWithNoLocalController`, `_DoesNotSupersedeLocalController`,
    `_ReadOnly_NoInputInjection`. GAP (slice-7 follow-on): Gateway.RunTerminal must send a focused
    session_id (handler is session-scoped; unit tests drive it with a session_id).
  - **F3 — remote-tier pump-suppression** (server.go pump: on `s.remoteTier`, OpLease only, skip
    snapshot loop + TDataOut, still drain frames for end-detection). Independent of F1/F2, lands
    first; removes the gateway lease drain-or-die/evictPump hazard and is the precondition for
    input Slice 3. RED: `TestRemotePump_SuppressesRawOutput`.

## 4. Input slices (daemon side already built in A5: handleDataIn/handleResize/controlGateOpen)

Gateway holds NO lease conn today (persistent journal-OUT + fresh conn per command;
take_control unrouted). New: a persistent lease-holding UDS conn + gateway-side seq-gate.

- **1 — phone-core take_control encoder.** RED `TestSignTakeControl_BindsGateTokenAndExpiry`
  (`internal/phonecore/command_test.go`): ContentHash==SHA256(gateToken), Action==take_control,
  signed tuple verifies, ExpiresAt round-trips; seal->`remotegw.OpenRemoteCommand` recovers
  Action+GateToken+TTLSeconds. Prod: `SignTakeControl` + `SealTakeControlEnvelope` in
  `phonecore/command.go`; extend `protocol.RemoteCommand` with `GateToken`/`TTLSeconds` omitempty.
- **2 — phone-core input-frame encoder + shared seq allocator.** RED
  `TestSealInputFrame_SharedSeqSpaceRejectsReplay`: one `Sequencer` -> strictly increasing seqs;
  `MailboxReceiver` accepts in order, rejects replay (ErrStaleSeq); interleaving with a command
  seal proves ONE shared seq space; `remotegw.OpenInputFrame` recovers kind+data/cols/rows. Prod:
  `phonecore/input.go` (`InputFrame{T string; Data []byte; Cols,Rows int}` + `SealInputData`/
  `SealInputResize` + `Sequencer` atomic.Uint64); `remotegw/input_in.go` (`OpenInputFrame`
  seq-gated like `OpenRemoteCommandGuarded`). The `t` discriminator makes decode unambiguous vs
  `RemoteCommand` (which has no `t`).
- **3 — gateway daemonConn: raw-input writer + lease reader.** RED
  `TestLeaseConn_WriteDataInAndDrainOutput` (real remote-tier daemon + fake session): after
  take_control, `writeDataIn(bytes)` reaches the fake session; reader captures OpLease.Generation.
  NB with pump-suppression (§0) the reader captures OpLease + acks, no raw output to drain.
  Prod: `remotegw/lease.go` — `writeDataIn`/`writeResize` + `readLoop`.
- **4 — gateway lease-session manager.** RED `TestLeaseManager_TakeControlThenInputSameConn`:
  one persistent conn, take_control establishes the lease, following InputFrame -> TDataIn on
  the SAME conn -> fake session receives it; input after End/close dropped. Prod:
  `LeaseManager{conns map[session]*leaseConn}` Begin/Input/End/teardown.
- **5 — gateway mailbox routing + Service wiring.** RED `TestCommandBridge_RoutesInputVsCommand`:
  interleaved {kill, take_control, input} routes kill->ForwardCommand(fresh),
  take_control->LeaseManager.Begin, input->LeaseManager.Input, all seq-gated by the ONE receiver;
  replayed input seq dropped (ErrStaleSeq) NOT forwarded. Prod: extend `CommandBridge.handle` to
  peek `t` + route; inject LeaseManager into `NewService`/`Service`.
- **6 — daemon R7 expiry binding (the only daemon change for input).** RED
  `TestTakeControl_ExpiryBoundToSignedExpiresAt`: signed ExpiresAt=now+2m, TTLSeconds=3600 ->
  expiry now+2m; signed ExpiresAt beyond now+max clamps to now+max. Prod: `handleTakeControl`
  `expiry = min(*c.ExpiresAt, now+maxControlSessionTTL)`. protocol.md prose only (GG-7-neutral).
- **7 — full E2E + adversarial (SECURITY-CRITICAL, cross-model review).** RED
  `TestPhonesim_TakeControlTypeE2E` (extend `phonesim_e2e_test.go`): pair -> take_control ->
  seal keystroke -> fake session receives it; + adversarial cases §5. Prod: `phonesim`
  TakeControl/Type/Resize helpers.

## 5. Adversarial refusal matrix (input) -> slice

| Case | Enforced by | Pinned |
|---|---|---|
| Replay / reorder / dup | gateway `MailboxReceiver` ErrStaleSeq | 2 (encoder), 5 (route), 7 (E2E) |
| Stale lease generation | daemon `controlGateOpen` clause 4 (built) | 6/7 |
| Expired session | `controlGateOpen` clause 3 + R7 | 6/7 |
| Kill-switch off mid-stream | `controlGateOpen` clause 1 (built) | 7 |
| Cross-device take_control (unauthorized) | `requireRemoteAuthz` (built) | 7 |
| Cross-device INPUT (2nd trusted device) | accepted single-device residual (ADR) | flagged |

## 6. GG-7 + frozen-layer

- Renderer ADDS rows: `terminal` on Control + a `TerminalSnapshot` field table + the two ops.
  `protocolmd_bidi_test.go` enforces both directions.
- Input adds NO GG-7 rows (raw `TDataIn`; take_control reuses `ttl_seconds`/`gate_token`/
  `expires_at`); `RemoteCommand.GateToken/TTLSeconds` + `phonecore.InputFrame` are outside the
  GG-7-covered set (Control/SessionView/LaunchReq only).
- Do NOT change `internal/remote/crypto` (frozen) or the shim wire/S10 (recommended Slice F
  keeps the shim frozen).

## 7. Open questions surfaced (defaults taken in §1; revisit if the operator disagrees)

Slice F fan-out vs shim-peek (took fan-out); pump-suppression unifier (adopted); styling
(out); live-tail full-vs-diff (full); frame cap (viewport clip). None block the start —
Slice A (SnapText) is a pure sanitization function with zero architectural entanglement.
