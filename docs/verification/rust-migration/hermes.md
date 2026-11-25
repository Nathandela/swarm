# Rust Hermes adapter verification

The Rust adapter retains the two custody models in [ADR-037](../../adr/ADR-037-hermes-shared-native-owner.md). Its classic path uses the characterized CLI, viewport chrome and write-once conversation identity. Saved-session resume uses the source's profile and the explicit `--no-restore-cwd` flag. Upstream Hermes 0.20.6 can still restore its recorded directory, as documented in the [classic adapter contract](../../design/hermes-adapter.md).

A private, enabled Discord configuration and stable Hermes >= 0.22.5 select shared-native mode for fresh launches. The saved mode controls resume. The daemon verifies the existing local gateway's PID, birth identity, release code, instance, authority epoch and exact profile before accepting a ticket. The shim owns the PTY viewer. The shim owner record pins a digest of the complete native descriptor. Every durable native target and transfer verifies the descriptor against that independent record, including the nonsecret origin selectors and exact native discussion. The fixture substitutes a different canonical discussion on the same scope and verifies that roster, messaging, stop and successor transfer refuse the changed record. The shortcut `r` resumes a detached Hermes discussion in the Rust board.

Native semantic messages pass literal text to `prompt.submit`. A retained `submitted` receipt proves admission, including a queued receipt with execution generation zero. The operation stores the bound descriptor and intent hash before submitting, recovers a matching pending admission after a dropped reply, and leaves unresolved admissions fenced. Native tool approvals and clarification remain in the provider viewer. An attributed connector message requires the exact active installation, audience, source revision, rendered composer and persisted worker cutover under the connector effect lock. A state-only receipt change leaves the pending-operation fence in place.

Explicit stop records the exact intent, cancels observed native queue admissions, requests an interrupt at the observed generation, and invokes session-scoped background stop. The shim then stops its viewer group and detaches an exact provider subscription. Viewer loss reports `NativeWorkUnknown` and Needs input. Delete stays blocked and retains the workspace while native job completion is unproven. A detached source keeps stop authority through a prepared successor reservation. Its exact successor takes authority when the viewer owner and bound descriptor are durable. The predecessor and the starting shim share a private lock so a restarted daemon can revoke an abandoned reservation before retrying or stopping, and a stale shim cannot spawn a revoked viewer. Darwin's final group signal can return EPERM for an unreaped, exited leader. The shim checks that the leader is the group's sole member before accepting that result.

## Reproduce the fixture checks

Run `cargo test --locked hermes` from the workspace on an available target host. On an execution-only Linux x86_64 host, build and transport the Rust test binaries with the workspace's `scripts/verification/rust_linux_tests.py` runner. The fixture starts a private daemon, a shell-backed viewer and an in-process Unix control and loopback WebSocket owner. It uses synthetic provider identities and local sockets.

The tests check version admission, the literal classic command, calendar validation, corroborated classic chrome, saved-profile resume, classic stop/delete, native epoch and canonical identity binding, epoch replacement refusal, restart, durable replay, lost admissions, provider-owned approval, typed scoped stop, workspace retention and scoped resume after the activation switch is disabled. Negative cases cover the wrong connector installation for an otherwise valid operation, a state-only forged receipt, restart before a successor starts, refused successor startup, and restart after durable successor custody. The fixture checks for any unexpected provider RPC, including session close or approval response.

## Results

| Environment | Result |
| --- | --- |
| macOS arm64 | The locked workspace suite passed, including both Hermes unit tests and both Hermes daemon integration tests. |
| Linux x86_64 | The two cross-built integration tests executed and passed on a Linux host using a private tmpfs namespace. |
| Rust formatting and Clippy | `cargo fmt --all --check` and `cargo clippy --locked --workspace --all-targets -- -D warnings` passed on macOS. |

Installed Hermes, Linux arm64 execution, and published artifact qualification remain open. The two-host checks use synthetic credentials and a fixture gateway.
