# Scheduled agents: operator guide

Swarm's scheduler is a per-user, per-host catalog driven by a local timer. A timer tick checks calendars and asks the local Swarm daemon to dispatch eligible work. The scheduled execution path currently admits **Codex** for `observe`, `workspace-write`, and `agent-default` authority, and **Claude Code** for `agent-default` authority. Although other adapters appear in the editor, their scheduled routes are refused by policy. This is not qualification of every adapter or of every host/provider setup.

## A Gaston-style example

On the machine that hosts the Gaston checkout, schedule a Friday developer-experience review. Replace the alias and checkout placeholders with ones you use. Choose an agent authority that permits the SSH access you intend.

```sh
cd <absolute-path-to-checkout>
cat > /tmp/gaston-review.txt <<'EOF'
Review Gaston's onboarding and development workflow. Use the existing SSH alias
<colleague-dev-vm> to read the approved development setup and build logs.
Leave the checkout and VM unchanged. Rank friction points by impact, cite
examples, and suggest the smallest improvement for each. In your closing
chat message, summarize the findings and anything you could not verify.
EOF

swarm schedule create \
  --name 'Gaston weekly DX review' \
  --dir "$PWD" \
  --agents codex \
  --authority agent-default \
  --workspace worktree \
  --prompt-file /tmp/gaston-review.txt \
  --timezone Europe/Zurich \
  --weekly Friday --time 08:30

swarm schedule timer install
swarm schedule timer status
swarm schedule list
```

The prompt is ordinary task guidance, not an access-control boundary. `observe` is the requested policy passed to the Codex scheduled launch; confirm the actual CLI's configured permissions before relying on it. `worktree` makes an occurrence-owned Git worktree in a private sibling directory next to Swarm's session state. `directory` uses the selected checkout itself and can expose concurrent edits to that checkout. The editor previews the resolved directory, timezone, next times, authority, agent order, and dispatched prompt before save. You can also create and manage jobs in the TUI under **More → Automations**; selecting “Schedule in this directory” pre-fills the current session directory.

For an existing SSH config entry, the alias is deliberately a placeholder. The timer runs on the host where it is installed and inherits that host user's environment and SSH configuration; it does not run through an SSH connection initiated by the local laptop. Check that the host can locate the agent CLI. A prompt can name existing SSH aliases. For unattended access, configure OpenSSH on that host with an explicit identity or IdentityAgent and verify that the chosen authority permits the network operation. Swarm filters the daemon environment and does not forward SSH_AUTH_SOCK from your shell.

## Calendar and missed runs

Calendars use a named IANA timezone (not `Local`) and support a one-time date, daily, weekdays, weekly weekday, or five-field cron. A wall time skipped by a daylight-saving jump is skipped; an ambiguous wall time is run at its first instant. `--catch-up 1h` (or the TUI's catch-up choice, currently one hour) allows only the latest missed calendar slot when it is within that grace period. With no grace, a slot at least one minute late is recorded as missed. The scheduler advances past older missed slots rather than replaying a backlog. Pausing suppresses calendar dispatch; manual `run` is still allowed on a paused job. `run` creates a separate occurrence and does not move the calendar cursor.

macOS installs a per-user LaunchAgent at `~/Library/LaunchAgents/com.swarm.schedule.plist`, checking every minute. Linux installs a systemd **user** service and timer under `${XDG_CONFIG_HOME:-~/.config}/systemd/user`; `Persistent=true` allows a missed timer to fire after the user manager becomes active again. These timers do not wake a sleeping host. On macOS, launchd coalesces missed calendar intervals and checks on wake; the scheduler's grace policy decides whether the latest slot is still eligible. Check both service status and scheduler history after an outage.

```sh
swarm schedule timer status       # installed/runtime state
swarm schedule status             # alias for timer status
swarm schedule tick               # run a scheduler check now
```

Install or uninstall from the host account that owns the jobs:

```sh
swarm schedule timer install
swarm schedule timer uninstall
```

Install requires a stable executable path. Linux requires a working systemd user manager. The installer preserves/restores existing unit definitions and activation state if a change fails; status reports installed and active/inactive, not whether an agent provider is healthy.

## Agent order, usage limits, and workspaces

Agents are tried in the order shown with `→`. A launch refused before starting can advance to the next configured route. If a running worker reaches a recognized provider usage limit, Swarm requests a stop and waits for verified stop evidence before trying the next configured agent in the **same occurrence workspace**. The replacement prompt tells the next worker to inspect and preserve earlier changes and includes the previous session reference and available handoff evidence. It does not transfer provider credits, guarantee a new provider has quota, or qualify an unverified adapter route. If stop cannot be verified or launch outcome is uncertain, the occurrence is sent to attention instead of automatically starting another worker.

Each scheduled occurrence gets a distinct worktree when `worktree` is selected. Worktree mode requires a Git working tree. With `directory`, the first worker can use the selected checkout directly. An overlap can be allowed only by the job's overlap setting in the UI/spec, and isolated worktrees are used for concurrent occurrences. Swarm keeps unfinished workspaces for inspection and recovery; it does not automatically commit, push, merge, or delete agent changes.

Claude Code can ask you to trust a new checkout or approve a tool under its configured permissions. When Swarm reports **Needs input**, select the job in Automations, open **History**, choose the run, then select **Open worker** to answer the native prompt. Review the checkout before trusting it. Use **Run now** with the intended checkout and authority to confirm the worker can start with the local provider login before relying on the timer.

## Completion, review, and archive

The worker is instructed to post a concise closing chat message with its result, current state, and artifact links, then call the run-scoped `swarm schedule complete --summary ...` command (repeat `--link` for artifacts). The command only works inside the active scheduled worker session and validates the current attempt, session, and private completion credential. A receipt records the worker's assertion that the requested task is complete; it is **not** an independent quality check, test result, clean diff, commit, or merge.

After the receipt is stored, Swarm projects the completion onto the worker session: it applies the `automation` tag and a completion marker. The board displays a ◆ for a session with that marker and tag. Changing or clearing the tag removes that visual marker; the run receipt/history remains. The native chat is the place to read the worker's closing message. Review the diff and links in the saved workspace before accepting the result. A process exit without a receipt is incomplete, not completed.

In the TUI, open Automations to inspect job detail and run history; run detail shows outcome, workspace, agent order, detail text, completion summary, and links. CLI equivalents are:

```sh
swarm schedule show <job-id>
swarm schedule history <job-id>
swarm schedule run <job-id>
swarm schedule pause <job-id>
swarm schedule resume <job-id>
swarm schedule archive <job-id>
```

Archiving prevents future calendar and manual runs while retaining the job and history. It does not delete sessions or worktrees. The current store has no automatic schedule-history or workspace retention cleanup; records and retained worktrees can grow over time. Review disk usage and remove artifacts only after you have verified they are no longer needed. Do not treat timer uninstall as an archive or data deletion operation.

## Diagnostics and rollback

Start with read-only inspection:

```sh
swarm schedule timer status
swarm schedule list
swarm schedule show <job-id>
swarm schedule history <job-id>
```

In the TUI, refresh the Automations list and inspect the occurrence detail. States distinguish queued, working, needs input, incomplete, exhausted, ready, attention, blocked, and missed. For a worker, open its linked Swarm session and inspect the chat and workspace. For `attention` or unknown launch/stop outcomes, inspect the daemon session before retrying; do not assume no worker was started. `swarm schedule tick` is a useful manual scheduler check, but it does not prove that the provider CLI will authenticate or complete work.

To stop future automatic checks while preserving the catalog, use `swarm schedule timer uninstall`. To stop future runs for a particular job while keeping history, use `swarm schedule pause <job-id>` or `archive`. These operations do not kill an already running worker. Inspect and stop a live session through the normal Swarm session controls when needed. Reinstalling the timer resumes calendar evaluation against the saved job definitions and their missed-run policy.

The private catalog lives at `schedules/state.db` under the daemon state directory. Manual invocation IDs allow a retry after a lost reply to return the original occurrence.

For supported CLI syntax, run `swarm schedule help`. `swarm schedule create --spec-file <file>` and `edit <job> --spec-file <file>` accept a JSON job specification; `list --json` emits machine-readable job data. The completion command's `--link` option is repeatable.
