# R7 fixture extension: what is RECORDED, what is SCHEMA-DERIVED, and what is still unknown

- **Date:** 2026-08-20
- **Wave:** R7 (`agents-tracker-hggx.8`, Mirror M4.1-M4.5)
- **Extends:** the R1 gate fixtures in this directory, whose provenance is
  `docs/verification/r1-codex-gate.md` §"Fixture inventory".
- **CLI:** `codex-cli 0.147.0` at `/usr/local/bin/codex` (same binary the R1 gate ran; see
  `codex-version.txt`).

## Why this file exists

The R1 gate captured real frames off a live connection, and those frames are the golden corpus.
They do not cover every shape R7 must map: the gate never ran a `commandExecution` to completion on
the observer connection, `protocol-methods.txt` inventories client requests and server
**notifications** but not the server-to-client **requests**, and three questions ADR-013 §R7.9 left
open (`turn/steer`'s echo id, `thread/read`'s backfill, the 0.147.0 decision vocabulary) were not
answerable from the recording at all.

None of that is filled in by invention. It is filled in from the server's **own generated
bindings**, which the R1 gate itself named as the authoritative offline source:

```bash
export CODEX_HOME=<isolated scratch home>
codex app-server generate-ts          --out <dir>   # TypeScript, incl. the method-name unions
codex app-server generate-json-schema --out <dir>   # JSON Schema per type, v1/ and v2/
```

Both commands are **offline**: they read nothing from the account, start no server, and cost
nothing. They were run on 2026-08-20 under
`CODEX_HOME=/Users/Nathan/.claude/jobs/72c288b2/tmp/r7-schema/codex-home` with output under
`/Users/Nathan/.claude/jobs/72c288b2/tmp/r7-schema/{ts,schema}`.

## The three provenance classes, and the rule for each

| Class | Files | Rule |
|---|---|---|
| RECORDED | every R1 file in this directory (`frame-samples.json`, `approval-request.json`, `turn-*.json`, `errors-observed.json`, `ws-handshake.txt`, `protocol-methods.txt`) | A test may assert on these as observed truth. No R7 change may contradict one. |
| SCHEMA-DERIVED | `r7-schema-methods.txt`, `r7-schema-derived-frames.json` | Every field is present in the generated binding named in the file's own `source` key. The **values** are illustrative; the **shape** is authoritative. A test asserts on field names and unions, never on a value being a real observation. |
| STILL UNKNOWN | `r7-open-questions.md` | Named, with the probe that would settle it. Nothing downstream may assume an answer. |

A test that needs a *value* rather than a *shape* must use a RECORDED file. That is why the
`agentMessage` delta batching tests drive `frame-samples.json`'s real delta frame and only the
`commandExecution` tests drive a schema-derived one.

## What the generated bindings settled

1. **The server-to-client request inventory is exactly ten methods**, not the eight the gate counted
   (the count came from an older run of the same union). See `r7-schema-methods.txt`. The two that
   matter to R7 are `item/commandExecution/requestApproval` and `item/fileChange/requestApproval`;
   the gate recorded the second one live (`approval-request.json`) and the Codex adapter declares
   only the first (`internal/adapter/codex/codex.go:44`).

2. **The decision vocabulary at 0.147.0 differs per approval kind**, which is why ADR-010 §5 and the
   R1 gate disagreed — they were describing different requests, one version apart:
   - `FileChangeApprovalDecision` = `accept | acceptForSession | decline | cancel` (four string
     variants; this is what the gate recorded).
   - `CommandExecutionApprovalDecision` = `accept | acceptForSession | decline | cancel` **plus two
     OBJECT variants**, `{"acceptWithExecpolicyAmendment": {...}}` and
     `{"applyNetworkPolicyAmendment": {...}}`. ADR-010 §5's `acceptWithExecpolicyAmendment` is real
     and is a command-execution decision, not a file-change one, and it is **not a bare string**.
   The consequence for the adapter is recorded in `r7-open-questions.md` Q1: an object-shaped
   decision id cannot ride `DecisionChoice.ID` (a string) as-is.

3. **A `commandExecution` item carries its arguments AND its results in the same object.**
   `ThreadItem`'s `commandExecution` variant has `command`, `cwd`, `commandActions`, `status`
   (`inProgress | completed | failed | declined`), `aggregatedOutput`, `exitCode` and `durationMs`.
   This closes ADR-013 §R7.9's first open question in the lean direction: open the `tool_run` at
   `item/started` and fill `output_excerpt`/`exit_code` at `item/completed`, dropping
   `item/commandExecution/outputDelta` entirely.

4. **The composer echo can be EXACT, and needs no text correlation and no turn-scoped fallback.**
   `TurnStartParams` and `TurnSteerParams` both carry an optional `clientUserMessageId`, and the
   `userMessage` variant of `ThreadItem` carries `clientId`. So the daemon mints the id, sends it,
   and reads it back off the `item/started` frame. `TurnSteerResponse` is `{turnId}` only — the
   question ADR-013 §R7.5 asked ("does the steer reply carry the message's itemId?") is answered
   **no**, and is moot, because the client supplies the correlation key up front.

5. **A lossless post-outage backfill exists.** `ThreadReadParams` is
   `{threadId, includeTurns?: boolean}` and `Thread.turns` is documented as "only populated on
   `thread/resume`, `thread/rollback`, `thread/fork`, and `thread/read` (when `includeTurns` is
   true)". `TurnItemsView` is `notLoaded | summary | full`. This closes ADR-013 §R7.6's "largest open
   mechanical question" in the favourable direction — a daemon restart with a successful rejoin can
   backfill and need not emit a `structured_gap`.

## What it did NOT settle

`r7-open-questions.md`. In particular the TUI-flag question stays open and got slightly worse:
`codex --help` at 0.147.0 shows `--remote <ADDR>` with no thread-id companion flag, and
`codex resume <SESSION_ID>` is a separate subcommand whose composition with `--remote` is
unrecorded. ADR-013 §R7.2e's go-ahead handshake degrades cleanly either way, which is why this is a
question and not a blocker.
