# Remote Phase 1 — Daemon Foundation — Evidence

**Slice**: the durable journal (`internal/journal`), two-phase launch idempotency
(`internal/idempotency`), and the protocol journal ops + remote-tier extensions
(`internal/protocol`), plus the daemon-side wiring that hooks them into the existing
supervisor. Implements R-JRN, R-IDP, and R-PROT.3/.7 per ADR-007 D6 and plan
amendments D.0-A3 (two-phase idempotency), D.0-A7 (journal hooks the choke point),
and D.0-A11 (error taxonomy).

## What it delivers

- **Durable daemon-wide journal** (`internal/journal`): append-only, versioned
  records with a monotonic `Cursor`, fsync-before-ack, torn-tail tolerance on replay,
  segment rotation + retention, and an atomic `ReadFrom(cursor)` that returns a roster
  snapshot + the events after the cursor + a `FullResync` signal when the caller's
  cursor fell below the retained floor (R-JRN.4/.5/.6). A delivery-layer `Debouncer`
  that never delays a terminal record and never sits in the durable log (A7).
- **Two-phase launch idempotency** (`internal/idempotency`): a `prepared ->
  executing -> completed/failed` record keyed by `operation_id`, fsync'd BEFORE the
  side effect, replay returns the cached outcome, `ResolveOutcomeUnknown` for the
  at-most-once interrupt case, and TTL/cap `Compact` (tmp+rename) (R-IDP.2/.3, A3).
- **Daemon wiring** (`internal/daemon`): the journal is hooked at the single
  `saveMetaLocked` CHOKE POINT (A7) so every status/lifecycle/reconcile transition is
  journaled by ONE hook rather than a named-caller list; a separate `Delete` tombstone
  hook; WAL-consistent ordering (journal append BEFORE the meta write, so a crash can
  leave journal-without-meta but never meta-without-journal); and `LaunchSpec.OperationID`
  wired through the two-phase reservation so a replayed remote launch reuses the reserved
  session and spawns nothing.
- **Protocol** (`internal/protocol`): `ErrorCode` refusal taxonomy + `Transient()`
  (A11); `JournalRecord/JournalResume/JournalBackend` (optional-interface seam mirroring
  the existing `stopEvents()` pattern); `journal_read` (snapshot+range) and
  `journal_subscribe` (ordered `journal_event` stream reusing the bounded-queue
  evict-the-wedged-subscriber discipline, S9/L1); additive omitempty Control carriers
  (`operation_id`, `interaction_id`, `device_id`, `device_sig`, `cursor`, `issued_at`,
  `expires_at`, `approve`, `error_code`, `journal`, `full_resync` — times as `*time.Time`);
  and `ServeRemote`, the dedicated remote-tier server that requires an `operation_id` on
  every remote mutating op (D.0-A1/A4). protocol.md documents every new wire field (GG-7).

## TDD evidence (GG-5)

Failing-first RED committed at `64d6411` (26 tests across `internal/journal`,
`internal/idempotency`, `internal/daemon`, `internal/protocol`), captured under
`docs/verification/remote-phase1-red/daemon-red.txt`. This GREEN commit makes them pass
without modifying any RED assertion. The journal-subscribe test's harness was corrected
(not its assertions) — see the flakiness note below.

## Design decisions (flagged for review)

- **A7 choke-point hook**: the journal derives each record's TYPE by diffing the
  PREVIOUS meta against the next inside `saveMetaLocked`. Because a launch reservation
  inserts the session into `d.sessions` BEFORE its first `saveMeta`, a naive
  map-membership `prevExists` would never emit a `launched` record (prev == next ==
  Running). A `persisted bool` on the session struct (false for a raw reservation,
  flipped true by `putMem` on first commit) makes `launched` fire exactly once, and
  reconcile-driven `lost`/`exited` (session not yet in the map) derive correctly. This is
  the minimal seam that satisfies all six journal tests including the reconcile
  choke-point proof.
- **A3 mechanism deviation (flagged)**: A3's wording is "operation_id persisted AS PART
  OF the reservation, same fsync". The implementation instead persists launch idempotency
  in a SEPARATE `idempotency.Store` fsync, then reserves. Both give crash-safe
  no-double-spawn and the R-IDP.3 test passes; the separate store is cleaner (no unused
  `operation_id` key on every local session's meta.json). Flagged for the audit to accept
  or require the literal single-fsync form.
- **idempotency.Store has no Close()**: every write is individually fsync'd, so nothing is
  lost on an unclean exit; the daemon does not call a Close (there is none). A 3-line
  `func (s *Store) Close() error` is a suggested follow-up so `Close`/`abandon` can release
  the fd deterministically.
- **Journal send-buffer bound** (`journalSndBuf`, server.go): a journal-subscribe
  connection's kernel send buffer is bounded (best-effort `SetWriteBuffer`) so a wedged
  subscriber blocks after a few KB rather than pinning hundreds of KB of kernel memory and
  deferring eviction — production-justified hygiene, and it makes the S9 eviction prompt.

## Quality gates (GG-4)

Run in ISOLATION (see the flakiness note on why full-parallel runs on a loaded machine
are not a clean signal):

- `go build ./...` — clean (whole tree).
- `go vet ./...` — clean.
- `gofmt -l internal/journal internal/idempotency internal/daemon internal/protocol` — empty.
- `go test -race -count=1 ./internal/journal/... ./internal/idempotency/...
  ./internal/protocol/... ./internal/daemon/...` — all green (journal 28.8s,
  idempotency 2.4s, protocol 10.7s, daemon 33.0s).
- `TestProtocol_Journal*` under `-race`, run sequentially: 25/25 then 5/5 green.

golangci-lint is not on this PATH.

## Flakiness note (IMPORTANT for the next agent / CI)

`TestProtocol_JournalSubscribeOrderedAndEvictsWedged` asserts the S9/P-3 property that a
subscriber which stops reading its socket is evicted within a bound. Triggering that
eviction requires the server's per-subscriber bounded queue to OVERFLOW, which requires
the wedged subscriber's socket writer to block, which requires the OS socket buffers to
fill — a CPU-scheduling- and OS-buffer-sensitive condition. On this development machine
(running the project's own `swarm` daemon at ~40% CPU, terminals, and several concurrent
worktree agents; load average 6-9) a full-parallel `go test ./...` run STARVES the
fan-out goroutine and the assertion can fail.

This is environmental, not a defect: the PRE-EXISTING, proven
`TestFanout_WedgedSubscriberDisconnectedWithinBound` (unrelated to this slice) fails under
the exact same parallel starvation. Both are reliable in isolation (verified 25/25 and
5/5 under `-race`) and on an unloaded CI box.

Harness corrections made to the journal test (assertions unchanged):
1. Drain the healthy subscriber CONTINUOUSLY (a fixed-count read left the "healthy"
   subscriber wedged too, so eviction raced between the two).
2. Gate the eviction check on the healthy subscriber having received > `eventQueueCap`
   frames — this guarantees the fan-out delivered enough to overflow the wedged
   subscriber's queue (and evict it) BEFORE `eventuallyClosed`'s reads drain and un-wedge
   the wedged socket.
3. Bound the wedged client's recv buffer + a generous 15s deadline to tolerate slow
   scheduling.

Recommended CI: run `internal/protocol` with `-p 1` or in isolation, or on a box with
spare cores, so the wedged-eviction tests are not starved.

## Integration notes / follow-ups

- The remote-tier `ServeRemote` requires `operation_id` on mutating ops but the per-command
  device Ed25519 SIGNATURE verification (R-POL.9 / D4) is a separate slice — this lands the
  socket/operation_id half, not the signature-verification half.
- Daemon-side approval expiry (R-POL.8 / A6) is out of scope here; `expires_at` is carried
  on the wire but not yet enforced daemon-side.
- Add `idempotency.Store.Close()` (see above) before the daemon's Close/abandon can release
  the store fd deterministically.
