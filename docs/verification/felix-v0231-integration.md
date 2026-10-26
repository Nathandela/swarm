# Felix fix integration, v0.23.1

The owner authorized integration and patch publication on 2026-10-26 after
reviewing Felix Arbez-Gindre's open PRs against main `24173422` / v0.23.0.
The detailed execution plan is recorded in Beads `swarm-ssq`.

## Integrated changes

| Original PR | Accepted scope | Integration correction |
|---|---|---|
| [#65](https://github.com/Nathandela/swarm/pull/65), `49fd9431` | Accept `interaction=error` as a durable direct-input baseline | Original code and regression tests are unchanged |
| [#58](https://github.com/Nathandela/swarm/pull/58), `f0ba3f9c` | Negotiate `paste_input`; frame text only when DECSET 2004 is enabled; keep Enter outside the paste | Preserve current input embargo/OpenCode capabilities; strip markers in a single pass so removal cannot reconstruct a delimiter |
| [#64](https://github.com/Nathandela/swarm/pull/64), `e3320374` | Shared durable-line writes, short-write detection and failure latching until reopen | Automatic legacy salvage is omitted; migration retains strict decoding and identifies the segment on error |
| [#66](https://github.com/Nathandela/swarm/pull/66), `97ea89eb` | Default agent handoff/delegation to an isolated worktree and refuse temporary worktrees | Resolve destination ancestors at preflight and creation; reject dangling aliases; retain the source checkout's commit |

Original authorship and source commit references are retained in the integration
commits. This is an adaptation of the four contributions, not acceptance of the
original journal recovery algorithm.

## Regression evidence

The failed-turn input tests failed against unchanged main with
`invalid direct-input baseline "idle"/"error"` and passed with #65.

Before correction, the new paste regression failed for nested/reassembled start
and end markers. After correction, the shim receives exactly one outer paste
and the intended body, including its embedded newline. The input embargo test
also covers the new paste write path.

Before correction, handoff tests accepted both `.swarm` and `worktrees` aliases
into temporary storage. The final tests check both daemon preflight and the
production creation hook, no registered worktree or output files on refusal,
a dangling ancestor, a valid durable alias, and a source worktree with its own
commit and uncommitted edits. The last case proves that only committed source
work reaches the child. Resolving a missing descendant exposed an existing
trailing-slash/Lstat issue in the shared cleanup resolver; its fix has a direct
regression alongside the discussion deletion tests.

The retained journal tests inject partial writes, short writes, fsync failure
and reservation failure, and check publication, latching and restart cursor
custody. A nested record-shaped payload at torn EOF cannot manufacture a
control or cursor; the newline-terminated corruption is rejected without
changing its source file.

Build, vet, complete local lint and the focused race suites passed. Lint used
Go 1.26.0; the initial focused tests used that compiler, and supplementary broad
qualification uses the release compiler, Go 1.26.10.

## Local environment and publication gates

The initial full local run inherited umask `0002` and a temporary root under
the owner's home tree. This caused private-storage fixture failures, also
reproduced on unchanged main. With umask `0022` and a private root under `/tmp`,
the complete login race suite passed.

`TestTerminalRestoreRedrawsUnchangedBoard` fails on this VM on unchanged main,
including three repeated runs with Go 1.26.10. Beads `swarm-xmx` tracks that
local failure. Only the supplementary local full race invocation excludes
that exact test; release CI retains it without exclusions. External-service,
Android SDK and physical-device coverage are determined by the relevant
hosted gates, not inferred from local package success.

The main-required-ci ruleset `20874942` contained only the obsolete 14 required
branch checks. Its enforcement was disabled while preserving its conditions,
check definitions and bypass actors. This aligns main with the existing
release-only Actions policy. Release-tag CI, container gates, signing checks
and publication dependencies are unchanged.

Publication must run through `.github/workflows/release.yml`: its full Linux
race suite, macOS, runtime-service, Android artifact, container and packaging
gates precede signed publication. The final workflow and downloaded-artifact
verification will be recorded after completion.

## Runtime boundaries

Existing shims retain their negotiated capabilities. A daemon restart alone
does not add bracketed-paste support to an older shim; the session's shim must
be replaced through its normal lifecycle. The wire version does not change.

A new handoff worktree starts at the source checkout's HEAD. Commit what the
child needs before handing off. Explicit `swarm spawn --worktree=false`,
non-git sources and the TUI hands-off method retain their documented behavior.
The copied context document still lives under temporary storage.

No ambiguous journal record is automatically reconstructed. Legacy corruption
that strict migration rejects still needs an explicit, evidence-preserving
repair. This patch publishes binaries; no busy daemon restart, native-history
cleanup, Android app rollout or hosted-service deployment is authorized by
this qualification record.
