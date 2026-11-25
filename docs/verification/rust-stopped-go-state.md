# Stopped Go session compatibility rehearsal

This slice rehearses a Go-created, stopped, classic Claude session on a copied private state root. Both daemons read the same schema-1 metadata as their durable source. Rust projects the Go owner roster and updates the original metadata for rename and tag. A Go rollback starts on the same copied root and reads both Rust mutations.

## Local witness

Use an isolated checkout and the Go oracle at `3173ecc08077d7646dba57441a42d3242196b5cb`. Build the candidate from the revision under test.

```bash
go build -o /private/tmp/swarm-stopped-go ./cmd/swarm
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_state.py /private/tmp/swarm-stopped-go target/debug/swarm
cargo test --locked -p swarm --bin swarm supervisor::stopped_go
```

The script allocates a new private root, supplies its own synthetic Claude executable and creates one session through the real Go owner socket. The synthetic agent announces a fixture conversation ID through its PTY. Go captures that ID and emits its original shim and exit evidence. The script stops the Go daemon, copies the state, and obtains a Go roster from the copy. The copy's endpoint ID is distinct from the source's, because both daemons derive the namespace from the state path. Rust serves precisely the same roster on the copy and the original local session ID remains stable.

The rehearsal checks explicit opt-in, cross-binary flock exclusion, corrupt-row isolation, rejection of ambiguous schema and custody, owner-only side-file validation, a swapped directory inode, a completed tag commit without an acknowledgement, and an idempotent retry after a daemon crash. The pinned Go binary then shows the Rust rename and tag on the same session. The script verifies the fixture conversation ID, opaque metadata and protected side-file bytes, including a synthetic credential that never appears in a protocol reply. The unit witness exits a child after synchronizing the staged metadata and before replacing the committed record, then confirms that the next attempt can commit over the healthy old record.

The full copied-state rehearsal passed on Darwin arm64 (macOS 27.2.1) and on a native Linux x86_64 VM. The Go oracle used Go 1.26.5, and the candidate was built with Rust 1.99.1. A unit failure injection after rename verifies that a retry resynchronizes the directory and preserves an opaque decimal beyond IEEE 754 precision. This evidence covers ordinary, fully stopped Claude only. The verifier uses process crashes and does not simulate sudden power loss.

## Scope of the transition

`SWARM_DAEMON_STOPPED_GO_COMPAT=1` enables the local stopped compatibility surface on the root's canonical `daemon.lock` and `daemon.sock`. The supervisor advertises roster subscriptions and allows label edits. It refuses launch, resume, attach, deletion and nonqualified state. It leaves provider history, durable receipts and launch credentials under their existing owners. The rollback reopens the same state, so no snapshot restore erases edits made under Rust.

The qualification still needs Rust-created attempts and intervening launch/receipt operations, cross-language live-shim takeover, other providers and schemas, retained history read APIs. Keep stopped compatibility isolated from installed state until the wider transition gates pass. Go daemons started with intentionally independent custom locks and sockets are outside this rehearsal.
