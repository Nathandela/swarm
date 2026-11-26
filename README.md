# Swarm

<p align="center">
  <img src="docs/assets/swarm-hero-flightwritten.png" alt="Swarm terminal session dashboard project artwork" width="960">
</p>

Swarm keeps coding-agent sessions running when you close your terminal. Its keyboard-driven board groups sessions by whether they need input, are working, are ready for review, or have finished. Each worker has its own PTY. Reopening the board or restarting the supervisor reconnects you to that worker.

**This branch is the Rust migration preview.** For the currently shipped application, installation instructions and upgrade channel, use the [stable release](https://github.com/Nathandela/swarm/releases/tag/v0.29.22) and its [operator README](https://github.com/Nathandela/swarm/blob/v0.29.22/README.md).

## Run the preview

Build locally with Cargo. The release rehearsal used Rust 1.99.1. Open a disposable workspace in a fresh home to try the preview alongside a stable installation.

```sh
cargo build --locked --bin swarm
preview=$(mktemp -d /tmp/swarm-preview.XXXXXX)
mkdir -p "$preview/home" "$preview/work"
binary="$PWD/target/debug/swarm"
(cd "$preview/work" && env -i HOME="$preview/home" PATH="$PATH" TERM="${TERM:-xterm-256color}" SWARM_DAEMON_STATE="$preview/s" "$binary")
```

Press `n` to start a session. Choose an installed provider, enter a directory and press Enter. The board attaches to the new worker. The footer shows the available controls for the focused field or session. The preview can launch Claude Code, Codex, agy, Hermes Agent and OpenCode. Each provider has its own native controls and version requirements. [Provider workflows](docs/specifications/rust-migration/workflows.md) · [Terminal qualification](docs/verification/rust-migration.md)

Use the same home and state directory for scripting commands. For example, `env HOME="$preview/home" SWARM_DAEMON_STATE="$preview/s" "$binary" ls --json` lists that preview's workers. Closing the board leaves the supervisor and workers running.

## Working on Swarm

| Area | Where to start |
|---|---|
| Terminal board, CLI, per-user supervisor and PTY shims | [Rust application](crates/swarm/src) · [runtime contracts](docs/specifications/rust-migration/terminal-runtime.md) |
| Native agent drivers and managed sessions | [Provider code](crates/swarm/src/providers) · [qualification ledger](docs/verification/rust-migration.md) |
| Schedules, worktrees, Pilot and unattended daemon restart | [Workflows](docs/specifications/rust-migration/workflows.md) · [scheduling evidence](docs/verification/rust-schedules.md) · [Pilot](docs/verification/rust-pilot.md) |
| Discord messages, worker mail and calls | [Connector](crates/swarm/src/discord) · [pinned Discord media helper](services/discord-voice) · [call qualification](docs/verification/rust-discord-personal-calls.md) |
| State migration and rollback trials | [Stopped sessions](docs/verification/rust-stopped-go-state.md) · [live Go shim adoption](docs/verification/rust-live-go-shim.md) |
| Storage inspection and cleanup | [Cleanup and retention evidence](docs/verification/rust-storage-cleanup.md) |
| Local signing and release rehearsal | [Manual preview release](docs/operations/rust-preview-release.md) |

Run the development gates locally.

```sh
cargo fmt --all -- --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo test --locked --workspace
```

The migration also uses independent Go and Rust executables, separate private state roots, native macOS and Linux, and real PTYs. The release ledger records the exact scenarios, provider versions, observed results and open gates. [Reproducible comparison harness](scripts/verification/parity/README.md) · [Migration plan](docs/specifications/rust-migration/README.md) · [Release evidence](docs/verification/rust-migration.md)
