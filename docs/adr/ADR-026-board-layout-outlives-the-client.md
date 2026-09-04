# ADR-026: The board's grouping and ordering outlive the client that chose them

- Status: Accepted
- Date: 2026-09-04
- Amends: the session-board options window (`internal/tui/options.go`, owner decision 2026-08-27), whose stated rule was "the choice lives with the running client only"
- Affects: `internal/tui` (`layout_store.go`, `tui.go`, `options.go`), `cmd/swarm` (`runTUI`)

## Context

The options window (`o` on the general view) chooses how the board is sectioned
(`status` / `repo` / `tag`) and how rows are ordered within a section (`arrival` /
`activity` / `created` / `name`). The window was built in one deliberate shape --
one form, arrow navigation, applied on Enter -- and the choice was scoped, equally
deliberately, to the running process.

That scope is wrong for the way the board is actually used. An owner who groups by
tag does so because tags are how they think about their sessions, not because of
anything about the current process; the grouping is a standing preference, not a
per-run decision. Because swarm's whole premise is that sessions outlive the client
(ADR-001), the client is restarted often -- on upgrade, on a closed terminal, on a
daemon restart -- and every restart silently threw the choice away and re-sectioned
the board by status. The owner had to re-pick the same layout, by hand, after every
one of those events.

The same window also hosts the ContextGuard auto-compaction rows (ADR-023). Those
are already durable, but by a completely different mechanism: they are a
DAEMON-GLOBAL policy, read and written over the protocol under a compare-and-swap
revision, because a policy that governs sessions must be one value no matter who
asks. That mechanism is the wrong shape for a layout: how a board is sectioned is a
property of the terminal doing the drawing, not of the machine's sessions, and
putting it behind a protocol op would give it a capability, a revision, a
stale-revision refusal path, and a daemon-side store -- all to answer a question no
other client is entitled to.

## Decision

The grouping/ordering pair is persisted as a CLIENT preference and restored before
the first paint.

1. `internal/tui` declares a `LayoutStore` seam (`LoadLayout`/`SaveLayout`), injected
   with `WithLayoutStore` in the same idiom as `WithAttachRunner` and
   `WithDaemonRestarter`. The router itself keeps no filesystem knowledge, and every
   unit test drives an in-memory store.
2. `New` seeds the board from the store after the options are applied, so the board
   is already on the restored layout when Init paints (N-1).
3. Applying the window (Enter) commits the layout to the board and dispatches the
   durable write off the update loop.
4. The document names the modes by their rendered labels (`"tag"`, `"name"`), not by
   the enum's numbering, so it stays readable and survives a later reordering of the
   modes.
5. `FileLayoutStore` keeps it in one small JSON document at
   `<os.UserConfigDir()>/swarm/board-layout.json` -- the user's config directory,
   NOT the daemon's session state directory, which the daemon owns and hardens.
   Writes are atomic (temp file in the same directory, renamed over the target).

Both halves are best-effort BY CONTRACT, and this is the load-bearing part of the
decision:

- A store that cannot be read leaves the client on its built-in defaults and does
  not stop it coming up. A missing document is a first run, not a failure.
- A label this build does not have falls back FIELD BY FIELD, so a document written
  by a swarm with a mode this one lacks still contributes the half it understands.
- A write that fails is bannered and nothing is rolled back. The layout is applied
  to the running board before the write is even dispatched, so durable custody can
  never cost the owner the choice they just made.

The ContextGuard rows in the same window keep their existing protocol path
unchanged. One window, two custody models, because they are two different kinds of
setting.

## Consequences

### Positive

- The board comes up the way it was left, across upgrades, restarts and closed
  terminals -- the property the rest of swarm already promises about sessions.
- The seam keeps `internal/tui` testable without a filesystem, and keeps the
  preference out of the protocol, the daemon, and the capability set entirely.
- Nothing about the window's shape changes: same form, same arrows, same Enter.

### Negative

- A second custody model now lives behind one window. The code says which is which,
  and this ADR is the reason; a reader who assumes both rows travel the same way
  will be wrong.
- The preference is per-machine and per-user, not per-machine-and-per-terminal. Two
  concurrent swarm clients on one machine share the document, and the last one to
  apply a layout wins. That is the right default for one owner at one desk, and no
  reader is disturbed mid-session -- a restored layout is only read at start.

## Alternatives Considered

- **Daemon-global, over the protocol, like ContextGuard.** Rejected: it makes a
  drawing preference into a machine policy, and buys a capability negotiation, a CAS
  revision and a daemon store for something no other client should be reading.
- **In the daemon's state directory, beside `context-guard-settings.json`.**
  Rejected: that directory is the daemon's, created and hardened by it; a client
  writing into it inverts the ownership for no gain.
- **Persisting the whole options window, ContextGuard included.** Rejected: it would
  duplicate a value that is already durable elsewhere, and give the two copies a way
  to disagree.
