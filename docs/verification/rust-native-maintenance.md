# Native session maintenance

Reference `f26648d69086e329132ac7849954d2f29a895387`, especially `cmd/swarm/{cli_refresh,relogin}.go`, `internal/skeleton/{authwatch,cli_refresh,cli_identity}.go`, and `docs/ops/cli-session-refresh.md`.

Rust reads and writes the independent Go policy filenames from the local command. The status command never starts a daemon. An unresolved legacy recovery record freezes new actions and retains the original file. Rust records its own per-source claims with only launch selectors, executable identity and account digests. The shim verifies an exact idle conversation, the attach lease and pending input boundary, then writes the stop proof before signaling. A replacement binds the original source and exact installation before start. An existing replacement remains the only candidate after a missed acknowledgment.

The macOS fixture replaced a private agy 1.4.0 executable with 1.4.1. Two watcher passes observed the stable newer release and issued a guarded stop. The test killed the daemon after the shim signaled the source. The replacement daemon rejoined the exact agy conversation once. The fixture also holds the first successor before its status callback so a second daemon restart can exercise reservation recovery before acknowledgment. The ordinary Codex case used the retained synthetic App Server fixture from `6a673aca`, with the native `cwd` field added to its synthetic `thread/resume` response. Dry-run left the initial roster untouched. Same-account force recorded a stop and joined the exact original UUIDv7 rollout under the local synthetic account.

```sh
python3 scripts/verification/rust_native_refresh.py target/debug/swarm
python3 scripts/verification/rust_native_refresh.py target/debug/swarm /path/to/codex-fixture
python3 scripts/verification/rust_native_refresh.py target/debug/swarm /path/to/codex-fixture --changed-account
```

After an externally changed Codex identity, the source shim opens a bounded read-only native App Server, reads the original thread and confirms its working directory. It shuts down the probe before the old process can stop. Rust holds when the thread, checkout, profile, identity or installation proof changes. The fixture withdrew the original rollout, confirmed the old worker remained live, restored it and recovered once under a second synthetic native identity. Dry-run did not start a CLI probe. OpenCode replacement is refused. Native agentapi, scheduled, managed, isolated-worktree and unproved historical sessions stay outside automatic recycling. No live account was modified.

Pinned Go and Rust returned the same local policy status with no daemon. Each accepted independent enable and disable controls. Auth files in the provider fixture contained synthetic identifiers only.
