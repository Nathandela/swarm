# Stopped ordinary Codex compatibility

The copied-state fixture runs the pinned Go supervisor and its real shim, with the pinned repository's synthetic Codex CLI standing in for the provider. The supervisor joins the synthetic App Server and discovers its thread through the actual local protocol. The original Go shim publishes both the ordinary PTY worker and App Server birth epochs, a joined native-writer record, and the backend exit.

Rust accepts this stopped schema-1 record when the original private launch directory, CLI installation identity, backend command and socket coordinates, worker/daemon/backend birth evidence, and recorded exit agree. The offline copy excludes Unix sockets. A socket entry at the original source causes the Rust gate to refuse without removing it. The gate also refuses changed backend coordinates, extra backend arguments, malformed backend configuration, and incomplete stop evidence. This qualification checks the pinned Go builder’s exact fresh TUI arguments. Initial prompts and subcommands stay outside this proof.

```bash
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_codex.py PINNED_GO target/debug/swarm PINNED_GO_FAKE_CODEX
```

Both Go binaries come from `f26648d69086e329132ac7849954d2f29a895387`. Build the third argument from `./cmd/swarm-fake-codex` in that checkout. The witness confirms exact Go/Rust roster parity and preserves the old side files. On rollback, Go displays the labels written by Rust and launches the synthetic Codex process with the original thread ID.

This gate covers a stopped ordinary Codex attempt and its stopped local backend. Live backend adoption, manager/account/scheduled custody, and provider history interoperability have separate gates.

## Rust-created managed Codex 0.172.0

Stopped-Go compatibility can launch a fresh ordinary managed Codex 0.172.0 session using its default native account home. It records the physical provider binary and the Go-compatible CLI fingerprint, along with the native profile, account, checkout and launch options. The shim rechecks the profile before execution. Export requires a drained shim and backend, the exact native conversation and rollout, and an unchanged origin and viewer binary. It transfers the stopped row without manufacturing Go shim or backend files.

The Linux x86-64 and macOS arm64 witnesses used the installed Codex 0.172.0 vendor executable. Pinned Go accepts the exported row, reports the same CLI fingerprint after its own probe, and runs another native model turn in the same rollout. Export refuses a live Rust worker, changed native authentication, and an unexpected semantic ledger. The compatibility owner explicitly refuses structured operation dispatch, preserving native terminal input without transferring unreplayable Rust operation state.

Add `--cold` after `0.172.0` when running `live_go_codex.py`. The entire fixture runs in one Linux PID and network namespace with an isolated loopback model and synthetic authentication. Other Codex versions, explicit auth selectors, alternate native homes, worktrees, and unmanaged writer scopes require separate qualification. On macOS use the [exact-socket fixture](rust-live-go-codex.md) to qualify the signed executable while keeping other native sessions and their socket paths outside the sandbox.

## Resume a stopped Go-managed Codex 0.172.0 conversation

With stopped compatibility enabled, the board's **r** action and the ordinary owner `resume_from` request can open a stopped Go-managed Codex row in Rust. The source must be a fresh ordinary Go launch with its writers joined, an unchanged physical Codex 0.172.0 executable and the saved default native account. The request uses its original physical checkout and its saved PATH and launch options. Rust binds the unarchived native UUIDv7 rollout at its literal recorded checkout, checks the app-server reply against the exact rollout path and idle thread, and refuses a competing live or unconfirmed Rust writer. The existing Go row stays authoritative for the original attempt. A stopped Rust successor can be exported for pinned Go to resume with the same rollout, CLI identity, authority stamp and options.

The isolated macOS arm64 and Linux x86-64 fixture ran three distinct native turns through pinned Go, Rust and pinned Go again. It used a local model and synthetic authentication and verified one original rollout inode, byte-for-byte retention of the source Go files, and the original workspace-write policy. Wrong account, checkout, endpoint and duplicate writer requests were refused. On both hosts, joined Codex 0.172.0 processes can leave their exact short listener on disk. The production gate still refuses this state. The fixture verifies refusal and retires only its own listener under the exact vendor lock after a refused connection and an inode check. Automatic recovery of this field condition remains open.

On macOS use `sh scripts/verification/run-macos-live-go-codex.sh /path/to/signed/codex --stopped-resume`. On Linux run `python3 -Bu scripts/verification/stopped_go_codex_resume.py PINNED_GO RUST SCRATCH` in the private network, PID and temporary-directory namespaces described in the [live Codex witness](rust-live-go-codex.md). Stage native Codex 0.172.0 at `SCRATCH/bin/codex`. The Go metadata has no historical Codex configuration fingerprint. Rust anchors the current native configuration at admission and rechecks it before execution and export. Alternate homes, managed account bindings, upgrades, moved checkouts, previously resumed Go rows and scheduled workers require separate qualification.
