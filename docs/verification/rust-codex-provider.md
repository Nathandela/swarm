# Rust M5 Codex provider slice

The [provider matrix](../specifications/rust-migration/workflows.md) defines the retained behavior. The shipping Go reference is `f26648d69086e329132ac7849954d2f29a895387`.

The classic path admits stable Codex 0.100.0 and later. It preserves model, sandbox, and positional prompt boundaries and applies only observed terminal signatures. At stable 0.160.0 and later the managed path binds the viewer to a dedicated native app-server. The shim checks the backend peer PID and initializes the owner observer before starting the viewer. Thread identity is learned from that private connection, requires a canonical ID and exact physical checkout, and is never inferred from prose in the PTY. Managed status accepts events only for the bound thread and rejects a completion for a different turn.

```sh
GOARCH=arm64 go build -o /tmp/swarm-codex-fixture ./cmd/swarm-fake-codex
cargo test --locked -p swarm --bin swarm codex
cargo build --locked -p swarm
python3 scripts/verification/parity_codex.py --candidate "$PWD/target/debug/swarm" --provider /tmp/swarm-codex-fixture
```

The synthetic macOS arm exercised real daemon, shim, app-server WebSocket, viewer PTY, terminal lease, exact thread adoption and worker survival across daemon SIGKILL. The staged installed CLI passed the version probe. Its app-server returned `Operation not permitted` before binding in the network-denying macOS fixture, reproduced by a direct disposable invocation without Swarm.

Managed semantic writes, request decisions, backend rejoin after backend loss, provider-level stop and exact historical resume still need qualification. The unverified native cell remains open.
