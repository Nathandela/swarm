# Provider login qualification

Date: 2026-10-23

Integrated native switching, local quality gates and exact-tag CI have passed.
The table distinguishes native feasibility from complete Swarm runs.
The unreleased seamless handover work below remains under qualification.

## Opt-in Claude chat quarantine (2026-10-27)

The passive A2 schema fence and quarantine evidence is recorded in
[claude-chat-quarantine-2026-10-27.json](provider-logins/claude-chat-quarantine-2026-10-27.json).
The old production consumer passed four synthetic Claim/Start cases; this is
not native qualification. Managed metadata schema 6 requires the exact
`claude-chat` discriminator, while ordinary schema 5 remains unchanged.
Retained running or ended discussions cannot reach raw attach, input, resize,
drain or automatic recovery through old terminal paths. At this quarantine
checkpoint, chat launch, stdio shim, UI and capabilities were inactive. The
checkpoint receipt records the then-current main/release blockers and focused
checks; it does not qualify the integrated feature.

The applied constructor is development-only: one shim-owned stdio primary, bounded
R3 capture, real config/metadata binding, replay UUID, and signal/Stop/EOF cleanup.
Managed/native/public admission remains refused; message sending, a history
controller, decisions, recovery and public routing are not enabled. The chat
message-view exists but is unactivated, and native qualification remains open.
The [constructor receipt](provider-logins/claude-chat-constructor-2026-10-27.json)
records 170 applied focused race passes, 85 ordinary regression passes, 41
post-lint race passes, and passing build/vet/lint checks. Main/release gates
`h6sc.26`, `h6sc.28` and `h6sc.29` remain open.

The storage-only prerequisite allows strict development `fake6`/`claude-chat`
metadata for structured child tests while preserving ordinary `fake1` and
managed schema 5/6. Saved `fake6` mode is immutable; malformed or mixed rows are
retained and refused. It grants no managed-login, CLI, retirement,
native-writer-proof, OpenCode-binding or input-embargo authority, and enables no
managed/native/public launch admission, public chat/UI or capabilities.
[Storage evidence](provider-logins/claude-chat-fixture-storage-2026-10-27.json)
records 148 focused race passes, 855 library race passes (one skip), and passing
build, vet and lint checks.

## Published v0.21.0

[v0.21.0](https://github.com/Nathandela/swarm/releases/tag/v0.21.0) was published
from `b35b10ed5b8b960a02949a3a56c32183f4af7de7`. All 17 jobs in its
[release workflow](https://github.com/Nathandela/swarm/actions/runs/39873194146)
succeeded, including the full Linux race suite, macOS, Android, fuzz, performance
and container gates.

Downloaded artifacts passed trusted-signature verification, tamper rejection,
all seven archive checksums and all three compatibility-card comparisons. Ten
packaged binaries matched the release version, commit and Go 1.26.10 build
metadata. The container digest and provenance matched the exact tag, commit and
workflow; the published Homebrew cask matched the signed archive hashes.
[Release evidence](provider-logins/release-v0.21.0.json) records these checks.

The first unpublished attempt stopped on test defects: an unchecked Linux cleanup
error, a macOS-only temporary path and an OpenCode assertion mixing durable state
with a later live-cache projection. The fixes preserve the original contracts;
the packet assertion now requires exactly one native echo and one send from a
single durable snapshot. [Repeated Linux race tests and deterministic timing
proof](provider-logins/linux-phone-settlement-review.json) passed. Test fixtures
also passed with macOS's default temporary directory after path corrections.
Production runtime was unchanged by these follow-up fixes.

## Claude quota detection through attachment records

Claude Code 2.1.309 wrote a valid five-hour quota rejection after six primary
`attachment` records. The parser followed only user and assistant records, so
it lost the ancestry linking that rejection to the current prompt and left the
login available. The fix accepts validated attachment links without letting
attachments establish a prompt or terminal failure. Upstream reports also
describe attachments in the native ancestry chain: [Claude Code](https://github.com/anthropics/claude-code/issues/86752)
and [Agent SDK](https://github.com/anthropics/claude-agent-sdk-typescript/issues/431).

The [sanitized quota incident evidence](provider-logins/claude-attachment-quota.json)
records the regression, independent review and complete repository checks.
Read-only baseline and candidate binaries inspected identical private native
history bytes through the production file opener and bounded tail parser. The
baseline missed the quota; the candidate recognized its future reset. The live
history prefix stayed intact. This proves parsing of the real record shape;
the historical hook had already been consumed, so hook delivery and transcript
flush ordering are not claimed verified by this comparison.

[v0.21.3](https://github.com/Nathandela/swarm/releases/tag/v0.21.3) passed all 17
release jobs. Signed archive, compatibility and build-provenance checks passed;
both Linux binaries and the running daemon matched the installed release. Live
discussion identities survived the update. The reported discussion passed native
initialization and continuity checks on its selected login. A separately reviewed,
one-shot availability correction recorded the old inactive account’s proven reset
without changing credentials, account selection or running discussions.

## Published v0.21.2 and installation follow-up

[v0.21.2](https://github.com/Nathandela/swarm/releases/tag/v0.21.2) was published
from `e672e7b650d873c7a4f165d1b2d7961d52ed7cba`. Attempt 1 failed during Android
SDK/NDK archive setup before the build; rerunning the failed job on the unchanged
source passed. All 17 jobs in [attempt 2](https://github.com/Nathandela/swarm/actions/runs/39933878038/attempts/2)
succeeded. Release artifact checks passed signed-checksum verification and
tamper rejection, all seven archives, all three compatibility cards, and all ten
packaged binaries across twelve architecture slices. The Homebrew cask hashes
match the release checksums. The OCI provenance statement claims the expected
tag, source, image digest and workflow, but local Sigstore verifier setup failed;
cryptographic OCI attestation verification is not established.

The full repository run on this source passed with Go 1.26.10: 77 packages
passed, seven had no runnable tests, and 8,959 test events passed with 63 skipped.
Both signed Linux binaries were installed, and the running daemon matched the
verified release. Existing discussion process identities survived the handoff.

The [sanitized installation follow-up](provider-logins/native-installation-followup.json)
records the completed migration, fresh Codex enrollment and restoration of all
intended original discussions with exact native conversation and directory
continuity. New owner discussions were preserved. Both sampled original history
prefixes remain intact; subsequent owner activity means message counts are not
claimed unchanged. Every intended discussion passed native initialization checks;
Codex capabilities came from the live atomic journal snapshot and matched the
current native instance proofs. The fleet was not entirely idle because owner
work was active. The usual maintenance services were restored. These results remain
distinct from fresh paid Codex A→B→A qualification and the unqualified macOS path;
the canary helper refused a new test while an owner Codex discussion was running.

## Follow-up migration qualification

The importer now recognizes retained, unlocked v0.20 account-check lock files,
preserves Claude's global instructions and symlinked default-home preferences,
and retains cleared logins as unavailable metadata under their recorded identity.
An unused zero-byte Codex rollout does not block unrelated history import;
required missing history, conflicting identities and unsafe source files still
refuse conversion. Frozen-format fixtures cover the complete migration and
preservation of original source bytes.

A Darwin process that exits during writer inspection no longer causes a false
live-writer refusal. Rechecking the current zombie state preserves the existing
owner/incarnation checks and all refusals for unreadable live processes.
Remote-activity link tests explicitly establish their target permission mode,
so a private test-runner umask cannot change the fixture's precondition.

The full repository test run passed with Go 1.26.10: 77 packages passed, seven
had no runnable tests, and 8,923 test results passed. The skeleton package took
619 seconds, so the integrated run used an explicit 20-minute package timeout.
Focused race, vet and lint checks passed; the full migration package also passed
natively on Linux, including compressed Codex history. These checks and the
reviewed source fingerprint are recorded in the
[migration hotfix gate receipt](provider-logins/migration-hotfix-quality-gates.json).
These are implementation and synthetic migration checks, not the owner-host
conversion. The later actual migration and post-migration native checks are
recorded in the [installation follow-up](provider-logins/native-installation-followup.json).

## Native qualification

| Check | Observed result |
|---|---|
| Claude Code 2.1.308, Linux, distinct A→B→A logins | Passed: same native conversation and marker recall through the common context; original per-login secure storage stayed authoritative |
| Claude native refresh | Passed: forced cached access expiry, native refresh token changed, expiry renewed, exact continuation succeeded |
| Codex 0.163.0, Linux, distinct A→B→A logins | Passed: same thread, model and marker recall; latest auth returned to each original isolated login |
| Codex forced native API refresh | Passed in a separate run: one native `account/read` with `refreshToken:true` changed refresh/access tokens and renewed future access expiry; no credential copies, expiry edits, model or tool requests; latest source auth retained |
| Codex rollout-only thread reconstruction | Passed: native resume recovered the same thread and all three turns after scratch SQLite index files were parked; no model turn requested |
| Detached enrollment on Linux | Synthetic native CLI suite passed sign-in, cancel, deadline, conflicting completion, runner/supervisor crash and escaped-child containment |
| Detached enrollment on macOS | Synthetic Codex suite passed including escaped-child cleanup; file-only fake Claude terminal test excluded because macOS uses Keychain |
| macOS credential transaction | Disposable Keychain readback/partial-commit tests passed; real authenticated macOS A→B→A not performed |
| Complete Swarm manager, Claude, Linux | Passed A→B→A across two discussions: exact IDs/model/cwd, marker recall, prompt counts 1→2→3, six stopped-writer proofs, source grants restored |
| Complete Swarm manager, Codex, Linux | Passed A→B→A across two discussions: exact IDs/model/cwd, marker recall, prompt counts 1→2→3, six stopped-writer proofs, source grants restored; native model restored without a stale launch override |
| Compressed Codex history import | Full synthetic migration suite passed on Linux with zstd 1.4.8, including compressed-header decoding and preference for an existing plain rollout; no actual host migration performed |

The [post18 V7 Claude constructor/setup canary](provider-logins/claude-profile-setup-2026-10-25.json)
passed its bounded Linux 2.1.310 cases with synthetic grants: same-profile
second-CWD fresh Launch while the first native/shim identity remained unchanged,
a distinct second profile, and an expected-negative blank-source case. This is
not real authentication/account-admission, fresh-user onboarding, Mac native
constructor, workflow/MCP parity, task continuation or activation qualification.
Fresh users still complete native setup manually; existing profiles are not
backfilled.

The separate [Codex native refresh result](provider-logins/codex-native-refresh-result.json)
records a genuine native-owned refresh after checking the isolated profile had no
writers. The earlier A→B→A timestamp-aging observation remains unchanged. Pinned
Codex 0.163.0 source confirms the explicit API request performs a guarded reload,
then refreshes unchanged credentials with the native authority and persists them.
This run used one process. Concurrent refresh tolerance is supported separately
by synthetic native overlap and the [OpenAI maintainer explanation](https://github.com/openai/codex/issues/10332)
of the limited refresh-token reuse window; a real simultaneous-refresh experiment
was not performed. Swarm adds no refresh client alongside active native writers.

Native probes used harmless marker prompts and disabled tools. They establish
continuation and submitted-prompt counts, not a live tool replay guarantee. Busy
work, permission boundaries and injected quota/auth failures have synthetic
coverage. Independent review and local gates are recorded in
[independent-reviews.json](provider-logins/independent-reviews.json) and
[local-quality-gates.json](provider-logins/local-quality-gates.json).

All four disposable Linux canary runs and their helper directory were removed
after restoration receipts and a current zero-writer inventory were verified.
Original saved credentials remained intact, including the later-refreshed Codex A
grant. No source writes or auth requests were made during cleanup; see the
[cleanup receipt](provider-logins/native-canary-cleanup-result.json).

## Frozen v0.20 updater cutover

On 2026-10-23, the actual v0.20.0 source archive accepted the replacement's
compatibility card. Its wire-change guard deferred activation with a live daemon
or running shim metadata and preserved the installed binary. The frozen updater
does **not** gate detached sign-in or account-check workers when the daemon is
stopped; synthetic worker cases activated.

The replacement importer refused unfinished/live old workers before changing
existing file bytes or creating `logins/`. With entirely legacy state, its actual
rollback transaction accepted the generated v0.20 card, restored a fixture binary,
recorded hold/pending-convergence markers and handed off to the restored build.
After stopped worker proofs and a simulated empty external-native process
inventory, the retry passed. Creating new login state then correctly blocked a
v0.20 downgrade. Follow the [offline upgrade procedure](../operations/provider-logins.md#upgrade-and-recovery).

Both qualification tests passed. These used disposable state, synthetic process
references and fake executable payloads; no owner credentials, daemon, live sign-in
or migration were touched. The exec handoff was captured, and the drained external
process inventory was simulated. This proves refusal and rollback recovery, not
an automatic worker drain or a package-manager restore. Exact outcomes, source
hashes and limits: [frozen v0.20 cutover evidence](provider-logins/frozen-v020-cutover.json).

## Indicative remaining usage

The active login shows Claude's 5-hour and weekly remaining percentages and only
Codex's weekly percentage. Codex windows are identified by their native
`windowDurationMins: 10080`, including when weekly is the primary window. Claude
uses the documented `rate_limits` status-line input. Sources:
[Claude status-line fields](https://code.claude.com/docs/en/statusline#rate-limit-usage),
[Codex rate-limit notifications](https://developers.openai.com/codex/app-server#6-rate-limits-chatgpt).

The daemon holds two bounded, volatile provider snapshots. Each window retains
its own report and reset timestamps; absent sparse updates cannot refresh another
window. Registry identity, epoch and credential revision fence observations and
projection across A→B→A and reauthentication. Percentages never write the login
registry or trigger a switch. Missing, expired and 15-minute-old reports show
unknown; daemon restart also starts unknown.

Managed Claude launches compose a status-line observer with the common native
command, preserving its stdin, output, exit status and layout settings. Existing
project/inline or higher-priority managed overrides are preserved and may yield
unknown usage. No native settings file is modified. Only the conversation ID,
observation timestamp and numeric quota fields reach the authenticated local
daemon; raw input, credentials and transcript content are excluded. Delivery is
best-effort with a 100ms socket deadline, independent of hook sequence locks and
durable spools. Existing discussions acquire this observer at their next launch
or resume.

Focused normal/race tests cover both window slots, malformed/absent/zero values,
freshness, reset expiry, sparse updates, A→B→A, reauthentication, unchanged rotation,
callback authentication, no status mutation, native command preservation, shell
quoting, process-group cancellation, held hook locks and unavailable daemons.
Independent Astra review found no remaining blocking source issue. Installed
Claude 2.1.296 code inspection confirmed POSIX shell and process-group cancellation
semantics; this is distinct from live provider capture qualification.

[v0.22.0](https://github.com/Nathandela/swarm/releases/tag/v0.22.0) was published
from `0c4707f62123425368221c9e465184f91d44f8a3`; all 17 release jobs passed.
The full serial local run passed 9,010 tests across 77 packages. The initial
concurrent run exceeded the echo-latency budget and the supervisor's default
timeout; the serial rerun passed, including 0.111 ms echo latency against 10 ms.
Signed archives were verified before installation. The Linux daemon update
preserved all 22 existing native processes, conversations, directories and selected
logins. A real Claude turn completed, with fresh reports for both windows; an existing Codex discussion
supplied its weekly reading with no five-hour field.

The separate disposable Codex conversation check encountered `backend_unavailable`
when its thread join returned RPC `-32601` (not supported). It was cleaned up;
no completed Codex probe conversation is claimed. That startup path is tracked
in `agents-tracker-rlot`. Final checks confirmed all 22 existing discussions were
initialized on their current login runtime, with no resume/backend errors, held
input fences or pending switches. Sanitized results and qualification limits:
[remaining-usage.json](provider-logins/remaining-usage.json).

## Seamless rotation research (2026-10-24)

The [research evidence](provider-logins/seamless-rotation-research.json) records
isolated native Codex 0.163.0 HTTP experiments: an external-token change during a
local command preserved the current turn; a separate terminal-quota experiment
started a new turn on the same loaded thread with empty input, retaining its tool result
without adding a user message. Synthetic bearer identities and account headers
were checked together. Scripted responses do not prove that a real model cannot
repeat an action. Built-in ChatGPT routing, WebSockets and real fleet behavior
remain unqualified.

Extracted Claude 2.1.310 credential-reload and refresh-save functions passed mocked
tests; no complete live Claude account switch was exercised. Swarm characterization
tests reproduced loss of child activity evidence after daemon restart, tracked in
`agents-tracker-g6sw`. Native hot switching and turn-scoped recovery qualification
are tracked in `agents-tracker-h6sc`. This research changes no production behavior
and does not supersede ADR-034.

The [first structured Claude background-actor case](provider-logins/claude-structured-actors-2026-10-25.json)
observed parent success before its child completed. Native Claude then requested
another inference for the child's completion notification after background
membership was empty. The finite fixture refused that extra request; the case
remains failed, with verified cleanup. These events distinguish known-child
settlement from whole-session completion and do not authorize automatic recovery.
The later R4 case passes the finite notification follow-up through a distinct
native result and actual native `idle`, with exact lineage and cleanup proofs.
It does not qualify automatic resume, cancellation or broader workflows.
The separate cancellation case observed an exact child stop and native request
abort, then failed its predicted notification vocabulary: native task state was
`killed` but the typed notification was `stopped`. No notification follow-up or
final idle was qualified; the failed evidence remains preserved.
The next attempt accepted that notification, then refused an unqualified
child-scoped interruption with a `user` role. Its producer and ownership need
source proof before any exception; no tail or idle was qualified.
The [background Bash case](provider-logins/claude-background-bash-2026-10-25.json)
observed Linux Claude 2.1.310 reporting parent completion and native `idle` while
a durably joined background job remained alive. A fresh task snapshot and two
process samples confirmed it. The later R9 run passed this bounded case and
cleanup after an envelope-only stop-grace correction. Earlier failures remain
preserved. This qualifies neither whole-workflow completion nor automatic recovery.
The [bounded Cron source study](provider-logins/claude-cron-source-boundary-2026-10-25.json)
leaves scheduled-work retention across quota and structured scheduler custody
unknown. An empty background-job snapshot cannot settle those obligations.

The [fresh managed Codex TUI attempts](provider-logins/codex-fresh-tui-2026-10-25.json)
also remain unqualified. Known plugin metadata and a synthetic usage response
were observed before a CONNECT rejection. A masked diagnostic identified the
raw GitHub authority; source review found a default public announcement fetch.
The historical sender and path remain unknown. Real-account and continuation gates remain open.

Sanitized machine-readable observations are in
[provider-logins/](provider-logins/). They contain no login tokens or account IDs.

References and design: [ADR-034](../adr/ADR-034-provider-wide-native-logins.md).

The [managed Codex dotenv guard](provider-logins/codex-dotenv-auth-guard-2026-10-25.json)
now rejects the three workload-identity inputs that can preempt managed bootstrap.
Actual launch-path regressions pass, including final shim revalidation and
multiline settings. The full account-store/shim race suites passed, followed by
focused race and repository-wide build/vet/lint checks on the final source.
This does not qualify native account adoption or separate realtime/Guardian auth.

The [subscription policy checks](provider-logins/codex-subscription-policy-2026-10-26.json)
also refuse `OPENAI_API_KEY`, `OPENAI_ORGANIZATION` and `OPENAI_PROJECT` in
managed Codex `.env`, consistently with the inherited-environment filter.
The existing parser and transport/tool settings are preserved. This enforces the
selected-subscription policy; API-key-dependent optional behavior remains
unqualified. It does not demonstrate a default Responses authentication bypass
or a server-side account change.

The [current-main integration record](provider-logins/current-main-integration-2026-10-26.json)
records six-package race results (3,763 passes and 46 skips), a complete skeleton
rerun (2,047 passes and 28 skips), and 36 changed-fixture race passes. Build and
vet and lint pass. The repository gate remains blocked by the
[pinned-main terminal-restore regression](terminal-restore-2026-10-26.json).
This is a feature-branch checkpoint, not release qualification.

The [bounded Claude-chat transport prerequisite](provider-logins/claude-chat-drain-bounds-2026-10-26.json)
passed its 16 new checks, 201 hook-family race checks (one skip), build, vet and
lint. It adds no chat runtime or automatic-continuation capability.
