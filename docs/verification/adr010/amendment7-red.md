# ADR-010 Amendment 7 — legacy codex resume ids and torn rollouts: failing-first evidence

**Slice**: a codex session resumed before v0.13.16 seeded ids regains the thread it continues
from its launch argv at reconcile (H1), and the codex recovery scan passes over a torn rollout
instead of refusing every candidate (H2). Tracks `swarm-man`. Normative:
[ADR-010](../../adr/ADR-010-inter-session-orchestration.md) Amendment 7, committed before the
code (`05ca751f`).

Branch `feat/legacy-codex-id`, linked worktree, every command with `GOFLAGS=-buildvcs=false` and
the six `SWARM_*` variables unset.

## 1. Measured basis (owner's machine, 2026-09-02)

- Session `mwgm7slyux2b4nqj` ("WIP prod telemetry"): `conversation_id` empty, launched as
  `codex resume 01a056e8-6352-7901-9290-e8a09b37dc2e` (its `shim-launch.json` argv), daemon log
  `never announced a thread within 45s`, then `backend has no rejoinable thread: this session's
  app-server holds 4 threads` on every daemon restart. Hands-off answered
  `codex conversation history is unsafe to inspect`.
- The resumed thread's rollout `~/.codex/sessions/2026/08/31/rollout-2026-08-31T08-21-05-01a056e8-…jsonl`
  carries the user's turns of 2026-09-01 at 13:32, 14:05, 15:07 and 16:21 UTC: a codex resume
  continues the thread it is given.
- The other loaded threads are codex 0.151.0 sub-agents (`source: {"subagent": {"other": "guardian"}}`
  and `thread_spawn`, `parent_thread_id` = the main thread).
- Two torn rollouts in `~/.codex/sessions/2026/09/01/`: `rollout-2026-09-01T14-50-06-01a05d72-…jsonl`
  (0 bytes) and `rollout-2026-09-01T14-51-29-01a05d74-…jsonl` (first `session_meta` cut at byte 12289
  by a raw newline, a second `session_meta` on line two). Two of 1891 rollouts on the machine.
- All five codex resumes of 2026-09-01 (07:28 to 09:02 UTC) predate v0.13.16 (tagged 12:32 UTC),
  the release that seeds a resumed session's id at launch.

## 2. RED (tests written, code untouched)

```
--- FAIL: TestResumeHistory_CodexSkipsTornRollouts (0.00s)
    --- FAIL: TestResumeHistory_CodexSkipsTornRollouts/zero-byte_rollout (0.00s)
        resume_history_torn_test.go:35: history result = {Outcome:4 ConversationID:""}, want {2 "01a00038-33ec-7643-a5fd-169228389460"}
    --- FAIL: TestResumeHistory_CodexSkipsTornRollouts/first_line_still_being_written (0.00s)
        resume_history_torn_test.go:35: history result = {Outcome:4 ConversationID:""}, want {2 "01a00038-33ec-7643-a5fd-169228389460"}
    --- FAIL: TestResumeHistory_CodexSkipsTornRollouts/first_record_torn_by_a_raw_newline (0.00s)
        resume_history_torn_test.go:35: history result = {Outcome:4 ConversationID:""}, want {2 "01a00038-33ec-7643-a5fd-169228389460"}
--- FAIL: TestLocateTranscript_CodexNamedFileWithoutARecordIsNotFound (0.00s)
    resume_history_torn_test.go:57: LocateTranscript = ("", 4), want ("", 1)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	0.022s
```

Outcome 4 is `resumeHistoryUnsafe`, 2 is `resumeHistoryFound`, 1 is `resumeHistoryNoMatch`. Line
numbers and subtest names are as of `ac7b1094`; the review round below moved them (the reconcile
row is now "terminal codex resume without an id" at line 79, the torn-test lines are 38 and 60).

```
--- FAIL: TestReconcile_BackfillsResumedCodexConversationID (1.01s)
    --- FAIL: TestReconcile_BackfillsResumedCodexConversationID/codex_resume_without_an_id (0.02s)
        reconcile_resumed_id_test.go:66: conversation id after reconcile = "", want "01a056e8-6352-7901-9290-e8a09b37dc2e"
FAIL
FAIL	github.com/Nathandela/swarm/internal/daemon	4.593s
```

The five negative cases of the reconcile test (captured id kept, fresh launch, claude, argv
without an id, launch record missing) passed before the change, as they must: they pin what the
backfill leaves alone.

## 3. GREEN

```
ok  	github.com/Nathandela/swarm/internal/daemon	38.630s        (whole package)
ok  	github.com/Nathandela/swarm/internal/daemon	2.977s         (-race, TestReconcile*)
ok  	github.com/Nathandela/swarm/internal/skeleton	22.031s      (ResumeHistory|LocateTranscript|HandsOff|Codex|Resume|Recover)
ok  	github.com/Nathandela/swarm/internal/skeleton	429.042s     (whole package, first full run of the slice)
```

Two kinds of failure seen on this machine during the slice are environmental, not this change:

- `cmd/swarm` `TestRunHook_*` and `internal/hookclient` `TestEnvHookSocket_UnsetMeansPostSmartNeverDials`
  fail when run from inside a swarm session with `SWARM_DAEMON_SOCK`, `SWARM_HOOK_CAPTURE`,
  `SWARM_HOOK_SEQ_FILE`, `SWARM_HOOK_TOKEN`, `SWARM_SESSION_ID` or `SWARM_SHIM_HOOK_SOCK` set;
  they pass with all six unset (`ok cmd/swarm 0.341s`, `ok internal/hookclient 0.006s`).
- `TestR7E2E_APhoneComposerSend…RealGateway`, `TestR7E2E_APhoneApprove…RealGateway` (and, per the
  docs review, `TestPhonesim_ObserveTerminalRecoversAfterKillSwitchToggle`, `TestPBSEC6_*`,
  `TestPBSEC7_*`, `TestPBE2E1_*`) fail with `machine authorize phone: relay: quota exceeded` when
  selected with `-run` minutes after passing inside the full run. They reproduce identically on
  main's code. Filed as a bd bug (R7 gateway e2e relay quota).

## 4. The one expectation this amendment changes

The first whole-package run of `internal/skeleton` after the code change failed exactly one
subtest: `TestResumeHistory_CodexRequiresCanonicalFilenameIdentityAndCompleteLine/missing newline
is incomplete`, which asserted that a codex rollout whose only line has no trailing newline makes
the scan `Unsafe`. H2 says such a line is a write in progress and not a record. The subtest is
renamed "missing newline is not yet a record" and asserts `NoMatch`. No other assertion changed:
the malformed-record table (duplicate keys, trailing JSON, wrong types, missing fields, an invalid
parent id) still refuses, because those records decode and are refused by the strict schema, and
claude's scan is untouched, including its own "missing newline is incomplete" case.

The expectation changed because the decision changed, and the ADR landed first (`05ca751f`).
Against the pre-amendment code (the tip's test file over `05ca751f`, ADR only) the renamed subtest
fails as:

```
--- FAIL: TestResumeHistory_CodexRequiresCanonicalFilenameIdentityAndCompleteLine/missing_newline_is_not_yet_a_record (0.00s)
    resume_history_resolver_test.go:409: history result = {Outcome:4 ConversationID:""}, want {1 ""}
```

## 5. Review round

Three reviewers (adversarial on H2, code on H1, docs and evidence), all completing. Taken:

- H2: a skipped partial line was read but never charged to `MaxTotalBytes`, so the bound on hostile
  input became `MaxOpenFiles x MaxRecordBytes`; the bytes are now charged before skipping.
- H2: a torn NAMED file let the hands-off locator fall through to the next day, where a same-id
  rollout would be reported Found; a name hit now ends the search (`locateCodexInDay` reports
  whether an entry named the id).
- H2: the newly skipped shapes "blank first line" and "control character inside a string" are
  pinned, and `null` / `[]` first records are pinned as still refused.
- H1: the launch-record reader is shared with the hook-token re-read (`readShimLaunch`); the live
  shim, lost and exit side-file reconcile branches are covered; comments corrected (the positional
  argv guarantee, the terminal branch's "no write here", ordering against `OnSessionStart`).
- Docs: Amendment 5 F1 and its consequences no longer claim the scan fails closed on torn rollouts
  or that every no-id source waits for `swarm-man`; the three superseded rows of
  `amendment5-red.md` carry an inline note; H1 names `swarm-9jo` and states the operative reason
  for excluding claude; H2's gloss covers non-JSON first lines.

Declined: a wording tweak of the hands-off "was not found" message for an empty named rollout
(the same message claude already gives for the same shape, and "no transcript record" is what it
means); splitting the `release: prepare v0.13.24` commit off the branch (every release since
v0.13.21 has shipped its prep commit on the feature branch that carried it).

RED for the two behaviour changes of the review round, the tip's tests over `ac7b1094`:

```
--- FAIL: TestLocateTranscript_CodexNameHitEndsTheSearch (0.01s)
    resume_history_torn_test.go:82: LocateTranscript = ("<home>/.codex/sessions/2026/09/02/rollout-2026-09-02T09-02-54-01a05c35-0630-7000-8000-000000000000.jsonl", 2), want ("", 1)
--- FAIL: TestResumeHistory_CodexPartialLineStillCountsAgainstTheBudget (0.00s)
    resume_history_torn_test.go:100: history result = {Outcome:2 ConversationID:"01a00038-33ec-7643-a5fd-169228389460"}, want {4 ""}
```

The other review-round tests are pins, not reds: the "lost shim", "exit side-file" and live-shim
reconcile cases and the two extra torn shapes pass on `ac7b1094` as well (`ok internal/daemon 4.161s`).

## 6. Gates

Recorded from the final run over the reviewed tip, below.

## 7. Rollout

Recorded after the release.

