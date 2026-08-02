# Spike S-B — Interaction Capture

**Bead**: `agents-tracker-35v` (plan D.18 R-SPK-B), depends on `agents-tracker-d9d`.
**Question**: can swarm capture the REAL nested permission-request payloads from `claude` and `codex` (especially `tool_input`) without breaking the frozen adapter boundary (E9.2)?
**Environment**: macOS arm64, Go 1.24.2, real `claude` 2.1.214 (Opus 4.8, claude.ai OAuth, Max plan) and real `codex-cli` 0.144.1, both with live auth. All payloads below are genuine CLI output, captured on 2026-07-18; nothing is fabricated or hand-written from memory of the docs.

## VERDICT: PARTIAL (capture is proven and additive-safe; production integration is ADR-required)

Both CLIs were driven for real and both `tool_input`/`command` shapes were captured with full fidelity (R-SPK-B.1, R-SPK-B.2 both technically succeed — see taxonomy for exactly which fields were confirmed stable across repeat runs vs. observed once). The verdict is PARTIAL rather than PASS strictly because of R-SPK-B.3: the concrete change needed to carry this data through swarm's *production* path is a change to the frozen `internal/adapter` contract (`SignalSource`/`HookPayload`), and this repo has already decided, twice, in writing, that such a change is ADR-level work regardless of whether it's additive (see "Production integration shape" below). The E9.2 I/O-token grep itself is not the blocker — no banned token is needed — the blocker is process, not code.

## R-SPK-B.1 — Claude Code hook payloads (real capture)

### Method

`hookprobe` (`cmd/swarm-char/hookprobe/main.go`) posts canned literals, not a real CLI's stdin, so it can't answer this. I forked it into a throwaway relay (`spikesbrelay`, under `.claude/jobs/6878515f/tmp/spike-sb/relay/`, own `go.mod`, stdlib only) that reads its own stdin, dials `$SWARM_CHAR_HOOK_SINK`, and forwards the raw bytes as one newline-delimited line — exactly the wire format `char.go`'s `hookSink.readConn` already expects (`cmd/swarm-char/char.go:255-275`). It also tees a forensic copy to `$SPIKE_SB_RAW_DIR` for redundancy.

I built the **actual** `swarm-char` binary (`go build ./cmd/swarm-char`, no source changes) and drove real `claude` through it — `characterize()` (`cmd/swarm-char/char.go:79`) unmodified, real PTY, real vt emulator, real hook sink. The relay was wired in as every hook command via an inline `--settings` JSON (mirroring `claude.go`'s `hookSettingsJSON`), with `--setting-sources ""` so only that inline JSON applied (no interference from my personal `~/.claude/settings.json`).

Two environment gotchas, recorded so they don't cost the next person an hour:
- **Workspace trust**: a CLI-fresh `cwd` shows a blocking "do you trust this folder" dialog before anything else; it consumes the first scripted keystroke burst if not accounted for. Trust is persisted per-path in `~/.claude.json`'s `projects` map (`hasTrustDialogAccepted`), and appears to be inherited for children of an already-trusted ancestor (e.g. everything under `~/.claude/` was pre-trusted).
- **Path-based sensitivity**: a workdir nested under `~/.claude/...` makes `Write`/`Edit` calls get silently flagged "sensitive file" and denied *before* any interactive dialog — this is unrelated to the permission-request mechanism under test, so all interactive captures below use `~/spike-sb-work` instead.

Ten real `claude` invocations were run in total (headless `-p` probes + interactive PTY probes); the interesting ones are preserved as `adapter.Fixture` JSON (real `characterize()` output, `fx.Validate()`-passing) under `docs/verification/fixtures/spike-sb/`.

### What actually happens (confirmed sequence)

For a tool call that needs approval, in the **interactive TUI** (not `-p`/headless — see below), the real hook sequence is:

```
UserPromptSubmit → PreToolUse → PermissionRequest → Notification(notification_type=permission_prompt) → [human decision] → PostToolUse → Stop
```

This exactly matches `internal/adapter/claude/claude.go`'s documented `hookEvents` table and its Notification-subtype comment (`claude.go:45-51`) — confirmed against a live CLI, not just the adapter's own doc comment.

**Headless `-p` (print) mode is a materially different code path**: `PermissionRequest`/`Notification`/`PermissionDenied` never fired in any `-p` run. A `Bash` call that needed no confirmation (`echo ...`) ran straight through; a `Write`/`Edit` call was silently denied with no permission hook firing at all — Claude's own reply said outright: *"the file is flagged as sensitive and the permission request could not be approved... you'd need to... run in an interactive session where you can approve the prompt."* I could not fully separate "headless mode never shows the dialog" from "the workdir was `.claude`-nested" as the sole cause (time/budget didn't allow a clean `-p` + non-`.claude`-path control run) — flagging this as an open question, not a settled finding, but the CLI's own text plus the total absence of `PermissionRequest`/`Notification` across every `-p` run is suggestive that headless mode is not the reliable capture path.

### Real payloads (redacted only where a path/id would be noise)

`PreToolUse` — Bash, an auto-approved (no dialog needed) run (`docs/verification/fixtures/spike-sb/claude-bash-pretooluse-no-escalation.json`):
```json
{"hook_event_name":"PreToolUse","tool_name":"Bash",
 "tool_input":{"command":"echo hello-interactive-spike-approve","description":"Echo a test string"},
 "tool_use_id":"toolu_015j..."}
```
This run is kept deliberately alongside the PermissionRequest captures: it shows `PreToolUse` fires and carries `tool_input` regardless of whether the CLI ends up asking for permission — a plain `echo` never escalated to `PermissionRequest` at all (auto-approved), only `touch`/`Edit` did (see below). `PreToolUse` is a strictly *upstream* signal from the permission decision, not proof one is coming.

`PreToolUse` — Write (headless `-p` probe; not saved as a fixture since it never reached `PermissionRequest` — see the headless-mode caveat above):
```json
{"hook_event_name":"PreToolUse","tool_name":"Write",
 "tool_input":{"file_path":"/…/notes.txt","content":"hello\n"}}
```

`PreToolUse` — Edit (`docs/verification/fixtures/spike-sb/claude-edit-permissionrequest-run1.json`):
```json
{"hook_event_name":"PreToolUse","tool_name":"Edit",
 "tool_input":{"file_path":"/…/edit-target3.txt","old_string":"line two",
               "new_string":"line TWO EDITED","replace_all":false}}
```
**Edit's diff-shaped field is `old_string`/`new_string`/`replace_all`, not a unified diff** — this answers the exact open question in the spike brief. A unified-diff-*like* structure does exist, but only downstream, in `PostToolUse`'s `tool_response.structuredPatch` (same fixture):
```json
"structuredPatch":[{"oldStart":1,"oldLines":3,"newStart":1,"newLines":3,
  "lines":[" line one","-line two","+line TWO EDITED"," line three"]}]
```

`PermissionRequest` — Bash (`claude-bash-permissionrequest-run1.json`, the only run where a Bash call escalated to a real dialog — see taxonomy note on stability), full payload:
```json
{"hook_event_name":"PermissionRequest","tool_name":"Bash",
 "tool_input":{"command":"touch approval-test.txt","description":"Create empty approval-test.txt file"},
 "permission_suggestions":[
   {"type":"addDirectories","directories":["/Users/…/spike-sb-work"],"destination":"session"},
   {"type":"setMode","mode":"acceptEdits","destination":"session"}]}
```
The on-screen dialog it corresponds to (decoded PTY capture): `Bash command  touch approval-test.txt  Create empty approval-test.txt file / Do you want to proceed? / 1. Yes / 2. Yes, and always allow access to spike-sb-work/ from this project / 3. No`. `permission_suggestions` is a genuinely useful field for a structured-approval UI: it's the CLI itself telling the client what one-click follow-ups are meaningful.

`Notification` (both PermissionRequest runs, identical shape):
```json
{"hook_event_name":"Notification","message":"Claude needs your permission","notification_type":"permission_prompt"}
```

**Not captured**: `PermissionDenied`. Across ten runs I only ever produced *approve* or *accept-with-mode-change* outcomes (an attempted "No" selection landed on the wrong numbered option for Edit's shorter menu and instead selected "acceptEdits"); the schema (`www.schemastore.org/claude-code-settings.json`) confirms `PermissionDenied` as a real, distinct event name, but I did not observe its payload shape live. Flagged as unfinished, not fabricated.

## R-SPK-B.2 — Codex app-server `requestApproval` (real capture)

### Method — no wiring existed, so I built the minimal client

`internal/adapter/codex/codex.go`'s package doc is accurate: there is no live app-server producer anywhere in this repo. `codex app-server` (default `--listen stdio://`) speaks JSON-RPC 2.0 over newline-delimited JSON on stdio — confirmed by `codex app-server generate-json-schema --out ... --experimental`, which dumps the exact protocol (`ClientRequest.json`, `ServerRequest.json`, etc.) without needing to guess field names by trial and error. I wrote a ~90-line standalone Python JSON-RPC client (`.claude/jobs/6878515f/tmp/spike-sb/codex_client.py`, no swarm code touched) that does `initialize → thread/start(approvalPolicy:"untrusted") → turn/start(input:[{type:"text",text:...}])`, logs every line verbatim to an ndjson transcript, and on receiving a server-initiated request named `item/commandExecution/requestApproval` records it and replies `{"decision":"accept"}` so the turn completes.

It worked on the first real run. Two independent runs (different commands) both produced the request; the file each command was supposed to create was actually created afterward, confirming the accept round-trip is real, not just captured in transit.

### Real payload (`docs/verification/fixtures/spike-sb/codex-requestapproval-run1.json`)

```json
{
  "method": "item/commandExecution/requestApproval",
  "id": 0,
  "params": {
    "threadId": "019f754d-7234-...", "turnId": "019f754d-780a-...",
    "itemId": "exec-ce472af0-...", "startedAtMs": 1784379444437,
    "environmentId": "local",
    "command": "/bin/bash -lc 'touch codex-approval-test.txt'",
    "cwd": "/Users/…/codex-workdir",
    "commandActions": [{"type": "unknown", "command": "touch codex-approval-test.txt"}],
    "proposedExecpolicyAmendment": ["touch", "codex-approval-test.txt"],
    "availableDecisions": ["accept",
      {"acceptWithExecpolicyAmendment": {"execpolicy_amendment": ["touch", "codex-approval-test.txt"]}},
      "cancel"]
  }
}
```

Run 2 (`codex-requestapproval-run2.json`, a redirect command) has an **identical field set**, only the values (ids, command text) differ — the `command`/`cwd` fields are stable across runs, confirming R-SPK-B.4's "stable across two runs" column. `reason` (documented in the schema as "optional explanatory reason, e.g. request for network access") was `null`/absent in both — neither test command was ambiguous enough to trigger it, so its populated shape remains unobserved (same honesty caveat as `PermissionDenied` above).

The adapter's `eventSources` name (`codex.go:44`, `item/commandExecution/requestApproval`, mapped to `idle`/`permission`) matches the real wire method name exactly, including the schema-documented note that this is "the NEW API... used for Turns started via `turn/start`" — there is a second, legacy shape (`ExecCommandApprovalParams`, no `item/` prefix, `callId`/`conversationId` instead of `itemId`/`threadId`) that the adapter's naming does **not** cover; worth a one-line note on the adapter's doc comment if codex's legacy conversation API is ever wired up instead of the app-server's new `turn/start` API.

## R-SPK-B.4 — Taxonomy

| CLI | event | field path | example (redacted) | stable across runs |
|---|---|---|---|---|
| claude | PreToolUse | `tool_input.command`, `.description` | `"touch approval-test.txt"` | yes (Bash: 3 runs, incl. the auto-approved one) |
| claude | PreToolUse | `tool_input.file_path`, `.content` | Write | shape only, 1 run |
| claude | PreToolUse/PermissionRequest | `tool_input.file_path`, `.old_string`, `.new_string`, `.replace_all` | Edit | **yes — identical across run1/run2** (byte-for-byte same keys, only path/id values differ) |
| claude | PreToolUse | `tool_input.file_path` | Read | yes (2 runs) |
| claude | PermissionRequest | `tool_name` + `tool_input` (same shape as PreToolUse) + `permission_suggestions[].type` | `addDirectories` \| `setMode` | **only 1 genuine Bash escalation observed** (`echo` never escalated); Edit escalation seen twice, identical shape both times |
| claude | Notification | `notification_type` | `"permission_prompt"` | yes, every run that reached PermissionRequest (3/3) |
| claude | PostToolUse (Edit) | `tool_response.structuredPatch[]` | unified-diff-like `{oldStart,oldLines,lines:[...]}` | seen once (only one Edit run reached resolution before timeout) |
| claude | PermissionDenied | — | **not observed in any of 10 runs** | n/a |
| codex | item/commandExecution/requestApproval | `params.command` | `"/bin/bash -lc '...'"` | yes (2 runs, identical key set) |
| codex | item/commandExecution/requestApproval | `params.cwd` | abs path | yes (2 runs) |
| codex | item/commandExecution/requestApproval | `params.reason` | `null` (schema: populated for e.g. network access) | yes (both null) |
| codex | item/commandExecution/requestApproval | `params.availableDecisions[]` | `accept` \| `acceptWithExecpolicyAmendment` \| `cancel` | yes (2 runs) |
| codex | item/fileChange/requestApproval | — | **not observed** (no file-edit prompt triggered in the runs I had budget for) | n/a |

Honest accounting: the Bash `PermissionRequest` capture happened once, not twice — the first several attempts hit either the workspace-trust dialog or an auto-approved benign command before I found the reliable trigger (a mutating command, e.g. `touch`, in an already-trusted non-`.claude` directory). Edit's `PermissionRequest`, by contrast, really was captured twice independently with an identical field set, which is the strongest stability evidence in this table.

## R-SPK-B.3 — Production integration shape

**The capture mechanism itself needs zero I/O-boundary changes.** `internal/adapter/fixture.go`'s `HookPayload.Raw json.RawMessage` (`fixture.go:38`) *already* stores the full raw body — `char.go`'s hook sink stores whatever's posted to it verbatim; every fixture above proves `tool_input`, `permission_suggestions`, `structuredPatch` etc. all survive intact through the *characterization* path today. There is no E9.2 violation anywhere in this spike's design: the relay and the Codex client are both outside `internal/adapter` entirely (a throwaway `cmd/swarm-char`-adjacent binary and a standalone script), and neither touches a banned token (`os.Open*`, `net.Listen/Dial`, `exec.Command/LookPath`, `syscall.*` — the list in `internal/adapter/boundary_test.go:23-30`).

**The gap is specifically in *production*, not characterization**: `cmd/swarm/main.go:516`'s `parseHookStdin` deliberately keeps only top-level **string** fields (skipping `turn`/`interaction`, dropping any nested object like `tool_input` entirely) because the daemon callback (`hookclient.Callback`) and the engine's `SignalSource.Descriptor` are typed `map[string]string` — a flat status-dimension model, not a payload-carrying one. That's not a bug; it's the E9.1 contract as designed. Carrying `tool_input`/`command` through to a structured-approval UI means either (a) adding a raw-body field to the callback/payload that bypasses the flattening, or (b) adding an opt-in `SignalSource.CapturesRaw bool` (or similar) so an adapter can declare "this event's body should be preserved, not flattened."

**Both of those are schema changes to `internal/adapter`, and this repo has already ruled on that exact question, twice, before this spike started**: `docs/verification/audit-002-remote-control-design.md:31` — *"The adapter boundary is frozen (Epic 9) — extending it is its own ADR-level change"* — and `docs/research/remote-control-design.md:54` (G-6) — *"The adapter boundary is frozen (Epic 9) — extending it is ADR-level work."* `internal/adapter/adapter.go:1`'s package doc independently calls the boundary "FROZEN." Adding a field is additive in the Go-compatibility sense (`internal/adapter/adapter_test.go`'s `TestFrozenTypeShape` uses named-field struct literals, so a new field wouldn't break that specific test), but the freeze here is a **process** commitment, not just a compile-time one — the two docs above pre-committed to requiring an ADR for exactly this kind of extension, independent of E9.2's grep.

**So: additive-safe technically (E9.2 clean), ADR-required procedurally (repo convention, already on record).** Recommend the ADR title something like "Structured hook-body capture for approval UIs," scoped narrowly to: (1) a `RawBody json.RawMessage` (or `[]byte`) field on `HookPayload`/the daemon callback, populated only when an adapter's `SignalSource` opts in; (2) explicit non-goals (no change to the flattened `map[string]string` descriptor path, no new I/O primitive, no adapter owning the raw body's transport).

## Deliverables

- This file: `docs/verification/spike-SB.md`
- `docs/verification/fixtures/spike-sb/claude-bash-permissionrequest-run1.json` (the one genuine Bash `PermissionRequest`), `claude-bash-pretooluse-no-escalation.json` (a benign Bash call that never escalated, for contrast), `claude-edit-permissionrequest-run1.json` + `-run2.json` (two independent Edit `PermissionRequest` captures, identical shape) — real `adapter.Fixture` JSON (schema-valid, `PTYCapture` + `HookPayloads` both populated) from the actual `swarm-char` binary driving real `claude`.
- `docs/verification/fixtures/spike-sb/codex-requestapproval-run1.json`, `codex-requestapproval-run2.json` — real captured JSON-RPC requests from `codex app-server`.
- `docs/verification/fixtures/spike-sb/codex-rpc-transcript-run1.ndjson` — full send/recv JSON-RPC transcript for run 1 (initialize through turn completion) for anyone who wants the surrounding context.
- Throwaway harness (relay + Codex client + settings JSON + scripted-input files), left under `.claude/jobs/6878515f/tmp/spike-sb/` per instructions — not committed.
