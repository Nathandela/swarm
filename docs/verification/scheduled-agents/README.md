# Scheduled agent qualification

## Host probes on November 19, 2026

Disposable Swarm supervisors, private state directories and fixture Git repositories were used on macOS arm64 and the Linux amd64 VM. No colleague VM was contacted.

| Scenario | Observed outcome |
| --- | --- |
| Two overlapping Git worktrees on both hosts | Writes stayed in the selected checkout. The replacement process could read progress left in the same worktree. |
| Public CLI and private catalog on both hosts | Create, edit with revision checks, list, pause, resume, archive and personal templates round-tripped through the binary. |
| Local real terminal | An 84×20 PTY created a job end to end using the keyboard. The CLI read back the selected timezone, authority, ordered agent and workspace. A 24×4 PTY navigated back to the board. |
| Real Codex, observe authority, both hosts | A manual occurrence launched an isolated native worker. Its restricted completion tool submitted a receipt. The scheduler observed the closing chat message and advanced the same occurrence to ready. |
| Linux periodic systemd user service | A negative control terminated the cold-started supervisor and worker. With the release service's lifecycle settings, two timer firings completed and both original process identities remained live. This test used the project's fake provider. |
| Catalog recovery | Go tests cover concurrent claims, duplicate ticks, uncertain manual replies, edit/archive boundaries, DST and clock rewinds. The suite also passed as a native Linux test binary on the VM. |
| Scheduled launch crash window | A real supervisor received SIGKILL before identity persistence, before release, and after shim release. Targeted race tests confirmed that retrying the operation did not start a second worker. |
| Polling cost | Testing with 1,200 retained historical occurrences showed the same number of idle-tick allocations as an empty history. |

The opt-in Linux probe runs under the account's systemd user manager. From the checkout, run the following command.

```sh
SWARM_SCHEDULE_SYSTEMD_PROBE=1 go test -count=1 -v ./internal/scheduletimer -run '^TestSystemdColdScheduleTick$'
```

The CLI catalog check accepts a candidate binary. It uses fresh temporary state.

```sh
go build -o /tmp/swarm-schedule-qa ./cmd/swarm
python3 scripts/verification/schedule_catalog.py /tmp/swarm-schedule-qa
```
