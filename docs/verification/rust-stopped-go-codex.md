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
