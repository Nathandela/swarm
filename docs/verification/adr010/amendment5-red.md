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

**Over-engineering hunter** — verdict: proportionate; the interface earns its place over an
agent-type switch (the day-from-id knowledge stays inside the codex adapter). Three findings, all
taken: two comments F3 had left stale (`spawn.go` package doc, the copy test's doc) and the
timestamped `CreateTemp` name, redundant once each copy has a directory of its own — the copy is
now a plain `handoff.md`. One suggestion declined: a shared `session_meta` identity parser between
`parseCodexSessionMeta` and `codexTranscriptNamesItsConversation` would save about eight lines by
editing the tested resume path; left alone.

**Code-analysis reviewer** — seven findings, six acted on:

| # | Finding | Disposition |
|---|---|---|
| 1 | A thread resumed in another directory is refused with "not found" | First given its own outcome and refusal text; then OVERTAKEN by the adversarial review below, which showed the cwd clause itself could never match a real swarm-launched thread. The clause is gone, so the case no longer arises |
| 2 | Evidence lacked gates and a live proof that `codex app-server` accepts `-c` and that the sandbox's `/tmp` is the host's | Gates recorded (§5). `codex app-server --listen unix://… -c sandbox_workspace_write.network_access=true` bound its socket within 8 s with no error output, run live on codex-cli 0.151.0. `/tmp` visibility is proven by the post-release smoke (§7). The `exclude_slash_tmp` knob is now named in F3 |
| 3 | `copyHandoff` stranded its directory when the copy itself failed | Taken: the directory is removed on every error after it is minted |
| 4 | A zero-byte rollout reads as "unsafe to open" rather than "not found" | Accepted as a known limit: `readCompleteLine`'s EOF semantics belong to `resolveCodex`, whose tests pin them, and a rollout without its first record is a crash artefact — superseded 2026-09-02 by ADR-010 Amendment 7 H2 (`amendment7-red.md`): such a rollout is now skipped as not a record |
| 5 | Pre-v7 (v4) thread ids are refused as not found | Accepted and named in the ADR (F1) |
| 6 | Physical versus logical cwd (a symlinked checkout) — pre-existing in `parseCodexSessionMeta`, now load-bearing for hands-off | Recorded; the owner's `data/` tree is not symlinked (`readlink -f` is the identity). Not fixed here |
| 7 | Test gaps: the v4 case was non-discriminating; no worktree (`AgentCwd`) case; no budget bound; no stray-entry case | Taken: the v4 case now writes the rollout under the session's own day and still expects not-found; new tests cover the worktree cwd, a stray and a malformed neighbour, and `MaxEntries` failing closed |

**Adversarial reviewer** (read-only; probed a `git archive` copy and, read-only, the real
`~/.codex/sessions` tree of 1888 rollouts). Nothing at HIGH for confinement, traversal, hangs, or
a refusal degrading into a launch: every failure path returns an error and launches nothing, and
`TranscriptDay` survived "", one byte, 36 bytes of 0xff, eighteen two-byte runes, a NUL-terminated
id, 37 bytes and a year-10889 prefix without panicking. Eight findings:

| # | Finding | Disposition |
|---|---|---|
| 1 | HIGH (functional): the cwd clause can never match a swarm-launched codex thread — codex records the app-server's cwd, which under swarm is `<stateDir>/sessions/<creating session>` (10 of 10 rollouts from 0.151.0, 36 of 45 app-server rollouts overall); verified live that the locator returned not-found with the real cwd and found with the state dir | Taken: the clause is removed; the identity check is the id. The ADR (F1) records the measurement and the trade it implies for a poisoned id. The fixture that hid it (`writeCodexHistory` writing `cwd == source cwd`) is now complemented by a test whose rollout names a state dir |
| 2 | MEDIUM: the ADR's claim that a missing id is recovered by `resolveCodex` is false on this machine — its cwd clause has the same defect, its window cannot match a resumed thread, and it fails closed on a 0-byte rollout and one with an undecodable first record in the three-day window (`Resolve` returned Unsafe live) | Accepted as a known limit of this slice and filed as `swarm-man`; the ADR's F1 limits and Consequences now say exactly what composes (sources with a captured id: twelve of eighteen live codex sessions) — Amendment 7 narrows this to the fresh-launch no-id case (H1 covers the legacy resumes, H2 the torn rollouts) |
| 3 | MEDIUM (process): evidence file was untracked in the reviewed range and its gates section empty; no live proof of the override's effect through the app-server path | Gates and live checks recorded here; the reviewer's own live reading of `turn_context.sandbox_policy` confirmed the per-turn policy comes from the TUI's config, so the override on the agent argv is the right lever, and the app-server argv carries it too. Effect demonstrated by the post-release smoke (§7) |
| 4 | LOW: a zero-byte or partial-first-line rollout reads as "unsafe to open"; a first line over 64 KiB likewise (real `session_meta` lines are ~22 KiB) | Accepted, as for the code-analysis reviewer's finding 4 — superseded 2026-09-02 by ADR-010 Amendment 7 H2 (`amendment7-red.md`): such a rollout is now skipped as not a record; the over-64 KiB line still refuses |
| 5 | LOW: two files claiming one id in a day — first in directory order wins, nondeterministically | Taken: two claimants fail closed as ambiguous, with a named refusal |
| 6 | LOW: day −1 was listed first and can never hold the file (0 of 1888 rollouts are stamped before their id), and the entry budget is cumulative, so a busy previous day could exhaust it | Taken: the id's day first, then +1, then −1 (kept only as insurance for a machine filing by a local time behind UTC) |
| 7 | LOW: `copyHandoff` stranded its directory on its own error paths | Already taken in the previous round |
| 8 | Informational: `exclude_tmpdir_env_var` is on in the live sandbox policy, so `TMPDIR` outside `/tmp` yields a loud pre-launch refusal; 16 pre-v7 rollouts exist | Both named in the ADR (F1, F3) |

The reviewer also saw two `TestRunHook_*` tests fail in its own sandbox; on this machine they pass
in isolation with the `SWARM_*` session variables unset (§5).

## 7. Rollout: v0.13.21 on the owner's machine (2026-09-02)

The first Release run for the tag failed in the fuzz gate (`FuzzReadFrame`, "context deadline
exceeded" at the 30 s fuzztime boundary, no failing input written; `internal/wire` is untouched by
the release and the same commit's PR run had passed) and publish was skipped. Filed as `swarm-vf1`.
The re-run published at 09:34 UTC with the signed checksums and the 4 + 4 + 1 archives.

```
swarm upgrade --stage      -> staged, 0.13.18 -> v0.13.21 (signature verified)
swarm upgrade --activate   -> binaries installed; converge deferred: session f4mcl7qjyreiw6ed is working
swarm daemon restart       -> exit 0 (from a terminal with the six SWARM_* variables unset)
daemon pid 3650303 -> 3767447, exe inode 6924 == /usr/local/bin/swarm, roster 30 -> 30
```

The deferral was correct: the only working session was the one driving the upgrade, and the
attended restart is the documented path for that case.

**Live smoke, in a scratch repository trusted for codex (`/tmp/swarm-smoke-20260902`).**

- `swarm spawn --cli codex` from a clean environment launched session `wythw7mqt4e3pkd3`. Its
  app-server argv was `codex app-server --listen unix://… -c sandbox_workspace_write.network_access=true`
  and its agent argv `codex -c sandbox_workspace_write.network_access=true <prompt>` (F2, both
  processes). Its rollout's `turn_context.sandbox_policy` recorded
  `{"type":"workspace-write","writable_roots":["/tmp/swarm-smoke-20260902","/tmp"],"network_access":true,…}`:
  the override took effect through the app-server/`--remote` path, which is the proof the
  reviewers asked for. Its `session_meta.cwd` was `<stateDir>/sessions/wythw7mqt4e3pkd3`, the
  measurement behind F1's missing cwd clause. The session latched its thread id from typed events.
- The codex turn itself did not run: the account had hit its usage cap ("try again at Sep 8th").
  The end-to-end supervised run (codex executing `swarm handoff`) is therefore deferred to
  `swarm-6bo`; F2's mechanism is proven above, its use by a live model turn is not yet.
- That left a genuinely rate-limited codex source, the case hands-off exists for. A hands-off
  launch (`handoff_from=ep-8d00dc6d/wythw7mqt4e3pkd3`, target claude) composed session
  `uzjtnk7nwbgpavxn` with `spawned_from=wythw7mqt4e3pkd3`, `spawn_intent=handoff`, empty
  supervision, cwd `/tmp/swarm-smoke-20260902`, and a prompt carrying the five pointers with the
  real rollout path under the daemon's resolved `~/.codex` alias. Past Claude Code's folder-trust
  dialog the successor announced read-only reconnaissance and ran `git status`, `git log`, a
  listing, and opened the rollout — the prompt's before-writing rules, followed. It exited with
  143 at 09:44:40, the moment the owner interrupted the session and removed the codex source from
  the roster; not a defect.
