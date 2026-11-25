# Rust migration qualification

## Initial fleet round, 25 November 2026

This branch is under development. The production Go reference is v0.29.22; the additional Discord call reference is da7b9f2d. The evidence below applies to an isolated Mac or Linux fixture. Production release, migration of existing sessions and real provider/Discord behavior have separate gates in the [roadmap](../specifications/rust-migration/README.md).

| Exercise | Observations |
|---|---|
| Frozen references and scope | Two local Go CLI references with pinned revisions and executable digests. Signed Darwin and Linux x86_64 archives verified. Linux archive SHA-256 `8baaf27ec6d10087039175a88c14a5dfbc1e10a5eac18f71efc002dd512f3549`. The released executable ran on native Linux x86_64. [Reference checks](../specifications/rust-migration/reference-checks.md) and [scope ledgers](../specifications/rust-migration/inventory/README.md). |
| Go/Go harness calibration | On macOS arm64 and Linux x86_64 the operator-level driver opened a real board, attached, echoed input, resized, detached, restored the terminal and stopped the fixture. Producer-injected extra CR changed the recorded bytes; replay located the divergence. Timeout and forced driver death exercised the external reaper. [Driver and commands](../../scripts/verification/parity/README.md). |
| Workspace consent | Old and new binaries wrote, read and revoked one another’s exact physical workspace consent. Replacing the directory revoked inherited trust. Help, guide/skill output, invalid arguments and non-UTF-8 status paths were compared as bytes. Passed on macOS arm64 and native Linux x86_64. [Two-writer test](../../scripts/verification/parity_setup_trust.py). |
| Rust client → Go daemon | A Rust reliable launch returned a real session. A Go retry of the same operation, with an unrelated environment variable changed and a path containing a redundant component, returned the same receipt. Roster, screen, timeout/refusal and live completion results matched. Both CLIs steered the original worker. [Local run](../../scripts/verification/parity_ipc.py). |
| Rust daemon and shim | The isolated runtime drove a real PTY, then replaced the daemon. Lease takeover, stale-generation and mismatched birth refusal, worker identity continuity, durable launch replay and process-group containment are tested separately. [Process-level test](../../scripts/verification/parity_runtime.py). |
| Dashboard slice | Native macOS Go/Rust trial matched the scripted board/attach/resize/detach observations. The launch form auto-attached, and reattachment after daemon replacement recovered the fixture screen. The complete dashboard visual and keyboard inventory is still open. |

On the first full Go round the build, vet and lint gates passed. Six tests failed across `cmd/swarm`, `internal/daemon` and `internal/e2e`. The checkout-inventory fixture had used the test process as its shim while inheriting the developer checkout as that process’s directory. Running it from its own fixture directory fixed the isolated reproduction. The remaining failures involved startup/readiness deadlines or the compatibility harness. Keep the first failures in the evidence even if a less contended run passes.

The first full cross-built Rust test round on Linux found a wide-character snapshot mismatch also observed on macOS, and a Discord test that tried to run the build host’s executable path. The fleet runs the actual Linux binaries on `tf_nathan` in private scratch without replacing the installed Swarm or using an automated runner. [Linux test runner](../../scripts/verification/rust_linux_tests.py).

Mixed-language live shim adoption, complete provider semantics, live Discord text/files/calls, 24-hour soaks, the full target matrix and installed upgrade/rollback remain release gates.
