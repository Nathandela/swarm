# ADR-024: A provider re-login recycles its stranded sessions automatically

- Status: Accepted (owner decisions 2026-09-01: fully automatic; defer mid-turn; delete the stale row after a verified resume; codex first over a generic seam)
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
   provider's identity every 30s and compares it against (a) the last identity it
   persisted and (b) each running session's stamp. On a change it freezes the stale
   set — stamped mismatches AND unstamped pre-ADR-024 sessions, which predate an
   observed change by construction — into a durable pending list, then works it down:

   - **idle** sessions are recycled: Kill → wait for the recorded exit → Launch with
     `resume_from` (name, cwd carried; env resolved from daemon.env exactly like a
     remote launch) → Delete the stale row (owner's one-row-per-conversation rule);
   - **mid-turn** sessions are deferred until idle — the watcher never interrupts a
     turn, doomed or not;
   - sessions with **no captured conversation id** are left running and logged once: a
     kill would destroy the only thing a manual resume needs too;
   - an **unknown identity** (missing/unparseable credentials — the mid-relogin
     window) holds everything: no baseline update, no recycle.

   Absent an observed change, unstamped sessions are never touched; stamped
   mismatches are recycled even when the change happened while the daemon was down
   (the stamp is ground truth).

4. **Opt-out, not opt-in.** The watcher is on by default;
   `<stateDir>/auth-watch.json {"disabled": true}` (written by
   `swarm relogin --auto off`) turns it off. A present-but-unparseable settings file
   counts as DISABLED: ambiguous config fails toward inaction for a component that
   kills sessions on its own.

5. **`swarm relogin` is the manual face**, for exactly the cases the watcher will not
   decide: with the watcher off it performs the recycle itself; `--force` recycles
   unstamped pre-feature sessions (the human's assertion supplies what the stamp
   cannot); with the watcher on, stamped-stale rows are only reported — two actors
   recycling one row race into duplicate resumes.

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
