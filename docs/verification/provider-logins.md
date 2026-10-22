# Provider login qualification

Date: 2026-10-23

Integrated native switching and local quality gates have passed. Exact-tag CI
runs before publication; the table distinguishes native feasibility from complete
Swarm runs.

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
