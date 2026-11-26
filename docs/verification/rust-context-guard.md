# Context Guard owner policy

Rust reads and writes the pinned Go owner's `context-guard-settings.json` and negotiates `context-guard-settings` on the local supervisor connection. A fresh owner reports revision zero with auto-compaction disabled at 80 percent. An identical change leaves the revision and file alone. Changes require the current revision and a threshold from 40 through 95. The supervisor publishes a new revision after the settings file and containing directory are synced.

The Go → Rust → Go owner fixture exercises the private socket, a simultaneous update at one revision, unchanged settings on stale or invalid input, persisted file permissions and refusal of a corrupt document. The settings surface has a separate acceptance gate from the version-specific Codex observations and automatic compaction.

```sh
cargo build --locked -p swarm
python3 scripts/verification/context_guard_settings.py /path/to/pinned-go target/debug/swarm
```

The shim-owned Codex observer verifies the backend version and native thread, consumes exact occupancy and compaction lifecycle notifications, and projects Context Guard in the owner roster. Exact `0.151.0` can write an automatic compact call. Other characterized `0.150.x` and `0.151.x` versions only observe, and uncharacterized versions including `0.172.0` expose no guard. The durable executing record precedes the native write. Unknown or lost outcomes hold, and a continuation is armed only in the living observer that sent the compact. Settings writes and native effects share a lock. A controller, touched or unproved terminal draft, active turn, pending native request, or unresolved semantic operation prevents automatic writes. Settings revisions and native timestamps invalidate old occupancy.

The Linux fixture ran official Codex 0.151.0 against a disposable loopback model. Native occupancy reached 86 percent. A real controller lease prevented the action, and detaching without edits allowed the native compact and one continuation. Replacing the owner preserved the same shim and guard projection without another continuation. No fixture processes survived. The reducer tests also cover malformed counter shapes and duplicate JSON keys, exact thresholds and hysteresis, stale settings and observations, version limits, and recovery from persisted executing state. The provider sometimes opens an ephemeral non-persisting system thread for UI metadata, which is separated from the retained viewer thread using the exact native version and thread shape. [Pinned native temporary thread implementation](https://github.com/openai/codex/blob/rust-v0.151.0/codex-rs/tui/src/temporary_structured_request.rs)

After compaction completes, an independently admitted semantic input or a new root native turn displaces its pending continuation. A later idle notification cannot restore it. The reducer fixture includes an unrelated root turn followed by idle and separate out-of-scope child and duplicate compaction notifications.

Native refresh and Context Guard share the shim control lock. A running compaction or unacknowledged continuation prevents a maintenance stop. A durable maintenance stop fences both automatic compaction and continuation writes.
