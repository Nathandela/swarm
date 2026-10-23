# Provider login qualification

Date: 2026-10-23

Integrated native switching, local quality gates and exact-tag CI have passed.
The table distinguishes native feasibility from complete Swarm runs.

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

Sanitized machine-readable observations are in
[provider-logins/](provider-logins/). They contain no login tokens or account IDs.

References and design: [ADR-034](../adr/ADR-034-provider-wide-native-logins.md).
