# Epic 14 — Real-CLI Smoke / Characterization Harness (T13)

**Bead**: `agents-tracker-54z` (Epic 14) · **Resolves**: Epic 11 deferrals **D1** and **D2** plus the bead-54z VERIFY list.

**Status**: harness BUILT and COMPILE-verified; **D1 and D2 remain VERIFY-pending until a human runs it once** against the authenticated CLIs. This is by design — confirming the real CLI behavior is a BILLABLE action that must never run automatically or in CI.

## What it is

A skip-by-default, build-tag-gated harness that, when a human runs it, drives the
**real** `claude` and `codex` CLIs through the frozen adapter contract
(`Command`/`Resume`), captures their real hook/event stream + PTY output, asserts
the adapters' descriptors/`SignalSources` match reality (failing loudly on drift),
and — on drift — re-records the adapter fixtures so they track the real format
again (T-6).

| File | Role |
|---|---|
| `internal/smoke/doc.go` | untagged package doc; keeps the package buildable when the tag is absent (no harness logic) |
| `internal/smoke/realcli.go` | `//go:build realcli` engine: PTY driver, stand-in daemon hook socket, app-server capture, fixture rewrite |
| `internal/smoke/realcli_test.go` | `//go:build realcli` entrypoint `TestRealCLISmoke` (subtests `claude`, `codex`) |

## Safety gating (BILLABLE — never CI — never automatic)

Running the harness launches the real CLIs, which is billable and auth-requiring.
Two independent gates make an accidental run impossible:

1. **Build tag `//go:build realcli`** — excluded from the untagged `go build ./...`
   / `go test ./...`, and **no CI job passes `-tags realcli`** (CI's only build tag
   is `integration`, on two `internal/engine` jobs). Proof:
   - `grep -n "\-tags" .github/workflows/ci.yml` → only `-tags integration`.
   - `grep -rn realcli .github/workflows/ci.yml` → no matches.
   - `go test ./internal/smoke/` (untagged) → `[no test files]`; `TestRealCLISmoke`
     does not exist without the tag, so nothing launches.
2. **Runtime opt-in `SWARM_REALCLI=1`** — without it every subtest `t.Skip`s, so
   even a stray `go test -tags realcli ./...` launches nothing. Each subtest also
   skips when its CLI is absent from PATH.

## How it is verified WITHOUT running it (compile-only)

```bash
go build -tags realcli ./...   # compiles realcli.go            → clean
go vet   -tags realcli ./...   # type-checks realcli_test.go too → clean
go build ./...                 # normal build, harness excluded  → clean
go test  ./internal/smoke/     # untagged: [no test files]       → nothing runs
```

None of these launch a CLI.

## The exact human command (run only when you intend to spend money)

```bash
SWARM_REALCLI=1 go test -tags realcli -run TestRealCLISmoke -v ./internal/smoke
```

Optional tuning env (all have safe defaults):

| Env | Meaning |
|---|---|
| `SWARM_REALCLI_TIMEOUT` | per-run wall clock (default `45s`) |
| `SWARM_REALCLI_CLAUDE_PROMPT` | initial prompt driving Claude to a tool use |
| `SWARM_REALCLI_CLAUDE_APPROVE` | keystrokes sent to approve Claude's permission prompt |
| `SWARM_REALCLI_CODEX_PROMPT` | initial prompt for Codex |
| `SWARM_REALCLI_CODEX_APPSERVER` | argv for the D1 app-server capture (e.g. `codex app-server`); empty ⇒ D1 skipped |
| `SWARM_REALCLI_CODEX_INIT` | path to a JSON-RPC handshake file fed to the app-server's stdin |
| `SWARM_REALCLI_UPDATE_FIXTURES` | `always` \| `on-drift` (default) \| `never` |

## What it checks (the VERIFY list)

### Claude (D2 — real hook value names)

Claude's real hooks travel the **production** path unchanged: the adapter's
`--settings` injection wires each event to `swarm hook <event>`, which posts an
authenticated `engine.Callback` to the daemon socket. The harness builds the
`swarm` binary into a temp dir on PATH, stands up that socket, and injects the
same four per-session env vars the daemon injects at spawn — so it decodes the
**real** callback stream the daemon would see (via `hookclient.Decode`). It then
asserts:

- **Hook event names**: every event the CLI actually fired is one the adapter
  declares in `SignalSources` (an undeclared event ⇒ drift). Confirms whether
  `PermissionRequest` and the other declared events are real.
- **Notification subtype field**: if a `Notification` fired, its real payload
  carries the field the adapter reads its subtype from (currently
  `notification_type`) — else drift. Confirms the D2 field name.
- **Conversation-id surface**: `ExtractConversationID` recovers the id from the
  live capture (the `Session <uuid>` marker), and the composed `--resume <id>`
  argv is accepted by the real CLI.
- **Version**: detected version is in the adapter's supported range (L-2).

### Codex (D1 — typed app-server event stream + VERIFY items)

Codex reports through typed app-server JSON-RPC events whose live producer was
deferred (D1), so `swarm hook` is not involved. The harness:

- launches Codex via `Command` with `--sandbox read-only` + prompt and captures
  the PTY (confirms the **launch flags** compose and do not error the launch);
- confirms the **conversation-id surface** (`threadId`) when present, and that the
  composed **`codex resume <id>`** subcommand is accepted (an
  `unknown/unrecognized subcommand` marker ⇒ drift);
- when `SWARM_REALCLI_CODEX_APPSERVER` is set, captures the **real app-server
  typed-event stream** and compares the observed JSON-RPC method names against the
  adapter's declared `eventSources` (`turn/started`, `turn/completed`,
  `item/commandExecution/requestApproval`) — a declared method the real stream
  never carries ⇒ drift; a real status-shaped method the adapter does not declare
  is surfaced as a note.

The exact app-server invocation and its JSON-RPC handshake are themselves VERIFY
items, so the harness takes both from the environment rather than hard-coding an
unconfirmed protocol.

## On drift: fixture re-record

When drift is observed (or `SWARM_REALCLI_UPDATE_FIXTURES=always`), the harness
re-records the affected fixture — `internal/adapter/claude/testdata/claude.json`
and/or `internal/adapter/codex/testdata/codex.json` — from the live capture,
validating it first (never persisting a fixture the loader would reject). The run
logs `RE-RECORDED <path>` loudly; **review the git diff before committing** so a
drift is an intentional, reviewed characterization update.

## Deferrals still open until this is run once

- **D1** — the real Codex app-server typed-event stream: characterized only when a
  human runs the harness with `SWARM_REALCLI_CODEX_APPSERVER` pointed at the real
  app-server. Until then the Codex runtime status driver remains the grid
  heuristic (audit-010), and the typed mapping stays fixture-proven.
- **D2** — the real Claude hook value names (`PermissionRequest` existence, the
  `notification_type` field, `permission_prompt`/`idle_prompt`/`auth_success`):
  confirmed only when a human runs the Claude subtest against the authenticated
  CLI. The descriptors are drift-resilient and B5's safe default guards the
  unknown-name case in the meantime.

Both are marked VERIFY-pending here and stay so until the harness is run once and
its output (and any re-recorded fixture) is reviewed and committed by a human.
