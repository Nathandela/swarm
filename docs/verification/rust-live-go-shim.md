# Live Go shim terminal adoption

This local-only rehearsal uses the pinned Go oracle at `3173ecc08077d7646dba57441a42d3242196b5cb` and a synthetic classic Claude process. Both supervisors operate on one fresh private fixture root. The live socket, metadata and provider process stay at their original path throughout. The worker continues to own the same PTY while either supervisor is absent.

```bash
go build -o /private/tmp/swarm-live-go ./cmd/swarm
cargo build --locked -p swarm
python3 scripts/verification/live_go_shim.py /private/tmp/swarm-live-go target/debug/swarm
```

The Rust supervisor accepts live Go state only with both `SWARM_DAEMON_STOPPED_GO_COMPAT=1` and `SWARM_DAEMON_LIVE_GO_COMPAT=1` on the canonical Go lock and socket. Admission checks the original private directory, schema-1 classic Claude role, wire version 2, Go launch evidence and native custody, both process birth identities, and the operating-system peer identity of the Go shim and hook sockets. Version skew and a concurrently running Go supervisor cause refusal before Rust binds its owner socket.

The witness uses Rust `swarm peek` for a one-shot grid, and sends attach, binary input and resize through the owner protocol. A second attach takes over the terminal and blocks the stale writer. A real Rust board on an outer PTY attaches, delivers input to the existing worker, and detaches. Pinned Go then restarts against the same root and displays the same session, native conversation, exact worker epochs and final grid.

A genuine authenticated Go callback posted while Rust owns the terminal is retained by the live Go hook spool. The Rust handoff preserves both the original Go metadata bytes and the hook fold cursor, so pinned Go can observe and fold that callback after rollback.

This slice qualifies terminal custody and hook preservation for the existing Go shim. It leaves Go hook ingestion and unsolicited exit reconciliation to the Go rollback supervisor. New Rust launches while a Go shim is active, other provider custody models, Go clients on Rust-created shims, and the remaining interoperability combinations have separate gates.
