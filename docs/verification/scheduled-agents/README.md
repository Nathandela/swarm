# Scheduled agent qualification

## Host probes on November 19, 2026

Disposable Swarm supervisors, private state directories and fixture Git repositories were used on macOS arm64 and the Linux amd64 VM. No colleague VM was contacted.

| Scenario | Observed outcome |
| --- | --- |
| Two overlapping Git worktrees on both hosts | Writes stayed in the selected checkout. The replacement process could read progress left in the same worktree. |
| Public CLI and private catalog on both hosts | Create, edit with revision checks, list, pause, resume, archive and personal templates round-tripped through the binary. |
| Local real terminal | An 84×20 PTY created a job end to end using the keyboard. The CLI read back the selected timezone, authority, ordered agent and workspace. A 24×4 PTY navigated back to the board. |
| Real Codex, observe authority, both hosts | A manual occurrence launched an isolated native worker. Its restricted completion tool submitted a receipt. The scheduler observed the closing chat message and advanced the same occurrence to ready. |
| Real Codex, workspace-write authority, both hosts | The worker wrote the fixture file inside the worktree. Files at the two tested paths outside it remained absent. The real closing message and scoped receipt produced Ready. |
| Real Claude Code 2.1.327, agent-default authority, Linux VM | The worker inherited existing checkout trust and signed-in provider credentials. It submitted the scoped receipt and reached Ready after Swarm verified its closing assistant message. After the original process exited, the board reopened the same native conversation in the saved workspace and displayed that message. |
| Restricted Codex conversation reopen on Linux | After the completed worker exited, Board Open resumed its existing conversation in a trusted checkout. The read-only sandbox and never-prompt approval policy were retained. The resumed process had no completion credential, and the terminal displayed the original closing message. |
| Claude Code startup, macOS arm64 | A newly isolated worktree asked for native checkout trust and Swarm reported Needs input. An already-trusted checkout reached a native login prompt despite the shell reporting an existing login. After each test worker was stopped, the run became Incomplete and retained its workspace. |
| Retained final assistant messages from an ended Codex worker on Linux | Codex refused native continuation in the disposable checkout because of its checkout trust policy. In a separate real Ready run in that checkout, Run History displayed a sentence from the original closing assistant message that the scoped receipt did not contain. |
| Linux periodic systemd user service | A negative control terminated the cold-started supervisor and worker. With the release service's lifecycle settings, two timer firings completed and both original process identities remained live. This test used the project's fake provider. |
| Catalog recovery | Go tests cover concurrent claims, duplicate ticks, uncertain manual replies, edit/archive boundaries, DST and clock rewinds. The suite also passed as a native Linux test binary on the VM. |
| Scheduled launch crash window | A real supervisor received SIGKILL before identity persistence, before release, and after shim release. Targeted race tests confirmed that retrying the operation did not start a second worker. |
| Cold calendar to native completion, both hosts | A one-time trigger fired through launchd on macOS and systemd on Linux. Each cold-started the supervisor from its saved environment. The real Codex session reached Ready, and an extra OS-service tick retained one occurrence and one worker attempt. |
| Provider fallback | Injected a native provider quota event with the bound conversation identity. The production observer and actual shim stop path advanced to the next configured route in the same workspace. No live account was deliberately exhausted. |
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
