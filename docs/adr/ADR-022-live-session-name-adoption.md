# ADR-022: A session's name follows the name its CLI shows, newest wins

- Status: Accepted (owner decision 2026-08-28: "newest wins", both rename paths stay)
- Date: 2026-08-28
- Source: bead agents-tracker-tila (investigation), agents-tracker-zlvc (this change)
- Amends: the v0.5 rename (`internal/daemon/daemon.go` `Rename`), which until now was the only writer of `Meta.Name` after launch for a Claude session
- Affects: `internal/adapter` (new optional extension `LiveNameSource`), `internal/adapter/claude`, `internal/persist` (`Meta.NameSetAt`), `internal/daemon` (`RenameAt`, launch stamp), `internal/skeleton` (`livename.go`, the hook path)

## Context

swarm and the agent CLI each keep a name for the same conversation. At launch they
match: the claude adapter passes the swarm name as `--name`. After that nothing talks,
and a rename on either side leaves the two apart. Measured on the owner's machine on
2026-08-28: of the ten swarm sessions whose Claude transcript could be joined, three had
drifted, for example swarm `WIP merging PR and modifying` against Claude's `claude-swarm`,
and Claude's `WIP investigate handoff feature` against swarm's `Investigate handoff feature`.
The owner uses both rename paths (`e` on the board, `/rename` in Claude) and wants one name.

What each CLI has, measured the same day:

- Claude Code 2.1.250 keeps a registry of RUNNING processes at
  `~/.claude/sessions/<pid>.json` carrying `sessionId`, `name`, `nameSource`
  (`user`, `peer`, `auto`, `derived`, `collision`, `hook`) and `nameSince`, the
  epoch-millisecond the name was set. The transcript also records
  `{"type":"custom-title"}` and `{"type":"ai-title"}` lines, re-appended on every resume;
  transcripts reach 195 MB, so they are not a live source. There is no way to rename a
  running Claude TUI from outside: the `rename_session` control request exists only on
  the SDK/stream-json transport.
- Codex 0.147 already syncs both ways over the shim's app-server (`thread/name/set`,
  `thread/name/updated`, `internal/skeleton/naming.go`), proven by
  `~/.codex/session_index.jsonl`, which holds swarm's launch default and then each swarm
  rename for a swarm-launched thread. The Codex TUI never titles a thread itself.
- opencode 1.17.9 titles sessions itself, in SQLite (`session.title`), with no hook to
  trigger a read and no standard-library reader; one such session has ever existed here.
- agy 1.1.22 has a `title` column that is empty on every row and unwritten since July.

## Decision

1. **Claude only, one way.** The name Claude shows for a session becomes the swarm
   session's name. Nothing new pushes a swarm rename into Claude; `--name` at launch
   keeps covering the start. Codex is left as it is. opencode and agy get nothing.
2. **Newest wins.** `Meta.NameSetAt` records when swarm last stamped the name: launch and
   every swarm rename stamp `now`; an adopted Claude name stamps Claude's own `nameSince`.
   A registry name is adopted only when its `nameSince` is later than `NameSetAt`. So a
   rename in Claude reaches the board; a later rename in swarm holds until Claude renames
   again; an older Claude name never overrides a newer swarm one.
3. **The trigger is the hook callback the session already produces.** On every
   authenticated Claude callback (`serveHookInteractions`), once the conversation id is
   known, the assembly reads the registry directory, keeps the newest file naming this
   conversation, sanitizes the name like every other rename and applies it through
   `core.RenameAt`. `/rename` fires no hook of its own, so the board follows by the next
   prompt or stop, not instantly.
4. **The adapter stays pure.** `adapter.LiveNameSource` names the directory (relative to
   the daemon user's home) and parses ONE file; the assembly does the listing and reading
   (bounded: 256 files, 64 KiB each), exactly the split `TranscriptLayout` already uses.
5. **No schema bump.** `NameSetAt` is additive for the same rollback reason as `AgentCwd`;
   a zero value on an older record means "never stamped", which any registry name beats.

## Consequences

- Positive: the drift the data shows is closed for the CLI the owner uses most; the read
  is a handful of ~600-byte files per callback, well under a millisecond; the phone shows
  the same name for free, since it reads the same `Meta.Name`.
- Negative: a swarm rename of a Claude session is not visible inside Claude (its prompt
  box and `/resume` keep Claude's name) until Claude is next renamed; ended sessions are
  not re-read (no registry file exists for them); every callback re-reads the registry
  (documented ceiling in `livename.go`, cache by mtime if it ever shows up).
- Not done, deliberately: opencode (SQLite, no trigger, one session ever), agy (no title),
  conformance coverage for the new extension (unit tests in the claude package instead).

## Verification

`docs/verification/live-name-adoption.md`: the failing-first run (every test fails on the
missing symbols), the green run, and the real-data probe the decision rests on.
