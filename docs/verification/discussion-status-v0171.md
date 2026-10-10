# Discussion activity indicators — v0.17.1

Bead: `agents-tracker-8mi0`. Investigated on 2026-10-10 against main
`d46c56e5` and installed Swarm v0.17.0.

The indicator derives from the daemon's process/turn/interaction status. The
failures occur before rendering, in signal attribution and the terminal fallback.

| Reproduction | Previous result | Correct result |
| --- | --- | --- |
| Codex main turn active, child `turn/completed` on the same backend | Main discussion idle | Main stays active until its own completion |
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

Claude's child accounting now ignores rejected/replayed hooks. A stopped main
turn is kept distinct from a new main turn and from child tool activity, so a
child's completion cannot finish a newly started main turn. The grid fallback
honors outstanding children and still exposes permission dialogs. The spinner
reader requires Claude's leading glyph, ellipsis, valid elapsed duration and
metrics group; completion footers and ordinary quoted prose are negative controls.
Notification turns use the adapter's declarative subtype table.

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
- Go 1.25.0 / golangci-lint 2.12.2 reported zero issues, including a final scoped
  check of the thread-identity validation.

No dependencies, status vocabulary, wire/schema versions, or additional polling
were introduced. Android's required release identity is v0.17.1 / code 59.
Live Claude inference was not run; existing captured screens and hook timelines
were replayed. The pre-existing macOS skeleton test-helper build-tag problem is
tracked separately as `agents-tracker-tyyo`.

Protocol references: [Codex app-server](https://learn.chatgpt.com/docs/app-server)
and [Claude Code hooks](https://code.claude.com/docs/en/hooks).
