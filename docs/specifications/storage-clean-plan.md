# Safe manual storage cleanup

Status: revision 1 approved GREEN by Sol and Astra (2026-10-24).
Issue: agents-tracker-24z2. Branch: feat/storage-clean, based on c8f9b43e.

## Intent and scope

The owner requested an explicit CLI cleanup after the storage-lifecycle work.
This extends that completed plan's original no-new-command scope. Budgets remain
warning-only: cleanup never refuses new sessions or stops existing sessions.

`swarm clean [--apply] [--json]` previews by default. It cleans only the configured
Swarm diagnostic log and certified, stopped hook prefixes. It does not start or
stop the daemon, create a state directory, migrate state, or sweep arbitrary files.
No new dependencies, cleanup registry, protocol verb, quota, or background task.

Native homes/history (including old copies), credentials, projects/worktrees,
handoff inputs/staging, metadata, replay/idempotency state and unresolved recovery
receipts are preserved. Generic temporary directories and compiler caches are not
candidates. Successful/uncertain handoffs have no safe general retirement proof.
The rotator's login/native-home and minimal execution record paths remain reserved;
this feature makes no production internal/persist changes.

## Command contract

Preview runs no disk mutations, including creation, chmod, truncation, compaction,
or pending-certificate flush. Transient nonblocking locks on existing files are
permitted, released before return. Missing state/logs are a successful no-op.
Use direct CLI dispatch and clientConfig, never EnsureDaemon or daemon.Open.
Only --apply authorizes mutation; no force switch or age-based exceptions.

Report exact candidate paths, kind, status, reason, observed logical/allocated
bytes and eligible logical prefix bytes where proven. Status distinguishes
eligible, unchanged, cleaned, retained, and error. Coverage is explicit. Apply
runs fresh assessment; preview is neither a deletion manifest nor a reservation.
Do not promise physical free space: report observed allocated-byte change on
processed targets, including possible growth and failures after partial progress.
No whole-state second scan is needed. Unknown/replaced final identity is unknown,
never counted as reclaimed. No report file is written.

Exit 0 for a complete assessment including expected retained/busy/unproven items;
1 for IO/mutation errors or incomplete inventory; 2 for invalid flags/arguments.
A partial result lists completed work and errors without rolling back safe cleanup.
JSON is one report object; text uses the same results. Both explicitly name the
protected categories and that hooks require the configured daemon to be stopped.
Release all singleton/producer custody before formatting or writing output, so a
blocked pipe cannot hold daemon startup off. Output write errors produce exit 1. Entry/control characters are safely quoted
in text. Output contains no file bodies, hook payloads or credentials.

## Ownership and concurrency

The configured diagnostic log is the only log candidate: shims share this path.
Do not invent shim.log targets. Add an existing-only diagnosticlog inspection /
cleanup seam that reuses validation and the current >8 MiB retention policy.
Preview opens read-only; apply opens existing write/append, never O_CREATE or
chmod. Require regular, current-UID, single-link file, private real parent, matching
path/descriptor. Apply revalidates and truncates that same inode; existing append
writers survive. Files at/below the cap remain unchanged. Refuse unsafe aliases.

Hook work requires custody of the actual configured daemon singleton lock opened
without creation/chmod and acquired nonblocking, held through certificate reads
and each compaction. Missing, unsafe or busy lock retains hooks. Do not infer
safety from a missing socket or PID file. This conservatively avoids concurrent
daemon cursor/reset/deletion work; no automatic stop and no daemon RPC.

Discover only immediate real session directories under the configured real,
current-UID, non-group/world-writable state root; never recurse into arbitrary
state. Retain symlink directories and unsafe parents. Use bounded reads for the
existing meta.json, hook.fold and hook.fold.cert; validate session ID matches its
directory and explicitly known schemas 0/1/3/4; retain schema 2, negative and
future values. Metadata supplies only ID and recorded process identity; do not
copy runtime validators or infer cleanup authority from metadata. Use the durable certificate exactly; never
manufacture authority from a numeric cursor. Require cursor/certificate agreement.
Closed producer proof or exact recorded dead PID/start, incarnation and nonce,
producer lifetime lock, regular/single-link/UID and physical spool identity remain
mandatory, as in existing stopped retirement. Live or unknown producers are retained.

Share the existing proof and compaction implementation; expose only the minimal
inspection/apply seam needed by the CLI. Preview must not call mutating spool-open
or drainer retirement paths. Bound the spool descriptor read to 64 MiB under the
producer lock, retaining larger files; cap every read, not just an earlier stat.
Reuse the clean-record-boundary parser for a prefix estimate. Apply preserves all
bytes after the certified boundary, including torn tails, with existing floor /
temp / fsync / rename / directory-sync / producer-proof ordering. Add source inode
and parent identity revalidation immediately before destructive replacement.
Proof-update failure after replacement is a failed/uncertain action; remaining
payload stays protected and observed bytes are reported honestly. Existing stopped
retirement callers retain their proof requirements and receive the shared safety
fixes. Concurrent cleaners cannot both acquire custody.

These fences protect normal concurrent Swarm lifecycle operations and accidental
aliases/replacements. They do not promise defense against a malicious process with
the same UID rewriting private directories or lock files between syscalls.

## Bounded work and failure behavior

Candidate discovery reads immediate entries incrementally, up to 10,000 entries
and a cooperative two-second scan budget. Explicitly report incomplete inventory.
A started bounded file action may finish beyond that deadline; filesystem calls
are not forcibly preempted. No recursive measurement of native histories. A valid
session's metadata read is bounded (1 MiB); certificate/proof sidecars keep their
existing small bounds. Missing/legacy/ambiguous proof is retention, not corruption
repair. Unsupported or malformed metadata is retained visibly.

ENOSPC can prevent hook compaction because safe replacement needs temporary space;
return a visible error while preserving unread suffixes. Log truncation needs no
replacement payload and can help first. Run the log action before hooks. Do not
retry indefinitely or delete additional data after a failure to make room.

## Implementation and review lanes

Root owns CLI orchestration, configured singleton custody/discovery, integration
fixtures and documentation. Luna owns the small diagnosticlog existing-only seam
and focused tests. Sol owns the shared shim hook inspection/retirement seam and
safety tests, plus minimal skeleton certificate sharing if needed. Keep changes in
these lanes disjoint. Astra reviews boundaries and complete implementation; Sol
cross-reviews root integration. Require both Sol and Astra to approve this plan
before code implementation; fix review findings and repeat review on the final
source before qualification is declared complete.

## Falsification and qualification

Tests first: missing root remains missing; preview preserves bytes/modes/entries
and does not start a daemon; malformed args cannot mutate. Exercise configured
custom log/lock paths. Verify real nonzero oversized logs shrink in place while
append writers continue; under-cap logs, symlinks/hardlinks/FIFOs and changed
identities remain intact. Hooks: applied certified prefix removed with exact
unapplied/torn suffix retained; busy daemon/producer, missing/legacy/stale proof,
wrong identity, corrupt/future metadata and oversized spool retained. Preview and
apply agree in quiescence; changing state between them is revalidated. Concurrent
cleaners, source/path replacement, cursor mismatch, ENOSPC and interruption at
compaction durability boundaries cannot lose an uncertified suffix or receipts.
Protect representative histories/credentials/handoff/recovery fixtures byte-for-byte.
Test scan exhaustion, bounded malformed reads, error output and writer failures.

Run focused normal/race tests as lanes land, then build/vet/lint/full tests and
race coverage for goroutine packages on native macOS and Linux, reusing shared
build caches and supervised scratch. Record exact red/green commands, failures,
fixes and platform limits under docs/verification/storage-clean.md. Never clean
real user state as part of qualification. Commit/push this isolated branch and
Beads updates; deployment and merging the rotator branch are separate work.
