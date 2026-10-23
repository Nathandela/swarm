# ADR-010 Amendment 10: agent handoffs run in a worktree, never under /tmp (TDD evidence)

**Decision**: [ADR-010 Amendment 10](../../adr/ADR-010-inter-session-orchestration.md).
**Baseline**: v0.21.2 (`e672e7b6`). Date: 2026-10-23.

## Production symptom

On 2026-10-20 the "FII - v2" session delegated two sessions, "Rehan - Intensive preparation"
(codex) and "Rehan - Promotion and reuse Opus review" (claude), with
`--dir /tmp/rehan-native-timeout-fix`, a checkout the source agent had made by hand. Both
discussions were resumed several times in that directory. The machine rebooted on 2026-10-23 at
10:08 and `/tmp` was cleared; every attempt of both discussions pointed at a checkout that no
longer existed. Their last commits were on the remote, so the history was recovered by creating a
worktree of the repository and resuming both discussions into it with resume relocation;
uncommitted changes in the `/tmp` checkout were lost.

## RED

The regression surface, written before the implementation:

- `internal/worktree/isrepo_test.go`: `IsRepo` answers for a repository root, a directory inside
  one, a plain directory and a missing one.
- `internal/skeleton/handoff_worktree_test.go`, with the production worktree hooks wired into the
  daemon:
  - a first-class handoff child from a git checkout launches with worktree isolation, runs in
    `<checkout>/.swarm/worktrees/<slug>`, and starts at the checkout's HEAD;
  - a child from a directory outside git continues in that directory;
  - a handoff whose worktree would be under the temporary directory is refused by name, with no
    new row and no `.swarm` directory created;
  - a `swarm spawn --delegate` launch asking for a worktree under the temporary directory is refused
    the same way;
  - an ordinary launch in a git checkout with no lineage is not isolated.
- `cmd/swarm/spawn_worktree_test.go`: `--handoff` and `--delegate` in a git checkout send
  `Worktree`; `--worktree=false` opts out; `--prompt` and a directory outside git do not.
- `internal/tui/handoff_prompt_worktree_test.go`: for every supervision mode, the source's
  instruction says that the receiving agent starts in a new git worktree and asks it to commit,
  before the launch command.

First run, before any implementation: `internal/worktree` and `internal/skeleton` failed to compile
on the new symbols only (`IsRepo`, `handoffTemporaryRoots`). The other two failed on their own
assertions:

```
--- FAIL: TestRunSpawn_HandoffAndDelegateDefaultToAWorktreeInAGitCheckout/handoff
    runSpawn [--cli claude --dir /tmp/.../002 --handoff /tmp/.../003/context.md] sent Worktree=false, want true
--- FAIL: TestRunSpawn_HandoffAndDelegateDefaultToAWorktreeInAGitCheckout/delegate
    runSpawn [--cli claude --dir /tmp/.../002 --delegate /tmp/.../003/context.md] sent Worktree=false, want true
--- FAIL: TestHandoffPromptTellsTheSourceToCommitBeforeLaunching
    passive prompt never says the receiving agent starts in a new git worktree
```

Behavioural RED: with the implementation in place but its decisions disabled (the daemon never
isolates a handoff child, never refuses a temporary worktree, and the CLI never defaults the
flag), the suites fail on their own assertions:

```
--- FAIL: TestRunSpawn_HandoffAndDelegateDefaultToAWorktreeInAGitCheckout/handoff
--- FAIL: TestRunSpawn_HandoffAndDelegateDefaultToAWorktreeInAGitCheckout/delegate
--- FAIL: TestHandoffChildFromAGitCheckoutRunsInItsOwnWorktree
    handoff child launch options = map[], want worktree isolation
--- FAIL: TestHandoffChildRefusesAWorktreeUnderTheTemporaryDirectory
    a handoff child was launched with its worktree under the temporary directory: {...}
--- FAIL: TestDelegatedWorktreeUnderTheTemporaryDirectoryIsRefused
    a delegated child was launched with its worktree under the temporary directory: {...}
```

The non-git, opt-out, `--prompt` and ordinary-launch cases passed in that run, as they should: they
pin behaviour the change must keep.

## GREEN

With the decisions restored, the new tests and the existing handoff-directory, spawn, handoff
prompt and worktree suites pass. Full-suite results are recorded in the pull request.
