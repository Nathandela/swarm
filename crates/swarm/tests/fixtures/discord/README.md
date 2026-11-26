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

The agy fixture also exercises the private guild effect gate. Run both arms with
`python3 scripts/verification/rust_agy_native_input.py target/debug/swarm` and the same command with `--guild`.
The guild arm simulates permission withdrawal at the effect check. It verifies the shim refuses before sending and the same operation stays refused.

A guild worker uses connector-owned REST checks immediately before the native write. The connector seals the exact source and reviews current owner and bot grants, including private-thread membership. Relevant Gateway changes delivered during an in-flight operation cancel it locally. Discord and the provider have independent APIs. A remote permission change after the last REST response and before the native write can still race that write. A submitted or uncertain write retains its original operation ID for readback.

`hermes-0.22.5-queued.json` preserves the relevant field types from a local
Hermes v0.22.5 loopback probe of the pinned Rust lane. Session, profile,
operation and admission identifiers are redacted. In particular the epoch is
a positive number and the queued execution generation is explicitly null.

## Ordinary worker mailbox

The text fixture launches a real Rust worker through the owner protocol. The
worker CLI queues a question, the active manager reads the bound mailbox and
replies through its authenticated tool, and the worker reads the answer back.
An exact launch retry returns the same worker, a wrong proof is refused,
and a repeated question or reply read preserves its original receipt.

Build the fake app-server from the f266 source as above, using
`codex-worker-mail.patch` instead of `codex-manager.patch`. Run the native Rust
oracle with `SWARM_NATIVE_MAIL_FIXTURE=1`, `SWARM_FAKE_CODEX_MANAGER_PROOF=1`,
and the resulting fake app-server as `SWARM_FAKE_CODEX`.

For the storage comparison, copy
`scripts/verification/worker_mail_oracle/main.go` into the pinned Go source
under the same relative path and build it there. Run
`SWARM_GO_WORKER_MAIL_FIXTURE=/path/to/oracle cargo test --locked -p swarm go_and_rust_share_worker_notes -- --ignored`.
The fixture shares one isolated bbolt file across sequential Go and Rust operations.

The text path supports worker notes and answers during the proved parent turn.
Post-turn manager wake and file-bearing notes have separate qualification.
