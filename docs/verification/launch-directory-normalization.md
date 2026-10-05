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
