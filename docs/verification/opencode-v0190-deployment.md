# OpenCode rollout — Swarm v0.19.0

The owner explicitly requested deployment on 2026-10-19 after the outstanding
qualification limits were disclosed. [Swarm v0.19.0](https://github.com/Nathandela/swarm/releases/tag/v0.19.0)
is published from `9df173d145c02523ca753284c2699bbfe744fed6` and installed on the
owner's Mac and existing `tf_nathan` Linux AMD64 VM. Production implementation
is frozen at `d86cb92d05085826a1facca2759b0e7e6b3ae183`; the release commit adds
verification records. This rollout does not close the full maturity plan.

## Installed runtime and continuity

| Check | Mac | Linux AMD64 VM |
| --- | --- | --- |
| Installed Swarm / daemon | 0.19.0 / 0.19.0 | 0.19.0 / 0.19.0 |
| Distribution | Homebrew cask, installed receipt 0.19.0, not outdated | Standard signed stage/activate installer |
| Loaded daemon executable | Matches installed cask binary | Matches installed executable inode |
| Existing live native workers preserved | 2, unchanged process/start identities | 16, unchanged process/start identities |
| Doctor | No failed checks; auto-update warning | No failed checks; auto-update warning |
| OpenCode | Existing 1.19.3 retained | Verified official 1.19.3 installed in the owner's existing executable path |
| Remote gateway | Previously inactive; remains inactive | Previously inactive; remains inactive |

Only the supervisor daemons were restarted. The normal unattended restart first
deferred for active work; explicit daemon-only restarts used the saved owner
environments. The Mac environment equality check passed. Native discussions,
profiles and provider credentials were preserved. The VM's exact-version pending
convergence marker was cleared under the normal upgrade lock only after the
loaded daemon was verified; the previously inactive gateway was preserved.

Reopen an existing Swarm dashboard to load the new launch form. OpenCode now uses
the shared editable model selector, including `opencode/deepseek-v4.1-flash-free`
and `opencode/glm-5.3-flash-free`. An untouched model field keeps the native project
default. Stable OpenCode >=1.19.3 is admitted; newer versions still require native
binding/readiness checks and have not received this live qualification.

## Release gates and retries

The [release workflow](https://github.com/Nathandela/swarm/actions/runs/39314556981)
completed successfully with all 17 jobs, including full Linux race tests,
lint, cross-platform builds, bounded fuzzing, remote integration, fresh Android
AAR/Gradle gates, container hardening and signed GoReleaser/Homebrew publication.
Release-tag-only CI policy was retained. Android build verification is not an
Android app publication; no Play track or installed phone app was changed.

The successful run was attempt 3 on the same immutable tag:

1. The first Linux run failed at the appearance wait in
   `TestReconnect_HydratedBannerExpires`. Its test/helper and relevant production
   code are unchanged from v0.18.2. An incremental cursor redraw retained `co`
   and emitted `dex needs input (question)`; the helper strips control sequences
   without replaying the terminal, so the raw substring assertion missed the
   visible banner. Independent review confirmed this explanation. Twenty local
   focused race repetitions passed. The assertion remains tracked in
   `agents-tracker-vyhy.13`.
2. The next Linux run passed the reconnect/TUI and other application packages,
   but gomobile's dependency verification hit an HTTP/2 `INTERNAL_ERROR` from
   `sum.golang.org` while checking `go-spiffe v2.7.0`. The separate Android gate
   was already green; the checksum endpoint subsequently returned HTTP 200.
3. The final Linux rerun passed, allowing publication. No source/test changes,
   skipped checks or disabled checksum verification were used for these retries.

## Artifact identity

| Artifact | SHA-256 |
| --- | --- |
| Darwin universal archive | `1d056c7c36b2fd47eef7abe16b39fadd65c61d1f359b2a2f751248ade6f98890` |
| Installed Mac `swarm` | `2a4a2a4dbd138e042dd1965f32f4033dceb3893a514246de34ff3294030e85b2` |
| Installed Mac `swarm-remote` | `fc7d5f14722b29b3e305c74a4225223018438432ad232634458f34ebef4cfdb8` |
| Linux AMD64 archive | `30272b441cee5d94dae862fc7a04e527b59d62b80f3cf8a4447b2616e5dd9b83` |
| Installed VM `swarm` | `8de7ac605950d070a63377ac066a5408b8c1bda0c1aeccf784b2a8b15119354c` |
| Installed VM `swarm-remote` | `d409049d69005318cd0d2dfdde697da9c55a23feb0e119ce355af24c9006e35f` |
| Official OpenCode Linux x64 1.19.3 archive | `13a8fb2978302071b86f9d7e00da9c8895f9cd846978379c3bdc6a5333536c9b` |
| Installed VM OpenCode | `179ca421d61eefeadfa0c50e084a6e79c1623734732f0f4593f81ccfd3ab7ee9` |

The Mac executables were compared byte-for-byte with the published archive.
The VM used the existing signed updater and compatibility-card checks. OpenCode
was verified against its official release digest, checked for version 1.19.3,
and installed without overwriting an existing executable or copying credentials.
The compatibility card reports owner protocol 1, shimwire 1 and persistence
schema 3. Swarm v0.18.2 understands only schema 2; the rollback guard refuses
that downgrade once new managed OpenCode records exist. No schema downgrade or
destructive migration was attempted.

## Runtime qualification and remaining work

An actual Linux AMD64 OpenCode acceptance run passed on the VM using a private
candidate built from release source: 29.854 seconds including cleanup, three
genuine inputs, native child work, public request controls/history and two daemon
crash recoveries. It made nine scripted loopback requests and zero external-model
requests; native configuration was unchanged and all owned shims exited. This
tests the source candidate, not the final signed binary's model execution.
Installed signed artifact identity and daemon continuity were verified separately.

The new-source [one-hour workload](opencode-implementation-2026-10-18/minimum-hour-2026-10-19.json)
passed 3,600.654 seconds, 225 inputs, 11 crash recoveries and 897 loopback requests.
The corrected [sustained queue](opencode-implementation-2026-10-18/sustained-qualification.md)
has advanced to its 24-hour stage; 72 hours starts only after an actual 24-hour
pass. Neither pending duration is counted as passed. Physical-phone/hosted-relay
qualification, the full real-provider interoperability matrix and the unrelated
whole-Darwin baseline failures remain open. Strict turn Stop, account rotation,
automatic relogin, automatic OpenCode replacement and Swarm-driven compaction
remain unavailable as described in the [operations guide](../ops/opencode-native.md).

The Mac's Intel-prefix Homebrew launcher attempted its normal portable Ruby
upgrade, but could not execute the downloaded Ruby.
The normal cask engine was run with the existing native ARM Ruby and original
Homebrew paths; it successfully upgraded Swarm and its receipts/symlinks.
The command later auto-tapped `homebrew/core` (about 1.4 GB) and failed during an
unrelated installed-dependent `pkgconf` check because the Command Line Tools do
not support the installed macOS version.
Independent receipt, archive, executable and daemon checks establish the Swarm
deployment. Repairing normal Homebrew Ruby/CLT operation remains tracked in
`agents-tracker-vyhy.14`; no separate manual Ruby repair, Rosetta installation or
CLT repair was performed. VM disk availability fluctuated during deployment; its final
read-only check reported 2,030,596,096 bytes free. Headroom assessment is tracked
in `agents-tracker-vyhy.15`. No unrelated user data was removed.
