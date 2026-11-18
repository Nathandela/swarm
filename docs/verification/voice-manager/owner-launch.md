# Owner launch custody implementation

2026-11-08. P1 component evidence; the complete voice feature and full release gates remain open.

The new negotiated `owner-launch` operation binds a timestamped operation ID to the complete owner launch request before a process can be created. Exact retries reconcile the original worker or report uncertainty; they never redrive an uncertain owner reservation. Owner status reads are separate from signed-remote custody. Pilot create/resume persist the exact filtered request before dispatch and expose `launch-status` for reconciliation, including after the mutation window.

TDD evidence began with missing typed API and failing behavioral tests. Coverage includes conflicting environment, lost owner-socket reply, restart, deleted worker, linked resume, source deletion, reservation retention/capacity, lost stdout and expired-ID status reconciliation. Legacy interactive spawn/TUI behavior is unchanged.

Independent Astra review found three defects and each was reproduced before fixing:

- Raw process environment in the Pilot pending file: fixed by applying the existing allowlist and safe provider-origin/refusal metadata before both persistence and dispatch. A dummy unrelated credential must not appear on disk.
- Owner receipt visible through the existing alternate remote status path: fixed with remote-domain filtering; an actual signed-phone fixture now returns unknown for an owner operation.
- Applied status left the matching Pilot guard unresolved: fixed by settling only after successful complete stdout. Unknown, mismatched, missing-session and output-failure cases retain the guard.

The original independent overlay assertions and permanent negative cases pass. Astra approved the 22-file revision recorded in its source manifest, aggregate SHA-256 `b297ef6bc08c29868d516f910bb356dda8be28ae549bfb09963ab545a0ee40c8`.

Verification used existing TestMain/testbin supervision and shared caches. All test invocations remove inherited `SWARM_SESSION_ID`, `SWARM_HOOK_TOKEN`, `SWARM_DAEMON_SOCK`, `SWARM_SHIM_HOOK_SOCK`, `SWARM_HOOK_SEQ_FILE` and `SWARM_HOOK_CAPTURE` before running fixtures.

- Full `internal/daemon`, `internal/protocol` and `internal/idempotency` suites passed.
- Corrected-environment race selection across `cmd/swarm`, daemon, protocol, skeleton and idempotency passed, including `TestOwner*`, `TestPilot*` and protocol-document drift checks.
- Astra reran original/permanent regressions across all five packages successfully.
- Seven existing CLI tests failed identically on clean base `62e85aac`; all seven pass with canonical private scratch. They are tracked separately in `agents-tracker-uv6m.18`, without changing production login safety.
- One full skeleton run timed out in `TestR7R4_AResumedThreadThatBecomesSubscribableLaterKeepsTheHonestGap`. That test passed on the clean baseline in 10.353 s, and together with the new owner-launch fixtures on the implementation tree in 10.243 s. The complete package/repository gate must still be rerun; the timeout is not waived.

No production sessions, deployment or real account actions were used by these launch fixtures. Existing Pilot contexts still expire when their daemon socket binding changes; the persistent voice controller uses typed operation IDs rather than those temporary contexts.
