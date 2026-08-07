# RED evidence: ADR-010 Phase 2 — spawn, lineage, roster badges (agents-tracker-zqsk)

date: 2026-08-07
HEAD: 77bf7e1
worktree: .claude/worktrees/inter-session-orchestration

Scope: ADR-010 D2/D4 and Amendment 1 A4/A5 Phase 2 — the `spawned_from` / `spawn_intent`
lineage plumbed end to end (wire -> server -> daemon -> meta -> roster), the `swarm spawn`
verb, and the TUI roster lineage badges.

Test files (all new):

| File | Piece |
| --- | --- |
| `internal/protocol/spawnlineage_test.go` | 1 — schema round-trip, handleLaunch validation, roster exposure, protocol.md rows |
| `internal/persist/spawnlineage_test.go` | 1 — Meta round-trip, durable key set |
| `internal/daemon/spawnlineage_test.go` | 1 — LaunchSpec -> reserved Meta stamping |
| `cmd/swarm/spawn_test.go` | 2 — the `swarm spawn` verb |
| `internal/tui/lineage_test.go` | 3 — roster lineage badges |

## Frozen API the tests are written against

Mirroring the `ResumedFrom` precedent at every hop:

```go
// internal/protocol/schema/schema.go
type LaunchReq struct {
    ...
    SpawnedFrom string `json:"spawned_from,omitempty"`
    SpawnIntent string `json:"spawn_intent,omitempty"`
}
type SessionView struct {
    ...
    SpawnedFrom string `json:"spawned_from,omitempty"`
    SpawnIntent string `json:"spawn_intent,omitempty"`
}

// internal/persist/persist.go — NOT omitempty: the on-disk key set is the durable contract
type Meta struct { ...; SpawnedFrom string `json:"spawned_from"`; SpawnIntent string `json:"spawn_intent"` }

// internal/daemon/daemon.go
type LaunchSpec struct { ...; SpawnedFrom string; SpawnIntent string }

// cmd/swarm — the Phase 1 narrow-interface pattern, widened by one method
type agentClient interface {
    List() ([]protocol.SessionView, error)
    Subscribe() (<-chan protocol.Event, error)
    Kill(id string) error
    Launch(protocol.LaunchReq) (id, name string, err error) // == protocol.Client.Launch
}
func runSpawn(args []string, c agentClient, stdout, stderr io.Writer) int
var spawnStateDir = func() (string, error) { ... } // package-level indirection (the
                                                   // persist.writeTemp precedent) so a
                                                   // test points the handoff copy at a
                                                   // temp dir; production resolves the
                                                   // same state dir the daemon uses
```

Two conventions the tests pin, both inherited rather than invented:

- **`SpawnedFrom` is the LOCAL session id** — the value the daemon injects as
  `SWARM_SESSION_ID` (`internal/daemon/launch.go:413`), which is exactly what
  `Meta.ResumedFrom` already holds. It is carried verbatim: the server neither resolves
  nor namespaces it. The TUI therefore matches it against the local half of a row's
  namespaced id (`protocol.ParseID`).
- **No parent means no intent.** `handleLaunch` refuses a `spawn_intent` with no
  `spawned_from`, so when `swarm spawn` runs from a plain terminal (no `SWARM_SESSION_ID`)
  it sends NEITHER field. This is the only reading under which both Piece 1's validation
  rule and Piece 2's "empty when a human runs it" note hold at once; pinned by
  `TestRunSpawn_NoSessionEnvSendsNoLineage`.

## Commands

```
$ go build ./...
BUILD OK

$ go test ./internal/protocol/ -run 'TestSpawnLineage' -count=1
$ go test ./internal/persist/ -run 'TestSpawnLineage' -count=1
$ go test ./internal/daemon/ -run 'TestLaunch_StampsSpawnLineageIntoMeta|TestLaunch_UnlineagedLaunchStampsNothing' -count=1
$ go test ./cmd/swarm/ -run 'TestRunSpawn|TestUsage_ListsSpawnVerb' -count=1
$ go test ./internal/tui/ -run 'TestRoster_Lineage|TestRoster_OrphanChild' -count=1
```

## Failing output (compile-fail red, trimmed)

```
# github.com/Nathandela/swarm/internal/protocol [github.com/Nathandela/swarm/internal/protocol.test]
internal/protocol/spawnlineage_test.go:65:4: unknown field SpawnedFrom in struct literal of type LaunchReq
internal/protocol/spawnlineage_test.go:66:4: unknown field SpawnIntent in struct literal of type LaunchReq
internal/protocol/spawnlineage_test.go:85:10: got.SpawnedFrom undefined (type LaunchReq has no field or method SpawnedFrom)
internal/protocol/spawnlineage_test.go:87:58: too many errors
FAIL	github.com/Nathandela/swarm/internal/protocol [build failed]

# github.com/Nathandela/swarm/internal/persist [github.com/Nathandela/swarm/internal/persist.test]
internal/persist/spawnlineage_test.go:34:4: m.SpawnedFrom undefined (type Meta has no field or method SpawnedFrom)
internal/persist/spawnlineage_test.go:35:4: m.SpawnIntent undefined (type Meta has no field or method SpawnIntent)
FAIL	github.com/Nathandela/swarm/internal/persist [build failed]

# github.com/Nathandela/swarm/internal/daemon [github.com/Nathandela/swarm/internal/daemon.test]
internal/daemon/spawnlineage_test.go:34:7: spec.SpawnedFrom undefined (type LaunchSpec has no field or method SpawnedFrom)
internal/daemon/spawnlineage_test.go:48:14: reserved.SpawnedFrom undefined (type persist.Meta has no field or method SpawnedFrom)
internal/daemon/spawnlineage_test.go:77:102: too many errors
FAIL	github.com/Nathandela/swarm/internal/daemon [build failed]

# github.com/Nathandela/swarm/cmd/swarm [github.com/Nathandela/swarm/cmd/swarm.test]
cmd/swarm/spawn_test.go:119:10: undefined: spawnStateDir
cmd/swarm/spawn_test.go:219:15: undefined: runSpawn
cmd/swarm/spawn_test.go:228:11: req.SpawnedFrom undefined (type protocol.LaunchReq has no field or method SpawnedFrom)
cmd/swarm/spawn_test.go:272:13: too many errors
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]

# github.com/Nathandela/swarm/internal/tui [github.com/Nathandela/swarm/internal/tui.test]
internal/tui/lineage_test.go:35:4: v.SpawnedFrom undefined (type protocol.SessionView has no field or method SpawnedFrom)
internal/tui/lineage_test.go:36:4: v.SpawnIntent undefined (type protocol.SessionView has no field or method SpawnIntent)
FAIL	github.com/Nathandela/swarm/internal/tui [build failed]
```

## RED interpretation

`go build ./...` is green — production code is untouched. Every failure is the repo's
"undefined-only" red (`internal/protocol/harness_test.go`): the test binaries fail to
COMPILE on production symbols that do not exist yet — the two schema/Meta/LaunchSpec
fields, `runSpawn`, and the `spawnStateDir` seam. All test scaffolding (the
`fakeSpawnClient` recorder, the stub-daemon fixtures, the crash-injection probe, the
roster fixtures) compiles.

Two failures survive past compilation once the fields land and are therefore listed
separately: `TestSpawnLineage_ProtocolMDDocumentsFields` fails on the missing spec rows
(`grep -c 'spawned_from\|spawn_intent' docs/specifications/protocol.md` = 0 today), as
does the pre-existing reflection drift check `TestProtocolMD_ExistsAndDocumentsEveryField`
(GG-7 lockstep); and `TestUsage_ListsSpawnVerb` fails until `cmd/swarm/main.go`'s `usage`
documents the verb.

No existing test was modified. `agentClient` gains a method, so the Phase 1 assertion
`var _ agentClient = (*fakeAgentClient)(nil)` in `agentverbs_test.go` keeps compiling via a
base `Launch` method declared in the NEW file (a call landing there is a wiring mistake and
says so); the recording fake for the spawn tests is `fakeSpawnClient`.

## What each test pins

| Test | Behavior |
| --- | --- |
| `TestSpawnLineage_LaunchReqRoundTrip` | `spawned_from` / `spawn_intent` round-trip on `LaunchReq`; a request with neither emits neither key (omitempty). |
| `TestSpawnLineage_SessionViewRoundTrip` | The same round-trip and omitempty pin on the roster row. |
| `TestSpawnLineage_IntentVocabularyEnforced` | `""` / `handoff` / `delegate` are accepted and reach the daemon LaunchSpec verbatim; junk, wrong case, an unlisted word and a padded value are each `invalid_field` with zero launches. |
| `TestSpawnLineage_IntentWithoutSpawnedFromRefused` | An intent with no parent is `invalid_field`, nothing launched. |
| `TestSpawnLineage_RosterViewsCarryLineage` | `stampView` copies the persisted lineage onto both the `OpList` snapshot and the `OpSubscribe` stream; an un-lineaged session keeps both empty. |
| `TestSpawnLineage_ProtocolMDDocumentsFields` | GG-7: protocol.md carries a row for each new key, and the `spawn_intent` row states its closed vocabulary. |
| `TestSpawnLineageRoundTrips` | Meta lineage survives Save/Load (crash-safe). |
| `TestSpawnLineageKeysAlwaysPresent` | Both snake_case keys are written even when empty (the durable key set does not vary with the value). |
| `TestLaunch_StampsSpawnLineageIntoMeta` | The daemon stamps both spec fields into the reserved meta before any agent spawns (crash-injection probe at `phaseReserved`). |
| `TestLaunch_UnlineagedLaunchStampsNothing` | An ordinary launch stamps neither field. |
| `TestRunSpawn_PromptBuildsLaunchReq` | The exact `LaunchReq` for `--prompt`: agent, `--dir` cwd, `--model` -> `Options["model"]`, `--worktree`, `--name`, the prompt verbatim (multi-line, flag-looking text included), plus 80x24, `os.Environ()`, intent `delegate`, and `SpawnedFrom` from `SWARM_SESSION_ID`. |
| `TestRunSpawn_DirDefaultsToCallerCwd` | No `--dir` means the caller's cwd. |
| `TestRunSpawn_NoSessionEnvSendsNoLineage` | No session env: neither lineage field is sent and the spawn still succeeds. |
| `TestRunSpawn_HandoffCopiesFileAndPointsPrompt` | `--handoff` / `--delegate` copy the document into `<stateDir>/handoffs/` (dir 0700, `.md`, content verbatim, not referenced in place); the prompt is the one-line pointer at the absolute dest and never carries the body (A4); intent is `handoff` / `delegate` respectively. |
| `TestRunSpawn_HandoffCopiesGetUniqueNames` | Two spawns from one source file produce two distinct copies and two distinct prompts. |
| `TestRunSpawn_MissingHandoffFileIsAnError` | An unreadable document is exit 1 naming the path, with nothing launched. |
| `TestRunSpawn_FlagMisuse` | Exit 2 with no Launch for: missing `--cli`, no instruction source, `--prompt`+`--handoff`, `--handoff`+`--delegate`, unknown flag (argument validation precedes any file I/O). |
| `TestRunSpawn_LaunchError` | A daemon refusal is exit 1 with the cause on stderr and nothing on stdout. |
| `TestRunSpawn_PrintsSessionID` | stdout carries the bare session id (pipeable into `swarm watch`); the name goes to stderr. |
| `TestUsage_ListsSpawnVerb` | `usage` documents `swarm spawn`. |
| `TestRoster_LineageBadges` | A spawned row badges `from <parent>`; a row another VISIBLE row was spawned from badges `spawned`; an unrelated row badges neither. |
| `TestRoster_OrphanChildStillBadged` | A child whose parent has left the roster still names it. |
| `TestRoster_LineageBadgesRenderInTheView` | The badges reach the painted roster, not only the row helper. |
