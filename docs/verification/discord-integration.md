# Whole-swarm Discord integration evidence

Date: 2026-10-27. Working branch: `feat/discord-whole-swarm`.

The owner authorized implementation, review, direct main merge, release and installation on this Mac and `tf_nathan` after all gates. The owner also explicitly authorized a local mock Discord server. This file records implementation evidence; it is not yet a release or production Discord deployment record.

## Contract and evidence map

| Contract | Executable evidence/source |
|---|---|
| Separate places, arbitrary labels, private config/token custody | `internal/discord/config_test.go`, `unit_test.go`, `cmd/swarm/discord_test.go` |
| All registered discussions, default inclusion, explicit opt-out, rename/resume continuity | `service_test.go`, `core_review_test.go`, `replay_resume_test.go`, `discussion_test.go` |
| External author/invoker/endorser retained, no authority from display names | protocol origin tests, service/controls/context-menu tests |
| Immediate native submission with no added idle queue | `TestServiceExternalInputKeepsAttributionAndDoesNotWaitForIdle` |
| Immutable paged context and explicit audience-pinned publication | store/service/local tests, publication conflict/restart/uncertainty tests |
| Owner endorsement is separate from native tool decisions | `controls_test.go`, connected context-menu and native-card tests |
| Ordinary joined chatter invokes no model | `TestJoinedDiscussionKeepsContextButOnlyAddressedMessagesRun`, discussion edit/delete regression |
| Real Discord wire with local replacement worker | `TestConnectedMockDiscordContextEndorsementAndDeliberateReply` |
| Current thread-parent scope checked before source fetch/publication | `scope_test.go` |
| Interactive ACK is independent of message rate limits and channel lookup | `interaction_priority_test.go` |
| UTF-16-safe emoji labels/publications | `labels_test.go`, `autocomplete_test.go`, store chunk tests |
| Unknown native outcome reconciled through exact receipt without resend | `reconcile_test.go` |
| Safe same-session native input and unsupported-survivor refusal | [per-provider evidence](discord-protocol/README.md), native broker and connected skeleton tests |

Names in this table identify test files under `internal/discord` unless otherwise specified. Final reviewed commit and command results are recorded below when the integration is frozen.

## Completed integration checkpoints

The connected mock runs the production `runConnected` lifecycle with an actual local Gateway WebSocket, REST endpoints, private local IPC and source-message fetch. It observes a colleague request, owner button endorsement and deliberate final reply, and checks that no unsolicited transcript is published. Only the external coding-worker boundary is replaced in that test; native providers are qualified independently.

Focused race runs passed for Discord and command flows, including named routing, custom-state activation, Unicode labels, configuration custody, joined context and interaction deadlines. Broker race tests passed for integrated Claude and agy code. Focused build/vet/lint results and native fixture evidence live in the linked lane records. Affected-scope results do not substitute for final repository gates.

Native fixtures use actual private copies of provider binaries with a synthetic local model service. macOS OS sandboxing and Linux read-only mount/fresh loopback-only network namespaces constrain side effects. Positive and negative write/network checks establish isolation before running the provider. They access no native account credentials and are not live cloud-provider model runs.

## Independent review

Astra reviews have independently challenged private-file custody, durable replay, origin correlation, transport cancellation, rate limits, lifecycle, controls and native admission. Reproduced defects were assigned to implementation agents and corrected with behavioral regressions. Final [core/control/publication review](discord-core-astra-review-2026-10-27.json) and [native/gate-correction review](discord-native-astra-review-2026-10-27.json) approve their scopes. Component approval alone does not approve the entire release.

## Open release gates

Final independent component reviews and guarded native qualification are complete. Claude 2.1.296/310/311, agy 1.4.0/1.4.5/1.4.7 and pinned Hermes v0.22.5 have the linked Mac/Linux evidence. The [frozen source gates](discord-release-gates-2026-10-27.json) passed on `1c29e277`: full build, vet, lint and race suite, 9,701 passing tests/subtests in 82 packages, zero failures, 822.75 seconds. The 67 ordinary-suite skips do not substitute for the separately recorded native qualification. Release-tag CI, signed publication, installation on both hosts and installed-artifact checks follow. No production sessions have been automatically restarted.

The data volume on `tf_nathan` was full during qualification. Only explicitly verified regenerable Go build-cache objects were reclaimed; no provider histories or project data were deleted. Native qualification uses owned RAM-backed scratch. The later bounded cleanup removed 4,851 standard owner-owned, singly linked cache objects older than 24 hours, reclaiming 921,128,960 allocated bytes. Available data-volume space was then 4,137,484,288 bytes; it is rechecked before installation.

Full-suite qualification exposed existing fixture races: a fake compaction attempt was recorded before its actual write boundary, an environment file was observed before its write completed, and a privacy fixture depended on transient global native-process inventory. Deterministic regressions and independent review corrected fixture synchronization/isolation without weakening production checks. Raw failed and final gate logs remain in local task scratch; only final command summaries belong in the release record.
