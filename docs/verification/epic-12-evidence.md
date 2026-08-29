# Epic 12 — Evidence

**Epic**: Worktree isolation (`agents-tracker-0b9`)
**Commits**: 1492757 (worktree + hooks), e778363 (F2 rollback compensation).

## TDD evidence (GG-5)

Designer wrote the failing suites first; red log in [epic-12-red/worktree-red.txt](epic-12-red/worktree-red.txt) (undefined-only, confirmed genuine). The F2 fix was a genuine red-to-green (stash the fix → test fails "worktree still has entries" → restore → green).

## Criterion walk (E12.1 – E12.3)

| Criterion | Evidence |
|---|---|
| E12.1 launch toggle creates worktree (S-3) | `worktree.Create` makes `.swarm/worktrees/<name-slug>` on `swarm/<name-slug>-<id>` for named sessions, adding the id to the directory only when the friendly slug is occupied and using an id-only fallback when unnamed; the id is path-validated FIRST (ADR-004 charset — no traversal); non-repo → clear error; second Create for the same id is refused with the first intact |
| E12.2 delete teardown (R-3) | `worktree.Remove` = `git worktree remove --force` + `git worktree prune`; the directory is gone |
| E12.3 hooks, no inline core | daemon Config gains optional `PreLaunch(id,spec)→cwdOverride` + `PreDelete(meta)`; worktree logic registered from OUTSIDE; grep confirms no worktree-specific branching in daemon core |

## Security / correctness (both reviewer focus areas cleared)

- **No git argument injection**: id validated before any git/os call; the cosmetic name is reduced to lower-case letter/number runs separated by hyphens; all git commands are `exec.Command` variadic argv (no `sh -c`); the branch suffix can't be a flag; `RemoveAt` accepts only direct children of the repo's worktree root. Production ids are lowercase base32 and remain present in full.
- **R-3 teardown never skipped**: `PreDelete` runs before `store.Delete`, its error logged/propagated, but `store.Delete` (the mandatory directory teardown) runs unconditionally — a hook failure can't skip it.
- **F2 fix — no orphan on rollback**: a launch failing AFTER a successful PreLaunch now calls the compensating PreDelete before `dropReserved` on all 7 rollback paths (crash-injection paths that keep the meta are untouched — reconcile/Delete clean those). TDD-verified.

## Review outcome (protocol step 5)

**Opus (independent): FIX REQUIRED → resolved.** Mechanism approved (no injection, teardown-never-skipped, core clean, 49 daemon tests green). F1 (end-to-end toggle wiring: LaunchReq.Worktree field + TUI transmit + daemon hook registration in runDaemon) is the walking-skeleton assembly — **deferred to Epic 8** (recorded on that bead). F2 (rollback orphan) fixed.

## Accepted limitation (recorded)

`worktree.RemoveAt` deletes the persisted worktree directory + prunes but leaves its `swarm/<slug>` git branch (matches the approved E12.2 contract and S-3's literal "remove + prune"). Branch accumulation is untidy but harmless (a lightweight ref; the heavy checkout is cleaned). Reaping the branch (`git branch -D`) would be a small R-3 completion — a follow-up, not a blocker.

## Quality gates (GG-4)

gofmt · build · vet · `go test ./internal/worktree/ ./internal/daemon/ -race -count=3` green (7 worktree + 50 daemon tests; git-dependent tests skip cleanly if git absent).
