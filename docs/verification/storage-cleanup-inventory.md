# Storage cleanup inventory — 2026-10-17

This is the initial read-only inventory. The later
[v0.18.0 rollout and cleanup](lightweight-storage-rollout.md) records deployment,
executed removal batches and retained material; its results supersede these
pre-cleanup allocation and authorization snapshots.

Read-only follow-up for `agents-tracker-3bxu`. The checkpoint was
2026-10-16 22:00:01 UTC; allocation and process scans ran around it while the
machine remained live. These are snapshots, not a consistent backup or proof of
reclaimable bytes. No user files were moved/deleted and no native writers stopped.

## Current Mac allocation

| Class | Allocated GiB | Disposition |
|---|---:|---|
| Swarm parent state tree | 75.140 | Includes session workspaces and control stores |
| Actual daemon state root | 75.089 | Includes accounts, remote and journal stores |
| Largest legacy workspace | 59.569 | 1,045 immediate subdirectories; preserve unique work |
| Second legacy workspace | 12.754 | Live web/node cwd references; preserve active work |
| Third legacy workspace | 1.412 | Many live native/backend cwd references |
| Fourth workspace | 0.535 | Unclassified |
| Existing structured journal, included above | 0.155 | New live-only migration handles this separately |
| Native Codex state, outside Swarm | 17.936 | Provider-owned history/state |
| Native Claude state, outside Swarm | 3.114 | Provider-owned history/state |

The data volume reported 317.764 GiB available. Earlier 226–306 GiB Swarm
measurements are historical and changed during unrelated live activity; no
reduction is attributed to the source fixes or this investigation. APFS shared
blocks, hard links and open deleted files also limit direct `du` interpretations.

## Cache candidates

| Recognizable material | GiB | Evidence |
|---|---:|---|
| 127 Rust target roots | 8.604 | Standard Rust cache markers |
| One Go build cache | 0.371 | Hash buckets, README and trim markers |
| 15 Cargo registry trees | 2.053 | Download/index/source cache layout |
| One Go module cache | 0.995 | Explicit module-cache subtree; local edits unchecked |
| Two pnpm stores | 1.133 | Standard v10 stores; local package edits unchecked |
| Classified candidate total | 13.157 | No candidate proved writer-free |
| Remaining allocation | 61.983 | Unclassified, including possible unique work |

The local detailed manifest lists 13 representative leaf targets, exact paths,
process observations and preservation dependencies. The public summary omits
private project paths and provider thread identifiers.

## Actual migration constraints

- Several rows marked `exited` or `lost` still have live process cwd references.
  Two exited rows had 17 and 12 references respectively. A missing state directory
  also still had three Swarm process cwd references. Status alone is not safe
  deletion evidence.
- Native thread metadata still points into the largest legacy workspace. Its
  original intended project directory no longer exists. A recovery destination
  and preservation of unique source/output are required before relocation.
- Candidate compiler/package caches remain subject to current writer and ownership
  checks; recognizable layout alone is not deletion authorization.
- Confirming permission to interrupt active writers, revalidating exact process
  identities, preserving unique work, removing approved cache targets and
  measuring the result are separate steps. No bulk deletion was performed.

## Current VM allocation

Earlier SSH timeouts and the 15.3 GiB / 918 MiB root-free / 5.5 GiB data-free
observations are historical. Read-only SSH succeeded on 2026-10-17. The
allocation/free-space checkpoint was
06:17:56 UTC; metadata and process coverage were sampled between 06:15:57 and
06:17:21 UTC. The targeted Swarm-tree `du` completed without errors.

| Class | Allocated GiB | Disposition |
|---|---:|---|
| Swarm parent state tree | 14.813 | Supersedes the historical 15.3 GiB observation |
| Actual daemon state root | 14.813 | Includes session rows and control stores |
| 236 session rows | 13.181 | Source, outputs and caches remain unclassified |
| Accounts | 1.164 | Preserve provider/account state |
| Structured journal | 0.434 | Existing legacy capture; no migration run here |
| Other directories without metadata | 0.034 | Ownership and contents unchecked |

Remote state used 24 KiB and the idempotency directory 4 KiB. Other state-root
files and directory overhead used about 1.05 MiB. The eight largest session
directories used 2.003, 1.678, 1.158, 1.155, 1.138, 1.084, 0.699 and 0.615 GiB;
their private identifiers and project names are omitted. Root/data free and
available space was 1.575 / 1.587 GiB respectively.

An additional metadata-only checkpoint at 06:19:51 UTC found one `daemon.log`
using 1,011,712 allocated bytes, 235 `hooks.spool` files using 8,560,640 bytes,
and 537 known hook-spool sidecars using 1,232,896 bytes. These amounts are already
included in the totals above. No `remote-activity.log` or numbered log rotations
were present at the known parent/state-root paths; file contents were not read.

The roster contained 170 exited, 64 lost and two running rows. No readable
process cwd pointed into Swarm state. Of 41 same-user processes, 36 allowed cwd
inspection; five denied it and had other process names, rather than native
CLI, Swarm or node names. These observations do not prove writer absence: cwd
inspection misses open files and access-denied processes. No session directory
is declared safe to delete, and no VM source/cache byte count is proved
reclaimable. The RAM validation workspace, native CLIs and user data were not
modified by this inventory. No detailed VM artifact was retained.

The later [native validation](lightweight-storage-linux.md) completed at
07:11:06 UTC. The machine remained live: a subsequent metadata-only observation
found 239 rows (170 exited, 64 lost, five running), with new Claude rows outside
the private test roots. At 07:18:48 UTC, after task RAM artifacts were removed,
root/data free space was 0.472 / 0.391 GiB. These changing observations do not
establish a cause, reclaimable bytes or a cleanup result; existing data was not
purged and the new binary was not installed.

## Local detailed manifest

Owner review artifact on the inspected Mac:
`/private/tmp/swarm-storage-cleanup-manifest-20261017-7pi58gvn.md`

It contains the exact candidate paths and process evidence needed for staged
cleanup. Revalidate it before acting: processes and directories can change.
