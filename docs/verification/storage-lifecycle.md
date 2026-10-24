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

Implementation tests, code review outcomes and final quality gates will be
recorded here as they execute. No production cleanup, release, installation or
live unit migration is claimed by plan approval.

## First implementation review

Astra independently returned GREEN for Linux updater cleanup and doctor storage
accounting, and reran their focused tests on native Darwin Go 1.26.9. These are
component results; Linux and integrated qualification remain outstanding.

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
