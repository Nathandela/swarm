# RED evidence: ADR-010 Phase 1 agent read verbs (agents-tracker-r09r)

date: 2026-08-07
HEAD: f4318c4
worktree: .claude/worktrees/inter-session-orchestration

Scope: ADR-010 Amendment 1, A4/A5 Phase 1 — `swarm ls [--json]`, `swarm watch <id>`,
`swarm kill <id>`. Zero protocol changes: the verbs wrap the existing
`OpList`/`OpSubscribe`/`OpKill` client calls.

Test file: `cmd/swarm/agentverbs_test.go` (new).

## Frozen API the tests are written against

Following the internal/tui precedent (`internal/tui/tui.go:31` — a narrow, stub-friendly
daemon surface so every unit test runs with no daemon and no socket):

```go
type agentClient interface {
    List() ([]protocol.SessionView, error)
    Subscribe() (<-chan protocol.Event, error)
    Kill(id string) error
}

func runLS(args []string, c agentClient, stdout, stderr io.Writer) int
func runWatch(args []string, c agentClient, stdout, stderr io.Writer) int
func runKill(args []string, c agentClient, stdout, stderr io.Writer) int
```

`cmd/swarm/main.go` dispatch stays thin: resolve the socket (`SWARM_DAEMON_SOCK`, else the
daemon default, as `dialClient` already does) and hand the `*protocol.Client` to these.

## Commands

```
$ go build ./...
BUILD OK

$ go test ./cmd/swarm/ -run 'TestRunLS|TestRunWatch|TestRunKill|TestUsage_ListsAgentVerbs' -count=1
```

## Failing output (compile-fail red)

```
# github.com/Nathandela/swarm/cmd/swarm [github.com/Nathandela/swarm/cmd/swarm.test]
cmd/swarm/agentverbs_test.go:100:7: undefined: agentClient
cmd/swarm/agentverbs_test.go:177:15: undefined: runLS
cmd/swarm/agentverbs_test.go:217:13: undefined: runLS
cmd/swarm/agentverbs_test.go:273:13: undefined: runLS
cmd/swarm/agentverbs_test.go:314:12: undefined: runWatch
cmd/swarm/agentverbs_test.go:373:12: undefined: runWatch
cmd/swarm/agentverbs_test.go:401:10: undefined: runWatch
cmd/swarm/agentverbs_test.go:432:12: undefined: runWatch
cmd/swarm/agentverbs_test.go:467:14: undefined: runKill
cmd/swarm/agentverbs_test.go:482:14: undefined: runKill
cmd/swarm/agentverbs_test.go:482:14: too many errors
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]
FAIL
```

## RED interpretation

`go build ./...` is green — production code is untouched. The test binary fails to
COMPILE on the not-yet-existing production symbols only (the repo's "undefined-only" red,
per `internal/protocol/harness_test.go`): the `agentClient` interface and the three run
functions. All test scaffolding (the `fakeAgentClient` recorder, the SessionView fixtures,
the JSON decoders) compiles. `TestUsage_ListsAgentVerbs` additionally fails once the
package compiles, because `usage` in `cmd/swarm/main.go` does not yet document the verbs.

## What each test pins

| Test | Behavior |
| --- | --- |
| `TestRunLS_Table` | Minimal human table: `ID AGENT GROUP NAME` header plus one row per session; an empty roster still exits 0. |
| `TestRunLS_JSON` | `--json` marshals the FULL `[]protocol.SessionView` (raw status dims, server-derived group, both timestamps, summary), not a reduced projection. |
| `TestRunLS_ListError` | A daemon-side List failure is exit 1 with the cause on stderr, never a silent empty table. |
| `TestRunWatch_ImmediateMatch` | The initial `List` is consulted first: an already-satisfied predicate exits 0 at once (bounded by an elapsed assertion) for each of `needs_input` / `ready_for_review` / `completed`. |
| `TestRunWatch_MatchAfterEvents` | The Subscribe stream is filtered by session id and predicate; the FINAL matching `SessionView` is printed, not the stale List row. Includes `--until change` matching any event for that session. |
| `TestRunWatch_Timeout` | No match before the deadline is exit code 2 with a timeout message on stderr and no SessionView on stdout. |
| `TestRunWatch_BadArgs` | Missing session id, unknown `--until` value, and an unknown session id are each exit 1, immediately (an unknown session must not block until the timeout). |
| `TestRunKill` | Happy path passes the id through verbatim and confirms on stdout; a daemon error is exit 1 with no confirmation; a missing id refuses without ever calling Kill. |
| `TestUsage_ListsAgentVerbs` | The top-level `usage` string documents `swarm ls`, `swarm watch`, `swarm kill`. |
