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

Candidate prepared; publication and host deployment are pending. The checked
Mac and `tf_nathan` installations both report v0.22.0. The VM upgrade timer is
active and its remote gateway is inactive.

After signed publication, install through the existing ownership paths and use
safe unattended convergence. Preserve live native sessions and histories; report
any deferred old daemon/shim owners honestly. Update the existing upgrade timer
implementations where needed for the storage fixes. Do not run `clean --apply`
on user state as part of deployment. Record release-run, archive/signature,
installed-version and read-only cleaner checks below when completed.

## Hosted-runner test-supervision correction

The first candidate tag at `fb20750f` was not published. Release workflow
[40173868486](https://github.com/Nathandela/swarm/actions/runs/40173868486)
failed after functional tests passed: the test supervisor could not inspect
persistent same-UID runner processes (`/proc/1093/exe`, `/proc/1251/exe`), so it
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
The unpublished candidate tag is recreated on this correction; the complete
release workflow must pass afresh before any publication or installation.
