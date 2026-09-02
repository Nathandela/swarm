# ADR-010 Amendment 5 — codex sources: failing-first evidence

**Slice**: a codex-sourced handoff, by both methods. Tracks `swarm-p24` (hands-off, F1) and
`swarm-7h9` (supervised through the sandbox, F2/F3). Normative:
[ADR-010](../../adr/ADR-010-inter-session-orchestration.md) Amendment 5.

Branch `feat/codex-handoff`, worked in a linked worktree; every command below ran with
`GOFLAGS=-buildvcs=false`, because Go's VCS stamp walks past a linked worktree's `.git` file
(known on this machine, see `bd memories tf-wks-nathan`).

## 1. The measured problem

Live state on the owner's machine, 2026-09-02, before any change: `daemon.log` held no line
containing `handoff`; none of the 34 session metas carried `spawn_intent=handoff`; the
`<stateDir>/handoffs` directory had never been created. No codex-sourced handoff had ever got
past the CLI.

`codex sandbox` (codex-cli 0.151.0) running the exact operations `swarm handoff` performs, from
this checkout, under the sandbox mode swarm launches codex with:

```
$ codex sandbox -c 'sandbox_mode="workspace-write"' -- sh -c '...'
touch ~/.local/state/swarm/.probe -> Read-only file system
touch /tmp/probe-tmp-ok           -> ok
swarm ls                          -> dial unix .../daemon.sock: connect: operation not permitted

$ ... -c 'sandbox_workspace_write.writable_roots=["~/.local/state/swarm"]'   (no network flag)
swarm ls                          -> connect: operation not permitted        (writable roots do not help)

$ ... -c 'sandbox_workspace_write.network_access=true'
swarm ls                          -> ok
mkdir -p /tmp/x && touch /tmp/x/y -> ok                                       (a /tmp subdirectory is writable)

$ codex sandbox -- sh -c 'mktemp -d'      (this cwd's default mode is read-only)
mktemp: failed to create directory via template '/tmp/tmp.XXXXXXXXXX': Read-only file system
```

So: the socket connect is refused by the network seccomp filter and only `network_access=true`
lifts it; the state dir is read-only; `/tmp` is writable under workspace-write and nothing is
writable under read-only. Each clause of the amendment answers one of those lines.

The rollout layout, measured on four real files under `~/.codex/sessions/2026/09/02/`:

```
dir         name stamp             id prefix       UUIDv7 time (UTC)      version
2026/09/02  2026-09-02T00-32-54    01a05f88-7ab7   2026-09-02T00-32-54    7
2026/09/02  2026-09-02T00-32-55    01a05f88-7c2f   2026-09-02T00-32-55    7
2026/09/02  2026-09-02T00-33-00    01a05f88-9196   2026-09-02T00-33-00    7
2026/09/02  2026-09-02T00-33-01    01a05f88-9311   2026-09-02T00-33-01    7
```

Stamp equals the id's embedded time to the second; the directory is its UTC day. That is F1's
whole premise, and it was measured before a line of the locator was written.

## 2. RED, stage 1 (tests written, no production change)

`internal/adapter/codex` fails to BUILD on the two undefined symbols; `internal/skeleton` and
`cmd/swarm` fail BEHAVIOURALLY, for the right reasons:

```
# github.com/Nathandela/swarm/internal/adapter/codex [github.com/Nathandela/swarm/internal/adapter/codex.test]
internal/adapter/codex/transcript_test.go:25:40: undefined: adapter.DatedTranscriptLayout
internal/adapter/codex/transcript_test.go:27:24: undefined: adapter.AsDatedTranscriptLayout
FAIL	github.com/Nathandela/swarm/internal/adapter/codex [build failed]
FAIL
--- FAIL: TestLocateTranscript_CodexFindsTheRolloutUnderTheIDsDay (0.00s)
    locate_codex_test.go:69: LocateTranscript = ("", 0), want ("/tmp/TestLocateTranscript_CodexFindsTheRolloutUnderTheIDsDay2936490514/001/.codex/sessions/2026/09/02/rollout-2026-09-02T00-32-54-01a05f88-76f0-7000-8000-000000000000.jsonl", found)
--- FAIL: TestLocateTranscript_CodexTriesTheNeighbouringDays (0.00s)
    locate_codex_test.go:86: LocateTranscript = ("", 0), want ("/tmp/TestLocateTranscript_CodexTriesTheNeighbouringDays4078375188/001/.codex/sessions/2026/09/02/rollout-2026-09-02T00-00-01-01a05f6a-5418-7000-8000-000000000000.jsonl", found)
--- FAIL: TestLocateTranscript_CodexRefusesByName (0.00s)
    --- FAIL: TestLocateTranscript_CodexRefusesByName/no_rollout_on_disk (0.00s)
        locate_codex_test.go:123: LocateTranscript = ("", 0), want ("", 1)
    --- FAIL: TestLocateTranscript_CodexRefusesByName/the_file_names_another_thread (0.00s)
        locate_codex_test.go:123: LocateTranscript = ("", 0), want ("", 1)
    --- FAIL: TestLocateTranscript_CodexRefusesByName/the_thread_ran_in_another_checkout (0.00s)
        locate_codex_test.go:123: LocateTranscript = ("", 0), want ("", 1)
    --- FAIL: TestLocateTranscript_CodexRefusesByName/a_first_record_that_is_not_a_session_meta (0.00s)
        locate_codex_test.go:123: LocateTranscript = ("", 0), want ("", 1)
    --- FAIL: TestLocateTranscript_CodexRefusesByName/a_first_record_that_is_not_JSON (0.00s)
        locate_codex_test.go:123: LocateTranscript = ("", 0), want ("", 1)
    --- FAIL: TestLocateTranscript_CodexRefusesByName/a_canonical_id_that_carries_no_day (0.00s)
        locate_codex_test.go:123: LocateTranscript = ("", 0), want ("", 1)
--- FAIL: TestHandsOff_ComposesForACodexSourceFromItsDatedRollout (0.00s)
    locate_codex_test.go:164: hands-off composition of a RUNNING codex source was refused: handoff: source agent "codex" has no characterized transcript layout; hands-off supports claude sources only in this sweep
--- FAIL: TestHandsOff_RecoversACodexConversationIDFromProviderHistory (0.00s)
    locate_codex_test.go:205: recoverable codex source refused: handoff: source agent "codex" has no characterized transcript layout; hands-off supports claude sources only in this sweep
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	0.375s
FAIL
--- FAIL: TestRunHandoff_BuildsLinkedLaunchFromCurrentSession (0.00s)
    handoff_test.go:101: private handoff copies = 0, want 1
--- FAIL: TestRunSpawn_HandoffCopiesFileAndPointsPrompt (0.00s)
    --- FAIL: TestRunSpawn_HandoffCopiesFileAndPointsPrompt/--handoff (0.00s)
        spawn_test.go:356: handoff copies [], want exactly one copied document
    --- FAIL: TestRunSpawn_HandoffCopiesFileAndPointsPrompt/--delegate (0.00s)
        spawn_test.go:356: handoff copies [], want exactly one copied document
--- FAIL: TestRunSpawn_HandoffCopiesGetUniqueNames (0.00s)
    spawn_test.go:413: handoff copies [] after two spawns, want two distinct copies
FAIL
FAIL	github.com/Nathandela/swarm/cmd/swarm	0.020s
```

## 3. RED, stage 2 (the interface declared, the codex adapter untouched)

To get a behavioural red for the codex package rather than a build failure, the codex adapter's
two files were stashed after the interface existed and the tests re-run:

```
--- FAIL: TestR7CodexBackend_DescribesTheRecordedAppServerArgv (0.00s)
    r7_backend_test.go:58: plan.Args = [app-server --listen unix:///var/folders/xx/swarm/sessions/01JSESSION/codex.sock], want [app-server --listen unix:///var/folders/xx/swarm/sessions/01JSESSION/codex.sock -c sandbox_workspace_write.network_access=true]; recorded at r1-codex-gate.md:53. Note that `codex app-server proxy --sock` is NOT the bridge to this endpoint (gate correction 2) and must never appear here
--- FAIL: TestSandbox_EveryCodexArgvLiftsTheNetworkFilterForTheSwarmCLI (0.00s)
    sandbox_test.go:45: Command argv [codex --sandbox workspace-write go] does not carry `-c sandbox_workspace_write.network_access=true`; the swarm CLI cannot reach the daemon from inside the sandbox without it
    sandbox_test.go:45: Resume argv [codex resume a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d -c check_for_update_on_startup=false] does not carry `-c sandbox_workspace_write.network_access=true`; the swarm CLI cannot reach the daemon from inside the sandbox without it
    sandbox_test.go:45: app-server argv [app-server --listen unix:///var/folders/xx/swarm/sessions/01JSESSION/codex.sock] does not carry `-c sandbox_workspace_write.network_access=true`; the swarm CLI cannot reach the daemon from inside the sandbox without it
--- FAIL: TestTranscriptDay_ReadsTheUTCDayOutOfAUUIDv7ThreadID (0.00s)
    transcript_test.go:35: the codex adapter is not an adapter.DatedTranscriptLayout; hands-off cannot locate its rollouts (ADR-010 Amendment 5 F1)
--- FAIL: TestTranscriptDay_RefusesIDsThatCarryNoDay (0.00s)
    transcript_test.go:61: the codex adapter is not an adapter.DatedTranscriptLayout; hands-off cannot locate its rollouts (ADR-010 Amendment 5 F1)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/codex	0.006s
FAIL
```

Every failure names the missing behaviour: no `DatedTranscriptLayout`, no `-c
sandbox_workspace_write.network_access=true` on any of the three argvs.

## 4. GREEN

```
PASS
ok  	github.com/Nathandela/swarm/internal/adapter/codex	0.008s
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	0.316s
PASS
ok  	github.com/Nathandela/swarm/cmd/swarm	0.025s
```

All new tests pass, and every pre-existing test in the three packages still passes. Two existing
tests were changed BECAUSE the decision changed, and the ADR landed first (Amendment 5):

- `TestHandsOff_RefusesAnUnsupportedSourceAgentByName` no longer lists `codex` (F1).
- `TestR7CodexBackend_DescribesTheRecordedAppServerArgv` expects the override on the app-server
  argv (F2).
- The `cmd/swarm` handoff-copy tests assert the private temp directory contract instead of
  `<stateDir>/handoffs` (F3); the relative-path test now uses a relative `TMPDIR` in place of the
  removed `spawnStateDir` seam.

## 5. Gates

Run on the branch tip in the linked worktree (`GOFLAGS=-buildvcs=false`), 2026-09-02:

| Gate | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `golangci-lint run ./...` (v2.11.4 locally; CI pins v2.12.2) | 0 issues |
| `go test -race ./...` | 68 packages ok, 3 failed in the full run, all 3 ok in isolation (below) |

The three failures in the full run were not the branch's. `cmd/swarm` (`TestRunHook_*`) and
`internal/hookclient` (`TestEnvHookSocket_UnsetMeansPostSmartNeverDials`) assume no `SWARM_*`
session variables in the test process's environment, and this run was launched from inside a
swarm-managed session; the same hookclient test fails identically on untouched `main` with that
environment and passes with it unset. `internal/remote/relay`
(`TestMailboxDiscard_DisconnectedGatewayCannotAuthorizeDeletion`) is a timing-dependent test in a
package the diff does not touch. Rerun in isolation with the six `SWARM_*` variables unset:

```
ok  	github.com/Nathandela/swarm/cmd/swarm	34.755s
ok  	github.com/Nathandela/swarm/internal/hookclient	2.341s
ok  	github.com/Nathandela/swarm/internal/remote/relay	106.455s
```

CI runs the same gates on the pull request and again on the release tag (`release.yml` reuses
`ci.yml`), from a clean environment.

## 6. Review round

Three independent reviewers ran over the pushed branch, read-only (an adversarial
correctness/confinement reviewer, an over-engineering hunter, and a code-analysis reviewer).
Their findings and what was done with each:

