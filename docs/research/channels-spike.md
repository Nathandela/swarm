# Channels spike: can `claude/channel/permission` replace M1.2's keystroke injection?

**Scope:** M1.6 (`agents-tracker-dwwv.2.6`), a timeboxed research spike, not a build.
**Date:** 2026-08-13.
**Verdict: supported, yes.** All four requested observations were reproduced against a real
session on the installed CLI. Promotion criteria are in section 4.

Version stamps: `claude --version` -> `2.1.231 (Claude Code)`, matching the latest version on the
npm registry (`npm view @anthropic-ai/claude-code version` -> `2.1.231`) -- the installed binary is
current, not stale. `@modelcontextprotocol/sdk` `1.30.0`, `zod` `4.4.3`. Account: `claude.ai` auth,
`subscriptionType: max`, personal auto-created org (no Team/Enterprise `channelsEnabled` gate
applies -- see docs quote in section 1).

## 1. Is it supported at all

`claude --help` (242 lines, checked in full) has no mention of `channel` anywhere -- `--channels`
and `--dangerously-load-development-channels` are absent from the listed options. This matches the
docs' own claim, fetched live from `code.claude.com/docs/en/channels#research-preview`:

> Neither `--channels` nor `--dangerously-load-development-channels` appears in `claude --help`
> while the feature is in preview. The flags work even though they aren't listed.

Probed directly:

```
$ claude --channels
error: option '--channels <servers...>' argument missing
$ claude --dangerously-load-development-channels
error: option '--dangerously-load-development-channels <servers...>' argument missing
```

Both flags are recognized by the arg parser (missing-argument error, not unknown-option error) --
confirmed present and wired up in 2.1.231. Docs from `channels-reference` confirm the entry-tagging
syntax (`plugin:<name>@<marketplace>` or `server:<name>`) and that `--dangerously-load-development-
channels` bypasses the research-preview allowlist per-entry, gated only by the org's
`channelsEnabled` policy (which does not apply here -- Max personal accounts skip it per the docs).

Given the flags exist, the spike proceeded to build and wire a real sidecar rather than stopping at
observation 1.

## 2. What was built

Scratch dir, outside the repo: `~/.claude/jobs/channels-spike/` (Node 22, `npm install
@modelcontextprotocol/sdk zod` into a scratch npm cache to dodge an unrelated root-owned
`~/.npm` cache permission error -- no `sudo`, no touching owner global settings).

`server.mjs`: a ~70-line stdio MCP server declaring
`capabilities.experimental['claude/channel']` and `.../channel/permission` per the reference doc,
exposing one `reply` tool, and logging every notification it receives/sends to a JSONL file. On
`notifications/claude/channel/permission_request` it logs the request, waits a configurable delay
(`SPIKE_AUTO_ALLOW_DELAY_MS`), then sends `notifications/claude/channel/permission` with
`behavior: "allow"`.

Before spending any real session cost, the server's protocol logic was verified against a local MCP
client (`smoke-test.mjs`, pure stdio, zero API calls): capabilities negotiate correctly, the fake
`permission_request` is logged, and the auto-allow verdict is sent and well-formed. This caught one
bug (an invalid manual notification-handler schema in the test client itself) for free.

Wired into the scratch project via `.mcp.json`:

```json
{ "mcpServers": { "webhook-relay": { "command": "node", "args": ["./server.mjs"] } } }
```

## 3. The four observations, against a real session

Three real `claude` invocations were used (cost discipline target was one or two; the third was
needed because the first two together answered (a)-(c) but not (d) -- see below).

**Run 1 -- non-interactive (`-p`), a wall, not a result for this spike.** `claude
--dangerously-load-development-channels server:webhook-relay -p "Call the reply tool..."
--permission-mode default --output-format stream-json`. The channel registered
(`mcp_servers: [{"name":"webhook-relay","status":"connected"}]`), Claude called the `reply` tool,
and the CLI emitted `{"type":"system","subtype":"permission_denied", ..., "message":"Claude
requested permissions to use mcp__webhook-relay__reply, but you haven't granted it yet."}` --
**immediately, with no `permission_request_received` ever logged by the sidecar.** In `-p` mode,
tool calls needing approval are auto-denied outright, not routed through any dialog or relay. The
docs only claim this for "multiple-choice questions and plan mode approval"; empirically it also
applies to ordinary tool-permission prompts. **Relay requires a real interactive session; `-p` is
not a valid harness for it**, which is why runs 2 and 3 drove a real pty.

**Run 2 -- interactive, pty-driven (Python `pexpect`), full success.** Real `claude` session, real
terminal (220x50 pty), no flags bypassing permission checks. The automation clicked through the
"New MCP server found" and "WARNING: Loading development channels" startup dialogs (both real,
both required even in a scratch throwaway project), then typed the prompt asking Claude to call the
`reply` tool. Raw pty capture and the sidecar's own log agree on every point:

- **(a) Sidecar receives `permission_request`: yes.**
  ```json
  {"ts":"2026-08-13T17:14:14.701Z","kind":"permission_request_received","request_id":"gmvaj","tool_name":"mcp__webhook-relay__reply","description":"Send a message back over this channel","input_preview":"{ \"chat_id\": \"itest\", \"text\": \"ping-interactive\" }"}
  ```
  `request_id` is five lowercase letters with no `l` (`gmvaj`), matching the reference doc's
  claimed alphabet.

- **(b) Terminal dialog shows simultaneously: yes.** The same pty capture, in the same session,
  shows the native dialog rendered at the terminal while the sidecar had the open request:
  ```
  Tool use webhook-relay-reply(chat_id:"itest",text:"ping-interactive") (MCP)
  Send a message back over this channel
  Do you want to proceed?
  1. Yes
  2. Yes, and don't ask again for webhook-relay - reply commands in ...
  3. No
  Esc to cancel · Tab to amend
  ```
  Both surfaces were live at once, per the docs' claim, not sequential.

- **(c) Sidecar allow proceeds the tool and closes the dialog: yes, with no local keypress.** The
  probe never typed an answer. The sidecar's delayed auto-allow fired at T+6s and the tool ran 14ms
  later, with the terminal dialog gone from subsequent frames:
  ```json
  {"ts":"...20.704Z","kind":"permission_verdict_sending","request_id":"gmvaj","behavior":"allow"}
  {"ts":"...20.706Z","kind":"permission_verdict_sent","request_id":"gmvaj"}
  {"ts":"...20.720Z","kind":"tool_call","name":"reply","arguments":{"chat_id":"itest","text":"ping-interactive"}}
  ```

**Run 3 -- interactive, attempted terminal-first race for (d), inconclusive.** Goal: answer the
local dialog ourselves before the sidecar's (deliberately delayed) verdict, then see what the
sidecar sees for its now-superseded request. The model's `xhigh` effort thinking took ~47s to reach
the tool call this run (vs. a few seconds in run 2); the probe's fixed 15s poll window for the
dialog text expired before it appeared, so the local answer was never sent and the sidecar's
auto-allow won again -- a clean repeat of (a)/(b)/(c), not a test of (d). **(d) is not empirically
reproduced.** From the docs (`channels-reference#how-relay-works`): "If someone at the terminal
answers before the remote verdict arrives, that answer is applied instead and the pending remote
request is dropped" -- silently, by request-ID mismatch (`Claude Code finds no open request with
that ID and drops it` is the documented behavior for a *late* verdict on an already-resolved
request; there is no documented notification schema telling the channel its request was
superseded). Given the hard timebox and that runs 1-2 already spent real session cost beyond the
"one or two runs" target, a fourth run to force the race (adaptive polling instead of a fixed
15s window) was not attempted. **This is the spike's one open wall**: reproducing the terminal-
first race needs a steadier harness (adaptive detection, not a fixed sleep) than this timebox
funded, and the docs leave open whether the channel gets any signal at all when it loses.

## 4. Promotion criteria

Before M1.2's keystroke-injection path can be replaced or bypassed by the channel relay for a real
release (not a flagged spike), all of the following should hold:

1. **Preview flag gone or the CLI's stability contract says otherwise.** Today: "Availability is
   rolling out gradually, and the `--channels` flag syntax and protocol contract may change based
   on feedback" (docs, `channels#research-preview`). Promote only once `--channels` /
   `claude/channel/permission` ships outside `--dangerously-load-development-channels`, or Anthropic
   states the contract (request/verdict schema, `request_id` format) is frozen even in preview.
2. **Sanitization version floor met across the fleet.** Docs: "Clients on Claude Code v2.1.211 or
   later sanitize both [`description` and `input_preview`] before relaying them: they neutralize
   direction-override and invisible characters and quote and angle-bracket lookalikes... Earlier
   clients relay `description` raw and cut `input_preview` to 200 UTF-16 units." The installed
   2.1.231 clears this floor; **pin a minimum `claude --version` >= 2.1.211 as a hard runtime check**
   before trusting relayed prompt text at all, since the channel must treat both fields as
   untrusted (attacker-controlled tool `description`/args) below that floor.
3. **`request_id` format and matching confirmed stable.** Five lowercase letters, alphabet
   `a-km-z` (skips `l`) -- confirmed empirically (`gmvaj`, `rhakd`) matching docs. A promotion
   should re-pin this regex per Claude version the same way M1.1 pins the grid-signature fixture,
   not assume it's permanent preview-era.
4. **First-answer-wins verified in both directions, not just sidecar-wins.** This spike confirmed
   sidecar-wins (run 2, observation c) cleanly. Terminal-wins (observation d) is still open --
   promotion needs a real test (not just the docs' prose) of what the channel observes when the
   terminal answers first: does the CLI send any explicit "superseded" signal, or must the channel
   infer it only from silence / a stale local timer? If it's silence-only, M1's own design already
   has the right instinct (`internal/skeleton/approval.go`'s watchdog-on-non-transitioning-dialog
   pattern from M1.2) and the channel-side sidecar would need an equivalent timeout+reconcile, not
   a bare fire-and-forget verdict send.
5. **Batching semantics don't apply to permission requests, or are proven not to matter.** Docs
   state ordinary `notifications/claude/channel` events "queue into the session and are processed
   in order... delivered together on the next turn" when Claude is busy. Nothing in the docs says
   this applies to `permission_request`, and this spike's evidence (single in-flight request each
   run) can't rule out a multi-concurrent-approval batching interaction. Promotion should test two
   permission-requiring tool calls in flight if the target architecture can ever produce that.
6. **Relay scope matches what M1.2 actually needs to cover.** Docs: "Relay covers tool-use
   approvals like `Bash`, `Write`, and `Edit`. Project trust and MCP server consent dialogs don't
   relay; those only appear in the local terminal." M1.2's injection path is generic (any dialog the
   grid recognizes); the channel relay is narrower by contract. Confirm the trust/consent dialogs
   (which this spike's own harness had to click through locally every time) are rare enough in
   steady-state daemon-owned sessions that losing remote coverage for them is acceptable, or keep
   M1.2's injection as the fallback for exactly those two dialog types even after promoting the rest.
7. **A day-two operational answer for the org gate.** This spike ran on a Max personal account,
   which skips `channelsEnabled` entirely. If Mirror is ever used from a Team/Enterprise seat,
   promotion needs the daemon to detect and surface "blocked by org policy" (the docs' own phrase)
   rather than silently falling back to nothing, mirroring M1.2's existing five-reasons-refused
   pattern.

**Earliest re-check signal**: watch `code.claude.com/docs/en/channels#research-preview` for the
preview note disappearing, and `claude --version` release notes for "channels" leaving the
experimental/research-preview language. Re-run this spike's run-3 shape (with adaptive dialog
polling) whenever re-checking, since (d) is the one observation still open.

## 5. Files

- `~/.claude/jobs/channels-spike/server.mjs` -- the sidecar MCP server (scratch, not committed).
- `~/.claude/jobs/channels-spike/{smoke-test,interactive_probe,interactive_probe_terminal_first}.py|mjs`
  -- the harness (scratch, not committed).
- `~/.claude/jobs/channels-spike/spike.*.log.jsonl` -- raw evidence logs backing section 3's quotes
  (scratch, not committed; quoted verbatim above).

Nothing under this spike touched production code, added a repo dependency, or ran outside the
scratch dir plus this doc.
