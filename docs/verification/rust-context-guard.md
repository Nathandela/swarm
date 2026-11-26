# Context Guard owner policy

Rust reads and writes the pinned Go owner's `context-guard-settings.json` and negotiates `context-guard-settings` on the local supervisor connection. A fresh owner reports revision zero with auto-compaction disabled at 80 percent. An identical change leaves the revision and file alone. Changes require the current revision and a threshold from 40 through 95. The supervisor publishes a new revision after the settings file and containing directory are synced.

The Go → Rust → Go owner fixture exercises the private socket, a simultaneous update at one revision, unchanged settings on stale or invalid input, persisted file permissions and refusal of a corrupt document. The settings surface has a separate acceptance gate from the version-specific Codex observations and automatic compaction.

```sh
cargo build --locked -p swarm
python3 scripts/verification/context_guard_settings.py /path/to/pinned-go target/debug/swarm
```
