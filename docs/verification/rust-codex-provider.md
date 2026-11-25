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

The managed `message` route binds the submission to the proven shim incarnation and exact native thread. It persists uncertainty before writing `turn/start`, records native admission separately from completion, and refuses changed input under the same operation ID. The synthetic arm verifies an exact Unicode multiline payload, one native turn across replay and daemon restart, and a lost native reply that stays uncertain without retransmission. Structured `requests` reads require exact thread subscription and exclude wrong-thread requests. A confirmed request-stream join is required before any native send. A fresh thread without a rollout can accept ordinary terminal input while semantic input remains unavailable. An unresolved receipt fences later semantic operations even if the provider reports idle again. A supervisor-wide receipt binding prevents an operation ID from dispatching on a second session. Direct PTY input blocks semantic submission until a verified empty composer is available.

Full native request details and decisions, provider-level turn cancellation, backend replacement, and exact historical resume still need qualification. The unverified native cell remains open.

### Installed Linux process with an isolated model service

On Linux x86_64, the installed Codex CLI 0.172.0 started its actual App Server and viewer, joined the exact native thread and rendered an answer supplied by a loopback Responses fixture. The answer string differed from the prompt. The HTTP fixture received one request. Replacing the Rust supervisor preserved the same worker and thread; the reaper found zero survivors. The clean-home arm without fixture authentication instead showed the native sign-in screen. Neither run used a provider account.

Use the actual native executable, not a Node launcher copied without its package. The driver starts the complete scenario inside one PID and network namespace, with a private `/tmp` for the native program’s own sandbox. The release binary used on this host had SHA-256 `d1a2a2dcada0d35b3c2b34a8caa7598a8c60d72bd7adb274f93045bb5cb17748`.

```sh
python3 scripts/verification/parity_codex.py --candidate "$CANDIDATE" --provider "$NATIVE_CODEX" --native --loopback-model --scratch /dev/shm
```
