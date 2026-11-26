# Stopped classic agy compatibility

The stopped-Go compatibility mode accepts an ordinary schema-1 agy session written by Go wire v2. It preserves the original Go metadata and adds Rust roster and label changes through the same Go-readable record. Live classic agy has a separate [native terminal witness](rust-live-go-agy.md).

The pinned Go agy fixture supplies a matching private launch record, absolute agy executable identity and captured conversation identifier. Go's ordinary agy shim commits its exit report after joining the PTY group and closing its hook spool. Its side files differ from the Go Claude and Codex managed writer proofs. Qualification requires the matching exit metadata and extinct shim birth identity, with no managed writer record, backend/socket custody, or scheduled ACP launch. The witness verifies live classic qualification and separate refusal for native input binding, ACP arguments, unfamiliar status-line wrappers, backend declaration, changed executable identity and a managed writer side file.

```bash
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_agy.py PINNED_GO target/debug/swarm
```

The Go reference is `f26648d69086e329132ac7849954d2f29a895387`. The script creates a synthetic agy CLI and uses the real pinned Go supervisor, shim, owner protocol and identity discovery. It copies the stopped root, compares Rust and Go's roster, verifies byte preservation of the session side files, and restarts pinned Go on the copy. Go sees the Rust rename and tag and invokes agy with the exact original `--conversation` ID on resume.

This gate covers a stopped, ordinary agy PTY and a Go-readable rollback. Scheduled ACP, managed native input, and shared custody remain separate qualification cases.
