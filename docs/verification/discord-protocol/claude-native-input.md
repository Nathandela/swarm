# Claude native message admission

2026-10-26. Source implementation and isolated native qualification; not a release/deployment record.

## Contract

For an enabled local Discord installation, `skeleton.Config.NativeInputEnabled` is evaluated for each new session launch and passed through to the core. Nil/false leaves ordinary launches unchanged. The integration supplies a predicate over its local enabled configuration; no provider-specific publish action is needed. A qualified fresh Claude shim gets only session-owned `--plugin-dir` artifacts. Existing shims are preserved, and origin-bearing sends against unproven instances refuse with explicit owner-selected refresh guidance.

The qualified runtime is exactly Claude Code **2.1.296**. Unknown/future versions are not inferred compatible. The shim owns a 0600 Unix HTTP socket and descriptor, random generation and authentication token. The embedded mod registers its actual native session ID and version. The daemon checks shim PID/start time and retained generation/worker identity before crossing durable Begin. A claimed request is never handed out twice; missing replies remain uncertain and are reconciled without replay. Outstanding unknown native delivery preserves the composer's existing ordering refusal.

The mod invokes the provider's `$.prompt.submit({text})`; it never types terminal keys, adds tool checks, or answers permissions. Claude owns idle/queue/refusal behavior. Swarm reports submission only; native queue drops remain refusals, and no receipt is interpreted as model/task completion. Provenance stays in exact durable request/receipt evidence; the mod does not invent transcript history or authority from text coincidence. Generic Claude degradation still refuses, with no raw fallback.

Primary contract: [Use the mods API](https://code.claude.com/docs/en/plugins/mods/api#start-a-turn-from-a-background-job), [mods reference](https://code.claude.com/docs/en/plugins/mods/reference), and [provider types](https://github.com/anthropics/claude-code/blob/main/mods/types/claude-code.d.ts). The installed native fixture confirms `$.http.fetch(...,{socketPath})`, native queue submission, and actual permission separation. Channels remain preview and are not used by this implementation.

## Verification

Failing-first cases: missing native broker API; origin-bearing Claude fell through to generic legacy request binding; the CLI launch decoder silently dropped the new opt-in. The implemented behavior now refuses unproven native ingress before Begin and keeps the additive launch flag end to end.

- Broker behavioral tests cover authentication, exact version/worker identity, changed-payload rejection, no claim replay, explicit native refusal, escaped 4096-byte messages, and acknowledgements that cannot consume the following queued request.
- Connected skeleton tests cover no fallback/Begin for unproven Claude and the existing exact-origin, OpenCode import/restart, and legacy attribution regressions.
- `SWARM_NATIVE_ADMISSION_FIXTURE=1 go test ./internal/claudeinput -run TestNativeClaudeModalAdmission -count=1 -v` passed against installed 2.1.296. This uses the actual embedded mod and Go Unix broker, a synthetic local model, and an isolated HOME/auth directory. It does not access account credentials.

The native fixture parks a real Bash permission dialog, submits an unrelated teammate message through the broker, observes the command remain unexecuted for three seconds, explicitly refuses it locally, then observes the exact marker in the next native model request. The native promise reports the turn started. The reproducible harness is [claude_native_admission.py](../../../scripts/verification/claude_native_admission.py).

Affected caller build, vet, lint (0 issues), connected behavioral tests, and targeted race tests passed. The final native Go broker/mod/modal fixture also passed under `-race` (10.02 seconds). Its [result](claude-native-modal-2026-10-26.json) contains only synthetic probe identity and outcome evidence.

Only this version and fixture matrix are qualified here. Full native account/provider execution, installed host versions, whole-repository gates, independent review, and deployment remain integration gates.

## Other native ingress observations

Installed agy 1.4.0's own `run_command` environment supplies its conversation, project, trajectory/source metadata, local language-server address/token, and native executable. In a scratch HOME with a fake local Gemini model, `agy agentapi get-conversation-metadata <own-id>` succeeded; `agy agentapi send-message <own-id> <marker>` returned the exact recipient/content receipt while a different native tool permission dialog remained pending, and did not execute that command. Later model-context consumption remains unproved in that probe. A bootstrap command can register only its own exact session binding; no process scan or global/workspace config mutation is needed. This is feasibility evidence, not an implemented agy adapter.

The native `agentapi` command is documented by [Google Sidecars](https://antigravity.google/docs/sidecars#agentapi), and self-message wakeup is used by [Google's Artemis notifier](https://github.com/google/artemis/blob/main/mcp_server/notifiers/agentapi.py). The environment executable names `agy` itself, so the invocation includes the `agentapi` subcommand.
