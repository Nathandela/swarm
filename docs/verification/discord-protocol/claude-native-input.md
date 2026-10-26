# Claude native message admission

2026-10-27. Source implementation and isolated native qualification; not a release/deployment record.

## Contract

For an enabled local Discord installation, `skeleton.Config.NativeInputEnabled` is evaluated for each new session launch and passed through to the core. Nil/false leaves ordinary launches unchanged. The integration supplies a predicate over its local enabled configuration; no provider-specific publish action is needed. A qualified fresh Claude shim gets only session-owned `--plugin-dir` artifacts. Existing shims are preserved, and origin-bearing sends against unproven instances refuse with explicit owner-selected refresh guidance.

The qualified runtimes are exactly Claude Code **2.1.296, 2.1.310 and 2.1.311**. Unknown/future versions are not inferred compatible. The shim owns a 0600 Unix HTTP socket and descriptor, random generation and authentication token. The embedded mod registers its actual native session ID and version. The daemon checks shim PID/start time and retained generation/worker identity before crossing durable Begin. A claimed request is never handed out twice; missing replies remain uncertain and are reconciled without replay. Outstanding unknown native delivery preserves the composer's existing ordering refusal.

The mod invokes the provider's `$.prompt.submit({text})`; it never types terminal keys, adds tool checks, or answers permissions. Claude owns idle/queue/refusal behavior. Swarm reports submission only; native queue drops remain refusals, and no receipt is interpreted as model/task completion. Provenance stays in exact durable request/receipt evidence; the mod does not invent transcript history or authority from text coincidence. Generic Claude degradation still refuses, with no raw fallback.

Primary contract: [Use the mods API](https://code.claude.com/docs/en/plugins/mods/api#start-a-turn-from-a-background-job), [mods reference](https://code.claude.com/docs/en/plugins/mods/reference), and [provider types](https://github.com/anthropics/claude-code/blob/main/mods/types/claude-code.d.ts). The installed native fixture confirms `$.http.fetch(...,{socketPath})`, native queue submission, and actual permission separation. Channels remain preview and are not used by this implementation.

## Verification

Failing-first cases: missing native broker API; origin-bearing Claude fell through to generic legacy request binding; the CLI launch decoder silently dropped the new opt-in. The implemented behavior now refuses unproven native ingress before Begin and keeps the additive launch flag end to end.

- Broker behavioral tests cover authentication, exact version/worker identity, changed-payload rejection, no claim replay, explicit native refusal, escaped 4096-byte messages, and acknowledgements that cannot consume the following queued request.
- Connected skeleton tests cover no fallback/Begin for unproven Claude and the existing exact-origin, OpenCode import/restart, and legacy attribution regressions.
- `SWARM_NATIVE_ADMISSION_FIXTURE=1 go test ./internal/claudeinput -run TestNativeClaudeModalAdmission -count=1 -v` passed against installed 2.1.296. This uses the actual embedded mod and Go Unix broker, a synthetic local model, and an isolated HOME/auth directory. It does not access account credentials.

The native fixture parks a real Bash permission dialog, submits an unrelated teammate message through the broker, observes the command remain unexecuted for three seconds, explicitly refuses it locally, then observes the exact marker in the next native model request. The native promise reports the turn started. The reproducible harness is [claude_native_admission.py](../../../scripts/verification/claude_native_admission.py).

Affected caller build, vet, lint (0 issues), connected behavioral tests, and targeted race tests passed. The final native Go broker/mod/modal fixture also passed under `-race` (10.02 seconds). Its [result](claude-native-modal-2026-10-26.json) contains only synthetic probe identity and outcome evidence.

On the authorized Ubuntu x86_64 VM, retained 2.1.310 passed in 11.99 seconds and current 2.1.311 passed in 10.23 seconds. Both versions were admitted only through a test compiler overlay before their production allowlist changed. The fixture now reports the exact descriptor version instead of a hardcoded 2.1.296 label. [VM evidence](native-vm-modal-2026-10-27.json) records the isolated execution contract.

Only this exact version and fixture matrix are qualified here. Full native account/provider execution, installed host versions, whole-repository gates, independent review, and deployment remain integration gates.

## Other native ingress observations

Installed agy 1.4.0's own `run_command` environment supplies its conversation, project, trajectory/source metadata, local language-server address/token, and native executable. In a scratch HOME with a fake local Gemini model, `agy agentapi get-conversation-metadata <own-id>` succeeded; `agy agentapi send-message <own-id> <marker>` returned the exact recipient/content receipt while a different native tool permission dialog remained pending, and did not execute that command. Later model-context consumption remains unproved in that probe. A bootstrap command can register only its own exact session binding; no process scan or global/workspace config mutation is needed. This is feasibility evidence, not an implemented agy adapter.

The native `agentapi` command is documented by [Google Sidecars](https://antigravity.google/docs/sidecars#agentapi), and self-message wakeup is used by [Google's Artemis notifier](https://github.com/google/artemis/blob/main/mcp_server/notifiers/agentapi.py). The environment executable names `agy` itself, so the invocation includes the `agentapi` subcommand.

## Native receipt review corrections

Independent review found three recovery/interleaving defects: a retained native rejection left the operation executing and fenced later sends; status could commit success after receipt retention failed; and the sender could ignore a stronger rejection retained by a concurrent status query. Failing behavioral tests reproduced all three, including the real protocol replay path.

Native input now settles its delivery outcome by comparing the exact retained receipt and its explicit submitted/started/rejected disposition under the idempotency store lock. Stale success cannot overwrite newer rejection, and admission-shaped native evidence retains the existing no-demotion guard. Repeating an identical already-failed outcome preserves its disposition and the existing durability/compaction barriers. A rejected unresolved input becomes a durably coded failed operation, its replay stays rejected, and the next message is no longer fenced. A retention/settlement failure remains uncertain. Targeted source/race tests, vet, and lint passed after correction.

## Fixture isolation

Native fixtures execute a private physical copy of the selected provider binary. macOS additionally applies a per-invocation Seatbelt profile: writes stay under the test scratch root (plus owned terminal devices), Unix IPC stays under that root, and outbound TCP reaches only the fake provider's exact localhost port. Allowed-write/PTY/provider checks and denied-outside-write/unrelated-listener controls run before provider startup. The native updater remains disabled. The guarded Claude 2.1.296 modal fixture passed (12.34 seconds).

Linux qualification must run the entire fixture, fake provider and helper tree inside an externally established read-only filesystem/fresh network namespace with only loopback and an owned writable scratch mount. `native_fixture_sandbox.py` requires the original host netns inode and verifies that the namespace differs, has no non-loopback interface, permits scratch work and denies outside writes/external TCP. An environment flag alone is not proof of isolation.
