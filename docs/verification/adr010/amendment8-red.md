# ADR-010 Amendment 8 — exact codex restart rejoin: TDD evidence

**Decision**: [ADR-010 Amendment 8](../../adr/ADR-010-inter-session-orchestration.md).
**Bug**: `agents-tracker-9n1t` (`swarm-9jo` in Amendment 7). **Release target**:
v0.13.25. Independent re-review is complete. This record remains incomplete until the
CI/release/rollout gates below are closed.

## RED: v0.13.24 reproduced the ownership error

The v0.13.24 rollout recorded three sessions with an already-persisted conversation id failing
restart rejoin because each app-server listed four loaded threads:

```
backend has no rejoinable thread: this session's app-server holds 4 threads
```

That is the production RED: `rejoinSessionBackend` ignored the durable selector, authored
`backend_unavailable`, and the phone withdrew chat. The exact session ids and rollout observations
remain in [Amendment 7's rollout evidence](amendment7-red.md#7-rollout).

The failing-first regression surface is:

- `internal/skeleton/rejoin_persisted_identity_test.go`: select the exact persisted id among
  several loaded threads; retain fail-closed legacy selection; reject an invalid/mismatched resume
  response; recover future-send authority without erasing the history marker; fence replacement.
- `internal/skeleton/resume_exclude_turns_test.go`: both the initial resume and rollout-race retry
  carry `excludeTurns: true`; the characterized missing-rollout error registers the already
  identity-selected initialized sink pending retry, and only a later matching response marks the
  live subscription. The test separately confirms that subsequent live notifications still arrive.
- `internal/appserver/frame_limit_test.go`: the inbound WebSocket limit remains 8 MiB; replay is
  avoided rather than the allocation boundary weakened.

Independent review found two gaps after the first implementation. Both were converted to tests
before their corrections; the exact RED command was:

```
$ go test ./internal/skeleton \
    -run 'TestAdoptRejoinedThreadForInstance_DurableIdentityConflictFailsClosed|TestRegisterBackendForInstance_StaleCannotOverwriteOrDeleteSuccessor' \
    -count=1 -v
rejoin_persisted_identity_test.go:258: legacy adoption reported success although durable metadata already names another thread
rejoin_persisted_identity_test.go:324: live backend after stale rollback = (<nil>, false), want untouched successor for "df17ef9ac291914ae9f9b7fcac577615"
FAIL
```

## Focused GREEN after the review corrections

The focused composition tests passed in the shared integration checkout:

```
$ go test ./internal/skeleton ./internal/appserver \
    -run 'Test(RejoinPersistedIdentity|ResumeThreadExcludesHistoricalTurnsOnTheInitialAndRetryCalls|InboundWebSocketFrameLimitRemainsEightMiB|ResumeThreadOnce|R7R3)' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  29.615s
ok  github.com/Nathandela/swarm/internal/appserver 3.095s
```

The same selection/resume set under the race detector:

```
$ go test -race ./internal/skeleton ./internal/appserver \
    -run 'Test(RejoinPersistedIdentity|ResumeThreadExcludesHistoricalTurnsOnTheInitialAndRetryCalls|InboundWebSocketFrameLimitRemainsEightMiB|ResumeThreadOnce|R7R3)' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  16.119s
ok  github.com/Nathandela/swarm/internal/appserver 2.623s
```

Those two review gaps were:

1. Legacy adoption must read durable metadata back and require the selected id; a concurrent
   write-once winner must not leave routing on A while `Meta.ConversationID` stores B.
2. Backend registration must be replacement-atomic; a stale registration must not overwrite and
   then remove the successor's entry between its two instance checks.

They now have named tests. The exact focused GREEN after their fixes was:

```
$ go test ./internal/skeleton \
    -run 'TestAdoptRejoinedThreadForInstance_DurableIdentityConflictFailsClosed|TestRegisterBackendForInstance_StaleCannotOverwriteOrDeleteSuccessor' \
    -count=1 -v
--- PASS: TestAdoptRejoinedThreadForInstance_DurableIdentityConflictFailsClosed
--- PASS: TestRegisterBackendForInstance_StaleCannotOverwriteOrDeleteSuccessor
PASS
ok  github.com/Nathandela/swarm/internal/skeleton  8.359s
```

The broader affected surface and its focused race lane also pass:

```
$ go test ./internal/skeleton \
    -run 'TestRejoinPersistedIdentity_|TestAdoptRejoinedThreadForInstance_|TestRegisterBackendForInstance_|TestResumeThreadExcludes|TestR7R3_|TestR7R4_|TestGapReauth|TestRegisterBackend' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  58.128s

$ go test -race ./internal/skeleton \
    -run 'TestRejoinPersistedIdentity_|TestAdoptRejoinedThreadForInstance_|TestRegisterBackendForInstance_|TestResumeThreadExcludes|TestR7R3_TheRecordedRolloutRace' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  9.898s
```

## Subsequent deterministic RED/GREEN rounds

The next review round added two feed-generation races. The implementing-agent transcript recorded
the RED behavior, but no standalone RED output file was retained:

- `TestNoteBackendLostForFeed_RemovedOldFeedCannotDegradeRegisteredSuccessor` paused an old feed
  after its exact removal, registered a successor, and then released the old loss completion. The
  unfenced completion degraded/scarred the already-live successor.
- `TestSubscribeSessionThread_ObsoleteMissingRolloutFeedStopsWhenReplaced` replaced a feed while
  its missing-rollout retry was pending. The obsolete connection issued another `thread/resume`;
  the deterministic assertion is `obsolete feed issued resume call 2 after replacement, want loop
  exit`.

The implementing agent then captured the exact two-test GREEN:

```
$ go test ./internal/skeleton \
    -run '^(TestNoteBackendLostForFeed_RemovedOldFeedCannotDegradeRegisteredSuccessor|TestSubscribeSessionThread_ObsoleteMissingRolloutFeedStopsWhenReplaced)$' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  6.456s
```

The final adversarial round made same-instance feed replacement own all connection-scoped cleanup
and exercised the legacy capability-migration lock path. Its RED details are reconstructed from the
implementing-agent transcript and final assertions because no immutable RED log was retained:

- replacement left the old connection/request/feed lifecycle attached (the final deterministic
  assertion renders this as `old connection close count after replacement = %d, want exactly 1`);
  an old or late feed callback could consequently survive cleanup and be routed through the
  successor;
- the pid-only legacy migration child exceeded the three-second bound and was killed, proving a
  lock re-entry deadlock in `noteBackendUnavailableForInstance`.

The root implementing pass then ran the final four lifecycle tests together:

```
$ go test ./internal/skeleton \
    -run '^(TestRegisterBackendFeedForInstance_SameInstanceReplacementRetiresOldGeneration|TestNoteBackendUnavailableForInstance_LegacyPIDOnlyMigrationDoesNotDeadlock|TestNoteBackendLostForFeed_RemovedOldFeedCannotDegradeRegisteredSuccessor|TestSubscribeSessionThread_ObsoleteMissingRolloutFeedStopsWhenReplaced)$' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  9.872s
```

## Independent final review

An independent, read-only post-implementation reviewer accepted the final lifecycle patch. The
reviewer reran the exact four tests above (`8.504s`) and the same selection under the race detector
(`11.358s`), with all four passing both times. The ordinary fresh-launch and stale pre-registration
controls also remained green:

```
$ go test ./internal/skeleton \
    -run '^(TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists|TestR7R4_StaleJoinRegistrationCannotGapOrDegradeReplacement)$' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  11.292s
```

The root finalization pass covered the broader affected daemon surface in normal and race modes:

```
$ go test ./internal/skeleton \
    -run 'Test(RejoinPersistedIdentity|RegisterBackendForInstance|RegisterBackendFeedForInstance|NoteBackendLostForFeed|NoteBackendUnavailableForInstance|SubscribeSessionThread|R7R4_StaleJoinRegistration|R7Fix_TheThreadJoin)' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  14.943s

$ go test -race ./internal/skeleton \
    -run 'Test(RejoinPersistedIdentity|RegisterBackendForInstance|RegisterBackendFeedForInstance|NoteBackendLostForFeed|NoteBackendUnavailableForInstance|SubscribeSessionThread|R7R4_StaleJoinRegistration|R7Fix_TheThreadJoin)' \
    -count=1
ok  github.com/Nathandela/swarm/internal/skeleton  16.477s

$ go test -race ./internal/appserver -count=1
ok  github.com/Nathandela/swarm/internal/appserver  3.278s
```

`git diff --check` was clean after those runs.

Required closure evidence:

- `[COMPLETE REVIEW — independent re-review accepted durable adoption, replacement-atomic
  registration, feed-generation fencing, cleanup, late-callback rejection, and the legacy
  migration lock correction; exact normal/race and fresh controls are recorded above.]`
- `[PENDING CI — pull-request run id and all required jobs green on the reviewed commit.]`
- `[PENDING ROLLOUT — attended daemon restart over a live multi-thread Codex session: roster
  preserved, no new backend_unavailable gap, composer restored, phone send reaches the same
  persisted conversation.]`
