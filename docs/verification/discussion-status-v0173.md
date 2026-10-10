# Discussion activity indicators — v0.17.3

Bead: `agents-tracker-8mi0`. Investigated on 2026-10-10 against main
`d46c56e5` and installed Swarm v0.17.0.

The indicator derives from the daemon's process/turn/interaction status. The
failures occur before rendering, in signal attribution and the terminal fallback.

| Reproduction | Previous result | Correct result |
| --- | --- | --- |
| Codex main turn active, child `turn/completed` on the same backend | Main discussion idle | Main stays active until its own completion |
| Codex approval resolved while its turn resumes | Permission cleared, turn still idle for up to 30s | Turn immediately active and permission cleared |
| Claude main Stop with a child running, idle composer after the 30s typed-signal window | Discussion idle | Discussion stays active; the last child's end settles a stopped main turn |
| Claude's captured `✶ Blanching… (4s · ↓ 74 tokens)` screen after that window | Active CLI classified idle | Timed star-spinner row classified active |
| Claude Notification such as `auth_success` or `agent_completed` during work | Turn idle | Turn preserved; only recognized turn-related subtypes supply a turn |

Read-only live probes against installed Codex 0.162.1 found two active discussions
with four loaded threads each, and another with one. `thread/read` confirmed the
main threads' runtime status was active. The producer previously applied turn
events without comparing their thread identity with the discussion's identity.
The new tests reproduce that admission error against the released producer.

Backend ingestion now checks the registered, adopted, or persisted conversation
identity before status, transcript, approval and name handling. Duplicate,
case-aliased, missing and foreign thread identities cannot pass that boundary.
The initial thread announcement still establishes a fresh launch's identity.
Existing strict JSON decoding and connection/session replacement fences are used.
The adapter's approval-resolution row now restores active as well as clearing the
permission interaction; clearing only interaction left a computing turn idle.

Claude's main turn and each child's lifecycle are tracked separately. Child IDs
have independent sequence high-waters: duplicate starts cannot inflate activity,
unknown/internal stops cannot consume a sibling, and reordered child callbacks
cannot overwrite or be discarded by a newer main-turn callback. Child tool hooks
cannot overwrite the main turn or clear another actor's permission wait. Both
typed producers use one authenticated/validated reducer before deriving the
discussion's status. The grid fallback honors outstanding children and still
exposes permission dialogs. The spinner
reader requires Claude's leading glyph, ellipsis, valid elapsed duration and
metrics group; completion footers and ordinary quoted prose are negative controls.
Notification turns use the adapter's declarative subtype table.
The captured-hook replay helper now retains the recorded `agent_id`. The
background-work capture ends with a stop for a different/internal agent; its test
now explicitly ends the actual resumed child before asserting drainage.

Verification:

- Failing-first checks reproduced the Codex child-completion and foreign-transcript
  bugs against the released ingestion code, and the Claude clock-expiry,
  outstanding-child and unrelated-notification bugs before their fixes.
- Native macOS engine/Codex/Claude tests passed with `-race`; a native CLI build
  using release-pinned Go 1.25.0 reports `swarm 0.17.1`.
- Linux `go vet ./...` and the repository's race suite were exercised with Go
  1.25.13, an init process, and a non-root user. The anonymous runner's owner-name
  failure was rechecked successfully with `USER=swarm-test`. Two synthetic Codex
  fixtures had inconsistent thread identities; they now echo the requested thread
  and announce the recorded completion's thread, and their race checks pass.
- Linux `go build ./...` passed. GoReleaser's exact-commit release gates independently
  run the full suite, Android checks, container scans and signed publication.
- The final actor-state implementation passed all Go packages with `-race`.
  The concurrent 50-session UI fixture hit a request timeout; its complete package
  passed when rerun without competing packages, followed by the Linux build.
- Go 1.25.0 / golangci-lint 2.12.2 reported zero issues, including a final scoped
  check of the thread-identity validation.

No dependencies, status vocabulary, wire/schema versions, or additional polling
were introduced. Android's required release identity is v0.17.3 / code 61.
Live Claude inference was not run; existing captured screens and hook timelines
were replayed. The pre-existing macOS skeleton test-helper build-tag problem is
tracked separately as `agents-tracker-tyyo`.
The first unpublished release gate failed on the unchanged shim fixture's missing
child PID readiness marker; follow-up is tracked as `agents-tracker-fail`.

Protocol references: [Codex app-server](https://learn.chatgpt.com/docs/app-server)
and [Claude Code hooks](https://code.claude.com/docs/en/hooks).

During final verification, PR #50 landed on main and advanced the source release identity to v0.17.2. The actor reducer was rebased onto that update, error attention was included in requester ownership, and a race regression proves child activity cannot erase that attention. Publication therefore uses the next patch, v0.17.3, and preserves the concurrent work.
