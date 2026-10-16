# Storage cleanup inventory — 2026-10-17

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

## VM limit

Both current SSH attempts timed out on port 22, so current state, process ownership
and installed Go paths could not be checked. Previous measurements are historical:
about 15.3 GiB Swarm state, with a later root/data free-space observation of
918 MiB / 5.5 GiB. They are not present-state measurements or cleanup approval.

## Local detailed manifest

Owner review artifact on the inspected Mac:
`/private/tmp/swarm-storage-cleanup-manifest-20261017-7pi58gvn.md`

It contains the exact candidate paths and process evidence needed for staged
cleanup. Revalidate it before acting: processes and directories can change.
