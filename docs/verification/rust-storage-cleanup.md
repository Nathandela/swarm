# Storage cleanup and live log retention

The Rust `swarm clean` entry point uses the operator's configured state directory, lock and diagnostic-log path. The default is a read-only preview. Both text and JSON output report before/after logical and allocated sizes. The log is truncated in place only when its logical size exceeds 8 MiB. A stopped hook spool requires the owner lock, both Go generation locks, a matching committed fold certificate, the recorded producer, and the original device/inode before it can lose a prefix. The command rechecks directory and lock identities before replacing files.

Run the two local comparisons from the repository root, passing absolute Go and Rust executable paths.

```sh
python3 scripts/verification/parity_clean.py "$GO_ORACLE" "$RUST_CANDIDATE"
python3 scripts/verification/parity_log_retention.py "$GO_ORACLE" "$RUST_CANDIDATE"
```

Native macOS arm64 and Linux x86_64 passed the public cleanup comparison. With the singleton held, the log was reduced and the spool was retained. Both journal locks, a mismatched cursor certificate, a future metadata schema and a lock inside the session directory prevented spool mutation. Once the fixture released its locks and restored valid evidence, each implementation removed exactly the certified record and retained the full unread record, torn tail and recovery receipt. Repeating apply made no change. Neither executable started a daemon. A missing state root remained absent.

Both hosts also exercised live writers. The supervisor reduced a sparse oversized log while keeping its inode. A surviving PTY shim reduced the same log after its daemon exited. Moving the log and placing a different oversized file at the old pathname left the new file untouched through the next retention interval. The test restored the original binding and stopped the exact fixture worker. These trials use generated data.
