# Storage lifecycle patch release v0.22.1

The owner authorized the next patch version on 2026-10-25. The latest published
release was verified as v0.22.0; this release is v0.22.1.

## Scope and qualification

The candidate integrates `feat/storage-clean` and its reviewed bounded-storage
ancestry into main. It includes preview-first `swarm clean`, warning-only storage
accounting, diagnostic retention, certified hook retirement, safe handoff export
reuse, updater temporary cleanup and supervised test scratch. The unmerged
`feat/native-login-handover` work is excluded; no provider credential/history
migration or new execution-record work is included.

Runtime product source is unchanged from qualified commit `56899d97`. The
release adds the test-supervision correction documented below; no runtime
cleanup or provider behavior changes in that correction. See
[storage lifecycle evidence](storage-lifecycle.md) and
[manual cleanup evidence](storage-clean.md) for native macOS/Linux tests,
independent reviews, the fixed Linux inode-reuse regression, and disclosed test
retries. Wire/schema versions are unchanged by this release.

The existing tag-only release workflow must pass its reusable CI and container
gates before publishing signed archives and updating the Homebrew cask. No
branch/PR/scheduled workflow triggers or gate bypasses are introduced.

## Publication and rollout status

[v0.22.1](https://github.com/Nathandela/swarm/releases/tag/v0.22.1) is published
from `4b275c0a59b9ea4ba2db734991109dbb0a140dea`.
[Release workflow 40176513876](https://github.com/Nathandela/swarm/actions/runs/40176513876)
passed all 17 jobs on the corrected candidate. A concurrent documentation-only
mobile plan was preserved while integrating the correction; it adds no runtime
mobile behavior to this release. [Sanitized machine evidence](storage-v0221-release.json)
records signatures, archive/binary hashes, gate outcomes and rollout limits.

The checksum signature verified against the existing compiled trust slot; a
modified-checksum negative control was rejected. Both deployment archives matched
the signed hashes, their compatibility cards matched the source, and the packaged
build metadata named the release commit. Both installed binary pairs match those
verified files.

| Host | Installed CLI/binaries | Running daemon | Recorded live identities |
|---|---|---|---|
| Mac | 0.22.1, Homebrew ownership preserved | 0.22.0; safe convergence deferred | 17/17 preserved |
| `tf_nathan` | 0.22.1, signed stage/activation | 0.22.0; safe convergence deferred | 52/52 preserved |

Both unattended convergence attempts returned 2 because sessions were working.
No forced daemon or native-agent restart was performed. Runtime convergence is
still pending under `agents-tracker-pnaq`; existing updater timers will retry.
The VM's unresolved upgrade convergence marker and ordinary rollback state are
preserved. Old shims retain their earlier behavior until they retire.

`swarm clean --json` completed in preview mode on both installed copies: Mac
reported one unchanged diagnostic target and 24 retained items; VM reported one
unchanged target and 223 retained items. No `clean --apply`, history purge or
credential migration was performed. Doctor reported zero failures on both hosts,
with warnings including the expected old-daemon/new-binary mismatch.

Both installed updater files matched the previous standard templates before
replacement. The Mac now uses the reviewed Perl log-retention wrapper with its
loaded state preserved; the VM now uses the subshell fetch cleanup, with its timer
still active. No custom updater configuration was overwritten.

Homebrew completed the Swarm upgrade and recorded cask version 0.22.1, then
returned 1 while trying to reinstall unrelated `pkgconf`: the installed CLT does
not support macOS 27. Swarm's hashes/version were checked independently;
`pkgconf` remains executable as 2.5.1. No SDK/CLT installation was altered.
That host-tooling issue is tracked separately as `agents-tracker-fi0w`.

## Hosted-runner test-supervision correction

The first candidate tag at `fb20750f` was not published. Release workflow
[40173868486](https://github.com/Nathandela/swarm/actions/runs/40173868486)
failed during test cleanup: several application suites reported PASS, but
supervised cleanup and the supervisor's own cleanup tests failed because it
could not inspect persistent same-UID runner processes (`/proc/1093/exe`, `/proc/1251/exe`), so it
retained scratch and failed the gate. A fresh attempt reproduced the refusal
(`/proc/1070/exe`); that blocked attempt was canceled rather than retried blindly.
No cleanup safeguard or release gate was disabled.

The correction excludes only permission-denied Linux processes whose positive
kernel birth ticks are proven strictly earlier than the test harness. Equal,
newer, unavailable or malformed identities and non-permission errors still block
cleanup; readable exact-executable matches still get stopped regardless of age.
The ordering exemption is not used on Darwin, whose identity token is based on
wall-clock time. Preexisting external executors remain fixture-owned custody.

Native Linux RED tests reproduced both EACCES/EPERM refusal and interference
with verified owned-child cleanup before the fix. The corrected complete
`internal/testbin` suite passed natively on Linux, native Linux race passed in
16.625 s, macOS race passed in 10.081 s, vet passed and lint reported zero issues.
Sol and Astra independently approved the narrow source and test boundary.
The candidate tag was recreated before publication. The complete corrected
release workflow subsequently passed, including the hosted Linux and remote-v2
gates that exposed the original refusal.
