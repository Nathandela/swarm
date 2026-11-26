# Retained Go Discord state

The retained-state witness uses original Go personal and teammate configurations and their bbolt stores, built by Go at f26648d69086e329132ac7849954d2f29a895387. The fixtures include persisted audience exclusions, a settled Go delivery with exact replay indexes, and uncertain delivery and ingress cases. A synthetic credential stays in its original file and the test makes no Discord calls.

`swarm discord migrate-state --dry-run --state-dir DIR` inventories supported private Go evidence without opening the ledger writable. For a qualified disabled installation, the command without dry-run commits a dormant retention record bound to the exact installation, directory, config and database digest. `--rollback` marks the transition rolled back without rewriting the original Go config, database or credential. Rust grants neither Gateway nor VM project authority. A writer lock, unresolved effect, unknown ledger data or changed source leaves authority with Go.

`swarm discord migrate-supervisor --dry-run --state-dir DIR` checks both live owner namespaces and the original connector selector. It binds the original Go socket peer to its kernel birth record and verifies the destination Rust peer executable and held owner lock. For an original Go connector, the apply command updates Go-readable home and prior-owner coordinates while leaving the connector database and earlier selector in place. Go retains its earlier routing for previously bound discussions. The witness persists a teammate discussion through the original Go binding transaction before the move. After the move, the pinned Go runtime resolves that same discussion to the original owner, and a newly bound discussion to the new owner. Both sockets report their respective running synthetic agent sessions throughout. The pinned Go ledger still reads the same completed and uncertain delivery outcomes. If config publication finishes before the new selector is written, the same command completes selection on retry. Connector runtime startup and existing Rust Inbox migration require separate qualification.

In a disposable copy of the pinned Go repository, copy the fixture program to `cmd/swarm-discord-state-fixture/main.go`, the bindings fixture to `internal/discord/migration_fixture.go`, and the route fixture to `internal/teammatemanager/migration_fixture.go`. Build `./cmd/swarm-discord-state-fixture` in that copy and run the witness with both binaries.

```bash
python3 scripts/verification/discord_retained_state.py PINNED_GO RUST PINNED_GO_FIXTURE
```
