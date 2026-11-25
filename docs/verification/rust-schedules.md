# Rust scheduling evidence

The Go reference is `f26648d69086e329132ac7849954d2f29a895387`. The Rust catalog retains the Go `schedules/state.db` layout, Bolt buckets and indexes, occurrence IDs, JSON records, request hashing and cross-process lock. The pinned Rust Bolt engine uses compatibility mode. The included fixture was created with the historical Go store and contains a job, revision, template, manual occurrence, bound attempt, completion receipt and release/projection timestamps. Rust tests read the original fixture bytes, preserve the receipt and attempt, append a calendar claim, and reopen the catalog. A separate Go reader reopened a Rust-edited copy. Reusing the catalog format avoids an account-state migration or a Go runtime dependency.

`scripts/verification/schedule_catalog.py` passed against both the historical Go binary and the Rust binary in separate temporary state roots. The Rust tests exercise DST gaps and folds, Lord Howe's half-hour shift, cron day combination, a century leap gap, clock rewind, host timezone travel, competing claims, revision fencing, stale manual retries, draft/paused state, private files and timer rollback. The worktree and execution fixture retains original changes on verified quota fallback, creates separate overlap worktrees, reconciles a persisted launch intent, freezes uncertain prompt delivery, rejects stale credentials and separately verifies the final message and projection after restart. With 1,200 terminal occurrences the active index stays empty. Twenty combined idle claim and active-index reads took 3.2 ms on the development macOS arm64 host.

The cold timer fixture installs only test definitions through an isolated manager shim, disables external networking, and runs the generated argument vector and daemon paths with the fixture provider missing from the timer's PATH. A Rust-owned supervisor restores the private saved launch environment. The fake Codex uses the managed App Server WebSocket protocol and a fresh thread without a seeded rollout. It exercises an exact native opening turn, separate receipt socket, MCP bridge, a pending first receipt, matching final assistant history, automation tag and verified workspace release. Authority is applied to both the dedicated App Server configuration and its exact native turn. The native turn fields were checked against a schema generated from an installed Codex using a disposable home.

The macOS arm reproduced EPERM from an exact worker group containing only its exited, unreaped leader. The shim now accepts this case only with its owned child exit proof and a positive census of exactly that PID. Failed inspection or additional members preserve uncertainty. The App Server group also has to be signalled and joined before the shim records its exit. [XNU process group census](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/proc_info.c)

```sh
cargo test --locked -p swarm
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked -p swarm
go build -o /tmp/swarm-schedule-codex-fixture ./cmd/swarm-fake-codex
python3 scripts/verification/schedule_catalog.py "$PWD/target/debug/swarm"
python3 scripts/verification/parity_schedule.py \
  --candidate "$PWD/target/debug/swarm" --provider /tmp/swarm-schedule-codex-fixture
```

The fixture proves the Rust command and protocol boundaries with synthetic inference. Real provider authentication, Linux user-manager activation and actual macOS unit activation remain release gates. The Rust executor admits the managed Codex route. Scheduled Claude and agy, native reopen of an ended run, and Automations board review require their own integrations. A Rust daemon also requires separately qualified migration of existing Go session metadata.
