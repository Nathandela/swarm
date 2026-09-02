# ADR-025: The daemon answers a CLI's own launch gate

- Status: Accepted
- Date: 2026-09-02
- Source: bead swarm-1mq — a Claude Code session launched in a repository the CLI had never been trusted in died at its folder-trust dialog, on every new repository, since Claude Code 2.1.258
- Affects: `internal/adapter` (new optional extension `LaunchGateAnswerer`), `internal/adapter/claude` (`trustdialog.go`, recorded 2.1.258 grid under `testdata/trustdialog`), `internal/engine` (one more claude dialog hint), `internal/skeleton` (`launchgate.go`, called from the grid tap), `internal/smoke` (the realcli harness answers the gate through the adapter)

## Context

A `claude` started in a directory with no `hasTrustDialogAccepted` entry in `~/.claude.json`
draws its folder-trust dialog before anything else: before the `--settings` hooks fire,
before a positional initial prompt is submitted. Trust is not inherited from a trusted parent
directory (measured live: a fresh child of the long-trusted `/home/ubuntu/data` still asked),
so every new repository hits it. No flag or environment variable skips it; the CLI's own
guidance for its remote-control mode is "run `claude` there once to accept the trust dialog".

Between the two recorded versions the dialog inverted. 2.1.231 preselected
`1. Yes, I trust this folder`; 2.1.258 drops the digits and preselects `No, exit`. The bare
Enter that accepted on 2.1.231 now exits the CLI with status 1. Measured through the daemon
on 2026-09-02: Enter on the gate left a `completed` session with exit code 1, no
conversation id, no hook ever fired, and nothing trusted. Down then Enter accepted the gate,
after which the initial prompt was submitted and the hooks fired normally.

Two harness facts made the failure worse than the CLI change alone:

- the status engine read the dialog as a settled composer. Its marked option row begins
  with the composer glyph and its standing row was not one of the claude dialog hints, so
  the board said `ready_for_review` about a session that could do nothing until a human
  answered, and `swarm watch --until needs_input` never fired;
- nobody was at the glass for an agent-driven `swarm spawn` or handoff into a new
  repository. Those sessions stalled indefinitely with their prompt undelivered.

## Decision

1. **The daemon answers the gate.** The grid tap already samples every running session's
   screen on its 500 ms cadence. When a sample shows a gate the session's adapter records
   an answer for, the daemon joins the session's shared tap as a readWrite subscriber, reads
   the keys off the grid that subscription was seeded with, and writes them once. This is
   exactly the apply-by-injection path ADR-013 chose for a phone approval (Mirror M1.2,
   `internal/skeleton/inject.go`), reused for the same reason: the gate is proven on the
   screen the write is about to land on, so a dialog the owner answered a beat earlier is
   never typed at.
2. **The answer is read off the grid, never assumed.** `claudeAdapter.LaunchGateKeys`
   recognizes the dialog by its title under the box rule, both option labels, and exactly
   one selection marker; it answers with a cursor move only when the marker is not on the
   accept row. Both recorded versions are fixtures and both are tested. A screen that is
   not positively that dialog is a refusal.
3. **At most once per session.** A CLI that ignores the keys costs one write, not one every
   poll. The unanswered gate then shows as `needs_input`, because the engine now knows the
   dialog's standing row (`Enter to confirm · Esc to cancel`).
4. **It is an optional adapter extension** (`adapter.LaunchGateAnswerer`), discovered by
   type assertion like every other extension. Absence is a signal: an adapter that records
   no gate is complete, and the daemon types nothing for it.

## Why answering is the launch's own meaning

The trust dialog asks whether the owner trusts the directory Claude Code is about to read,
edit and execute in. Under swarm that directory was chosen before the CLI existed: the
owner typed it into the launch form, or an agent chose it for a spawn that the daemon's own
launch policy already admits. The launch is the answer; the dialog is the CLI asking a
question the harness has already settled. Refusing to answer it does not add a safety
check, it converts an accepted launch into a dead session.

## Alternatives rejected

- **Pre-writing `hasTrustDialogAccepted` into `~/.claude.json` before launch.** A mutation
  of the owner's global CLI config from a daemon, which ADR-001's adapter contract (T-2:
  per-invocation flags, never a global-config mutation) forbids, and a race with every live
  `claude` that rewrites that file.
- **A native skip flag.** None exists in 2.1.258 (binary strings and `--help` searched).
- **Leaving it to the human, with only the `needs_input` reading fixed.** Fixes the board
  and the agents' `watch`, but the reflexive Enter still kills the session, and agent-driven
  spawns still need a human to notice.
- **Answering from the launch path with a fixed delay.** A guess at timing and at the key
  map; the grid-gated tap write needs neither.

## Consequences

- A session launched in a never-trusted directory reaches its first prompt without human
  intervention, on both recorded dialog layouts.
- The directory becomes trusted in the owner's Claude Code config exactly as if they had
  answered themselves, since the answer is the CLI's own dialog being accepted.
- A future dialog layout the recognizer does not match is not answered and is shown as
  `needs_input`; recording it under `internal/adapter/claude/testdata/trustdialog` and
  extending the recognizer is the upgrade path.
- Residue, stated: the recognizer does not compare the dialog's displayed path with the
  session's cwd (paths wrap, abbreviate, and resolve through symlinks on macOS), so a nested
  interactive `claude` started by the agent inside the session's own PTY for another
  directory would also be answered. An interactive nested CLI inside a tool call blocks the
  agent anyway, and the frozen threat model already trusts the operator's own agents with
  the launch policy that admits that directory.
