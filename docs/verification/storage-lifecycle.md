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
