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

On November 27 both native hosts repeated the complete signed `0.1.0` to `0.1.1` fixture lifecycle from source `944a39365c3a28e31a85b93ac300c9ad61648a15`. The first Linux namespace mapped the host root to UID 65534, so the packaged helper refused that ancestry. A fresh Linux namespace with a disposable root owned by the test user passed. The macOS arm64 and Linux x86_64 logs have SHA-256 `f63bad6c5d8923abfadfb62c422eba5ec8c81dce611a260ad8117de2b0d2f9e3` and `3002e6bb3eadb30940f1d99dd63c6921161a13f9a06d5420c79fd8acf7924c06`. The exact final signed release candidate retains its own install and transition gate.

## Release notices

The frozen collector resolved four Swarm targets and four DAVE addon targets from Cargo locks and emitted 660 files totaling 5,052,195 bytes. All 335 selected locked crate versions have at least one included legal notice. Fallback texts come from dated crate VCS revisions tied to Cargo package metadata. The MPL text comes from Mozilla and the inventory reports corresponding public source locations. The upstream Node license travels with each selected Node runtime. Each DAVE native npm package identified the same source revision as the JavaScript package, `72d1c6d081f4993d618806990f1d3a1f6f53ed67`. The signed asset contains that revision's MIT notice and Cargo lock. The exact compiler and enabled features used by the external npm producer remain unverified. [DAVE source](https://github.com/Snazzah/davey/tree/72d1c6d081f4993d618806990f1d3a1f6f53ed67) · [Mozilla Public License](https://www.mozilla.org/MPL/2.0/)

## Boundary

The supported updater scope is one explicit private managed installation and a locally obtained signed Rust feed. Cold activation holds the managed lease and any applicable configured daemon locks. Unknown crash leftovers defer to offline custody reconciliation. The updater does not start or restart services. The pinned Go stable channel, package-manager in-place transitions, externally managed writers at other roots, automatic release-feed selection, and live compatibility across generations need their own qualification. For the confirmed classic Go state export route, see [the stopped-state rehearsal](rust-stopped-go-state.md). No tag, release or production installation was changed.


## Integrated package on November 26

The archives built from `cb630ec5b1131e4b4ae6d706a1132120e03a7dbd` carried the signed manifest, both updater documents, the four architecture slices, pinned media and dependency notices. The rehearsal used a disposable Ed25519 key. On macOS arm64 and Linux x86_64 the board harness copied the verified executable into its isolated fixture and passed actual PTY launch, daemon crash and reattach. Both installed media checks passed. The universal macOS output also passed all-slice ad hoc code-signature verification and its x86_64 media check under Rosetta. A hardware-virtualized ARM64 Linux guest also ran the installed launcher and its packaged media helper.

| Archive | SHA-256 |
|---|---|
| Universal macOS | `78f4a2b81b6c79acad52b584255591e18a52d5f1ee3a057df6350ba9ee748654` |
| Linux x86_64 | `51b4f398246bfa7b778ef345ec95111bc57edd9eb699088e7191981b938dd266` |
| Linux arm64 | `5ef709452623aeb1f941f28ab12a90f78eba7faa32ed637039cb4e83c6d8cf34` |


### Linux arm64 installed board and media

An offline guest on Apple Silicon booted the installed LinuxKit 6.10.14 aarch64 kernel under Apple's Virtualization framework. Its ext4 disk contained the official Docker Library Python 3.13 Debian Bookworm arm64 image, manifest `sha256:1fe519ebd04989f9d6927bfa447fb0767110a1f5934c9fc95d6cfafbfc358c90`. The extra Debian packages for Bubblewrap, process inspection and OpenSSL were checked against the signed Bookworm release index. The VM had two CPUs, 1536 MiB of memory, a serial console and one private disk.

UID 65534 verified the signed archive in the table above, installed it in a private managed prefix, matched the release card to `cb630ec5` and ran the bundled Node/DAVE media check. The board witness copied that verified executable into its fixture, launched a real PTY, killed its daemon and reattached to the surviving session. Installing and checking the release used the stable managed launcher. The PTY run used the copied executable. It reaped two fixture processes, removed two sockets and found zero survivors. The guest returned status 0 and powered down. The kernel SHA-256 was `a0322f813e924d375694a171624449e4110d6f371f22051fafb86d79784fc926`. The full serial capture SHA-256 was `2d5d5ded9795218757651392f229d587ab8ef6dd7d383f3fdeaf433932fc079f`.

The same witness can run as an ordinary user on an aarch64 Linux host with Python 3, OpenSSL, Bubblewrap, procps, and a short private scratch directory.

```sh
python3 "$dist/swarm-rust-install.py" verify "$dist" swarm-rust_0.1.0_linux_arm64.tar.gz \
    --public-key "$public_key" --managed --prefix "$prefix" --check-media
"$prefix/swarm" release-info
"$prefix/swarm" release-check-media
python3 scripts/verification/parity/test_board.py --supervisor "$prefix/swarm" --board "$prefix/swarm"
```

This run covers the signed November 26 development archive. Release qualification requires rebuilding and running the same checks against the selected release source.
