# SPIKE S-E: workflow mode — background children make "done" a lie

The complaint (2026-08-07, observed on two live sessions): a claude
orchestrating background agents shows ready_for_review ("done") whenever its
main turn ends, although the workflow is still running. The daemon journal
quantifies it: 76-91 working/ready_for_review flips PER SESSION that day —
each ready window during an active workflow phase is a false "done" (with a
completion banner and a phone push).

## Capture

`fixtures/spike-se/workflow-background.json` — swarm-char (spike-SD method,
same env-scrub wrapper), claude 2.1.224, sonnet, prompted to launch one
background agent (sleep 45) and end its turn. Hook sink registered the
spike-SD event set PLUS SubagentStart, TaskCreated, TaskCompleted,
PostToolBatch, TeammateIdle.

Timeline (ms offsets):
- +20.1s PreToolUse{Agent, run_in_background:true} + SubagentStart +
  PostToolUse (background launch returns immediately) + PostToolBatch
- +22.3s Stop — MAIN TURN ENDS, CHILD STILL RUNNING. The false-done window
  opens here.
- +24.2s / +30.5s SubagentStop (the child wrapped up early — it launched its
  sleep as a background Bash)
- +30.6s UserPromptSubmit — the AUTO-CONTINUATION wake enters as a prompt
- then SendMessage/SubagentStart again (agent resume), Stop, SubagentStop,
  and a Monitor PermissionRequest -> Notification{permission_prompt}.

## Findings

- F1: SubagentStart and SubagentStop both fire, tightly bracketing every
  background child (and agent RESUMES fire SubagentStart again).
  TaskCreated / TaskCompleted / TeammateIdle never fired on 2.1.224.
- F2: Stop fires while children run — outstanding-children accounting
  (starts minus stops) is exactly 1 during the false-done window. Nothing in
  the current 7-event registration represents that state.
- F3: the auto-continuation fires UserPromptSubmit, so active is restored
  once the workflow resumes on its own; UserPromptSubmit therefore CANNOT be
  used to reset any child accounting (it fires mid-workflow).
- F4 (grid): while children run and the main loop is idle, the status bar
  reads "⏸ manual mode on · 1 shell · ← 2 agents · ↓ to manage" — the
  esc-to-interrupt segment is absent, but the shell/agents segments are
  present. A genuinely quiet claude shows "? for shortcuts" (spike-SD). The
  chrome distinguishes the two states.

## Proposed correction (bead agents-tracker-<workflow>)

1. Register SubagentStart: turn=active, and increment a per-session
   outstanding-children counter in the engine; SubagentStop becomes
   turn-NEUTRAL (its current active mapping was the agents-tracker-707 race
   source) and decrements with a floor of zero.
2. While the counter is positive, Stop's idle turn is masked to active —
   the boundary is still recorded (fix E), but the session stays WORKING
   until the last child stops. UserPromptSubmit must not reset the counter
   (F3); a lost SubagentStop self-heals through the grid path (3) once the
   typed-signal suppression window lapses.
3. Grid: a claude frame whose status row carries the mid-dot
   "N shell" / "← N agents" segments reads (active, none, conclusive) even
   with the composer visible — the workflow analogue of the busy row, and
   the self-correcting fallback for counter drift in both directions.
