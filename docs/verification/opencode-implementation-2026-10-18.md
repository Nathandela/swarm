# OpenCode implementation evidence — 2026-10-18

Implementation is active under `agents-tracker-vyhy`. This record covers accepted changes and measured contracts; it does not certify the full maturity plan or a release. Installed Swarm remains unchanged.

## Current maturity

| Surface | Implemented locally | Qualification boundary |
| --- | --- | --- |
| Launch and model selection | Existing editable picker, complete native model IDs, target-project defaults, exact native binding before initial input | OpenCode 1.19.3; explicit fresh-session variant bootstrap remains unsupported |
| Monitoring and communication | Native busy/attention/error state, exact input receipts, owner/pilot messages, passive supervision and separate native-child hierarchy | Idle and tool-step completion never prove task completion; compaction cannot transfer ownership without native lineage |
| Continuity | Shim-owned normal TUI, daemon crash/rejoin, exact resume, native history export and actual source checkout preservation | Actual OpenCode-to-OpenCode passed; the Claude side of the mixed-harness handoff matrix was a fixture |
| Structured interaction | Bounded live transcript, request sets, owner permission/question controls, signed phone decisions and native phone composer | Strict turn Stop is unavailable; native APIs cannot protect a newer turn with an expected-turn precondition |
| Phone history | Separate imported native pages, encrypted cursor custody, live-row precedence and immutable phone state snapshots | Host paging is under final integration; native history does not create journal cursors, input ownership or decision authority |
| Operations | Pinned runtime/version checks, health-based capability withdrawal, safe unsupported refresh and observed quota/compaction outcomes | Account rotation, relogin and Swarm-driven compaction remain unavailable |

The model-selection request is implemented: opening the OpenCode launch form uses
the existing selector, including built-in free models, with editable manual entry
for other exact IDs. Leaving it untouched preserves OpenCode's target-project
default instead of choosing the first catalogue entry.

## Integrated controls and independent review

Native permission and question decisions now run through public owner CLI,
protocol, durable operation custody, shim input fences and the private SDK bridge.
The complete observed request hash, worker instance, root, generation, exact native
target and expiry fence each decision. Same-operation retries inspect retained
evidence; unknown writes are not posted again. `swarm decide --status` performs a
receipt read. The [native controls matrix](opencode-native-controls-2026-10-18.json)
exercised actual root/child requests, permission once/always/reject, grouped custom
questions, stale hashes, wrong generations, duplicate decisions and changed answers.

The phone path preserves the displayed request hash through the facade and signed
body. It durably records answer custody before mailbox append, keeps unresolved
answers across restart, prevents a second append after reply loss, and releases
retry only after qualified no-effect refusal or request resolution. Native phone
messages use the same native input custody as owner/supervisor messages, including
exact source and operation attribution. A clipped echo settles only with the
qualified full input hash. The [phone review](opencode-implementation-2026-10-18/phone-request-review.md)
and [connected packet checks](opencode-implementation-2026-10-18/native-phone-packet.md)
separate real signing/encryption/gateway/core/journal/phone processing from the
in-memory mailbox and scripted SDK fixture used by those checks.

Independent Astra review fixed three further failures: direct bridge recovery
accepted ambiguous user-input shapes, retained terminal outcomes could be
downgraded by weaker later evidence, and a silent native process freeze left
composer authority looking fresh. Exact full-row input qualification, ranked
durable outcome updates and the existing observer's bounded health read now cover
those boundaries. An actual native SIGSTOP test withdrew health within 3.022
seconds and recovered after SIGCONT. [Frozen review and native results](opencode-implementation-2026-10-18/final-native-review.md).

Late review also reproduced unfinished-input ownership expiry after 24 hours and
missed completion when a single input pushed its user row out of the startup
window while the daemon was offline. These fixes and native paging remain active
integration gates; they are not counted as accepted merely because the failures
have deterministic reproductions.

The first implementation workers ran through an isolated Swarm daemon in separate Git worktrees using **`opencode/deepseek-v4.1-flash-free`** and **`opencode/glm-5.3-flash-free`**, with the native catalogue's supported `reasoningEffort: max` option. Native message records verify those provider/model identities and report zero model cost. GLM reviewed DeepSeek changes and DeepSeek reviewed GLM changes; independent Astra reviews and supervisor tests determine integration. A lifecycle prototype written before failing tests was rejected and preserved separately; its replacement began from the clean baseline with a test-only commit.

At 14:25 UTC the native UI showed **Free limit reached**, **Free usage exceeded**, and a retry in about 9½ hours for four workers. This is the built-in free service's quota, separate from the earlier incorrect OpenRouter route. The local authentication inventory has no OpenCode Zen credential. The owner then explicitly authorized Sol/Astra to implement the remaining work. Quota-bound workers were retired through Swarm, preserving their code, native bindings and review evidence; replacement agents work in separate worktrees. [Quota observations](opencode-implementation-2026-10-18/free-quota-observations.json), [native screen](opencode-implementation-2026-10-18/identity-free-quota-screen.txt).

## Accepted local change

`agents-tracker-79si` is implemented by `e1844845` and `8f8d6eff`. `swarm watch SESSION --until ... --timeout ...` now accepts the commands generated by the handoff template, alongside the existing flags-first form. Extra positional arguments and malformed flags still fail. Fully interleaved argument order remains unsupported.

DeepSeek produced failing regressions before its fix. GLM independently reviewed the change; Astra checked an additional 24-case argument matrix. Review strengthened the committed regression to distinguish `--until change` from the default predicate and prove the session-first timeout. The supervisor then passed full `cmd/swarm` tests, focused race tests, vet, build, and real commands against the isolated daemon: completed snapshot, change timeout, unknown flag and extra positional rejection. [Check results](opencode-implementation-2026-10-18/watch-verification.json), [Astra matrix](opencode-implementation-2026-10-18/watch-astra-argument-matrix.txt), [author iteration](opencode-implementation-2026-10-18/watch-model-result.txt), [GLM review](opencode-implementation-2026-10-18/review-watch-model-result.txt).

`agents-tracker-xl6k` is implemented locally by `2cf71bda`, `a890c1d1`, `33f65f57` and `56430f78`. First-class supervised handoff now resolves the source's actual provider checkout; missing sources and worktree rows whose checkout was never captured refuse before reservation. Generic spawn and resume retain their directory contracts. Free GLM authored the change; free DeepSeek and Astra reviewed it. The supervisor's real CLI/protocol/daemon/shim race smoke proved that a child process's actual cwd is the source worktree, while passive completion notification still reaches the source. Linux-target full lint passed with zero issues. [Actual CLI evidence](opencode-implementation-2026-10-18/handoff-integrated-cli-worktree.txt), [DeepSeek review](opencode-implementation-2026-10-18/review-handoff-model-result.txt), [Astra review](opencode-implementation-2026-10-18/handoff-astra-final-review.md).

Provider-aware identity work uses test-first `2a675e52` and implementation `a33c6f0a`: characterized OpenCode native IDs pass generic resume composition and adoption validation; corrupt IDs refuse before binary resolution, history recovery or same-ID deduplication. Other providers keep their existing external UUID policy. The pure adapter Resume contract remains unchanged. All adapter packages and focused resume/handoff tests passed under race; vet and build passed. [Behavioral red](opencode-implementation-2026-10-18/identity-supervisor-red.txt), [final checks](opencode-implementation-2026-10-18/identity-supervisor-green.txt). This does not implement native SQLite history export or certify managed resume.

The integrated standalone runtime now persists schema-3 native binding custody, authenticates its private SDK bridge, observes before initial submission, preserves native configuration origins and holds the existing shim input fence during semantic dispatch. Actual OpenCode 1.19.3 launch took 3.49 seconds; daemon crash/rejoin preserved the exact root, creation time, shim process and backend generation. Subsequent normal-exit and hard-native-kill resumes preserved root/profile/creation identity under a new worker generation. All these probes made zero model calls. Native model/agent visibility was checked separately from user-message admission. [Curated native integration evidence](opencode-implementation-2026-10-18/native-runtime-integration.json).

Managed OpenCode history now exports through the exact live SDK binding or the pinned CLI for an ended source. The immutable 0600 native JSON export includes provenance and bounded pagination/content; mixed roots, foreign part identities, creation mismatch, duplicate rows, cursor loops and exceeded limits refuse. Live/ended exports and exact resume passed against the native runtime; successful cross-provider model execution is still a separate gate.

Owner semantic sends now enter durable operation custody with immutable native-input coordinates and retained admission evidence. `swarm message` and pilot preserve the terminal `swarm send` contract. Regression tests cover late unsequenced client replies, uncertain retry retention, lifecycle ending races and exact original-input waits despite later work. Actual native SDK probes verified exact existing/missing-message responses and the live Prompt ref's empty/draft/empty transitions without submitting. Public decision authorization and phone interaction publication are integrated as described above.

Owner roster, watch, pilot and TUI reads now negotiate a bounded native observation with current children, pending requests, input outcomes, provenance and explicit truncation. Historical children remain native history rather than an ever-growing live roster. Passive notices into managed OpenCode use stable event/attempt operation IDs and the same durable native admission path; uncertain delivery is reconciled by exact native evidence without another submission. A quiet supervisor may receive new notices even when a prior input's completion cannot be correlated. Native child readiness requires qualified input settlement rather than a bare idle status. Actual native draft/control/busy deferral, once-only delivery and completion while the daemon was down passed the [supervision matrix](opencode-implementation-2026-10-18/native-supervision-and-handoff.md).

Actual native tool-loop testing exposed and fixed premature completion from a provider `stop` that also contains ordinary tools. Successful outcomes now require the exact completed native message and its persisted parts; the pinned native loop continues when ordinary tool parts remain. In the updated root and native-child probes, 21 intermediate receipt reads per stage stayed admitted through the continuing tool loop, and only the final tool-free response completed the original input. The provider was a scripted localhost fixture, with no external model requests. [Native tool-completion evidence](opencode-implementation-2026-10-18/native-tool-completion.json), [source ordering proof](../../evidence/native-terminal-parts-source.md).

Both native usage-triggered compaction and context-overflow replay were exercised. Each created a new continuation user ID without an authoritative link to the original input. The overflow replay even copied the original text; text equality is insufficient to assign ownership. The original receipt correctly remains admitted with no terminal outcome after native idle. This is an upstream lineage limitation for automatic original-input completion, not a reason to block new messages into a quiet supervisor. [Native compaction evidence](opencode-implementation-2026-10-18/native-compaction.json).

## Native contracts measured

The non-generating OpenCode 1.19.3 creation probe verifies correct versus absent/wrong Basic authentication, caller-owned session IDs, explicit model/agent/variant binding, and legacy reads of the created row. Changed create arguments return the original binding and require validation. Unset model/agent remain absent even with a native configured default; creation alone does not prove the attached TUI's first prompt selection. A create request whose response was discarded is recovered by exact ID. Restart validation reads the original session and creation timestamp **before** any create retry, so recreation cannot masquerade as recovery. [Native creation results](opencode-implementation-2026-10-18/creation-contracts.json).

An independent Astra test of a frozen, uncommitted HTTP client snapshot passed against the real isolated 1.19.3 server without model calls. It covers authentication, caller-ID create/retry, binding mismatch refusal, legacy reads, SSE subscription before create, native child creation/events, empty status/request/history reads, cancellation, and exact session/child persistence across restart. This initial smoke does not qualify populated history, request decisions, generation, process ownership, or the final changing client revision. [Initial native client run](opencode-implementation-2026-10-18/http-native-client-initial.txt).

That initial review found concrete client defects: ambiguous write errors treated as definite rejection; malformed `null` snapshots treated as empty; native opaque history cursors rejected; foreign direct-child rows accepted; and permission/question/history fields lost through display-oriented structs. They were corrected before acceptance. A populated-history qualification used native `noReply` append calls only after pinned source established that they return before model, title and compaction execution.

Those client findings, including missing/null message parts, are fixed in isolated final revision `c81f0db5`. Astra's frozen-source adversarial race checks and real native populated-history/restart checks pass. That experimental HTTP client remains outside the selected SDK/Unix-socket runtime; unused transport code is not a maturity claim. [Final native check](opencode-implementation-2026-10-18/http-native-client-c81f0db5.txt), [adversarial check](opencode-implementation-2026-10-18/http-adversarial-c81f0db5.txt).

The live supervision run also confirmed that a successful terminal `send` during busy work is not a native prompt receipt. A review instruction was absent from native history and was re-delivered while idle, then verified by its exact native message ID. The semantic route must retain separate delivery, admission and correlated outcome states.

## Runtime topology qualification

The original `serve` plus `attach` candidate fails explicit startup selection: the attached empty root ignored the stored model/agent, and a global inline overlay still lost to an agent-specific model. It is not being enabled. [Overlay verdict](opencode-implementation-2026-10-18/bootstrap-verdict.json).

The replacement uses the normal TUI and a public plugin SDK bridge. Actual probes preserved native CLI model/agent precedence, created and navigated to an exact caller-owned root, relayed reads/events over an authenticated private Unix socket, preserved existing relative plugin origins, and used no TCP listener. The normal TUI owns its in-process native worker. Native `--prompt` was ignored after plugin navigation, while a plugin failure could leave Home free to auto-submit elsewhere; managed startup therefore withholds it. [Standalone verdict](opencode-implementation-2026-10-18/standalone-feasibility-verdict.json), [pinned source proof](opencode-implementation-2026-10-18/standalone-feasibility-source.json).

A minimal forwarding wrapper around the public native Prompt component preserved reactive host props, original ref/callbacks and the right-hand slot. It submitted one genuine input to the exact root with the native-selected Plan/DeepSeek/max tuple. Max came from private persisted native state; explicit variant bootstrap is not established. The provider subsequently returned 403 FreeTierError at zero reported cost, so successful generation is not claimed. Unknown competing composer plugins were refused before root creation; the public slot API cannot inspect ownership and replacement renders every contribution. Durable binding acknowledgement, receipts, later draft/shell/modal races, custom TUI configuration and broader plugin coexistence remain implementation gates. [Prompt qualification](opencode-implementation-2026-10-18/prompt-slot-verdict.json), [source proof](opencode-implementation-2026-10-18/prompt-slot-source-proof.json).

## Sustained and platform evidence

The actual one-hour continuity workload passed on frozen `8cd022f3`: **3600.385
seconds, 302 root inputs, 300 native children, 15 daemon SIGKILL/rejoins and 1205
scripted localhost provider requests**, with zero external model requests and all
owned shims cleaned up. Two earlier runs reproduced a native SSE cleanup leak;
the socket/aborted cleanup fix preceded the successful run. This historical run
predates final phone/history/review changes and did not declare resource budgets
before launch. It establishes continuity, not final-source bounded production
readiness. [Exact source, failures and limits](opencode-implementation-2026-10-18/native-hour-continuity.json).

The checked-in [native workload](../../scripts/verification/README-opencode-native.md)
adds declared Swarm RSS/FD/disk and dispatch/rejoin gates, an impossible-RSS
negative control, exact binary/script identities, workload results and owned
process cleanup. Actual budgeted smoke tests passed on macOS ARM64 and Debian
13 ARM64 under an ordinary user. The [Linux native run](opencode-implementation-2026-10-18/native-linux-arm64.md)
used the integrity-verified official OpenCode 1.19.3 Linux ARM64 package, three
inputs, a child, native public decisions and two crash recoveries. Neither run
certifies AMD64, physical Android or external-provider inference.

Full Linux application build, vet and race checks passed at `2663afde` under an
ordinary user with pinned Go/mobile tools: 78 package suites passed, seven had no
tests. Android's SDK/AAR gate was separate. [Frozen Linux gate](opencode-implementation-2026-10-18/linux-connected-gates.json).
A newer combined application run and final complete-source Android AAR/debug and
release test runs are in progress; earlier results do not cover those later files.

## Qualification limits

The full macOS baseline predating these changes fails existing Linux-managed-account fixtures, platform-specific reachability checks, and an installed x86 gobind binary on this ARM host. This is tracked by `agents-tracker-vyhy.10`. Linux qualification ran in an isolated ordinary-user container with pinned Go/mobile tools and private source/cache storage: vet and full application-package race tests passed at `8f8d6eff`; the mobile AAR test failed because the pinned Android NDK was absent. macOS lint also reports eleven pre-existing unused Linux-managed-account symbols, while Linux-target full lint passes after the handoff integration. [Linux lint](opencode-implementation-2026-10-18/linux-handoff-lint.txt). Neither the missing Android leg nor later runtime changes are covered by that earlier full race run.

Runtime, bridge, reducer, bounded live transcript, hierarchy, history export,
durable receipts, semantic supervision and public decision/phone paths are
integrated locally. The real private native Go AAR built for arm64-v8a and x86_64;
54 targeted Android request/transcript/detail/surface tests passed without skips.
That artifact predates the final history and runtime source, so a fresh complete
artifact and full debug/release tests remain required. Actual 24h/72h workloads,
physical phone/hosted relay, a complete real-model cross-harness matrix and final
release gates remain unqualified. No short test substitutes for elapsed sustained
qualification. Changes remain local; no release or installed-binary replacement
has occurred.
