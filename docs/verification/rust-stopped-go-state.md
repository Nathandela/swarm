# Stopped Go session compatibility rehearsal

This slice rehearses a Go-created, stopped, classic Claude session on a copied private state root. Both daemons read the same schema-1 metadata as their durable source. Rust projects the Go owner roster and updates the original metadata for rename and tag. A Go rollback starts on the same copied root and reads both Rust mutations.

## Local witness

Use an isolated checkout and the Go oracle at `3173ecc08077d7646dba57441a42d3242196b5cb`. Build the candidate from the revision under test.

```bash
(cd "$GO_REFERENCE_CHECKOUT" && go build -o /private/tmp/swarm-stopped-go ./cmd/swarm)
cargo build --locked -p swarm
python3 scripts/verification/stopped_go_state.py /private/tmp/swarm-stopped-go target/debug/swarm
cargo test --locked -p swarm --bin swarm supervisor::stopped_go
```

The script allocates a new private root, supplies its own synthetic Claude executable and creates one session through the real Go owner socket. The synthetic agent announces a fixture conversation ID through its PTY. Go captures that ID and emits its original shim and exit evidence. The script stops the Go daemon, copies the state, and obtains a Go roster from the copy. The copy's endpoint ID is distinct from the source's, because both daemons derive the namespace from the state path. Rust serves precisely the same roster on the copy and the original local session ID remains stable.

The rehearsal checks explicit opt-in, cross-binary flock exclusion, corrupt-row isolation, rejection of ambiguous schema and custody, owner-only side-file validation, a swapped directory inode, a completed tag commit without an acknowledgement, and an idempotent retry after a daemon crash. The pinned Go binary then shows the Rust rename and tag on the same session. The script verifies the fixture conversation ID, opaque metadata and protected side-file bytes, including a synthetic credential that never appears in a protocol reply. The unit witness exits a child after synchronizing the staged metadata and before replacing the committed record, then confirms that the next attempt can commit over the healthy old record.

The full copied-state rehearsal passed on Darwin arm64 (macOS 27.2.1) and on a native Linux x86_64 VM. The Go oracle used Go 1.26.5, and the candidate was built with Rust 1.99.1. A unit failure injection after rename verifies that a retry resynchronizes the directory and preserves raw JSON values, including long decimals, exponent spelling, and an object whose key resembles a serde implementation marker. This evidence covers ordinary, fully stopped Claude only. The verifier uses process crashes and does not simulate sudden power loss.

## Scope of the transition

`SWARM_DAEMON_STOPPED_GO_COMPAT=1` enables the local stopped compatibility surface on the root's canonical `daemon.lock` and `daemon.sock`. The supervisor permits roster subscriptions and label edits on the imported rows. Newly launched, ordinary Rust Claude sessions use their own PTY and control path. Imported rows retain their stopped state. It leaves provider history, durable receipts and launch credentials under their existing owners. The rollback reopens the same state, so no snapshot restore erases edits made under Rust.

The wider qualification still needs intervening operation receipts, cross-language live-shim takeover, other providers and schemas, and retained history read APIs. Keep stopped compatibility isolated from installed state until the wider transition gates pass. Go daemons started with intentionally independent custom locks and sockets are outside this rehearsal.

## Rust-created classic Claude attempts

The copied-state witness also starts a new ordinary Claude attempt under Rust beside the migrated Go row. It checks live PTY content with `swarm peek`, confirms the shim survives a Rust supervisor stop and restart, and uses `swarm kill` to stop it. The fixture emits a different Claude conversation ID through the candidate's actual authenticated hook path.

After stopping the Rust supervisor, `swarm daemon export-stopped-go` validates the copied root under the canonical lock. A live Rust shim, an unmatched birth identity, missing native observation, uncertain process containment, or a destination conflict makes export refuse. A durable intent binds one owner-private staging inode inside the source attempt before any credentials are written there. A retry reuses that inode to recover a partial write, then publishes without replacing an existing directory. A retry after directory publication verifies the target or a durable Go deletion record before completing the source receipt. Missing staging and destination without deletion evidence causes an explicit refusal and leaves the original Rust evidence available. A completed export uses the Go record as the authority for later edits, so Rust reopening the same copy reflects a Go rename without displaying a duplicate row.

The witness starts pinned Go on the exported copy and checks the intervening Rust labels and native conversation ID. It then resumes the exported attempt through Go's owner protocol and inspects the provider's observed `--resume` argument for the exact previously captured conversation ID. The exported attempt has no fabricated Go shim configuration or Go native process record.

This remains a drained rollback rehearsal for ordinary Claude attempts. Linux proves the dedicated subreaper has no children after the registered worker joins. Darwin proves the owned process group is absent. The fixture does not demonstrate custody of detached Darwin descendants. Reliable launch receipts, worktrees, other providers and roles, retained history APIs, and hot cross-language shim adoption have separate qualification gates.

## Resume a stopped ordinary Go Claude conversation

With stopped compatibility enabled, the board's **r** action and the ordinary owner `resume_from` route accept a qualified stopped Go Claude attempt. The source is a fresh ordinary launch created by Go v0.29.24 with Claude Code 2.1.329. Rust checks the fresh Go arguments, saved environment, Go shim's executable observation and writer-stop records. The saved and requested account must use the same explicitly supplied API key approved in the physical default Claude home. The original physical checkout and literal native transcript must match the saved Go conversation.

Rust anchors the current Claude project settings and transcript inode at admission and rechecks them in the shim. Export requires an authenticated Claude Stop hook naming that exact transcript, successful writer containment, and the same CLI identity, key, checkout and settings. The Rust successor exports a separate stopped Go row carrying the original conversation, CLI fingerprint and launch options. The original Go attempt retains its source files.

Go v0.29.24 provides no historical Claude OAuth fingerprint or project-settings snapshot. The verified scope uses the API key recorded verbatim by Go's launcher and anchors current project authority at migration. OAuth, custom credential providers or homes, historical settings reconstruction, moves, previously resumed Go rows, initial prompts and independently managed native writers require separate qualification.

Run the fixture on macOS with the released Go v0.29.24 executable and installed signed Claude binary:

```bash
cargo build --locked -p swarm
python3 -Bu scripts/verification/stopped_go_claude_resume.py GO_V0_29_24 target/debug/swarm CLAUDE_2_1_329
```

On Linux, stage the Rust candidate as `swarm` and the released Go v0.29.24 executable as `go-owner` alongside `stopped_go_claude_resume.py`, `stopped_go_state.py`, `native_fixture_sandbox.py`, and `rust_claude_worker_input.py`. Pass that directory and the physical native executable to `sh scripts/verification/provider_native_linux.sh STAGED CLAUDE_2_1_329 stopped_go_claude_resume.py`. Both commands use isolated homes and a synthetic loopback model. The model records that each resumed request includes the preceding turns, and each new Claude response is read from the original transcript.
