# Pinned Go manager wire fixture

Use a private temporary copy of the Go reference. Build the daemon before applying the fake provider patch. The enhanced fake app-server emits a joined native session, an exact user-message echo and one deliberately scoped manager tool call.

```sh
oracle=$(mktemp -d /tmp/swarm-discord-oracle.XXXXXX)
fixture=$(pwd)/crates/swarm/tests/fixtures/discord/codex-manager.patch
git archive f26648d69086e329132ac7849954d2f29a895387 | tar -x -C "$oracle"
(cd "$oracle" && go build -o "$oracle/swarm-oracle" ./cmd/swarm)
(cd "$oracle" && git apply --unidiff-zero "$fixture" && go build -o "$oracle/codex-manager-fixture" ./cmd/swarm-fake-codex)
SWARM_GO_ORACLE="$oracle/swarm-oracle" \
SWARM_FAKE_CODEX="$oracle/codex-manager-fixture" \
SWARM_FAKE_CODEX_MANAGER_PROOF=1 \
cargo test --locked -p swarm discord::manager -- --ignored
```

The test starts the Go daemon with a separate socket, synthetic account metadata and a cleared environment. It verifies an installed, scoped launch receipt, an exact provider turn, the published tool's Go-authorized call proof and stale-turn refusal. No Discord credential or provider network connection is involved.

Use the same fake provider against the native Rust supervisor by building the Rust binary and selecting it with `SWARM_RUST_MANAGER_ORACLE` in place of `SWARM_GO_ORACLE`. This arm also invokes the real Rust MCP bridge, confirms a single scoped publication through synthetic Discord acknowledgements, and rejects unmanaged input and a second manager reservation.

```sh
cargo build --locked -p swarm
SWARM_RUST_MANAGER_ORACLE="$(pwd)/target/debug/swarm" \
SWARM_FAKE_CODEX="$oracle/codex-manager-fixture" \
SWARM_FAKE_CODEX_MANAGER_PROOF=1 \
cargo test --locked -p swarm discord::manager -- --ignored
```

The Rust arm retains the installed Codex workspace policy. The Go enrolled manager arm applies its explicit full-access manager policy. This fixture qualifies manager custody and the publication path. Native permission-policy parity remains open.
