# Managed Rust preview upgrade rehearsal

The disposable rehearsal used two stamped Rust versions and the exact pinned media tree from the retirement-lane archives. The archived test feed carried a real app for the selected platform and placeholders for the other platform names. A scratch Ed25519 key signed each asset set. These feeds exercised installed behavior and are not distribution artifacts.

## Observed on both native hosts

| Action | Observation |
|---|---|
| Verify with `--managed --check-media` | The trust key, installer and matching app/Node/DAVE files stayed under the new private prefix. Versions on PATH and package databases were untouched. |
| `upgrade --json --dist …` | Returned the proposed version and owning distribution without writing state or the upgrade receipt. |
| `upgrade --stage --json --dist … --check-media` | Verified the signature, archive, executable identity and packaged helper. The old launcher stayed selected. |
| `upgrade --activate --json` with the old daemon holding its lease | Returned exit 2 and a deferred result on macOS arm64 and Linux x86_64. The old generation stayed selected even though the daemon had zero sessions. |
| Retried after the fixture daemon exited | The stable launcher resolved the new generation, the new packaged helper passed and doctor displayed the activation receipt. Direct mutation through the previously installed executable was refused. |
| `upgrade --rollback --json` before the new generation admitted writes | Restored the old route. Unattended selection of the abandoned build returned `held`. |
| `upgrade --rollback --json` after the new daemon was allowed to run | Refused without changing the selected release or the newer work file. |

The package boundary suite also verifies that stale effectful writer records hold the route, a tampered member cannot activate, an interrupted pointer rename leaves the old route intact, and an explicit matching prior-source rollback proof preserves new work. The current compiled allowlist is empty pending a tested bidirectional pair. The final loaded-image guard separately passed a native macOS valid/mismatched executable-slot probe, with Clippy warnings denied. Native installed tests passed before this final guard follow-up. The Rust workspace suite passed 118 default tests with five environment-specific tests ignored.

The first macOS fixture attempt inherited a pre-existing socket override. The supervisor refused that listener and the successful rerun used an empty environment and an explicitly scoped socket. The Linux fixture used scratch tmpfs and a real daemon write because that filesystem could not certify the creation time for a workspace-trust fixture.

## Release notices

The frozen collector resolved four Swarm targets and four DAVE addon targets from Cargo locks and emitted 660 files totaling 5,052,195 bytes. All 335 selected locked crate versions have at least one included legal notice. Fallback texts come from dated crate VCS revisions tied to Cargo package metadata. The MPL text comes from Mozilla and the inventory reports corresponding public source locations. The upstream Node license travels with each selected Node runtime. Each DAVE native npm package identified the same source revision as the JavaScript package, `72d1c6d081f4993d618806990f1d3a1f6f53ed67`. The signed asset contains that revision's MIT notice and Cargo lock. The exact compiler and enabled features used by the external npm producer remain unverified. [DAVE source](https://github.com/Snazzah/davey/tree/72d1c6d081f4993d618806990f1d3a1f6f53ed67) · [Mozilla Public License](https://www.mozilla.org/MPL/2.0/)

## Boundary

The supported updater scope is one explicit private managed installation and a locally obtained signed Rust feed. Cold activation holds the managed lease and any applicable configured daemon locks. Unknown crash leftovers defer to offline custody reconciliation. The updater does not start or restart services. The pinned Go stable channel, package-manager in-place transitions, externally managed writers at other roots, automatic release-feed selection, and live compatibility across generations need their own qualification. For the confirmed classic Go state export route, see [the stopped-state rehearsal](rust-stopped-go-state.md). No tag, release or production installation was changed.
