# Stopped classic agy compatibility

The stopped-Go compatibility mode accepts an ordinary schema-1 agy session written by Go wire v2. It preserves the original Go metadata and adds Rust roster and label changes through the same Go-readable record. Live classic agy has a separate [native terminal witness](rust-live-go-agy.md).

The pinned Go agy fixture supplies a matching private launch record, absolute agy executable identity and captured conversation identifier. Go's ordinary agy shim commits its exit report after joining the PTY group and closing its hook spool. Its side files differ from the Go Claude and Codex managed writer proofs. Qualification requires the matching exit metadata and extinct shim birth identity, with no managed writer record, backend/socket custody, or scheduled ACP launch. The witness verifies live classic qualification and separate refusal for native input binding, ACP arguments, unfamiliar status-line wrappers, backend declaration, changed executable identity and a managed writer side file.

```bash
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_agy.py PINNED_GO target/debug/swarm
```

The Go reference is `f26648d69086e329132ac7849954d2f29a895387`. The script creates a synthetic agy CLI and uses the real pinned Go supervisor, shim, owner protocol and identity discovery. It copies the stopped root, compares Rust and Go's roster, verifies byte preservation of the session side files, and restarts pinned Go on the copy. Go sees the Rust rename and tag and invokes agy with the exact original `--conversation` ID on resume.

This gate covers a stopped, ordinary agy PTY and a Go-readable rollback. Scheduled ACP, managed native input, and shared custody remain separate qualification cases.

## Rust-created stopped agy

An ordinary fresh agy session launched under stopped-Go compatibility can be drained and exported alongside the imported Go rows. Export requires the joined Rust shim's stop proof and the exact observed native conversation, physical checkout and original home. It checks the native transcript before any row is published and revalidates the physical source before rename. Go gets a stopped compatibility record and no synthetic shim or writer side files.

Pass `--cold` to `live_go_agy.py` to run this part of the native fixture. The official agy 1.5.17 executable completed it on Linux x86-64 and macOS arm64. A stopped daemon with a surviving Rust worker and an absent native transcript each blocked export. Restoring the original transcript admitted export. The pinned Go owner then resumed the exact Rust-created native conversation and added a new real loopback model turn to that original transcript.

## Rust resume of a stopped Go-created conversation

For a stopped Go agy row, the Rust owner returns a custody error on ordinary `resume_from` and points the operator to Go. The pinned Go shim records the original argv, the lexical workspace path and filtered launch environment, and a startup observation of the executable's installation fingerprint. The Go row carries the captured native conversation ID and the shim's birth identity. A matching exit report and absent shim and hook listeners establish completion of the classic Go PTY lifecycle. [Go installation observation](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/internal/shim/cli_identity.go) · [Go exit ordering](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/internal/shim/shim.go)

The launch and observation records identify the resolved launcher. Its metadata fingerprint covers the resolved file's path, inode and stat fields. Go retains `HOME` as a path and filters Gemini credentials and endpoint overrides from the ordinary child environment. The compatibility record leaves historical physical home, checkout and native account identity unbound. In the official agy 1.5.17 fixture, the native conversation summary has an empty `workspace_uris` and `default-cli-project`. The native last-conversation cache associates the UUID with the workspace path, which remains the same when the checkout at that path is replaced. These records establish enough for a roster, label edits, and Go's existing resume convention. Automatic launch under a new Rust shim requires stronger account and checkout custody. [Go environment allowlist](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/internal/persist/env.go)

Run the [official native fixture](../../scripts/verification/live_go_agy.py) with `--stopped-source` to exercise the boundary after graceful exit. Rust refuses against both the original checkout and a different inode at the identical path. The source side files, native transcript and loopback model call count stay intact through both requests. With the original checkout restored, pinned Go supplies the captured UUID as `--conversation` and appends a distinct user and assistant turn to the original native transcript. The fixture's local loopback credentials come from its scratch-only executable wrapper.
