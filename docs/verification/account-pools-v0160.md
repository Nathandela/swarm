# Account pools v0.16.0 verification

Verification date: 2026-10-05. Source base: `b3bd45d7ae56d1bd43804949cc748e523cf97273`.

## Behavior under verification

Enabling a provider assigns compatible new discussions and explicit owner resumes of ended discussions to its verified personal accounts. Original records, conversation IDs, history, settings precedence and hooks remain intact. Running unmanaged discussions stay on their current credentials. Accounts displays actual managed/unmanaged coverage.

Native configuration contracts are Codex **0.160.0** and Claude **2.1.289**. Historical schema-1 Claude 2.1.288 records retain their earlier contract. New native contexts require configuration compatibility 3 and recovery compatibility 3; an older binary cannot silently reinterpret them.

Ordinary original user configuration is installed at the native user tier in a private account profile. Project, local and invocation tiers retain native precedence. Original credentials, owner account metadata and ambient MCP OAuth credentials are excluded. Primary conversation history uses recorded aliases to the original native store; it is not copied or replaced during a switch.

Automatic recovery holds on unproved model, changed policy, private configuration divergence, uncertain writer custody or unavailable destination capacity. A stopped, explicit owner resume may capture changed original configuration using a durable refresh intent. Native private edits do not receive an automatic overwrite.

## Automated evidence

Release gates comprise build, vet, lint, the full normal test suite and full race suite. The official tag workflow reuses CI gates before signing and publishing. Release and deployment receipts are recorded separately from local code verification.

Final local gates passed: build (4.37 s), vet (6.18 s), lint (32.46 s, zero issues), full normal tests (511.24 s), and full race tests (869.41 s). Astra's final 1,698-file Go inventory matches the entire source exactly: `dcaede0760d7451fecb8bdd7b6305cf28c113577ed64d0d7ff83fa1a33abb15f`. Initial failures remain in private receipts; the final run includes the reviewed legacy-XDG correction, shim cleanup observation barrier, unused-wrapper removal and truthful legacy coverage assertion.

Meaningful regressions include:

| Boundary | Evidence |
|---|---|
| Gaston paths | Native configuration admission, ordinary owned group-writable directories and trailing autocomplete separators; interior traversal, unsafe symlinks and foreign ownership still refuse. |
| Original configuration | Both providers freeze explicit custom origins across scrub, resume and account change; native user/project/invocation precedence and Codex typed path rebasing retain meaning. |
| Crash recovery | Interrupted original-configuration refresh replays only recorded old/new states. Unrelated private edits and pending intents hold reuse and credential erasure. |
| Concurrent checks | Exact installed configuration can be reused while a credential worker holds its lock; install/update retains bounded configuration and stopped-writer fences. |
| Actual launch assembly | A real shim and synthetic child receive the selected private profile, fixed owned diagnostic route and persisted recovery fallback flag after environment filtering. The old filter fails this regression. Supplied, saved, live and explicitly empty environments are tested. |
| Native selectors | Unsupported Claude routing, managed/remote settings paths and unrepresented XDG origins refuse before writes. Literal path values `false` and `0` cannot bypass admission. Final account resolution removes alternate selectors. |
| Claude failed model | Authenticated prompt epoch, primary transcript ancestry and terminal native diagnostic request must agree. Newer prompts supersede old failures, including restart and delayed flush. Missing or malformed proof holds recovery. |
| Claude resume | Model omission is acknowledged only for the owned recovery candidate, exact conversation, embargo and persisted recovery model pin. Present aliases or a different model are rejected. |
| Retirement | Native history aliases cannot authorize source deletion. Pending configuration refresh and uncertain custody retain private credentials. |
| Pool disable | No new assignment after disable; only an already claimed, durable exact recovery destination may drain. |
| Accounts UI | Coverage uses visible discussion records; narrow terminal rendering is checked. |

## Actual native CLI experiments

These local experiments use synthetic credentials, blocked external access or a loopback endpoint. They characterize the installed binaries without establishing authenticated subscription continuation.

- Codex native config reads across two repository layers retain expected reasoning, sandbox and network policy. Native hook listing retains trust through exact original-path key relocation. Two private synthetic accounts stay separate after atomic replacement of the original authentication file and original profile directory. Native thread read/resume preserves the original conversation through the recorded history alias.
- Actual original Gaston configuration is replayed into synthetic namespaces for both implicit `HOME/.codex` and explicit canonical origins. Source and binary hashes remain stable; all probe descendants are reaped.
- Native Codex first-project trust writes are observed. Such private configuration divergence remains a hold; it is not transferred to another account.
- Claude native user/project settings and ordinary preferences retain tier behavior. Routine characterized trust booleans do not invalidate the cohort. Permission and MCP grants remain frozen.
- Actual Claude resumed `SessionStart` can omit the model. A concrete requested model takes precedence over restored transcript model.
- Actual Claude interactive failure records correlate prompt ID, primary user ancestry and terminal request ID; transcript and diagnostics may still be unflushed when `StopFailure` arrives. Durable bounded reconciliation covers this delay.
- A loopback overload experiment exercises Claude's configured Sonnet-to-Haiku fallback. The recovery flag keeps requests on Sonnet; the control switches to Haiku. This proves that characterized fallback path, not every possible provider failure path.

Private raw receipts reside under the session's `machine-account-rotation-impact` directory, including `native-codex-context`, Claude native probe directories, review overlays and `native-release-v0160`. They are not committed as account or credential material.

## Review

Two Sol implementers and an independent Astra reviewer inspected and iterated on the implementation. The [Audit Committee synthesis](account-pools-v0160-committee.md) records independent CLI reviews, validated objections and unavailable members. A source review, synthetic native experiment, signed release and authenticated acceptance are distinct evidence.

## Release and deployment status

Local final gates and final conditional Astra code review are complete. Official CI publication and VM activation remain in progress. The production preflight observed v0.15.5, seven verified native accounts and both provider rotation switches disabled. Deployment must retain original account generations, switches, discussion identities and live shim process identities.

Post-deployment acceptance will separately report a fresh Gaston managed launch, native settings and hooks, same-provider two-account continuation, observed quota and live-discussion continuity. A successful manual account move does not establish that a real provider quota exhaustion occurred. Acceptance requires completed native assistant turns and first-turn value recall after the switch; echoed terminal prompts do not establish a reply. Claude manual movement correctly holds when a successful turn has not supplied current model authority; no history record is deleted or reclassified to bypass that hold. Real exhaustion is never manufactured by exhausting a subscription.

## Scope limits

Cached imports and the Claude one-year environment token remain unavailable. Ambient MCP OAuth, automatic private trust transfer and every native rewind/up-arrow companion file are not claimed portable. An uncharacterized native version or nonempty unrepresented XDG origin holds admission. Codex native network policy may block tool-side Swarm socket commands; owner controls remain available.
