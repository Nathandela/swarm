# R1 verification: Codex app-server two-client feasibility gate

- **Date:** 2026-08-15
- **Gate:** `docs/specifications/remote-control-product-playbook.md` section 8.2; `docs/specifications/mirror-program.md` M4.0
- **Wave:** R1 (`agents-tracker-hggx.2`)
- **Installed CLI:** `codex-cli 0.147.0` at `/usr/local/bin/codex` (vendored binary
  `@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`)
- **Host:** macOS 26.5.2 (Darwin 25.5.0), arm64
- **Account:** ChatGPT auth (`codex login status` -> "Logged in using ChatGPT"), model `gpt-5.6-luna`
- **Scratch workspace:** `/Users/Nathan/.claude/jobs/72c288b2/tmp/codex-gate/` (isolated `CODEX_HOME`,
  git-initialised `ws/` containing only `README.md`; codex never operated outside that directory)
- **Fixtures:** `docs/verification/r1-codex-fixtures/`

## Overall verdict: PASS

All five legs pass. `codex app-server` supports true concurrent multi-client attachment to a single
live thread: a second JSON-RPC client can observe streaming item events, start turns, steer a
running turn, interrupt a running turn, and answer approval requests, and the real Codex TUI
attached to the same thread reflects every one of those actions live without losing ownership.

**RC-D4 consequence:** RC-D4 (Claude Code and Codex are the first full structured-chat providers) is
**unblocked**. Per section 8.2 the follow-on obligations now apply: make app-server the R7 session
backend, batch text deltas at the adapter, normalize tool and file-change items, use native
approvals, and replace heuristic status. No Codex semantic operation may be implemented by terminal
keystroke injection. The rollout-file-tail mirror-only fallback is **not** required.

## Corrections to the plan's guesses

Two assumptions in the playbook's section 8.2 text were wrong and the mechanics below are what was
actually run:

1. **The `--listen unix://PATH` endpoint is a WebSocket endpoint, not a raw JSON-lines socket.** A
   client must perform an HTTP/1.1 upgrade (`GET /rpc`, `Upgrade: websocket`,
   `Sec-WebSocket-Version: 13`) over the UDS and then exchange JSON-RPC messages as WebSocket text
   frames. Writing bare newline-delimited JSON to the socket produces total silence: no response, no
   error, no server log. This cost roughly ten minutes of the timebox and is the single most
   important integration detail for R7. See `r1-codex-fixtures/ws-handshake.txt`.
2. **`codex app-server proxy --sock` is not the bridge to this endpoint.** It forwards stdio bytes
   verbatim without performing the WebSocket upgrade, so it cannot drive a `--listen unix://`
   server. It targets the separate daemon control socket under `$CODEX_HOME/app-server-control/`.

The protocol is fully introspectable offline, which removes all guesswork about method names:

```
codex app-server generate-json-schema --out <dir>   # JSON Schema per type, v1/ and v2/ subdirs
codex app-server generate-ts --out <dir>            # TypeScript bindings incl. method-name unions
```

## Exact commands run

```bash
# leg 1 - supervised server
CODEX_HOME=$SCRATCH/codex-home codex app-server --listen unix://$SCRATCH/codex.sock \
  > logs/appserver.stdout.log 2> logs/appserver.stderr.log &

# logging MITM on the UDS so the TUI's own frames were captured too
python3 mitm.py $SCRATCH/mitm.sock $SCRATCH/codex.sock $SCRATCH/wire.bin &

# leg 2 - real TUI inside a PTY we own (python pty.fork, 120x40, TERM=xterm-256color)
codex --remote unix://$SCRATCH/mitm.sock      # cwd = $SCRATCH/ws

# legs 3-5 - second client: WebSocket-over-UDS JSON-RPC (rpc.py)
```

Config used for the thread under test: `approval_policy = "on-request"`,
`sandbox_mode = "read-only"` (read-only is what forces a file write to raise an approval).

## Per-leg verdicts

### Leg 1 - app-server on a unix socket as a supervised child: PASS

`codex app-server --listen unix://<scratch>/codex.sock` started under an isolated
`CODEX_HOME`, created `srw-------` at the requested path within ~3 s, and ran as an ordinary child
process (node launcher pid plus the vendored rust binary pid, both killable by the parent). It stayed
up across three full probe runs. Both `logs/appserver.stdout.log` and `logs/appserver.stderr.log`
remained **empty for the entire session** - the server emits nothing on either stream, so a
supervisor must not treat stdout/stderr silence as a liveness or error signal; liveness has to come
from the socket itself. Server-side diagnostics land in `$CODEX_HOME/logs_2.sqlite`, not on the
process streams.

### Leg 2 - real Codex TUI drives a thread through the server: PASS

The stock `codex --remote unix://<sock>` TUI was launched inside a PTY created with `pty.fork` and a
`TIOCSWINSZ` of 120x40. It completed its boot handshake through the server (banner
`>_ OpenAI Codex (v0.147.0)`, model resolved to `gpt-5.6-luna`, MCP servers booted), accepted typed
input, and on Enter drove a real turn end to end. With the prompt "create a file named hello.txt
containing the text hi" the TUI rendered `• Added ws/hello.txt (+1 -0)` and the approval dialog
`Would you like to make the following edits? 1. Yes, proceed (y) ...`. A `thread/loaded/list` call
from the second client showed exactly one new thread id appearing the moment the TUI booted
(`01a00335-9a50-79e2-8253-e08861d67c4d` in the legs 2-3 run), confirming the TUI's thread is a
first-class server-side thread and not a client-local construct.

### Leg 3 - second JSON-RPC client observes the same thread live, without stealing ownership: PASS

A second client connected to the same socket, sent `initialize` + `initialized`, discovered the
TUI's thread via `thread/loaded/list` (returns `{"data": ["<threadId>", ...]}`, plain id strings),
and joined it with `thread/resume {"threadId": ...}`. The response carried the TUI's own thread
object, including `"preview": "create a file named hello.txt containing the text hi"` - proof it is
the same thread, not a fork.

After joining, the observer received the live stream of that turn: **97 frames** including
49 `item/agentMessage/delta`, 5 `item/commandExecution/outputDelta`, 6 `item/started`,
6 `item/completed`, 4 `turn/diff/updated`, 3 `thread/status/changed`, 3 `thread/tokenUsage/updated`
and 1 `turn/completed`. In the leg 4 run the same mechanism delivered **586 `item/agentMessage/delta`
frames** for a single turn, so token-level streaming reaches the second client at full fidelity.

Ownership was not stolen or mutated. Across all three runs the TUI stayed connected and fully
functional after the observer joined: it kept rendering its own turn, kept accepting keystrokes, and
subsequently rendered turns the observer initiated. `thread/resume` on a running thread is documented
in the schema as a rejoin ("If thread_id identifies a running thread, app-server rejoins that thread")
and behaved exactly that way. `thread/unsubscribe {threadId}` exists for clean detach.

**Integration constraint found:** `thread/resume` fails with
`{"code": -32600, "message": "no rollout found for thread id <uuid>"}` for a thread that exists but
has not yet run its first turn - the rollout file is created when the first turn starts. In the runs
above the observer had to retry for 15-17 s after TUI boot before the join succeeded. R7 must either
retry-with-backoff or, preferably, have the Swarm daemon be the client that calls `thread/start`
itself so the thread id and rollout are known before any TUI attaches. This matters for RC-D1
(Swarm owns the session from process creation) and is the cleanest topology anyway.

### Leg 4 - second client's turn/start, turn/steer, turn/interrupt and approval reply all affect that exact thread, and the TUI reflects it: PASS

Method names are the real ones from the server's own generated bindings, not guesses.

**Approval reply - PASS.** The server delivered the approval to the **observer** as a JSON-RPC
server-request on the observer's connection:
`{"method":"item/fileChange/requestApproval","id":0,"params":{"threadId":"01a00335-...","turnId":"01a00335-d9d8-...","itemId":"exec-29bcdedd-...","startedAtMs":1786760259641,"reason":null,"grantRoot":null}}`.
The observer replied `{"decision":"accept"}` (schema: `accept` | `acceptForSession` | `decline` |
`cancel`). The server then broadcast
`{"method":"serverRequest/resolved","params":{"threadId":"01a00335-...","requestId":0}}` so other
clients can retire their dialog. **No key was ever pressed in the TUI**, yet the TUI's approval
dialog closed, the turn continued, and the TUI printed `• Created ws/hello.txt containing hi.`
`ws/hello.txt` exists on disk with contents `'hi\n'`. This is the exact fan-out/first-responder
approval semantic the phone surface needs.

**turn/start (idle thread) - PASS.** `turn/start {"threadId":..., "input":[{"type":"text","text":...}]}`
returned `{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","status":"inProgress",...}}`, followed
by `turn/started` and 586 `item/agentMessage/delta` frames. The TUI, which had typed nothing,
rendered the entire response (verified: the counting output "1. One ... 40. Forty is the number of
days traditionally associated with Lent" appears in the TUI's PTY byte stream).
Note `input` is an **array** of `UserInput`; passing an object yields
`{"code":-32600,"message":"Invalid request: invalid type: map, expected a sequence"}`.

**turn/steer (mid-turn) - PASS.** With the turn actively streaming, the observer sent
`turn/steer {"threadId":..., "expectedTurnId":"01a0033b-d0be-...", "input":[{"type":"text","text":"STOP counting immediately. Reply with only the word STEERED."}]}`
and received `{"turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d"}` - the same turn id, so the steer
was applied to that exact in-flight turn rather than queued as a new one. The agent's own output
stream then ended with `STEERED`, and the TUI rendered the steer as a user message
(`› STOP counting immediately. Reply with only the word STEERED.`) followed by `• STEERED`. The
`expectedTurnId` field gives a built-in optimistic-concurrency guard, which is exactly what a
two-surface composer needs to avoid steering a turn that already ended.

**turn/interrupt - PASS.** Re-run with correct timing (the first attempt raced a turn that had
already finished and correctly returned `{"code":-32600,"message":"no active turn to interrupt"}`).
The observer started a long essay turn, waited for 279 `item/agentMessage/delta` frames to arrive,
then sent `turn/interrupt {"threadId":..., "turnId":"01a0033d-812e-7fe1-82d1-c8703ec5e834"}`. It
returned `{}` and the server immediately emitted
`turn/completed` with `"status": "interrupted"` for that exact turn id (`durationMs: 6593`). The
delta count froze at 279 and was still 279 after a further 14 s, so generation genuinely stopped.
The TUI displayed `■ Conversation interrupted - tell the model what to do differently.` - again with
no keystroke sent to the terminal.

### Leg 5 - protocol fixtures and version recorded: PASS

Raw JSON-RPC frames were captured on the second client's connection for every event and response
type observed, plus the WebSocket upgrade bytes from the real TUI's connection captured through a
logging MITM on the UDS. The complete method inventory was extracted from the server's own generated
TypeScript bindings, so it is authoritative rather than observational: 27 `thread/*` + `turn/*`
client requests, 71 server notifications, 8 server-to-client requests.

## Fixture inventory

All files under `docs/verification/r1-codex-fixtures/`. Every file was scanned for
`eyJ|sk-|Bearer|access_token|refresh_token|api[_-]?key|id_token|password|secret` and for e-mail
addresses before copying; the scan is clean. The 3.8 MB `app/list/updated` connector-catalog frame
was **excluded** - it is unrelated to the gate and its MCP connector schemas contain the substrings
`sk-` and `secret`.

| File | Contents |
|---|---|
| `codex-version.txt` | `codex-cli 0.147.0` |
| `ws-handshake.txt` | First 400 bytes off the UDS: the real TUI's `GET /rpc` WebSocket upgrade request and the server's `101 Switching Protocols` reply |
| `protocol-methods.txt` | Full authoritative method inventory (client requests, server notifications) generated from `codex app-server generate-ts` |
| `frame-samples.json` | 22 deduplicated raw JSON-RPC frames, one per distinct kind seen on the observer connection, with direction and timestamp: `initialize`, `initialized`, `thread/loaded/list`, `thread/resume`, `thread/started`, `thread/status/changed`, `thread/tokenUsage/updated`, `thread/goal/cleared`, `remoteControl/status/changed`, `turn/start`, `turn/started`, `turn/steer`, `turn/interrupt`, `turn/completed`, `turn/diff/updated`, `item/started`, `item/completed`, `item/agentMessage/delta`, `item/commandExecution/outputDelta`, `item/fileChange/requestApproval`, `serverRequest/resolved`, `account/rateLimits/updated` |
| `approval-request.json` | The `item/fileChange/requestApproval` server-request as delivered to the second client |
| `turn-start.json`, `turn-started.json` | `turn/start` response and the matching `turn/started` notification |
| `turn-steer.json` | `turn/steer` response showing the unchanged `turnId` |
| `turn-interrupt.json`, `turn-completed-interrupted.json` | `turn/interrupt` response `{}` and the `turn/completed` frame with `"status": "interrupted"` |
| `errors-observed.json` | The three real error frames encountered, useful as adapter test cases |

Bulk artifacts left in the scratch directory and intentionally **not** copied (size, and they add
nothing beyond the deduplicated samples): full per-connection frame logs
`fixtures/observer*.jsonl` (~15 MB total), the raw UDS wire capture `wire.bin`, and the TUI PTY byte
streams `logs/tui-*.raw` with their decoded `.snaps` files. Scratch root:
`/Users/Nathan/.claude/jobs/72c288b2/tmp/codex-gate/`.

## Notes carried into R7

1. WebSocket-over-UDS, `GET /rpc`. Go side should use a websocket client over a `net.Dial("unix", ...)`
   connection; do not write raw JSON to the socket.
2. Have the Swarm daemon call `thread/start` and own the thread id, then let the TUI attach. This
   sidesteps the "no rollout found" join race and matches RC-D1.
3. Approvals fan out to attached clients as server-requests and are resolved first-come; use
   `serverRequest/resolved` to retire a stale dialog on the surface that did not answer.
4. `turn/steer` carries `expectedTurnId` - propagate it from the phone composer rather than
   inventing a Swarm-side guard.
5. `turn/interrupt` on an already-finished turn is an error, not a no-op; treat it as benign.
6. `item/agentMessage/delta` volume is high (586 frames for one ~40-line answer). The playbook's
   requirement to batch text deltas at the adapter is confirmed necessary.
7. app-server writes nothing to stdout/stderr; supervise on the socket, not the streams.
