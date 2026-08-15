# Wave R1 — decisions and capability skeleton: evidence

**Bead:** `agents-tracker-hggx.2` | **Playbook:** Wave R1 | Orchestrated 2026-08-14/15.

## Decision records (owner sign-off 2026-08-15)

ADR-015 (push-gateway split), ADR-016 (Web-PKI relay TLS), ADR-017 (terminal fallback +
capability records), ADR-018 (multi-machine pairings), ADR-007 B144 (launch deferral lifted
over machine-authored presets; pointer index; line-offset note). Each draft went through
adversarial citation-verified review (ADR-016's reviewer checked ~90 citations); ADR-015 was
rejected three rounds until the WakeV1 AAD/shape question was resolved structurally (the wake
envelope became its own versioned shape with push_address inside the AAD) and the B20
disclosure widening was stated honestly. Whole-set consistency passed on round 3 after a
citation-drift repair pass (the annotator's own insertions had shifted line numbers; repaired
per-citation with anchor quotes). ADR-007 ratified to Accepted-with-named-open-gates per
B144(c). Amendments recorded in ADR-009/011/013 and mirror-program with in-place markers.

## Source-transition sweep

~25 live documents amended to the single new direction (push, TLS, fallback, multi-machine,
launch); historical banners on remote-v1-roadmap, remote-control-design, and eight
verification records. Reviewed group-by-group against the owning ADRs; the strict rounds'
residuals (PB-PUSH-4 derived citations, relay-vps-deploy harmless-at-v1 sentence,
metadata-disclosure banner defects) applied and verified. Two matrix-vs-ADR conflicts resolved
in the ADR's favor and recorded here: the Caddyfile keeps reuse_private_keys scoped to the
expert policy (ADR-016 W5 overrides the matrix row), and metadata-disclosure took six edits
against ADR-015 P11's "exactly three" (the extra three were factually compelled: legacy
scoping, residual closure, Firebase reality).

## Capability skeleton (all TDD; RED under docs/verification/r1-red/)

- `schema.RemoteProfileV1` on the reconcile record (version 0 = not-yet-published sentinel,
  pinned); first real publisher landed with the ADR-016 lane (policy/pin/host fields).
- `schema.SessionCapabilities`: daemon-authored, immutable per instance, one-way degrade,
  carried on SessionView; producer deliberately unwired behind the version-skew gate
  (`agents-tracker-hggx.2.1`) so no optimistic structured record can reach the wire.
- Refusal-only semantic ops: session_launch, composer_send, operation_status, turn_interrupt,
  terminal_control_* enter the vocabulary with sealed stable refusals distinct from the
  unknown-action arm; wrong body versions refuse naming the profile's accepted versions;
  signatures validate before dispatch (mutation-proven: terminal_input/keepalive stay unmapped
  in the fail-closed actionClass switch); sessionless ops pin the operation sentinel against
  hostile wire session ids. Gateway routes the vocabulary to the daemon (the routing arm was
  initially left unstaged by the orchestrator — caught by CI, repaired same hour).
- MachineClient/MachineManager seam (ADR-018 MM3/MM4) with the SingleMachineAdapter; registry
  mutations refuse until R4; the reviewer-proven Start-reset fail-open fixed state-gated and
  pinned deterministically; the seam is B94-ledgered until R4 wires it (bidirectional — the
  wiring slice must delete the rows).

## Codex feasibility gate — PASS (r1-codex-gate.md, fixtures in r1-codex-fixtures/)

All five legs against codex-cli 0.147.0. RC-D4 unblocked; app-server is the R7 backend.
Binding integration findings: the UDS endpoint is WebSocket-upgraded JSON-RPC (raw JSONL gets
silence); the daemon should own thread/start; turn/steer carries native expectedTurnId;
supervise by socket, not streams. Recorded in ADR-013 by its own obligation 3.

## Also delivered in this wave's scope

- physical-handset-gate.md rewritten: playbook 11.2 as 95 stable PH-* rows, all [UNRUN],
  per-section blocking waves; citation-verified twice.
- push-gateway-api.md authored (Spec 0003-class contract): five operations as OpenAPI, WakeV1
  field-exact with ADR-015, wake-obligation state machine, relay+gateway collusion analysis,
  retention verbatim from playbook section 13; the hosting deferral attributed honestly to an
  out-of-band owner ruling.

## Exit walk (playbook R1 exit criteria)

Old clients degrade safely (profile absent → sentinel; unknown ops → sealed refusals): tested.
Unsupported semantic operations return sealed refusals: tested, distinct codes. Structured vs
fallback routing provider-authoritative: the capability record exists and derives from the
adapter seam; the wire producer is gated on hggx.2.1 (deliberate, recorded — the phone cannot
yet be lied to because nothing publishes optimistic records). Codex decision evidence-backed:
PASS recorded. Docs non-contradictory: sweep + consistency pass. Handset matrix ready: 95 rows.
