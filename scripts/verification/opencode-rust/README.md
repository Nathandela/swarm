# Installed OpenCode qualification for Rust Swarm

Both probes require installed OpenCode 1.19.3 and a Rust Swarm binary built from the revision being qualified. The probes verify the owner endpoint derived from a fresh state root before issuing commands. They use private test profiles and leave their scratch records for inspection.

## Linux

The runner needs Python 3, bubblewrap with working user namespaces, and the installed native binary at `$HOME/.local/bin/opencode`. Stage the compiled Linux binary and two probe files in a new RAM directory. It mounts selected system dependencies read-only and runs Swarm, the TUI, and the fixture in one private PID and network namespace.

```sh
run_root=$(mktemp -d /dev/shm/oc.XXXXXXXX)
cp /path/to/linux/swarm "$run_root/swarm"
cp scripts/verification/opencode-rust/loopback.py scripts/verification/opencode-rust/run-linux.sh "$run_root/"
sh "$run_root/run-linux.sh" "$run_root" qualification
```

The probe verifies that IPv4 and IPv6 external egress are unavailable and that loopback works before starting the daemon. It supplies scripted model responses for the initial prompt, semantic input, and native permission and question requests. It exercises receipt recovery across daemon restart, exact operation replay, request-content hashing, rejected grouped answers, draft fencing, history pagination, and exact-root resume. The final JSON line reports the completed assertions and number of fixture model calls.

## macOS

Run with the installed OpenCode on `PATH` and an absolute Rust Swarm binary path. The runner starts with a clean environment and applies a macOS sandbox-exec profile to test daemons and their workers.

```sh
scripts/verification/opencode-rust/run-macos.sh "$PWD/target/debug/swarm"
```

The macOS probe exercises the no-model worker lifecycle with outbound IP denied. It checks native binding, SSE-backed idle, history and request reads, daemon rejoin, worker stop and resume. The resume assertion compares both native creation identity and backend incarnation.

For pinned JavaScript fixtures and Rust unit and owner tests, run:

```sh
node --test internal/opencodebridge/tui.test.mjs
cargo test --locked --all-targets
```
