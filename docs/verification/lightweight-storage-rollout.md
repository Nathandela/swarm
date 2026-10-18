# Lightweight storage release and rollout

2026-10-17 · through `v0.18.2` · source `2f54b3e2`

The owner authorized release, deployment on the Mac and `tf_nathan`, and cleanup
of reviewed legacy Swarm buildup. The release and both supervisor deployments
completed. v0.18.1 corrected a Codex resume backend regression and a symlink-path
bypass. v0.18.2 closes additional deletion boundaries exposed by the actual
cleanup. The Mac now retains only the discussion controlling this operation;
all VM WIP discussions and their unique work are protected. Later checkpoints
below supersede earlier counts and recovery holds. This records the checked
cleanup and runtime checkpoints; the
[whole fixing plan](../specifications/lightweight-storage-plan.md) remains open.

## Release and deployment

[Release run 39014164018](https://github.com/Nathandela/swarm/actions/runs/39014164018)
completed successfully with all 17 jobs passing. The
[v0.18.0 release](https://github.com/Nathandela/swarm/releases/tag/v0.18.0) points
to `c9db413b`; its published notes describe the live-only history tradeoff and
remaining limits. The release gates supplement the
[native Linux verification](lightweight-storage-linux.md); their documented
skips and separate full-Mac-suite baseline failures remain limitations.

Both machines initially received the v0.18.0 supervisor. The Mac used its existing Homebrew
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
inactive. At this initial checkpoint, rollback slots retained v0.17.10; no older journal snapshot may
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
mapping references and no inspection errors. At this checkpoint, normal v0.17.10 binary rollback
slots remained; no conversation backup was retained for rollback.

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

## Patch v0.18.1 deployment

[Release run 39018530265](https://github.com/Nathandela/swarm/actions/runs/39018530265)
passed all 17 jobs before [v0.18.1](https://github.com/Nathandela/swarm/releases/tag/v0.18.1)
was installed on both hosts. Both installed builds embed clean source revision
`59e0ef84c5144abd26f744d8537b13f4c33a78e7`. The Mac used the verified cask-only
Homebrew upgrade with automatic cleanup and autoremove explicitly disabled. The
VM used standard signed staging and activation, including its narrow sudo
fallback for the root-owned binary pair. The supervisor remained Ubuntu-owned. Its ordinary binary rollback slot now
contains v0.18.0; no journal rollback copy was retained.

Saved-environment supervisor restarts preserved all six current live shims on
each machine. Kernel PID/start identities and replacement-daemon roster entries
were checked together. A new non-test VM session appeared during live activity;
its metadata was retained along with the original five. Loaded runtime images
matched the installed release. The previously active Mac gateway was restarted
and its loaded executable matched the installed patch after asynchronous restart
settled. The VM gateway was inactive at the final check.

| Patch deployment checkpoint, before native recovery | Mac | VM |
|---|---:|---:|
| Retained session metadata rows | 51 | 240 |
| Live, adopted shims | 6 | 6 |
| Durable journal content bytes | 994,180 | 1,001,742 |
| Journal segments | 16 | 16 |
| Durable conversation records | 0 | 0 |
| Running sessions with a recorded backend planning failure | 4 | 0 |

The VM doctor had no failures. At this checkpoint the Mac's four preexisting
v0.18.0 degraded native terminals were still running; supervisor replacement
cannot add an omitted backend to an already-running shim. Owner confirmation of
quiescence was requested because the external owner Kill API cannot atomically
observe local controller leases or composer queues and could discard an unsent
draft. The subsequent approved recovery is recorded below.

Both exact installed patches passed the strict isolated real-Codex smoke against
Codex 0.163.0, without prompts or model turns. The first VM attempt used an
incomplete leader-thread-only `/proc` child probe and is discarded as observation
evidence. The corrected probe unions all shim threads, calibrates against the
known live backend PID, and requires native frontend arguments and both OS
working directories to match the outside project. Owned native children, shim,
daemon and scratch data were confirmed stopped/removed afterward.

Both installed patches also repeated the 22 MiB terminal-output test: identity
tails stayed at 65,568 bytes, no legacy captures appeared, exits succeeded and
owned scratch data was removed. These checks establish the tested new-session
behavior, not a whole-machine or long-term growth ceiling.

The initial task-owned synthetic processes, VM RAM helper roots, Mac temporary tools,
raw reports, spent cleanup manifests and the extra smoke receipt were removed
after identity/reference checks. The Mac task-root scan found zero open
references and no inspection errors. At that checkpoint only the 11,319-byte private preservation
inventory for unresolved unique/live work remained. No duplicate conversation
backup is retained; bounded ordinary binary rollback slots remain separate.

## Approved native discussion recovery

The owner explicitly approved restarting all four affected Mac native terminals
and confirmed that no unsent drafts needed preservation. Exact source IDs,
idle state, kernel process identity, native IDs, selected projects and absence
of overlapping live conversations were checked before targeted stop/resume.
A macOS process-exit probe race was corrected before continuing; no uncertain
launch request was repeated.

Two replacements are verified healthy: native conversation IDs, names, tags and
selected projects are preserved; real backend/frontend processes use the correct
cwd and remote endpoint; latest capability controls bind `structured_chat=true`
to each current session instance. Independent verification found 54 retained
metadata rows, five live sessions, a 1,000,769-byte/16-segment durable journal and
zero persisted interaction records after loading these real native histories.

One replacement exited during startup. A bounded, no-prompt direct native PTY
probe captured Codex 0.163.0's project-trust refusal: the selected project has no
exact trust entry beneath an explicitly untrusted ancestor. The selected path
is already its physical Git root. Same model, environment and saved native
history are present; changing to a broader directory would change the workspace.
Official [remote trust-check source](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/tui/src/config_update.rs#L369-L382)
confirms this distinct native gate. Remote cwd override itself is supported;
its [native test](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/tui/src/app_server_session.rs#L3397-L3418)
retains cwd without overriding saved permissions.

The fourth source has the same missing-project-trust condition and remains
running untouched. The failed second source and its history are retained.
An owner decision is pending for exactly two project entries in the effective
native config. The prepared, unapplied edit adds only those trust entries;
parsed comparison proves the broader untrusted setting and every other existing
value remain unchanged. No trust or sandbox permission change has been applied.
The Mac doctor therefore still reports one running degraded terminal; the other
blocked discussion is stopped. Six private recovery files totaling 33,626 bytes are retained only
while that explicit security decision is pending; spent diagnostic files have
been removed and no original native-config backup has been created.

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

## v0.18.2 production deletion follow-up

[Release run 39026876838](https://github.com/Nathandela/swarm/actions/runs/39026876838)
passed all 17 jobs for source
`2f54b3e2835fbbd50247a7b722d0bf7ad88882a6`, including full Linux tests and race
tests, Mac lanes, lint, fuzz, mobile and signed publishing. The
[v0.18.2 release](https://github.com/Nathandela/swarm/releases/tag/v0.18.2)
was installed through the Mac's existing Homebrew cask ownership and the VM's
signed upgrade path. Automatic Homebrew cleanup and autoremove were disabled.
Both supervisor restarts preserved their existing native terminals. The Mac's
active gateway was restarted; the VM gateway remained inactive.

Production cleanup exposed canceled account aliases that acknowledged deletion
of an absent successor while retaining the requested source. The fix completes
the durably pinned target before deleting the explicitly requested alias family;
unconfirmed cancellation durability and a frozen ledger refuse effects. Related
regressions cover hidden-only terminal families, unknown IDs naming shared state
and physical symlink/parent semantics. See
[deletion verification](discussion-deletion.md).

All new boundaries failed before the fix and passed afterward. Final connected
native ARM race checks, native build/vet and Linux-target pinned lint passed.
The full native Mac normal and race runs both completed with failures in nine
existing platform/configuration/fixture packages, including the incompatible
installed mobile binding tool. Full native lint still reports 11 existing unused
Linux-only declarations. The green release gates do not imply a green full local
Mac suite; those limitations remain tracked in `agents-tracker-nsxw`.

### Owner-approved permanent cleanup

The owner subsequently authorized deep removal of all reviewed Mac discussions
except the controller of this operation, and all VM discussions outside the
protected WIP set. This supersedes the earlier Mac native-trust holds: the
affected Swarm attempts were deleted, with no trust configuration change.

| Host | Executed cleanup | Protected material |
|---|---|---|
| Mac | 53 of 54 retained attempts permanently removed; state fell from approximately 65.4 GiB to approximately 26 MiB at the live checkpoint | Current controlling discussion, external projects and existing provider-native history |
| VM | All 20 reviewed non-WIP attempts permanently removed; their pre-removal allocation was 1,281,400,832 bytes | Every original WIP discussion and its dependency closure: 220 retained attempts |

The VM protection set includes hidden predecessors, native-conversation aliases,
spawn lineage and workspace dependencies. Nineteen visible WIP discussions are
therefore not nineteen independently disposable directories. All 220 original
protected directories remained after the twenty removals.

Filesystem permission repairs were limited to directories inside exact approved
cleanup roots. On the VM, twenty root-owned Python cache directories required
owner repair; no source-file ownership or contents were changed. The final
privileged reference scan inspected 229 processes, found no target references
and reported no inspection errors before deletion. Shared account/control stores
and native histories were retained.

Seven detached Mac legacy workers were stopped using frozen kernel identity and
cwd checks. Two external temporary performance servers also exited during their
parent stops; parent signal forwarding is a possible cause, not established
proof. Their files were untouched, and the collateral exits were disclosed.
Twenty-five stale Git worktree registrations inside deleted state were removed
from two external repositories; branch, tag and stash references were unchanged.
Two unused old patch binaries and one dangling internal link were also removed.
The current controlling native process remains alive on its older shim, so its
bounded legacy rotations are still present. The final Mac doctor found zero
degraded sessions; existing agent-path and quiet-update-scheduler warnings remain.

### Retained VM terminal handoffs

The owner authorized one-at-a-time handoffs only when idle without a pending
permission prompt, and confirmed no unsent drafts needed preservation. Three
legacy terminals completed the guarded stop/resume and independent postcheck.
Their exact native conversation IDs, selected projects, effective profiles,
credential bindings, names/tags and policy files were preserved. Native history
files remained on their original outside-state inodes. Native PID registry,
machine/PID-namespace domain and kernel start tokens proved current custody.
No prompt or model turn was sent, and no model override was supplied.

New identity tails measured 13,530, 59,170 and 8,859 bytes, below the 65,568-byte
ceiling, without raw transcript rotations or final-screen archives. Exactly
3,480,290 content bytes of known, stopped-source capture files were retired;
lineage metadata and hook/control custody were preserved. An already-bounded
fourth terminal was left running. Two legacy originals remain held: one has a
permission prompt and one is active. Neither was stopped or relaunched.
Saved launch policy was checked; exact transient native model/permission UI
state is not independently exposed and is not claimed equivalent.

## Owner-directed simplification review

The owner questioned the cost of moving whole legacy workspaces and proposed
committing and pushing useful code/documents before deleting the rest. Three
Luna agents reviewed Git work, loose artifacts and the preservation/deletion
strategy in parallel, with follow-up checks of local-only refs and difficult
working copies. The assessment is read-only: no project commits, pushes,
workspace moves or further deletions were performed.

The bulk relocation preflight produced a prepared receipt for 169 directories
but applied zero moves. Its read-only preparation finished; the separate
time-limited terminal watcher was stopped while waiting, with zero additional
handoffs. Native WIP terminals remain untouched by this pause. The three earlier
verified handoffs remain completed.

The simpler route separates useful deliverables from the bulk of an old working
copy. Existing topic commits can be published unchanged on reviewed preservation
branches; small source/test/document changes can become focused commits.
Rebasing, resolving feature integration and merging into a main branch are not
required merely to preserve work. Only after the selected content is remotely
retrievable should disposable workspaces and build output be removed.

The review found substantive source/test patches and topic stacks, as well as
loose drafts already represented by tracked documents or stronger tests. Raw
logs, mail, checkpoint archives and operational evidence must not be swept into
a Git commit. Some contain private production/account information or
credential-like URLs. Canonical project checkouts also contain existing user
changes, so they must not be blindly switched, staged or cleaned.

Local-only branches and reflog revisions require content triage; an old revision
is not automatically a useful deliverable, and absence from a stale external
clone does not establish absence from the real remote. Linked worktrees and
borrowed Git object stores still require dependency checks before removal.
Provider-native discussion histories and WIP supervision records remain
separate from workspace cleanup. Detailed per-project findings are kept private
rather than publishing private project contents in this repository.

## Final obvious-only checkpoint

The owner authorized curated Git recovery with tolerance for losing uncertain
untracked work, then deferred the final recovery and requested only obvious
cleanup for now. No WIP project commit or push was performed. Automatic approval
review rejected the proposed whole-history branch push because its payload could
include generated client-specific data. That action was not retried. Read-only
source comparisons also found several candidate features already represented
in published project branches. Remaining source-only curation is deferred.

The final VM batch removed 27 exact cache, empty-directory, unused environment,
dependency, compiled-binary and preview targets. Their allocation was
1,377,144,832 bytes (1.28 GiB); the observed root-filesystem availability increase
over this batch was the same. Only the nested virtual environment was removed
from the larger evidence directory. Surrounding evidence, snapshots, source
bundles, Git stores, account/profile state and native histories were retained.
No broad sweep of loose files occurred.

Immediately before removal, privileged inspection covered 238 processes and
found zero target references or inspection errors. The 223 session rows and all
six live shim PID/start identities remained. The unfinished merge, client-source
bundle, external Git consumers and actively changing canonical projects were
untouched. Spent bulk-relocation and handoff-watcher tooling was removed after
the watcher stopped; no workspace relocation was applied.

| Final observation | Mac | VM |
|---|---:|---:|
| Installed binary and running supervisor | v0.18.2 | v0.18.2 |
| Health-check failures | 0 | 0 |
| Retained attempt rows | 1 | 223 |
| Allocated Swarm session-state bytes | 19,816,448 | 11,291,652,096 |

The Mac state is approximately 19 MiB, down from the earlier 65.4 GiB checkpoint.
The VM still retains approximately 10.5 GiB, primarily WIP workspace payloads and
managed native CLI state. Its root filesystem had 5,590,700,032 bytes available
after the batch. This is partial cleanup, with the remaining work deliberately
left for later. Existing non-failure doctor warnings and the two held legacy VM
terminals remain; neither permission prompts nor active work were interrupted.

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

## VM field check, 2026-10-18

The installed binary and running daemon still report v0.18.2. Doctor reports
226 persisted attempts, zero running or past degraded attempts, and no failed
checks. Seven visible discussions are running. The three warnings concern agent
discovery in the noninteractive SSH shell; those agents resolve in the saved
daemon environment. The upgrade timer is installed, and its latest service run
completed successfully. Remote control remains unpaired and off.

Six running attempts use identity rings no larger than 65,568 bytes. Repeated
measurements about a minute apart showed changed contents in busy rings with
unchanged size. They have no raw terminal archives. One intentionally retained
legacy active attempt still writes a raw transcript, approximately 13 MiB
including its rotation. No terminals were restarted or given input. Procfs
confirmed that all seven native CLI working directories are external projects,
outside supervisor state.

Allocated session state measured 11,300,618,240 bytes, compared with
11,291,652,096 at the previous cleanup checkpoint: an increase of 8,966,144 bytes
(8.55 MiB) over approximately 21 hours, with three additional attempt records.
Most of the retained 10.5 GiB is still the deliberately deferred legacy
workspace and managed-native payload. The journal uses approximately 1 MiB,
daemon.log approximately 1 MiB, and remote state approximately 16 KiB. These
observations support the new capture behavior; they do not establish bounds for
the remaining log/custody exceptions or every future workload.

Whole-machine disk management remains insufficient. Root availability fell from
approximately 1.59 GB to 78 MB during this check, then recovered as other work
changed. The data volume had approximately 19 GB available. Temporary storage on
root occupied approximately 20.6 GB, including approximately 10.5 GB under the
native Claude scratch root. Its payload includes repository clones, build
output, virtual environments and repeated Terraform providers. Several large
scratch trees belong to currently live native conversations. Active download
and extraction work also changed temporary usage during the measurements.
File modification times alone do not quantify net growth since deployment.
Native histories remain on the data volume and were not purged.

Under the owner's existing obvious-cleanup authorization, a bounded cleanup
removed only 13 old generated Chrome component download caches, retaining
browser profiles and all discussion scratch/source/history. Before deletion,
privileged inspection covered 275 processes with zero inspection errors and
zero references to the selected browser profile roots. The selected cache
allocation was 747,233,280 bytes; root free space increased by exactly that
amount, from 1,619,374,080 to 2,366,607,360 bytes. All seven live shim PID/start
identities remained unchanged. No project or runtime configuration was changed.
At the final 09:37 UTC check, root had 2,339,532,800 bytes available (96% used),
the data volume had 19,043,004,416 bytes available (88% used), and doctor still
reported no failed checks or degraded attempts.

This is field verification of the supervisor fix and a small cache cleanup,
not completion of the legacy recovery or a fixed-workload growth test. Follow-up
`agents-tracker-r9vj` tracks root exhaustion from agent scratch artifacts;
`agents-tracker-3x3c` and `agents-tracker-3bxu` retain the deferred WIP recovery.
Moving scratch to the data volume would protect root headroom temporarily but
would not itself reduce storage or establish a lifecycle for disposable work.
