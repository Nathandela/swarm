# Storage lifecycle implementation evidence

2026-10-24 · branch `feat/bounded-supervisor-storage` · `agents-tracker-5jmx`

The [implementation contract](../specifications/storage-lifecycle-plan.md) was
independently reviewed by Sol and Astra. Both returned GREEN for SHA-256
`dcf4b61eed028d007c7d98db90ce3830fb458e036a4cb5e046aa24f9dd29d3bd` before production
implementation began. This approves the plan, not the implementation or rollout.

The first review rejected the initial warning calculation excluding managed
native data, unsafe concurrent ownership of deduplicated exports, source deletion
invalidating successor inputs, and an unspecified hook-writer retirement proof.
The approved revision includes total-state warning semantics, serialized handoff
publication through launch outcome, source-independent new exports plus legacy
reference guards, and a mandatory incarnation-bound producer lifetime lock with
joined shutdown before exit publication and conservative retry.

The owner selected warning-only aggregate storage policy. Existing correctness
limits stay intact; no new storage-threshold admission refusal is authorized.
Native histories stay in place. Account/native-home and execution/continuation
work remains exclusively on `feat/native-login-handover`.

Implementation tests, review corrections and qualification results are recorded
below. No production cleanup, release, installation or live unit migration was
performed by this task.

## First implementation review

Astra independently returned GREEN for Linux updater cleanup and doctor storage
accounting, and reran their focused tests on native Darwin Go 1.26.9. These were
component results; final platform qualification is recorded separately below.

The updater's failing-first executable fixture reproduced leaked payloads on
successful fetch and download/checksum/install failures. The fix runs `fetch`
in a subshell before the final exec. The passing fixture verifies cleanup,
installed version, checksum/extract/install ordering, unconditional convergence
and exact restart exit status. No real download, sudo or installed binary is used.

Doctor's new tests initially failed to compile before the scanner existed.
Focused `TestStorage|TestDoctor` tests then passed. A separate Go overlay removes
only the new finding from the existing doctor flow to verify that its integration
assertion fails without changing shared source. Fixtures cover managed data in
the warning total, hardlink deduplication, directory/link allocation, sparse-file
logical size, inaccessible-directory partial coverage, traversal limits, root
symlink exclusion, low-space warnings and an untouched absent state directory.
Warnings preserve the existing exit-zero contract and never affect admission.

Representative commands: `go test ./cmd/swarm -run 'TestStorage|TestDoctor'
-count=1` and `go test ./internal/remote/supervise -run
TestLinuxUpgradeCleansPayloadBeforeRestart -count=1`. Existing shared caches were
reused. The implementation also passed a Linux cross-build at this checkpoint;
that is not evidence of native Linux execution.

## Diagnostic logs and test supervision

Astra returned GREEN for the diagnostic log change after connected-code review
and independent helper/template tests. Failing-first tests showed an oversize log
remaining above 8 MiB, unsafe aliases being accepted, and previous updater output
surviving the next run. Focused normal and race tests now pass, including a real
quiet shim maintaining the shared inode after daemon shutdown. An injected
daemon startup reconciliation failure also proved and then fenced a maintainer
leak on failed Open.

The Mac updater was executed through an actual uniquely named temporary launchd
job with fake brew/Swarm executables. It retained the current invocation, captured
both streams, preserved final status 17 and unloaded the exact job during cleanup.
The deployed unit was untouched. The template uses existing macOS `/usr/bin/perl`
and core Fcntl for safe descriptor setup; installation prerequisites and unit
reinstallation are documented. Same-inode retention has burst overshoot, and one
updater invocation remains unbounded. These are explicit retention limits.

The test supervisor's normal, panic, timeout and parent-TERM fixtures passed,
including registered detached/TERM-ignoring executables, unrelated-process
preservation, identity mismatch, retained cleanup failures, coverage and race
runs. Luna migrated short temporary roots and registered custom test binaries in
the allowed suites. Failing-first adversarial tests also demonstrated manifest
symlink/hardlink acceptance, a FIFO block, and an outside-file registration write.
Both manifest reads and writes now validate a bounded regular private descriptor
opened no-follow/nonblocking. Astra's second review returned GREEN after this
hardening and the root-user permission-fixture correction.

Coverage is intentionally the copied/registered executable set and migrated
scratch. Arbitrary detached system executables need fixture-specific custody;
parent SIGKILL/host crash remains outside the supervisor's cleanup guarantee.
The reserved login/identity/auth-related skeleton fixtures and handoff directory
fixture retain their explicit temporary roots pending owner coordination. No old
scratch, provider history or shared compiler cache was deleted.

## Hook payload retirement

The baseline real-launch regression retained 170 payload bytes after the session
ended. New cooperating shims hold a producer lifetime lock and an incarnation,
nonce and physical-file capability. Hook handlers join and the spool closes before
terminal publication. Only a durably advanced, lineage-matching cursor receives
a retirement certificate; legacy or uncertain evidence remains retained.

Focused normal/race tests cover actual full/partial end-session retirement,
sequence continuity, late accepted POST ordering, live/unknown/replaced producer
refusals, gaps and torn tails, cursor/certificate/compaction failures, startup and
busy-lock retries without authentication resurrection, and unsafe/oversized
sidecar rejection. One diagnostic reports exhausted retry attempts.

Astra's deep review found that a stopped retirement retry could recreate an
attempt after concurrent deletion. Seven regression fixtures reproduced creation
of missing directories, locks, spool or incarnation. The stopped path now uses
strictly existing-only opens throughout; normal producer initialization remains
unchanged. Astra re-reviewed GREEN. Post-fix targeted race tests passed for shim
(15.080 seconds) and skeleton (23.725 seconds), including the deletion seams.
Native Linux and final integrated qualification remain separate gates.

## Handoff inputs and review iterations

New OpenCode inputs live in a private state-level immutable export directory.
Identical canonical history/identity reuses an existing input; a definite failed
launch removes only an input it newly owns. Successful and uncertain inputs are
retained. Legacy source deletion checks actual retained consumers before each
destructive member operation, including aliases and partial family failures.

Astra rejected three successive concrete problems before returning final GREEN:
proposed deletion membership did not prove consumers were gone; a crash between
canonical link and staging unlink poisoned future reuse; and recovered/reused
files did not re-run all file/directory durability barriers. Each received a
failing regression before its fix. Real subprocess exits exercise publication
boundaries, and injected failures exercise unlink and sync retries. The final
focused handoff/deletion race suite passed in 28.930 seconds.

Complete validated staging/canonical pairs recover under the publication fence.
Invalid, partial or foreign-linked staging is preserved and refused, with an
owned-path/cause diagnostic and no conversation body. Such unproven state may
need manual inspection; universal automatic crash recovery is not claimed.
Similarly, lost producer proof after a safe spool replacement can preserve
payload permanently rather than authorize later disposal. These limits favor
retaining uncertain data over unsafe cleanup.

## Native Linux first qualification

The first reviewed snapshot was copied to one owned temporary VM tree, with
existing shared Go/module caches. Native Go 1.27.3 passed doctor and Linux updater
checks but exposed two portability issues: same-user nondumpable sshd metadata
prevented the test process scan from reaching owned children, and umask 0002 made
success fixtures group writable where the diagnostic log contract requires a
private parent. The fixtures now explicitly establish mode 0700, with production
policy unchanged; those tests pass under umask 0002.

The test supervisor now excludes only executable metadata assigned to foreign
kernel custody on Linux; it does not blanket-ignore permission errors. Other
observation/kill failures remain visible, after positively owned children are
stopped. A real nonroot Linux PR_SET_DUMPABLE fixture reproduced the failure and
then passed nested normal/panic/timeout cleanup while the unrelated opaque
process survived. Injected own-UID inspection and known-child kill errors remain
failures. Astra reviewed the correction GREEN. The opaque fixture has explicit
process-handle cleanup and a parent-death signal; generic sweeping makes no
claim over privileged/nondumpable fixtures. The implementation contract was
clarified to state this supported boundary and conservative lost-proof retention.
This component checkpoint did not close whole-snapshot qualification; final
results are recorded separately below.

## Controlled allocated-byte workload

Native Darwin arm64, Go 1.26.9, source snapshot `73523a79`. A temporary test-only
Go overlay reused the existing fake daemon/shim/OpenCode fixtures; production
source and installed services were unchanged. Astra reviewed the harness and
measurements GREEN. Three cycles reused each declared state root or log inode;
three panic and three timeout runs each received a unique supervised scratch root.

| Owned data | Observed allocated peak | After the relevant boundary |
|---|---:|---:|
| Each acknowledged hook payload | 90,112 bytes | 0 bytes for every ended attempt |
| Complete hook-workload state after three attempts | 258,048 bytes | 167,936 bytes, including retained metadata |
| Immutable input from three identical successful handoffs | 4,096 bytes | One unchanged 4,096-byte file (995 logical bytes) |
| Complete handoff-workload state after three successors | 253,952 bytes | 253,952 bytes, including retained successors |
| Each actual nonzero 9 MiB log burst | 9,437,184 bytes | 0 bytes on the same linked inode |
| Each copied panic/timeout scratch root | 3,416,064 bytes at child readiness | Root absent, 0 bytes, owned detached child stopped |

Allocation is kernel `st_blocks * 512`, with inode deduplication and no symlink
traversal. State/log measurements use nominal 10 ms polling plus explicit phase
samples; scratch uses readiness and completed-cleanup samples. These are observed,
non-atomic maxima and post-boundary snapshots, not device-exclusive physical
extents, universal peaks or a total-state ceiling. Log reclamation uses the
production five-second interval and actual writes, not sparse-file truncation.
The payload encoded on disk is 86,196 logical bytes, larger than its input body.
Retained metadata grows as expected; successful handoff inputs are not expired.
The measured Darwin allocation trajectory is not extrapolated to Linux.

The complete fixture source, command, raw output and hashes accompany this record
in [the workload artifact](storage-workload-2026-10-24.txt). The original temporary
instrumentation is evidence tooling, not a new product API or permanent test mode.

## Final custody challenge and fixture corrections

Root review challenged whether a durable fold cursor actually proved that critical
hook admission had succeeded. Astra confirmed a real gap: an authenticated
managed-Claude SessionStart could fail writing its ready evidence, yet the old
shared drain counted that persistence failure as a skipped record and advanced
both cursor and the new certificate. Stopped retirement could then remove the raw
accepted callback. The existing live DRAIN path could also compact past that
failure; certificate suppression alone would not solve the problem.

A deterministic negative-control overlay on `4cbeb192` blocked only
`login-native-ready.json` with a directory while leaving cursor storage writable.
Both live and stopped cases advanced past the failed record and a later record,
removed their payloads and could not retry after restoring the target. This
superseded the earlier source GREEN for that case. The correction in `73523a79`
classifies only proven decode/authentication/duplicate/typed-engine rejections as
terminal. Unknown ingestion errors return before numeric cursor persistence, so
later records cannot cross the unresolved failure. The underlying errors remain
available through wrapping. No login, native-home, credential, persistence schema
or execution/continuation implementation changed.

The regression now preserves the prior successful cursor/certificate, retains
failed and later records through live/stopped folding and fresh consumers, then
retries in order and retires after storage recovery. Eight terminal-rejection
controls remain disposable. Focused normal and race checks passed; Astra's
connected-code review returned GREEN. This is an injected filesystem write
failure, not a physical full-disk experiment. The aggregate policy remains
warning-only; persistent ingestion failures intentionally retain their payload.

Final Mac integration also exposed six Unix-domain-socket fixtures whose long
`t.TempDir` names exceeded the platform path limit after canonicalizing supervised
scratch. The same six tests failed before short socket-root creation and passed
afterward. Their new roots stay beneath supervised TMPDIR with cleanup; no
private-path guard or production socket behavior was weakened. An earlier reaper
fixture had similarly used its own mocked identity reader during cleanup; the
real kernel identity and bounded wait now keep that cleanup independent of the
fault under test. Earlier failing broad runs are not qualifying results.

## Final fixture qualification

The full normal runs of production snapshot `73523a79` completed. Native Darwin
had one remaining socket fixture failure: the maximum-length random suffix could
push `TestHookDrainer_GapBoundaryPrecedesRetainedAndFutureEvents` to the Unix
socket path limit. Native Linux had one unrelated existing readiness race in
`TestIntegration_ChunkedOversizedSnapshot`: the fixture treated a snapshot larger
than one frame as fully painted, then compared it with a later still-growing
snapshot (1,066,620 versus 1,088,697 bytes). All other executed normal tests passed.

A test-only overlay delayed the last painted row for five seconds. The old
size-only readiness check accepted an incomplete 1,088,682-byte snapshot with no
completion marker; the new condition waited and passed under the identical
delay. The final fixture emits one title marker through fakeagent `ask` after all
grid bytes/newlines, then blocks without further output. Readiness requires both
an oversized snapshot and that marker; decode failures are immediate failures.
The original attach-time, dimensions and byte-identical reattach checks remain.
All remaining socket-owning roots in the affected hook-gap family now use the
short supervised helper. A residual connected-helper audit found no further
concrete socket-root path-limit case in the inspected suites.

Astra approved the exact candidate and this qualification method. Commit
`14e38fa9` changes only the two test files above relative to `73523a79`; production
code is identical. Normal qualification therefore combines the completed full
normal runs with the complete protocol-package rerun and all 15 tests from the
affected hook-gap/conversation-scan fixture files on both hosts. Original failing
commands remain failures in this record; they are not relabeled successful.
The final full-repository race suites are separate gates on `14e38fa9`.

The earlier Linux archive-only staging failure was also kept distinct: seven
Android repository-hygiene checks needed `git ls-files`. A temporary local index
and synthetic commit were built from the exact archived tracked-file list; the
Git tree matched the source snapshot before rerunning the complete Android gate.
No missing-repository silent skip was found elsewhere. Later `73523a79` and
`14e38fa9` native runs use verified complete Git trees; no original Git history,
provider data or compiler caches were copied.

## Final qualification result

**PASS — source ready for integration; not deployed.** The final production
revision is `73523a79a7b8c1b5125a993fb874c6c92dd91ced`. Fixture-only revision
`14e38fa952f3f5f96ead579fcdb3fb184dcb8303` has Git tree
`944347638c948289cd4a9e23f7947343dd2ac84f`, verified identically in the isolated VM
checkout. Subsequent changes to this record and its artifacts are documentation
only. Root performed connected-code review; independent Sol and Astra source
reviews returned GREEN after the critical-ingestion correction. Astra also
reviewed the workload and the fixture-only qualification method. After the final
runs, Sol and Astra independently checked the raw gate records and hashes against
the compact artifact and both returned final source-and-qualification GREEN.

| Gate | Darwin arm64, Go 1.26.9 | Linux amd64, Go 1.27.3 |
|---|---|---|
| Whole production build and vet (`73523a79`) | PASS | PASS |
| Final affected-fixture vet (`14e38fa9`) | PASS | PASS |
| Full normal coverage, with the explicitly recorded fixture reruns | PASS | PASS |
| Full repository race (`14e38fa9`) | PASS, 78 packages, 1,190.93 s | PASS, 77 packages, 912.34 s |
| Corrected critical-ingestion regression and eight terminal controls under race | PASS | PASS |

Full golangci-lint 2.12.2 checks passed for both Darwin and the Linux target on
`14e38fa9`, with uncapped issue reporting; Linux-target lint ran on the Mac with
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`. No linter or gate was disabled. The full
normal and race commands were `go test [-race] -json -p 2 ./... -count=1
-timeout=30m`. Fixture follow-ups ran the entire protocol package and all 15 tests
from `hookgap_recovery_test.go` and `convscan_test.go`. Builds/tests used existing
shared caches, `GOMAXPROCS=2`, `GOTOOLCHAIN=local`, canonical temporary roots and
removed inherited hook/session environment variables. Native Linux used umask
0022; the earlier explicit diagnostic fixtures also passed under umask 0002.

The [compact quality-gate artifact](storage-quality-gates-2026-10-24.json) preserves
exact stage commands/statuses, source identities, original normal failures,
corrected results, package outcomes, test counts, declared skips and raw-log
hashes. It does not turn skipped provider/device opt-ins into executions or turn
the original failing full-normal commands into successes. The full final race
commands both exited zero, with no failed test actions or race-warning output.

No production state, native history, shared cache or installed service was purged.
Only exact task-owned temporary qualification material is cleaned after its
process/file-reference checks; three retained initial Linux RED fixture roots
accounted for 14,823,424 observed allocated bytes and were removed after such
checks. The feature worktree and compact committed evidence remain reviewable.
Integration with the account-rotator branch and actual release/host/unit rollout
are separate work in `agents-tracker-ih2v`.

Remaining product limits are deliberate: aggregate thresholds warn only through
on-demand doctor diagnosis; metadata, native histories and successful/uncertain
handoff inputs can grow. Diagnostic retention is best effort, old binaries retain
their old behavior, and one updater invocation has no strict size ceiling. Lost
proof or unproven partial staging preserves data rather than manufacturing cleanup
authority. Test cleanup covers copied/registered executables and migrated scratch;
arbitrary/opaque helpers need explicit custody, and parent SIGKILL/host crash is
outside that guarantee. These are not fixed-total-footprint or universal-cleanup
claims.
