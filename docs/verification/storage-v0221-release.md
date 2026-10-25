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

Production Go source is unchanged from qualified commit `56899d97`; subsequent
commits contain documentation and qualification evidence only. See
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
