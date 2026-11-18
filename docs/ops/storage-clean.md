# Clean Swarm-owned storage

`swarm clean` previews the cleanup candidates owned by Swarm. It does not start
or stop the daemon, create a state directory, or write a report file.
See the [implementation evidence](../verification/storage-clean.md) for the
reviewed source snapshot and current qualification boundary.

```sh
swarm clean
swarm clean --json
swarm clean --apply
swarm clean --apply --json
```

Preview is the default. Review the reported paths and statuses before running
`--apply`; apply performs a fresh assessment, so a preview is not a reservation
or deletion manifest. The configured diagnostic log can be trimmed while its
writers are open because cleanup preserves the file's inode. Hook payload cleanup
requires the configured daemon to be stopped and exact stopped-producer and
certificate proofs; Swarm retains hooks when it cannot prove they are safe.

Cleanup is limited to the configured diagnostic log and certified, stopped hook
prefixes. It preserves native histories, credentials and managed accounts,
projects and worktrees, handoff inputs and staging, session metadata,
replay/idempotency state, and unresolved recovery receipts. It does not sweep
generic temporary directories or compiler caches. Interrupted compaction can
leave staging files; cleanup retains files without proven disposal authority.

The report distinguishes eligible, unchanged, cleaned, retained and error
results, with coverage and observed bytes. An incomplete assessment or failed
action is reported as partial/error. Bytes observed changing on processed targets
do not guarantee filesystem free-space recovery; hard links, filesystem behavior,
and concurrent writes can affect actual allocation. No live cleanup has been
performed by this guide.

## Operation-log recovery

The daemon keeps duplicate-operation guards in `idempotency/idempotency.log` under its state directory. If a write returns a partial count or file sync fails, further mutations stop until the store reopens. Restore filesystem health before restarting the daemon. On reopen it preserves a valid final record missing its newline and removes an incomplete JSON tail.

New records fit within 4 MiB including the newline. Recovery and compaction can preserve historical records up to 16 MiB. A malformed interior record, read failure, or larger record stops startup before compaction. Preserve the complete file, including the bytes after the reported offset, for recovery. Removing the log discards the evidence used to prevent duplicate operations. A rollback to v0.26.0 with a historical record larger than 4 MiB can silently discard the later history on startup.
