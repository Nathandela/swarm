# Native phone request review and fixes

Reviewed root-authored P7 phone/CLI/gateway code from `985aa2ee`, including the connected Android card route, signed body, outcome handling and native request bindings. The fixes below are frozen at `3631bcaa`; this report does not claim an independent review of this lane’s earlier runtime or supervision implementation.

## Findings addressed

- A callback carried only the stable card ID, so replacement content could be signed after the user saw an older card. `ReplyNativeRequest` now requires the rendered request hash as its fifth string argument. NativeRequestView → TranscriptView → SessionDetailView → PhoneSurface forwards that hash, and Go compares it with the current complete binding before signing. Stable card IDs preserve drafts across unchanged-content expiry renewal.
- A definite local refusal left the existing card’s `submitted` latch set. The refusal path now resets that latch and keeps the draft. A typed machine rejection also forces a card rebuild when an unchanged panel would otherwise keep stale disabled controls.
- Successful mailbox append followed by a lost reply and app restart had no durable request-to-operation guard. The existing encrypted phone state now reserves a bounded native decision record before append. A fresh process reports uncertainty and returns the original operation; it never publishes another envelope for the same retained request. Normal live publication stays pending without an immediate failure notification.
- A late error could replace an admitted outcome after the request’s custody record was released. Both outcome entry paths preserve admitted native proof. Native receipt pointers are deep-copied across state snapshots. Wrong-scope receipts cannot settle custody; rejection releases it only with the correlated typed `nativeEffect: none` proof.
- Phone answer bounds differed from the native protocol. The phone now permits up to 128 choices per group, limits to 16 groups and 16 KiB canonical answer JSON, and rejects NUL answers before publication.

## Custody and caller contract

`NativeDecisions` is capped at 256 protected records in the existing kept encrypted content tier. It stores exact identities, hashes and receipts, with no answer text or replayable envelope. In-flight process markers are bounded by the retained records and pruned on retirement. The Android surface restores locks from the exact operation IDs overlaid on native cards.

Expiry, reconnect, a partial request set, a foreign generation and present-but-unrenderable requests cannot release uncertainty. A later complete current request set with exact matching scope and absent logical card ID can release its lock; that absence proves request resolution, never admission of this phone’s answer. Typed no-effect rejection can also release it. Machine admission stays distinct from native resolution.

The append helper is called inside the existing bucket lock, spanning sequence allocation through publication. The source guard verifies every production caller holds that lock. No answer queue, additional transport or broker was added.

Dependencies: the current request-set fold requires complete current scope/hash proof (`a8e48f4b`, `cd9d66d8`, `f3257238`). The protocol’s known no-effect rejection forwarding is peer commit `6621169e`; generic authorization/transport errors remain uncertain.

The CLI requires the observed content hash plus exact request/target selectors before sending, and the gateway forwards the signed body unchanged. No additional blocker was found in those inspected paths. The server-side decision effect and per-request unknown-operation guard are being qualified by the bridge/core lane.

## Verification

All commands stripped inherited `SWARM_*` variables and used native Go 1.26.9 plus the private native gobind. The complete suites passed:

- `go test -race ./internal/phonecore ./mobile -count=1`: phonecore 56.335 s; mobile 34.640 s. This includes encrypted restart/inventory, successful append plus lost reply plus fresh App with exactly one append, normal pending display, concurrency, capacity, wrong scope, qualified resolution/rejection, late proof retention, bind legality, exported surface and source lock/coverage contracts.
- Connected CLI/protocol/gateway native-focused race suites also passed: protocol 11.440 s, gateway 1.326 s, CLI 1.516 s. These are component/integration regressions, not a physical relay end-to-end claim. Output: `review-phone/p7-connected-go.txt`.
- Real `android/build-aar.sh` built both pinned ABIs from the frozen source in the isolated worktree. No stub or installed AAR was used.
- `:app:testDebugUnitTest` with NativeRequestViewTest, TranscriptViewTest, SessionDetailViewTest and PhoneSurfaceControlsTest passed 54 tests, zero failures/errors/skips. Kotlin/JNI compilation used the five-string API. Tests cover the actual callback route, rendered hash, multiple question groups, draft renewal and same-card local refusal reset.

Private AAR SHA256: `e8cbd1faf0ba17831a3d6b098513abcf30b3f0c7cfae4cdb5603580bf2224001`; size 13,248,400 bytes; mtime_ns `1792346669125394072`. The hash and mtime were unchanged before/after the JVM run. Result XML timestamps are 2026-10-18T18:05:54.414Z through 18:06:02.760Z.

Full artifact metadata and logs are under `/private/tmp/swarm-opencode-implementation-20261018/review-phone/`: `p7-phone-aar.json`, `p7-phone-aar.log`, `p7-phone-jvm.json`, `p7-phone-jvm.log`. The private AAR and result XMLs remain in `worktrees/p7-review-astra/android/`.

This lane did not exercise a physical handset or a full relay-to-native decision round trip. It made no external provider request and changed no installed app, owner configuration or credentials.
