# Lightweight storage release and rollout

2026-10-17 · `v0.18.0` · source `c9db413b`

The owner authorized release, deployment on the Mac and `tf_nathan`, and cleanup
of reviewed legacy Swarm buildup. The release and both supervisor deployments
completed. Final Mac checks found a Codex resume backend regression, so a patch
release is required before rollout sign-off. This records the checked cleanup
and runtime checkpoint; the
[whole fixing plan](../specifications/lightweight-storage-plan.md) remains open.

## Release and deployment

[Release run 39014164018](https://github.com/Nathandela/swarm/actions/runs/39014164018)
completed successfully with all 17 jobs passing. The
[v0.18.0 release](https://github.com/Nathandela/swarm/releases/tag/v0.18.0) points
to `c9db413b`; its published notes describe the live-only history tradeoff and
remaining limits. The release gates supplement the
[native Linux verification](lightweight-storage-linux.md); their documented
skips and separate full-Mac-suite baseline failures remain limitations.

Both machines now run the v0.18.0 supervisor. The Mac used its existing Homebrew
ownership and cask bookkeeping. Unattended convergence deferred with exit 2;
the authorized daemon-only restart used the existing private saved environment,
loaded in memory without displaying its values. Its previously running gateway
was restarted. The initial doctor report showed the matching daemon and no
failures. Later automatic Codex refreshes exposed four running sessions without
their structured backend; the final v0.18.0 check therefore had four failures,
seven OK findings and two warnings. This is tracked in `agents-tracker-yufd`.

The VM used the standard signed release staging step. Its root-owned binary
pair was installed atomically per file, while the supervisor remained owned by
the ordinary Ubuntu user. Upgrade and daemon singleton locks fenced the offline
journal transition and installation. The previously inactive gateway remained
inactive. Normal rollback slots retain v0.17.10; no older journal snapshot may
be restored after the new supervisor has accepted writes.

Native agents were not terminated for these daemon restarts. On the Mac,
kernel `sysctl` process-start tokens proved that six identity-matched live shims
predated the replacement supervisor. An initial probe using incorrectly
lowercased status fields was discarded; it is not evidence of zero live shims.
On the VM, all five existing live shim identities were adopted into the running
roster.

## Journal transition

| Host | Before | After |
|---|---|---|
| Mac | 167,600,128 allocated bytes | 983,697 content bytes in 16 segments |
| VM | 477,250,520 input bytes in 37 segments | 983,066 transition-output bytes in 16 segments; 985,470 bytes after live startup writes |

The Mac comparison uses allocated bytes before and content bytes after. These
figures are not an exact filesystem-free-space delta. The VM's cursor remained
679,838 through the offline transition and advanced to 679,843 after startup.
Generic conversation payloads were absent from the resulting durable journal.

The VM legacy journal contained 39 newline-terminated invalid physical lines.
Each had an incomplete first record followed by an independently valid complete
record. Offline repair preserved all 39 intact suffixes, including two capability
transition records, before applying ordinary production control retention.
The incomplete prefixes totaled 31,165 bytes; intact suffixes totaled 36,147
bytes. No incomplete record was reconstructed.

The repair used an immutable private RAM backup and the shipped Go journal
decoder and production migration options. Classified counts, byte totals,
control coordinates, cursor bounds and input hashes were checked before output
was accepted. Retained controls were verified against decoded source records.
The shipped fail-closed decoder was not weakened. Before snapshotting, privileged
inspection covered 218 processes and found no journal descriptor, mapping or cwd
references and no denied inspections. Accepted-action receipts, sequence custody,
account records and provider-native histories were not purged.

## Reviewed cleanup

The VM permanently removed one verified plain Go build-cache root and 512
stopped-writer legacy capture files. Their recorded allocated size was
1,497,186,304 bytes. The batch completed after file-identity and kernel process
reference checks. Eight capture files and two unclassified cache roots were
retained. This is the selected material's pre-removal allocation, not a claim
that filesystem free space increased by exactly that amount.

The post-cleanup VM check matched the running executable to the published
binary hash, confirmed all five live shim kernel identities and retained all
239 session metadata rows. Its journal used 987,927 content bytes in 16
segments, with no conversation records. Swarm state used 13,950,349,312
allocated bytes; the root filesystem had 3,149,848,576 bytes available
(about 2.93 GiB). Allocation and free-space observations include concurrent
machine activity and are not exact savings attributed to this batch.

The VM's private RAM journal backups, tools and transient migration files were
removed after a privileged scan found zero executable, cwd, descriptor or
mapping references and no inspection errors. Normal v0.17.10 binary rollback
slots remain; no conversation backup was retained for rollback.

The Mac permanently removed 96 verified Rust target directories, totaling
9,223,294,976 allocated bytes (about 8.59 GiB). Each root had the expected Cargo
cache markers and passed current process-reference checks. All 96 target roots
were absent afterward; all 96 project-parent filesystem identities and Cargo.toml
hashes were preserved. The helper refused its first attempt on a transient live
reference before deleting anything; a fresh, fully checked attempt completed.

The Mac postcheck retained 51 session rows: two old live shim identities and four
new Codex replacement identities matched the kernel. New working directories
were outside supervisor state; their identity tails were between 2,515 and
65,568 bytes, with no legacy transcript or final-screen archives. Its journal
used 993,085 content bytes in 16 segments with no conversation records. Swarm
state used 71,247,196,160 allocated bytes (about 66.35 GiB); the data volume had
379,118,649,344 bytes available (about 353.08 GiB). These are live observations,
not an exact cleanup free-space delta.

Unique project work, live workspaces and material with unresolved ownership or
writer evidence were retained. No whole-session purge or native-history purge
was used to obtain these results. Private manifests, reports and operational
backups support operator review; this public record omits project paths,
conversation identifiers, environments and helper source.

## Runtime capture checks

On each host, a synthetic session emitted 22 MiB of terminal output. The identity
tail measured exactly 65,568 bytes at both observations, no legacy transcript
rotations or final-screen archive appeared, and the shim exited successfully.
The synthetic process and its temporary root were cleaned up. These checks
exercise new shims; surviving old shims retain their earlier capture behavior
until they retire. Quiet identity tails grow lazily from their header to the
65,568-byte ceiling; smaller files do not indicate a failed bound.

## Codex resume regression found during rollout

The workspace guard correctly adds an explicit project `--cd` to Codex resume.
The backend-plan validator also checks replacement frontend arguments and
rejected that absolute path because it is outside the session directory. Thus
the native terminal CLI launched, but its app-server backend did not. Earlier
tests exercised command composition and backend planning separately, missing
this connected path. Removing `--cd` would restore the legacy native-cwd risk.

The patch binds the one resume `--cd` operand to the final core-selected
working directory while retaining session containment for backend endpoints and
all unrelated absolute arguments. Native ARM focused race tests pass across
adapter, Codex, skeleton and daemon packages, including adversarial path operands,
final-hook cwd forwarding and early explicit worktree-resume refusal. The real
adapter resume-to-planner regression failed before the fix and passes afterward.
Build of all packages, vet of connected packages and strict documentation checks
also pass. Installed real-Codex smoke evidence follows. The
installed v0.18.0 binary reproduced the failure against real Codex 0.163.0 in
a disposable empty native home; no prompt or turn was sent and the synthetic
session, daemon and scratch directory were removed.

Independent review also reproduced a workspace-boundary bypass in which
`filepath.Abs` cleaned `..` before symlink resolution. The kernel could enter
Swarm state while validation checked a different outside directory. The patch
resolves symlinks before cleaning. Requested, prelaunch and finalizer regressions
failed before the fix and pass under native race detection, including relative
paths, rollback and the inverse valid outside-directory case
(`agents-tracker-60ge`).

A patched native candidate then passed the strict isolated Codex 0.163.0 smoke:
backend and TUI OS working directories matched the outside project; the live
native TUI received the exact `--remote` endpoint and project `--cd`, proving
the backend handshake. No prompt or model turn was sent. The native processes
and scratch root were removed. This does not prove a real existing conversation
resume or a billable model response.

## Homebrew collateral change and repair

During the Mac cask upgrade, Homebrew automatically cleaned ten older formula
kegs. This was a collateral package-manager
change outside the Swarm cache batch. It broke the installed Vercel launcher,
whose shebang named a removed Node interpreter.

Repair changed only the shared `vc.js` shebang to the already installed Node
22.16.0 interpreter, compatible with the package's declared Node >=18 engine.
The JavaScript body and Vercel 50.23.2 package version were preserved. Four
`vercel`/`vc` version probes through both package aliases passed. No replacement
kegs or additional wrappers were installed; automatic cleanup was disabled for
the follow-up operation.

Vercel account access, application builds and deployments were not tested.
The Homebrew receipt still describes the removed runtime dependency graph, so
supported package-manager/runtime reconciliation remains follow-up work
(`agents-tracker-c78w`). A later
relink or package upgrade may rewrite the launcher. Owner-process open-file and
all-user executable-path checks found no removed-keg references; privileged
all-user loaded-library coverage was unavailable. This does not establish that
every application affected by the automatic cleanup was exercised.

## Remaining boundaries

Deployment and the verified batches do not complete legacy workspace recovery
or establish a whole-machine storage ceiling. Managed-Claude diagnostics,
shared debug-log bounds, folded stopped-hook payload retirement, audit retention
and the existing W4 live-orphan launch window remain unresolved. See the
[fixing plan](../specifications/lightweight-storage-plan.md) for their tracking.

Older host conversation detail is intentionally unavailable after restart or
RAM eviction; provider CLIs remain the history surface. Existing phone caches
and delivered copies retain their own lifetimes. A fixed-workload production
growth study and disposition of the retained unique/live work are still needed.
