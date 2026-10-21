# Cleanup follow-up — 2026-10-21

This continues the [repository tightening audit](../repository-tightening-2026-10-21/README.md).
Luna agents authored bounded changes and reviewed other agents' work; the root
integrated, checked and pushed the changes. No dependency or release-trigger
changes were made. The earlier dated audit and debt ledger remain unchanged.

## Implemented scope

| Change | Evidence and boundary |
|---|---|
| Current documentation | `79b1131a`: reconciled installation with the three-archive release matrix, companion gateway, source-build update limitations, current architecture, historical specification scopes and dated deployment evidence. Reviewed against release configuration and source; relative links and anchors checked. EARS, global gates and proposed ADR statuses were preserved. |
| macOS account checks | `aa3c7da7`: discover system policy through `/private/etc` on Darwin, preserving refusal of arbitrary symlink components and uncharacterized managed policy. Shared process identity checks distinguish proved absence from uncertain Darwin kernel results. Linux process identity semantics remain unchanged. |
| Platform test contracts | `aac8a728`: keep source-origin and unknown-version safety assertions portable; move only successful Linux credential-erasure/managed-child assertions to Linux files. Darwin checks the real unsupported-containment refusal and absence of the child marker. Remove the unused `Running` export; retain exactly two non-Linux reachability allowances for production functions whose callers are Linux-only, including the bidirectional stale-allowance check. |
| Audit retention | `f7bc259d`: cap audit-only remote activity at 1 MiB, atomically retaining up to 512 KiB of complete recent records plus the new record on overflow. Refuse symlinks, hardlinks and nonregular files. Oversized individual records are omitted. Operation receipts and replay/delivery custody are separate. [Policy](../../adr/ADR-033-live-only-supervisor-storage.md). |
| Android interaction reads | `2de88529`: replace the single clipped-detail request slot with one operation-ID ledger for overlapping detail/history reads. Matching unresolved outcomes remain pending; history silence, result ordering and detail-specific refusal handling stay intact. |
| Quiet-mailbox reconnect and Reload | `178dc9ec`: keep the backoff streak while an authenticated stale mailbox head blocks progress; reset after usable recovery. An explicit Reload queues across connection gaps and wakes one retry. The sole reader still diagnoses/discards using the durable, incarnation-bound recovery intent. Passive sync never authorizes discard. |
| Backend fixture lifetime | `3ffa8531`: discard six unused announce-PID outputs through `os.DevNull` and register temporary working directories before daemon cleanup. This removes a reproduced late marker-write/temporary-directory deletion race without changing backend assertions. |

## Failing-first and review evidence

The original audit recorder exceeded the cap and wrote through a symlink in the
new regressions before implementation. Final focused race tests also cover
concurrent writes across compaction, incomplete tails, oversized records,
permissions and unchanged adjacent operation custody.

The Android source gate failed before ledger wiring; the new JVM test exercises
overlapping detail/history requests, duplicate IDs, mismatched outcomes and
unresolved answers. Debug and release each passed **1,728 tests in 217 suites**,
with zero failures, errors or skips; Android lint passed after rebuilding both
AAR ABIs. Later mobile changes require the final artifact checks recorded below.

Independent reviews checked documentation authority, operation binding, policy
discovery, process identity and custody, and audit replacement safety. Review
feedback narrowed the queue comment to unbounded `Offer` consumers and replaced
the audit-size addition with a subtractive check to avoid integer overflow.

The phone regression failed against the original production source through a Go
overlay and a fresh workerd: retry attempt 1 repeated where attempt 2 was
required. The first full relay run then exposed explicit Reload missing the
short-lived stream during backoff. The final regression pauses the actual retry
loop, queues Reload without a stream or later machine traffic, verifies the
checkpoint does not cross the stale head before that action, and verifies the
discard-token roster command and reset retry streak. Review additionally caught
the old-stream-pointer teardown window; every queued action now signals the
session, and only an unclaimed live request can interrupt its retry timer.
Canceled or already handled signals do not bypass ordinary backoff. A failed
expedited dial resumes normal retry timing within the existing 15-second request
deadline; network unavailability can still return an honest offline error.

The final fresh-worker relay runner exited **0** with a 600-second outer budget.
It covers admission, protocol and cost, discard recovery and expiry, native
pairing, operator recovery, mobile pairing, phone security/E2E, doctor, alarm
cost and rate limits. The optional S19 input-latency benchmark was not enabled.
The final mobile package passed full normal and race tests using native gobind.

The final Darwin daemon package passed its full race suite. Actual Linux ARM64
container race runs passed account configuration, accounts, account custody,
native executable retention and audit compaction; a second run passed the moved
daemon, skeleton and upgrade cases plus the Linux reachability guard. No test
assertions were removed to turn a platform refusal into success. Independent
review caught a portable unknown-version hold inside a mixed test; that hold was
split back into the portable file and rerun before the final source freeze.

Both target configurations of the pinned Go linter passed with zero issues.
The final AAR rebuild was followed by an uncached rerun of all Android tasks:
debug and release each passed 1,728 tests, all result XML files postdate the
rebuilt AAR, and Android lint passed. The artifact digest and final Go summaries
are retained alongside this record.
See the [Go gate summary](go-summary.json) and
[Android artifact/test summary](android-summary.json).

The fresh full normal run exposed a separate daemon fixture `RemoveAll` failure.
Moving cleanup registration alone passed repetitions but did not prove the
separate PTY child had stopped. Review caught that limit. None of the six tests
uses the announce PID, so the final fix removes that filesystem side effect.
All nine backend tests then passed ten repetitions each in **both normal and
race modes**, and the final full daemon normal suite passed in 96.0 seconds.

Go checks used native ARM64 Go 1.26.9, with inherited `SWARM_*` variables removed,
and native gobind/gomobile. The pinned linter ran with Go 1.26.0 for Darwin and
Linux. Linux runtime checks used the available Go 1.26.0 ARM64 container, a
read-only source mount and private build cache. Android used the checked-in ABI
and SDK/NDK pins with native JDK 21. No system-wide tool installation was changed.

## Full sweep results and final reruns

| Check | Recorded result |
|---|---|
| Initial normal sweep | 74 packages passed, 5 failed, 7 had no tests. Failures identified platform contracts, reachability and Intel gobind. |
| Complete race sweep | 77 passed, 2 failed, 7 had no tests; zero data-race reports. The two failures were the old managed-launch fixture and Intel gobind. Final full daemon and mobile race reruns passed. |
| Fresh complete normal sweep | 78 passed, 1 failed, 7 had no tests. The only failure was the newly reproduced backend PID-marker cleanup race; the corrected full daemon normal rerun passed. Skeleton completed in 588.469 seconds. |
| Final affected-package checks | Full mobile normal/race, full daemon normal/race, focused account/process race, restored portable hold/refusal tests, actual moved Linux cases and 90 normal/90 race backend fixture cases passed. |
| Final build/vet and lint | Build and vet passed; pinned Darwin and Linux lint each reported zero issues. |

All **79 testable packages** passed normal and race coverage across the complete
sweeps and the corrected full-package reruns. The JSON preserves every original
nonzero sweep exit and its exact failing test names; those exits are not relabeled
as pristine whole-repository green commands. No reported failure remains
unresolved in this batch. The full Linux release matrix, optional latency
benchmark, physical handset and hosted rollout were not run.

## Operational limits

This is source and local integration verification. No installed supervisor was
restarted, no release was deployed, and no live activity log was compacted by
this cleanup session. Audit retention reduces forensic history when an updated
daemon next writes activity. It does not authorize deleting provider history or
unexpired safety state.

Physical handset, hosted push and production rollout gates remain separate.
The older quiet-mailbox issue `agents-tracker-y86g` asks for automatic recovery;
explicit owner-authorized recovery and reconnect pacing do not close that
separate product-policy question.
