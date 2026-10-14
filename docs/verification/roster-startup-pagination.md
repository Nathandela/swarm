# Roster startup and journal pagination recovery

Date: 2026-10-14. Baseline: v0.17.7, `e538d1e9`.
Tracked by `agents-tracker-6co2`.

## Observed failure and cause

The owner daemon consumed approximately 290% CPU and 1.1 GB of memory. Symbolizing
a two-second macOS stack sample against the released Go PC-line table identified
`handleJournalSubscribeFrom -> journalReadPages -> maxJournalPageRecords`, with
most samples in JSON serialization and compaction. Each page's binary search
serialized large prefixes of the remaining journal repeatedly. The retained
journal was approximately 156 MiB, making replay cost grow with the backlog for
every page.

The terminal independently discarded an initial List that exceeded its one-second
first-paint budget. No command applied the eventual result, and a failed List
could leave a connected empty board. Reconnect hydration also used that paint
budget even though hydration runs in a background command.

## Fix

- Keep the eager first-paint bound, then await and apply the same pending read.
  Existing connection-generation, source-channel and roster-revision checks fence
  stale results. Failed reads trigger recovery; closing the TUI cancels its wait.
- Give background hydration the transport's ten-second request budget.
- Size journal and roster pages by adding each record's actual JSON-encoded size
  to the envelope and comma overhead. Final frame bounds, ordering, cursor and
  continuation checks remain enforced. Check connection cancellation between
  pages so shutdown does not finish serializing an abandoned replay.

The wire schema, agent lifecycle, account policy and persisted history formats
are unchanged.

## Failing-first evidence

Before implementation, the new regressions failed:

- `TestStartup_LateRosterArrivesWithoutStatusEvents`: late snapshot discarded.
- `TestStartup_FailedInitialReadStartsRecovery`: no recovery after failed List.
- `TestReconnect_SlowRosterIsAccepted`: successful 1.1-second hydration rejected.
- `TestJournalPaginationWorkIsBoundedByPageSize`: 158,191,560 bytes allocated for
  journal pagination and 157,437,216 for roster pagination of 2,048 synthetic
  records with approximately 1 MiB of string payload.

The final non-race allocation measurements were 3,258,336 and 3,318,688 bytes.
The regression also checks every record, page size, cursor and continuation bit.
Additional tests cover stale startup completion after reconnection, cancellation
on TUI close, closed-connection pagination, mixed journal/roster pages, escaped
strings and raw interaction JSON decoded from the resulting wire frames.

## Validation

- Full build and vet: passed with Go 1.26.0, matching the repository's CI pin.
- golangci-lint v2.12.2: zero issues.
- Changed protocol package: full race suite passed.
- Changed TUI package: full race suite passed using canonical `/private/tmp`.
- First-paint gate: p95 7.20 ms over 25 reads of 50 real fixture sessions.
- Strict Phase B documentation manifest: passed.
- A disposable daemon built with the release's Go 1.26.9 runtime replayed a
  private local copy of 161,910 retained records over 154 legal frames, 153.3 MiB
  on the wire. First page: 2.042 seconds; complete replay: 5.038 seconds, including
  Python client decoding. The snapshot cursor remained constant. Graceful
  shutdown took 0.045 seconds. No live session
  metadata was copied or adopted, and the private copy was removed afterward.

The full macOS race suite was attempted. It remains red in unrelated account
platform/configuration fixtures, account containment/reachability checks and a
shim resize timing assertion. Representative account failures were reproduced
on the untouched v0.17.7 worktree. `/tmp` is a symlink on this Mac and is rejected
by private account-path checks; `/private/tmp` fixes those path-only failures but
not the existing platform cases. None of those guards or frozen tests was relaxed.
The full Linux CI run is required before publishing a release. Follow-up
`agents-tracker-4p4q` records the unrelated macOS gate failures.

## Owner installation

Installed `0.17.7+rosterfix` with Go 1.26.9, retaining the original Homebrew binary
as `swarm.before-rosterfix` next to the installed executable. The normal daemon
restart succeeded. All 19 roster entries, 11 observed live shim process/start
identities, stored conversation/account identities, and the saved daemon
environment survived unchanged. `swarm doctor` reports a matching patched daemon.
Existing OpenCode environment and Homebrew unattended-upgrade warnings remain.
