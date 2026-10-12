# Account pools v0.17.6 verification

Investigation date: 2026-10-12. Publication and installed acceptance remain pending until the release gates finish.

## Installed reproduction

On v0.17.5, enabling the Codex pool and requesting a new discussion through owner RPC returned `launch: accounts: private account or configuration is unavailable; review Accounts before resuming`. The previous disabled setting was restored after that experiment. The ambient CLI was Codex 0.162.1, whereas account configuration and enrollment require characterized 0.160.0. Claude already retained its qualified 2.1.289 executable; Codex followed the mutable npm installation.

Seven enrolled accounts were present: four Claude and three Codex. Only one Codex account had remaining observed capacity; the other two were exhausted. Initial visible coverage was seven managed/four unmanaged Claude discussions and one managed/twenty unmanaged Codex discussions. Thirteen unmanaged Codex discussions were running, and most used unqualified 0.161/0.162 versions. Native history compatibility for those versions is not asserted.

Daemon logs also contained failed resumes after unmanaged auth/CLI recycling. That path could stop a source before discovering that newly enabled account assignment could not prepare a successor. Doctor was green despite these application-level failures.

## Corrections and regression boundaries

- Managed Codex launch, enrollment, availability checks and recovery select a qualified runtime. Exact operator-provisioned native 0.160.0 is retained with content and file-identity checks, using the existing private Claude runtime mechanism. The daemon performs no downloads and does not change global authentication or the ambient CLI. Tampered retained state refuses fallback.
- Enabling a pool discovers compatible, observed, idle running discussions. The existing durable recycler owns stopping and resuming. Preflight locates history, compiles the native resume, selects an eligible account and freezes the private configuration before any stop. The selected binding and projection survive daemon restart; credential retirement retains pending destinations. Active turns, input/permission holds, controllers, duplicate conversations, unsupported versions and changed configuration hold the source.
- Upgrade worker capability 4 fences older readers from retained Codex enrollment and running-discussion enrollment plans. Existing protocol and shim wire versions remain unchanged.
- PR [#54](https://github.com/Nathandela/swarm/pull/54) adds explicit relocation for resumed discussions whose checkout disappeared. Connected-code review and targeted resume/PTY checks passed. Its switch-style lint correction was pushed to the contributor branch, and all official CI gates passed before merge.

New regression tests cover both providers, exact conversation/account preservation, one successor, durable restart, pending CLI-update adoption, pool disable before stop, CLI-refresh opt-out, unsafe sources, retained runtime restart/vendor deletion, tampering and upgrade compatibility. Detached native worker tests exercise account creation and cancellation without authenticating a real subscription.

The official Codex `rust-v0.160.0` Linux amd64 musl archive was verified against GitHub's published asset SHA-256 `306865417d4ee7a927785852910a527f41e1e159add390ac5ae3accb67d44a13`. Its private executable content SHA-256 is `12eb3e81114588aca3b7998f4f19e8997b056aca08e57a7ca7c8a3ec8c652aad`.

## Acceptance limits

Browser sign-in for a new real subscription and two-healthy-account Codex quota failover are not established by synthetic tests. The existing controlled two-account acceptance issue is `swarm-n21`; real quota exhaustion is not manufactured. Installed canary and release gate results must be added after execution.
