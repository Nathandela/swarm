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
