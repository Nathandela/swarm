# Launch directory separator normalization

**Scope:** source-level behavior and focused regression evidence for launch paths
with trailing directory separators.

`coreAPI.Launch` removes only trailing path separators from the requested
working directory before history lookup, worktree handling, configuration,
projection, and persistence. The root path remains `/` when trimming separators
would otherwise produce an empty string. Interior path components are untouched,
so the existing `accountconfig.safePath` checks for parent traversal and
symlinks continue to apply.

The change is exercised by
`internal/skeleton/launch_directory_test.go`. Its integration path drives the
public TUI directory autocomplete through the owner protocol and into synthetic
Claude and Codex executables, with worktree mode both enabled and disabled. It
checks that the submitted path with a trailing separator resolves to the same
actual provider working directory, persisted metadata, shim launch
configuration, and session projection. Control cases cover `/`, repeated root
separators, final and ancestor symlinks, parent traversal, and traversal through
a symlink; unsafe cases must be rejected before the synthetic provider starts.

The focused regression first failed against the prior behavior, then passed in
normal and race modes after the source change. Independent Astra review found
no blocker within the directory-fix scope; its separate focused race run passed
in 11.352 seconds. The implementation and regression SHA-256 values reviewed
were `583d99badc4f541677cdc5ce7a82568e223c96dd0c1dbdb84ccb75ae16c672f8`
and `9e641c7b8081ae7c15798aa381bc7d7c425fc46419726f76a1ee4909d5ded4d9`.

Local repository gates passed on 2026-10-05: build (5.43 seconds), vet (5.29),
lint (54.16), strict documentation manifest (0.04), uncached normal tests
(676.78), and uncached race tests (818.50). The later Android release identity
update to 0.15.5/code 52 passed the full Android gate package in 10.071 seconds
and independent metadata review. These checks use
synthetic providers and establish no authenticated account rotation or native
conversation continuation. Release and installed acceptance require separate
evidence.

Ordinary project configuration support and legacy migration are being
investigated separately. This patch does not implement either.

## v0.15.5 release and installed acceptance

[PR #46](https://github.com/Nathandela/swarm/pull/46) merged as
`c55eba4dca8ce953a1b632ced65d36680d0c1bc7`, with the same reviewed tree
`26642da0461cac8344fc5c700cdcb34c7af88cdc`. All 30 PR checks passed. The
initial branch fuzz job ended with `context deadline exceeded` at its 30-second
cutoff after 2,433,313 cases, with no failing assertion or saved input. The
independent same-commit CI run passed, including fuzz; a local unchanged
30-second run also passed. The signature matches the documented
[Go fuzz-runner deadline race](https://github.com/golang/go/issues/75804), but
the exact failing CI toolchain implementation was not inspected. One unchanged
failed-job rerun passed on attempt 2. No assertions or gates were bypassed;
`swarm-wv7` tracks the toolchain follow-up and the original failure is retained.

The exact tag **v0.15.5** points to the merge commit.
[Release workflow](https://github.com/Nathandela/swarm/actions/runs/37300645467)
attempt 1 passed all 17 jobs.
[The release](https://github.com/Nathandela/swarm/releases/tag/v0.15.5) was
published on 2026-10-05 at 11:22:25 UTC with 10 assets. Signed staging verified
both binaries' clean-source build metadata and embedded source/version, plus
the compatibility card. Account axes remain schema/jobs/inventory 1,
recovery/worker/shim/config 2; protocol and shim wire remain 1, discussion
schema remains 2. Android identity is 0.15.5/code 52; no Play publication is
claimed.

| Installed artifact | SHA-256 |
|---|---|
| swarm | `6632ddfac4af473d6c8c3000aecd02ccfe8e46738d847bbb2d8e3d165a45c7d3` |
| swarm-remote | `d6defccaa17b30c9f4e616090ae56a492edeb52c77210c251d1a4c8dc9ebd20a` |

Activation installed the verified pair and deferred supervisor handoff while a
discussion was working. Explicit supervisor restart with the saved environment
passed, followed by successful convergence. The responding daemon reports
0.15.5, its executable hash matches the installed binary, every doctor check
passes, and no pending convergence marker remains. All 158 saved session
records and the exact PID/start-tick identities of all eight live shims were
retained. Discussion identity/bindings, selected account identity/lifecycle and
generation metadata, rotation flags, and saved daemon environment were
preserved. This is selected metadata continuity, not credential or quota byte
identity or native conversation acceptance.

An overlay-only acceptance probe drove the actual installed binary's isolated
owner daemon through public `protocol.Launch`. Synthetic accounts, fake pinned
provider executables, temporary HOME/state, and network namespaces kept the
probe separate from production. On installed 0.15.4, canonical controls passed
and trailing separators reproduced `unsafe-project-directory` for both
providers. The same final probe passed on official installed 0.15.5 in 41.958
seconds: canonical and trailing paths agreed across persisted source metadata,
projection, shim configuration, bound profile, and actual provider cwd.
Binary hash/version stayed unchanged during each final run. Independent review
approved the bounded probe and its namespace/PID/start identity checks.

The first old-binary probe's eight-second Codex observation ended before its
normal 20-second backend fallback; that incomplete result is retained. Only
the temporary observation deadline was adjusted to 30 seconds. Subsequent
temporary safety instrumentation made cleanup fail closed on missing proof,
pinned namespace identity, and restricted process discovery to the isolated
namespace. Final baseline and installed runs report no scan errors or live
descendants, and their scratch directories are absent.

The fake Codex backend deliberately exits, so this confirms the launch boundary
and CLI fallback path, not native backend readiness. No real account sign-in,
model turn, quota exhaustion, rotation, or native conversation continuation was
performed. Project configuration/hooks, existing-discussion adoption, and
authenticated two-account acceptance remain separate tracked work.
