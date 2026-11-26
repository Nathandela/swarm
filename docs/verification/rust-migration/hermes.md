# Rust Hermes adapter verification

The Rust adapter retains the two custody models in [ADR-037](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/adr/ADR-037-hermes-shared-native-owner.md). Its classic path uses the characterized CLI, viewport chrome and write-once conversation identity. Saved-session resume uses the source's profile and the explicit `--no-restore-cwd` flag. Upstream Hermes 0.20.6 can still restore its recorded directory, as documented in the [classic adapter contract](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/design/hermes-adapter.md).

A private, enabled Discord configuration and stable Hermes >= 0.22.5 select shared-native mode for fresh launches. The saved mode controls resume. The daemon verifies the existing local gateway's PID, birth identity, release code, instance, authority epoch and exact profile before accepting a ticket. The shim owns the PTY viewer. The shim owner record pins a digest of the complete native descriptor. Every durable native target and transfer verifies the descriptor against that independent record, including the nonsecret origin selectors and exact native discussion. The fixture substitutes a different canonical discussion on the same scope and verifies that roster, messaging, stop and successor transfer refuse the changed record. The shortcut `r` resumes a detached Hermes discussion in the Rust board.

Native semantic messages pass literal text to `prompt.submit`. A retained `submitted` receipt proves admission, including a queued receipt with execution generation zero or explicit null before a claim. The operation stores the bound descriptor and intent hash before submitting, recovers a matching pending admission after a dropped reply, and leaves unresolved admissions fenced. Native tool approvals and clarification remain in the provider viewer. An attributed connector message requires the exact active installation, audience, source revision, rendered composer and persisted worker cutover under the connector effect lock. A state-only receipt change leaves the pending-operation fence in place.

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

## Guarded native release

The macOS arm64 Rust probe passed against Hermes Agent v0.22.5 at source revision `bf0203a794a07aac3ff764972ab9087baa4e4f4e`. The genuine provider gateway and TUI used a loopback scripted model with synthetic credentials. Two distinct canonical sessions shared a bound owner. Each received a queued receipt. The probe verified the exact synthetic Discord cutover and wrong-installation refusal, five loopback model calls, supervisor restart, A-only background stop with B still running, retained deletion refusal, exact saved-session resume, and native approval observation while the protected fixture file stayed intact. [Darwin native result](hermes-native-darwin.json)

The initial real probe found two release wire differences. Hermes projects the generation as explicit null before a queued admission is claimed. Its native snapshot projects the running flag and authoritative prompt array. Rust accepts the nullable field for queued or pre-claim terminal admissions and reads foreground state from the release snapshot. The synthetic fixtures exercise both wire forms.

Reproduce with the pinned source and an installed virtualenv, with the pinned TUI bundle built. The existing harness verifies its host write and network guards before launching Hermes and uses only a fresh scratch directory.

```sh
python scripts/verification/hermes_native_admission.py \
  --root "$fixture_root" --source "$pinned_source" \
  --hermes "$pinned_console" --node "$pinned_node" \
  --swarm "$rust_swarm" --probe "$PWD/scripts/verification/hermes_rust_probe.py"
```

The same pinned source and TUI ran on native Linux x86_64 with its installation mounted read-only in a separate network and PID namespace. The first launch exposed the ordinary five-second identity probe as too short for cold Hermes. The combined candidate uses the Go adapter’s ten-second Hermes budget. Both native sessions ran against five loopback model calls. A scoped stop preserved the sibling, a resumed viewer kept its exact discussion, and the native permission prompt protected the fixture file. Installed-release qualification and proof of the native job’s final completion remain open. [Linux native evidence](hermes-native-linux.json).


## Integration with manager and scheduled workers

The merged macOS arm64 candidate passed the locked offline Rust workspace suite, workspace formatting and all-target Clippy with warnings denied. The same binary passed the guarded native Hermes probe above, the Rust Codex manager admission and publication fixture, and the cold scheduled Codex fixture with an 8 KiB Unicode prompt. The scheduled fixture verified independent final output, worker exit, and workspace release. [Manager fixture commands](../../../crates/swarm/tests/fixtures/discord/README.md), [scheduled fixture](../../../scripts/verification/parity_schedule.py)

## Original Go classic terminal round trip

For an ordinary Go Hermes session, the Rust supervisor can adopt the existing Go shim on the original state root with both `SWARM_DAEMON_STOPPED_GO_COMPAT=1` and `SWARM_DAEMON_LIVE_GO_COMPAT=1`. The original shim keeps the PTY and hook spool. Rust checks that the launch points at the original private session directory, that the recorded arguments match the classic Hermes invocation, and that the socket peers, shim identity and unique PTY leader agree. It accepts the pinned Go launch shape with an absent CLI probe. A Hermes shared-native descriptor or stop marker prevents this path. Once the original Go shim records viewer exit, Rust displays a read-only Needs input row with native work unknown and directs the operator to the original Go supervisor. The adopted root cannot enter stopped-state export or start new Rust sessions while it retains a Go Hermes row.

The native witness builds Go from `f26648d69086e329132ac7849954d2f29a895387` and uses Hermes Agent 0.20.6 from `aff5125f8edf5095aef5d3d79bbbb101c95b9413`, installed with `uv sync --frozen --no-dev --python 3.11`. It launches the ordinary interactive classic CLI with a disposable home and a loopback model. Go, Rust and restored Go each submit a different terminal prompt. The witness verifies three ordered user/assistant pairs in the same native SQLite conversation, with the same Go shim, Hermes PTY worker and socket identities. A forged native descriptor is refused before Rust assumes custody. Original Go metadata and launch evidence stay byte-for-byte intact during the Rust turn. After restored Go exits the standalone CLI, Go records the same native conversation ID. Rust displays the viewer-only status and refuses label mutation and stopped-state export. The standalone round trip passed on macOS arm64 and Linux x86_64.

Run [the witness](../../../scripts/verification/live_go_hermes.py) with a new private directory and the two Swarm binaries.

```sh
python3 scripts/verification/live_go_hermes.py \
  --root "$(mktemp -d /tmp/hc.XXXXXX)" --go "$go_swarm" --rust "$rust_swarm" --hermes "$classic_hermes"
```

Hermes 0.22.5 uses its own gateway behind the classic UI. Pass `--gateway-backed` and the 0.22.5 console from the pinned Hermes release above to test this path with a private prestarted gateway. The fixture launches Go with Discord disabled and checks that the original Hermes gateway and native SQLite conversation survive supervisor handoffs, viewer exit, and rejected stopped-state export. The only provider-side action Rust takes is terminal input through the original shim.

On macOS the harness uses `sandbox-exec`. On Linux run the command in the existing native fixture boundary with a fresh network and PID namespace, an enabled loopback interface, a read-only host mount, and only the fixture root writable. The harness tests both the network and write guards before launching a provider.
