# ADR-024: A provider re-login recycles its stranded sessions automatically

- Status: Accepted; amended 2026-09-14 below (automatic recovery, defer mid-turn, retain source history, Codex first over a generic seam)
- Date: 2026-09-01
- Source: the 2026-09-01 incident — every codex session started before that morning's account switch answered each prompt with "Your access token could not be refreshed because you have since logged out or signed in to another account. Please sign in again."
- Affects: `internal/adapter` (new optional extension `AuthProbe`), `internal/adapter/codex` (probe + resume option flags), `internal/persist` (`Meta.AuthIdentity`), `internal/daemon` (`LaunchSpec.AuthIdentity`, launch stamp), `internal/skeleton` (`authwatch.go`; launch identity stamp; resume option merge in `composeLaunchSpec`), `cmd/swarm` (`relogin`)

## Context

Codex processes — the PTY TUI and the per-session `codex app-server` backend — load
`~/.codex/auth.json` once at startup and hold its tokens in memory. A logout/login to
another account rotates the stored credentials; every process started before the change
then fails each token refresh until restarted. Measured live on 2026-09-01: a re-login
at 07:28 UTC stranded 10 of 13 running codex sessions; the three the owner had manually
cycled minutes after the switch were fine.

The manual fix was, per session: Ctrl+X (kill), then `r` (resume-as-new-session). That
gesture already works end-to-end — the fresh processes read the new auth.json, and
`codex resume <threadId>` (from the captured `conversation_id`) restores the whole
conversation, including its per-thread model. Verified live: a recycled session
completed a real turn on the new account immediately.

Two facts shape the detection design:

- **mtime is a false signal.** Codex rewrites auth.json (fresh tokens, `last_refresh`)
  on every ROUTINE refresh. Watching the file's mtime or whole-file hash would recycle
  the fleet daily. The stable signal is the ACCOUNT identity inside the file:
  `auth_mode` + `tokens.account_id` (or the API key in apikey mode).
- **the error is only pixels.** The failure surfaces as ANSI-painted text in the PTY
  stream, not as a typed app-server event swarm could subscribe to. String-scraping the
  terminal for it would be version-fragile and fire only AFTER a user already hit the
  wall. Identity comparison detects the change before anyone types.

## Decision

1. **A new optional adapter extension, `AuthProbe`** (the `TranscriptLayout`
   discovery pattern): `AuthCredentialsFile()` names the credentials file relative to
   home; `AuthIdentity(raw)` derives a SHA-256 account digest that is invariant under
   token refreshes and never carries a secret. Codex implements it; a provider without
   a characterized credentials layout simply is not watched.

2. **Every launch stamps `Meta.AuthIdentity`** (additive, omitempty, no schema bump —
   the AgentCwd rollback reason) at the one entry every launch passes through
   (`coreAPI.Launch`), so each session records which account its processes loaded.

3. **The daemon's auth watcher** (`internal/skeleton/authwatch.go`) polls each probed
   provider's identity every 30s (bounded, regular-file-only credential reads) and
   compares it against (a) the last identity it persisted and (b) each running
   session's stamp. On a change it freezes the stale set — stamped mismatches AND
   unstamped pre-ADR-024 sessions, which predate an observed change by construction
   — into a durable pending list, then works it down under one governing rule,
   **destruction never outruns recovery** (2026-09-01 audit round):

   - a recycle first proves the resume is composable — the agent binary resolves on
     the SESSION's own saved env, the conversation id is captured — then durably
     records the kill as its own (`killed` mark; an unpersistable claim forbids the
     kill), re-checks the session is still quiet, kills, waits for the recorded
     exit, launches the replacement with `resume_from` (name, cwd, the session's
     own env and its handoff lineage carried), and only then Deletes the stale row
     (owner's one-row-per-conversation rule). An ended session carrying the killed
     mark is a resume OWED — completed on a later tick or by the next daemon
     incarnation, never dropped as "ended by other hands";
   - **mid-turn or mid-interaction** sessions (a permission prompt rides an idle
     turn) are deferred until quiet — the watcher never interrupts;
   - **worktree-isolated** sessions are never auto-recycled: the resume cannot
     follow the conversation into its checkout, and the auto-delete would
     `git worktree remove --force` uncommitted agent work. Warned once, left for
     the user;
   - sessions with **no captured conversation id** are left running and warned once:
     a kill would destroy the only thing a manual resume needs too;
   - an **unknown identity** (missing/unparseable credentials — the mid-relogin
     window) holds everything: no baseline update, no recycle;
   - the **first tick after daemon start never begins a kill**: reconciled sessions
     are seeded from persisted status, so new kills wait one interval for the
     engine to reclassify (owed resumes still complete immediately);
   - the daemon **dedups `resume_from` per source**: a source with a running
     resumed child yields that child, so a racing human `r`, `swarm relogin` and
     the watcher converge on ONE replacement, and the watcher's crash-replay of an
     owed resume is idempotent. In shutdown the watcher closes FIRST, before any
     assembly component its launches depend on.

   Absent an observed change, unstamped sessions are never touched; stamped
   mismatches are recycled even when the change happened while the daemon was down
   (the stamp is ground truth).

4. **Opt-out, not opt-in.** The watcher is on by default;
   `<stateDir>/auth-watch.json {"disabled": true}` (written by
   `swarm relogin --auto off`) turns it off. A present-but-unparseable settings file
   counts as DISABLED: ambiguous config fails toward inaction for a component that
   kills sessions on its own.

5. **`swarm relogin` is the manual face**, for exactly the cases the watcher will not
   decide: with the watcher off it performs the recycle itself; `--force` asserts a
   re-login happened and recycles what no stamp can prove — unstamped pre-feature
   sessions AND same-account re-logins whose stamps still match (see Limitations);
   with the watcher on, stamped-STALE rows are only reported (watcher-owned). It
   re-checks each row's freshness immediately before its kill, refuses a daemon
   whose endpoint id is not the local state dir's (`SWARM_DAEMON_SOCK` can point
   anywhere), and `--auto on|off` works with no daemon running.

6. **Side-fix: resume keeps the source's launch options.** The TUI's resume request
   carries only `resume_from`, and the composed argv silently dropped `--model` and
   `--sandbox` (observed live). `composeLaunchSpec` now merges the source's persisted
   `launch_options` beneath the request's own (request wins; reserved orchestration
   keys never chain), and the codex adapter's `Resume` appends option flags exactly as
   claude's always has.

## Alternatives rejected

- **Scraping the PTY for the error string** — reactive (fires after the user hit the
  wall), version-fragile, and ANSI-interleaved.
- **Watching file mtime** — recycles everything on every routine token refresh.
- **Restarting processes in place (same session)** — no such primitive exists; kill +
  resume-as-new-session is the proven, already-tested path the TUI's own `r` uses, and
  `ResumedFrom` keeps the lineage.
- **A protocol op for the watcher's settings** — the settings file is machine-local
  state the daemon re-reads each tick; `relogin --auto` writes it directly (the doctor
  local-read precedent), and no wire change means no protocol.md drift.

## Limitations (named by the 2026-09-01 adversarial audit, kept by design)

- **A same-account logout/login is invisible to the watcher.** The account identity
  is unchanged, so no stamp ever looks stale — yet the old processes hold revoked
  tokens. The detection primitive cannot express this event; `swarm relogin --force`
  is the human's assertion that covers it.
- **The idle check-then-kill window cannot be fully closed** without a daemon-level
  conditional kill. The watcher re-checks status immediately before the kill,
  shrinking the window to one roster read; a prompt landing inside that residue
  loses its turn (the conversation itself survives the resume).
- **The identity digest hangs on codex's auth.json field names.** A codex release
  that renames `tokens.account_id` silently degrades the feature to "hold"
  (identity unknown — never a wrong recycle); one that changes the value's meaning
  would trigger a one-shot fleet recycle at upgrade time.
- **A session launched with a per-session HOME** is stamped from that home (the
  launch stamp follows the env), but the watcher polls only the DAEMON's home:
  such a session is recycled or held by the daemon-home account's changes, not its
  own. Exotic; the stamp at least records the truth.
- The codex context meter restarts after a resume (observed 78% → 4%): codex
  rebuilds the thread from its rollout, and whether the model's effective context
  is preserved through that rebuild is codex-internal and unverified.

## Consequences

- After a re-login, stranded codex sessions come back by themselves within ~30s of
  going idle, under their names, in their cwds, with their conversations — the board
  shows one fresh row per conversation and the stale rows are gone.
- A session recycled this way restarts its codex context accounting (observed: a 78%
  context meter read 4% after resume): codex rebuilds the thread from its rollout.
  Whether that rebuild preserves the model's full effective context is codex-internal
  and not verified here.
- The codex update-available dialog can interpose on a resumed session's first screen
  (codex's own nag, once per new version). The recycle still completes; the dialog
  waits for a keypress like any other codex prompt.
- Claude and the other providers gain nothing until someone characterizes their
  credentials layout with an `AuthProbe` — deliberately, per the absence-is-the-signal
  rule.

## Amendment — 2026-09-14: retain history and distinguish launch from recovery

This amendment supersedes the original automatic source deletion, direct-child-only
resume deduplication, blanket worktree exclusion, and unconditional permission-flag
forwarding decisions above. The earlier incident observations remain historical
evidence, not acceptance evidence for the current implementation.

**Recovery keeps history.** The watcher persists the replacement candidate and checks
it on a later tick. Success requires a running replacement under the current account,
the expected conversation ID, and a live backend subscription for that thread and
session incarnation. A PID or a successful launch RPC is insufficient. An exited
candidate, wrong conversation, or readiness timeout stops automatic retries with a
logged manual-recovery reason; the source and replacement files remain intact. This
proves conversation transport readiness, not a successful authenticated model turn.
The manual `swarm relogin` command reports `started`, explicitly without claiming
verified resume readiness.

**One displayed discussion can contain several retained attempts.** Projection follows
explicit `ResumedFrom` lineage, including shared missing-parent anchors; independent
launches and handoffs do not merge because their names or conversation IDs match.
Conflicting nonempty conversation identities remain separate, even when a saved
resume edge or missing ancestor links them; a wrong-thread recovery cannot hide its
source. The newest ended attempt represents the discussion unless a running attempt exists.
Multiple running actors remain visible. Resume requests serialize their lookup and
launch and reuse an existing running member of the validated lineage/conversation.
Raw protocol lists and events keep each attempt's real ID, so exact-ID commands such
as `swarm watch` and handoff lookup continue to address the requested attempt.
`supersedes`, `superseded_by`, and `roster_hidden` annotate visibility; the TUI and
`swarm ls` apply the shared filter. TUI selection follows the replacement, including
source/replacement event reordering. Historical IDs remain usable for diagnostics;
they are not aliases that redirect every operation to the newest process.

Deleting the displayed attempt first archives its ended predecessors using durable
`RosterHidden` metadata, then performs the requested deletion. It does not delete
those predecessors' files or checkouts. The projection cannot conceal a running
actor. Deletion that would remove a checkout still used by retained sessions is
refused. An explicit resume from retained history can create a new visible attempt.

**Resume preserves the execution context.** The source's saved environment chooses
the executable and credential store; its effective checkout, name, tag, launch
options, and handoff metadata carry forward. A missing saved worktree path or missing
checkout is refused. Known isolated checkouts can therefore resume without automatic
worktree removal. Metadata inheritance occurs at resume time; editing a historical
attempt does not rename or retag every other attempt. The watcher holds sessions
whose credential store is unreadable or differs from the watched daemon account,
including uncharacterized legacy `CODEX_HOME` overrides. Controller ownership,
active turns, interactions, and unresolved provider effects continue to defer recycling.

**Account changes reload discussions even with terminal drafts (2026-09-15).**
The owner explicitly accepts losing unsent editor text when switching accounts.
Durable direct-input draft/submit markers therefore do not defer auth recovery;
they still protect automatic supervisor delivery. No terminal input is replayed.
On native resume, Swarm waits for the saved conversation ID to appear in Codex's
loaded threads and subscribes to that exact thread. It does not require a new
`thread/started` announcement for an existing conversation.

**Codex permission handling is specific to the local backend attachment.** The
Codex 0.154.0-and-newer stable-release path is
`codex resume <thread-id> --remote unix://...`:
Swarm's local app-server socket is handled by Codex's remote-workspace startup path.
Its permission-override rejection does not establish that the conversation is a
cloud task. For this attachment only, the backend plan removes the composed
`--sandbox` and `-c sandbox_workspace_write.network_access=true` arguments from the
TUI resume command. Model and other arguments remain. On resume, the backend
also receives the source launch sandbox as its `sandbox_mode` default when that
mode was recorded. Codex 0.156.1 restores saved named permission profiles, but
legacy threads without a saved profile use the backend defaults. Passing the
recorded mode to the backend prevents those threads silently becoming read-only.
Saved named profiles still take precedence, including their network restrictions.
When no source sandbox was recorded (including external conversation imports),
Codex uses its current server configuration; Swarm does not infer a historical
policy. See [0.156.1 verification](../verification/auth-recovery/2026-09-23.md).
Fresh launches and standalone fallback retain their complete permission arguments.
The live backend version selects this behavior at the 0.154.0 boundary; an exact
release allowlist would restore rejected permission flags on every upgrade. Older,
unparseable, prerelease, and build-suffixed versions retain the previous behavior
because the existing banner parser accepts numeric release tokens only. Upgrade
verification must exercise native resume and confirm the restored policy, rather
than classify a conversation from this error string. See the
[0.157.0 regression verification](../verification/auth-recovery/2026-09-25.md).

**Remaining boundaries.** Same-account logout/login still requires the explicit
`swarm relogin --force` assertion because stable account identity does not change.
Old clients that ignore the additive visibility fields still display raw attempts.
The phone's durable journal and reducer also remain raw: this change does not claim
one-row continuity on that surface or alter its atomic snapshot/cursor contract.
Already-disconnected lineage fragments cannot be reconstructed from names alone.
Recovery does not transfer an attached terminal or controller lease to a new session
ID, nor does it guarantee another account can access every saved conversation.
A live account-switch test that completes an authenticated turn remains a separate
acceptance gate from automated recovery and projection tests.
