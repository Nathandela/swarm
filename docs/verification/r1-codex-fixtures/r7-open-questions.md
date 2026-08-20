# R7: what the offline bindings did NOT settle

Companion to `r7-PROVENANCE.md`. Each row names the probe that settles it. Nothing downstream may
assume an answer; ADR-013 §R7.9 is the decision-level list and this is its evidence-level twin.

## Q1 — an object-shaped decision id does not fit `DecisionChoice.ID`

`CommandExecutionApprovalDecision` has four bare-string variants and **two object variants**
(`acceptWithExecpolicyAmendment`, `applyNetworkPolicyAmendment`), each carrying required
parameters the user would have to choose (an execpolicy amendment array, a host + allow/deny).
`adapter.DecisionChoice.ID` is a `string` and `InteractionSource.Decision(ref, decisionID string)`
is handed nothing else.

**Not a schema question — a design one.** R7's lean answer is to offer only the four string
variants on the card and to omit the two object ones, which is IS-TOOL-2's posture (a decision the
adapter cannot place is not offered rather than guessed at) and costs the phone nothing the terminal
does not still have. Whether the two object decisions are ever offered from a phone is an owner
ruling, not an implementer's choice, because it puts a policy amendment behind a remote tap.

## Q2 — can the TUI be pointed at a daemon-created thread?

`codex --help` at 0.147.0 shows `--remote <ADDR>` (accepting `unix://PATH`) with **no thread-id
companion flag**. `codex resume [SESSION_ID]` is a separate subcommand; whether
`codex --remote unix://PATH resume <id>` composes is **unrecorded**.

**Probe** (realcli, isolated `CODEX_HOME`, scratch workspace): start an app-server, call
`thread/start` from a second client, then launch `codex --remote unix://SOCK resume <threadId>` in a
PTY and observe whether `thread/loaded/list` still shows exactly one thread.

**Why it is not a blocker.** ADR-013 §R7.2e's go-ahead handshake makes the daemon a connected client
before the agent exists either way; the only difference is whether `agent_args` carries a thread id
or is empty and the daemon joins from `thread/started`.

## Q3 — rollout-to-resume, the ONE measurement R7 owes

How long after a thread's first turn starts does `thread/resume` stop returning
`no rollout found for thread id` (`errors-observed.json`)? The gate's 15-17 s is **boot-to-resume**
and does not answer it. ADR-013 §R7.2e makes this a named obligation before the no-flag path is
relied on. If it exceeds the first turn's duration, the no-flag path emits a `structured_gap` for
that turn rather than pretending to have seen it.

**Probe**: `internal/appserver/r7_realcli_test.go` (`//go:build realcli`, `SWARM_REALCLI=1`).

## Q4 — does `thread/read {includeTurns:true}` return `itemsView: "full"` in practice?

The binding says `Thread.turns` is populated and `TurnItemsView` has a `full` member; it does not
say which view `thread/read` chooses, and the gate observed `summary` on one `turn/completed` and
`notLoaded` on another. A `summary` backfill is lossy for a long turn.

**Probe**: same realcli harness — run a turn with several items, disconnect, reconnect, `thread/read`
with `includeTurns: true`, and compare the item ids against the ones streamed live.

**Consequence if it is `summary`**: ADR-013 §R7.6's silent reconnect becomes an honest
`structured_gap` for the un-backfillable interval, which is the arm that ADR already specifies.

## Q5 — is `turn/steer` legal on an IDLE thread?

`TurnSteerParams.expectedTurnId` is documented "Required active turn id precondition. The request
fails when it does not match the currently active turn." That is why R7 dispatches `turn/start` when
the daemon's turn state is empty and `turn/steer` when it is not — but the daemon's turn state is
IS-ENV-1's, not the server's, and the two can disagree for the width of one frame.

**Not settleable offline.** The R7 rule is therefore: dispatch on the daemon's own turn state, and
treat the server's rejection of a steer as the same benign class as
`no active turn to interrupt` — refuse the op with a coded refusal, having sent nothing else, and
never fall back to `turn/start` (which would start a turn the user did not ask for).
