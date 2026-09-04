# Remote control: tested scale-to-zero implementation plan

Status: proposed, not approved for production. Research and local probes: 2026-09-04. Source baseline: v0.13.27, `f16216189fdca841e74013c620fcd0e2b3e99d86`. Tracking: `agents-tracker-32sh` (planning), `agents-tracker-wjp4` (contract review and staging approval), `agents-tracker-lam5` (lower-change hosting alternative).

## 1. Decision and scope

Build a **hibernation-native encrypted relay**, and move the separate **push gateway to request-billed Cloud Run with transactional Firestore state**. Preserve current encrypted durable history, command authority, replay protection, revocation, background notifications and self-hosting. Change how infrastructure waits, not what the user can rely on.

The recommended target is Cloudflare Workers Paid + SQLite-backed Durable Objects for the relay, and Cloud Run minimum instances zero + Firestore in Zurich for push. Provisionally use EU-jurisdiction relay storage. Region selection requires owner acceptance: global edge/front-door metadata processing is not a Swiss-only guarantee. If Swiss-only hosting is required, this target must be reconsidered before deployment.

Keep the existing push origin through a Firebase Hosting custom-domain rewrite, subject to a deployed compatibility test. This matters because existing machine bindings retain the push URL, while the Android release build also pins the production hostname. Native Cloud Run domain mapping is not a suitable assumed Zurich production solution. Hosting documents Zurich rewrite support; it also imposes a 60-second request limit. [Hosting integration](https://firebase.google.com/docs/hosting/cloud-run), [Cloud Run domain mapping](https://docs.cloud.google.com/run/docs/mapping-custom-domains).

Do **not** include P2P, a transient-versus-durable message split, reduced history, a phone repository move, or a new cloud semantic-command executor in the first release. They multiply the proof burden and are unnecessary to remove the fixed VM/IP allocation. Evaluate them independently after this migration has measured economics.

This is a platform investment, not the cheapest engineering path to saving today's approximately $38–43/month. If near-term cash savings are the only objective, the previously studied small-VPS/Google hybrid remains more proportionate. The serverless design is justified by quiet-fleet scaling, reduced VM maintenance, and a future platform direction—not a proven short payback.

### What the evidence supports

Local Cloudflare runtime and Firestore emulator probes passed; existing Go contract tests and cross-language crypto vectors passed. They validate important APIs and failure modes, **not production hibernation, real phone latency, Google identity scope compatibility, cloud billing, or migration readiness**. [Reproduction and evidence](../verification/remote-scale-to-zero/README.md).

## 2. Architecture and ownership

```text
Phone foreground ── outbound WSS ─┐
                                 ├─ Worker routing ─ home Durable Object
Machine gateway ── outbound WSS ──┘                  encrypted mailboxes
                                                    cursors / grants / revokes

Machine wake obligation ─ HTTPS ─ Firebase Hosting ─ Cloud Run push service
Phone registration      ─ HTTPS ─ retained hostname      │       │
                                                     Firestore  Google FCM
                                                     stable key      │
                                                                  Phone wake
```

The phone and machine are both inbound WebSockets from the actor's perspective. No actor-owned outbound machine socket. The machine remains authoritative for semantic operations, local execution, uncertain-input receipts and the durable wake obligation. The phone remains authoritative for presentation and local durable reconciliation. The relay sees ciphertext and routing metadata, never session plaintext or cloud-executable commands. Push has no session contents and no authority to execute a command. Keep relay and push runtime/deployment identities separate.

### Target components

| Component | Responsibility | Important constraint |
|---|---|---|
| Thin Worker | Validate upgrade size/routing hint, route to canonical actor, edge abuse controls | Routing hint is not authentication; no raw identifiers/tickets in routine logs |
| Home Durable Object | Connection auth, membership, append/subscribe/ack, revocation, expiry, migration fence | All authority needed for an operation is checked transactionally within its home |
| Short-lived rendezvous object | Existing pairing exchange, expiry, burn protection | Pairing proof does not become permission to create a fresh home after revocation |
| Existing Go endpoints | E2EE, operation IDs, transcript/history, authenticated transport selection | No automatic replay of uncertain raw input; no cloud semantic execution |
| Cloud Run push | Existing API, signature/capability verification, attestation, bounded FCM submission | No durable state on local filesystem; no provider send inside a retried transaction |
| Firestore | Push token/capability/revocation state and reviewed shared replay/quota state | Server-only access, explicit expiry, transaction contention measured |
| Secret Manager | Stable versioned token-encryption key or wrapped-key material | Separate runtime/deployer access; no plaintext key baked into image |
| Firebase Hosting | Preserve public push hostname and TLS continuity | Never cache authenticated API responses; backend remains its own security boundary |

### Home identity is a security decision

Partition: one canonical home per machine relay identity, encompassing all its phone bindings and mailboxes. Current R4 registry namespaces have independent keys, sequences, cursors and push addresses (`mobile/machines.go`, `internal/phonecore/machineregistry.go`, `keycustody.go`). The scalar state and older single-machine comments do not describe the whole registry. The current live multi-machine connection machinery is partially staged; this migration must preserve its honest limitations, not claim those features already work. P0 must map every pairing, append, ACK and revoke orientation to exactly one home and test cross-home isolation.

For the first v2 release, define `home_id = hex(SHA256(domain || operator_namespace || current_machine_relay_RID))` with an unambiguous length-delimited encoding and fixed domain `swarm-relay-home/v1`; pin the exact encoding with vectors in P0. Only operator configuration selects the namespace. Authenticate the machine RID and bind home selection into the pairing/profile/transport-migration record. A hint in the WebSocket URL only locates the deterministic home; it grants nothing. A caller cannot append a random nonce, change the namespace or choose a fresh actor for the same RID. Initialize persistent authority only after valid machine proof and admission checks; an anonymous home hint must not allocate unbounded durable schema/state.

Keep the machine relay-auth identity immutable in this release. `remote init` loads the existing `machine.key`; repeat-init tests require identical bytes. Supported epoch-key rotation on revoke retains this identity. There is no existing transparent machine relay-auth-key rotation API to reproduce. If that identity is lost/recreated, require explicit new pairing/grants for the new RID/home; do not silently import old authority. Old consent names the old grantee RID and is invalid for the new one. Normal hosting migration retains keys and therefore the home. A future transparent auth-key rotation needs its own reviewed transition protocol. This avoids adding a central account/directory service just to stabilize actor names.

Current standing consents bind a grantee routing ID and ceremony, not an arbitrary new home. A retired ceremony must remain retired in the deterministic authority store. Presenting an old valid signature to a fresh actor must not re-pair a revoked phone. Operator restore/import authorization must be distinct from ordinary endpoint pairing credentials. Whole-phone removal must enumerate its independently keyed bindings; a home revoke is not falsely represented as a global device revoke. These are release-blocking tests.

Do not create one global actor: that recreates a shared throughput/failure bottleneck. Do not create one actor per socket: that splits membership, durable cursor and revocation facts. Per-machine actor placement is the starting proposal, not a claim of unlimited scale within one heavily used machine.

## 3. Non-negotiable contracts

Existing [product playbook](remote-control-product-playbook.md), [system invariants](../invariants/system-invariants.md), relay ADRs and [push API](push-gateway-api.md) remain authoritative until explicit amendments are accepted.

| Contract | Required behavior | Failure injection |
|---|---|---|
| Durable acceptance | Commit encrypted append before success or live forwarding | Crash before commit, after commit/before reply, after reply |
| Durable receiver cursor | Persist accepted/decrypted processing state before advancing ACK | Crash before local checkpoint and before ACK |
| Replay/catch-up | Replay unacknowledged retained ciphertext; do not reseal retries | Disconnect, ACK loss, partial batch, sparse sequence gaps |
| Semantic idempotency | Machine preserves existing operation-ID handling | Same operation through reconnect/retry; independent new operations |
| Raw input uncertainty | Never automatically replay uncertain input | Crash around PTY write/receipt boundary |
| Revocation | Retired consent cannot recreate authority; stale sockets cannot mutate | Revoke races append, reconnect, import, rollback and key rotation |
| Incarnation | Live migration preserves logical mailbox incarnation; disaster restore follows reset policy | Stale cursor/ACK against a different incarnation |
| Retention | Preserve existing seven-day/10,000-item limits and documented overflow semantics | User never returns; sustained writes; tombstone restore |
| Push privacy | Content-free wake only; encrypted token at rest; no payload/capability logs | Log/trace/error scan and revoked-token disposal |
| Capability isolation | Relay compromise yields neither push credentials nor endpoint command keys | IAM negative tests and artifact/image scans |

Preserve source-specific limits, not only the headline numbers. Translate append, rendezvous, registration, source, installation and global quotas from the current code/tests into a conformance table before porting. Avoid a shared public-actor creation endpoint that permits unbounded paid storage creation.

## 4. Relay implementation design

### 4.1 Source seams and protocol versioning

| Existing source | Planned change |
|---|---|
| `internal/remote/relay/wire.go`, `routing.go` | Versioned cross-runtime codec/auth vectors; keep v1 self-host behavior |
| `internal/remote/relay/client.go` and client transport construction | Explicit v2 negotiation and canonical home routing; no silent incompatible downgrade |
| `internal/remote/transport/follow.go` | Add subscription delivery behind existing transport contract; retain bounded drain/checkpoint/ACK behavior |
| `internal/remote/relay/store*.go`, handlers and tests | Extract behavioral conformance cases; implement SQLite actor equivalent |
| `mobile/`, `internal/phonecore/`, `internal/remotegw/` | Persist authenticated transport profile/epoch and surface honest reconnect state |
| `deploy/` | Add separately scoped Worker/DO build, migration and deployment definitions |

Do not ship a transparent v1 JSON port. Current cursors and wait sequence fields use Go `uint64`; JavaScript `Number` loses precision beyond 2^53−1 and SQLite signed INTEGER cannot represent all uint64 values. Use canonical decimal **strings on v2 wire**, BigInt in computation, and fixed-width 20-digit TEXT keys for sortable SQLite storage. Reject signs, fractional/exponent forms, non-canonical leading zeros, overflow, and numeric JSON values. At uint64 exhaustion, refuse allocation with a typed error; never wrap. The root interop probe demonstrates the actual Go-frame precision loss and safe ordering.

Retain the current four-byte big-endian declared length plus one-byte tag semantics if framing is reused. Enforce the 1 MiB declared-frame bound before allocation, zero/truncated lengths, aggregate message limits and a bounded incremental decoder if WebSocket message boundaries differ from frame boundaries. Correlate independent replies and delivery events explicitly. The local spike does not implement the full production codec.

Version 2 adds a long-lived authenticated subscription to a mailbox/incarnation from a durable cursor. Handlers register subscription state and **return**; subsequent append, close or real expiry events drive delivery. Do not hold an async handler open until a 25-second timeout. Absence of traffic is not an event.

### 4.2 Authentication and pairing

Preserve the current Ed25519 challenge bytes, HKDF routing-ID derivation, domain separation and consent verification. Port parsers with strict size/signature/canonicalization tests. Generate vectors from Go, verify in the deployed runtime and run inverse vectors where the runtime signs fixtures. The provided Node WebCrypto check is a first interoperability gate, not deployed auth proof.

The local DO probe uses a shared-HMAC fixture ticket to exercise routing and storage. That is **not Swarm authentication** and must not be promoted into production. Production challenge freshness, authenticated membership, current incarnation, current migration epoch, owner-controlled revocation and all quotas require the full conformance implementation.

Authentication performed before a storage transaction can become stale. Recheck membership/key generation, revocation and migration fence at the transaction that commits a mutation. Attachments may retain socket identity and subscription position, but never become an independent authorization database.

### 4.3 Durable schema and operation boundaries

Start with explicit schema migrations and an actor-local schema version. Suggested logical tables:

| Table | Fields and invariant |
|---|---|
| `home_meta` | schema version, canonical identity, mailbox incarnation, migration epoch, serving/frozen mode |
| `members` | routing ID, authenticated key material permitted by current contract, ownership/generation facts |
| `consents` / `retired_consents` | directed grants and ceremony retirement; bounded retention exactly matching current security semantics |
| `mailbox_meta` | route, allocated high-water, retained floor/compaction cursor, byte/depth accounting |
| `mailbox_items` | route + fixed20 cursor primary key, opaque ciphertext, expiry, required envelope metadata |
| `append_receipts` | only if required by the v2 transport retry contract; bounded authenticated ID and ciphertext digest, never semantic plaintext |
| `quota_windows` | minimal reviewed counters/expiry; no unbounded raw IP history |

An append transaction verifies fence and authorization, checks quota/depth/byte limits, assigns a cursor, inserts ciphertext and updates high-water/accounting. Only after commit may it reply and notify subscribers. Duplicate transport receipt handling must return the original result for identical bytes and reject an ID/body conflict. It must not replace end-to-end semantic idempotency.

A subscription uses a bounded indexed range query above the last durable cursor. Do not iterate numerically from cursor to high-water: deletions create gaps and the sequence space is 64-bit. Enforce both a frame-count and byte window; replenish with receiver progress. A slow reader cannot create unbounded memory, reads, sends or durable duplicate copies.

ACK checks route, socket generation, incarnation and migration epoch, is monotonic, and cannot exceed the eligible durable/sent high-water according to the chosen subscription contract. Missing ACK only delays compaction. A malicious reader must not delete another mailbox or invalidate its own future sequence allocation. Existing replay, overflow and sequence-continuity tests specify the rest.

Revocation changes membership/retirement state and purges the required mailbox data in the same atomic authority transition, then closes affected sockets. Every late handler rechecks durable authorization. Never trust a still-open socket because its attachment predates the revoke.

### 4.4 Hibernation, presence and deletion

Use `acceptWebSocket`, serializable attachments and runtime WebSocket auto-response where appropriate. No application `setInterval`, long-running promise, actor-owned outbound socket or keep-warm request loop. Necessary network keepalives are distinct from repeated empty mailbox/presence polling. Presence transitions must report reconnection/staleness honestly rather than fabricating online freshness from an old attachment. [WebSocket guidance](https://developers.cloudflare.com/durable-objects/best-practices/websockets/), [actor lifecycle](https://developers.cloudflare.com/durable-objects/concepts/durable-object-lifecycle/).

An alarm for real deletion does not inherently prevent hibernation. Schedule the **earliest** retained expiry, not the most recently appended item's expiry. The initial spike had this exact postpone-under-traffic bug; a staggered-expiry test now covers it. Bound each cleanup batch, account for alarm retries, and reschedule remaining due work. Enforce logical expiry on every read even before physical deletion finishes. Empty actors have no periodic alarm solely to show liveness.

Alarm work, deletes, indexes, attachments and metadata must be included in measured billing. Runtime-managed auto-response avoids additional actor duration; do not assume every application heartbeat is also absent from request metering without observing/documenting that dimension.

## 5. Push implementation design

### 5.1 Replace persistence by domain transactions

Do not mechanically replace bbolt `Get/Put` with Firestore calls. Add a transactional repository interface in `internal/pushgw/`, retain a bbolt implementation for self-hosting, and run the same behavioral suite against both adapters. `auth.go`, `cache.go`, `storage.go`, registration/allocation/revoke handlers and `wake.go` are the principal seams.

Proposed operations include `AuthenticateAndTouch`, `RegisterOrReturn`, `AllocateAddress`, `RotateToken`, `RevokeAddress`, `ClaimWakeAttempt`, `CompleteWakeAttempt` and bounded expiry cleanup. Return typed replay/conflict/expiry/quota errors consistently with the HTTP API. Pass clock and randomness explicitly for tests.

Current authenticated-request behavior touches installation activity and consumes replay state after valid signature verification even if subsequent body/domain validation fails. Preserve it or explicitly amend the contract; do not accidentally roll it back with a later failed allocation. Recheck the authenticated key generation inside the transaction to prevent a verification-versus-rotation race.

Firestore transactions may retry. The emulator forced an actual callback retry: a provider side effect inside the callback ran twice. Keep all FCM and attestation network calls **outside** retried transaction callbacks. Transactions may only inspect/modify repository state and produce a result for the caller. [Transaction behavior](https://firebase.google.com/docs/firestore/manage-data/transactions).

### 5.2 Distributed schema and privacy gate

| Collection | Purpose | Expiry and conflict handling |
|---|---|---|
| `installations` | Public auth key/generation, encrypted token and key version, activity/attestation/dead state | Existing revoke/180-day authenticated-activity contract |
| `addresses` | Installation reference, submit/revoke verifiers, binding state | Unbound expiry 10 minutes; cap enforced transactionally |
| `revocations` | Existing minimal revoke tombstones | Existing seven-day contract |
| `registration_attempts` | Idempotency-key hash, request-body hash, committed response reference | 10 minutes; same key/different body is conflict, not a new identity |
| `nonce_claims` | Installation + keyed nonce digest | 120 seconds; only one distributed claimant |
| `wake_attempts` | Idempotency digest, encrypted-body digest, attempt/generation/status/deadline | Five-minute retry obligation; bounded terminal retention |
| `rate_windows` | Reviewed per-source/per-address/global counters | Bounded windows and explicit cleanup |

The last four are **a proposed change**, not already permitted durable metadata. PG-RET-4 currently makes relevant caches memory-only and PG-RET-10 closes the durable field set. A hash/HMAC is still retained metadata. Accept an ADR, threat-model and metadata-disclosure amendment before adding these fields. Specify who sees each value, exact lifetime, encrypted backup lifetime and purge behavior. If that change is rejected, redesign shared coordination; do not deploy multi-instance Cloud Run with process-local anti-replay or quota maps.

The registration record is keyed by `HMAC(idempotency_key)`; its body digest is a field checked in the same transaction. Keying by `(idempotency_digest, body_digest)` incorrectly turns a conflict into a new identity. The source currently documents that composite-cache deviation, so stricter mismatch rejection is an explicit API amendment, including its status/error and compatibility behavior—not a claim that this probe exactly reproduces current behavior.

Registration ordering must preserve identical-body idempotent retry semantics and attestation policy, including the current cached identical-body retry bypass of re-attestation unless explicitly amended. Do not allow an unauthenticated caller to reserve a durable registration slot indefinitely. Concurrent first misses may verify the same Play Integrity token multiple times; measure real API behavior/quota amplification in P1. The final transaction can create only one committed identity for the accepted idempotency key/body combination.

Firestore TTL is asynchronous and does not automatically delete subcollections. Enforce expiry at use and provide indexed, bounded cleanup that meets the amended physical-deletion policy. TTL may be a backstop, not an auth/revocation timer. TTL deletes, backup and PITR usage have separate costs. [TTL behavior](https://firebase.google.com/docs/firestore/ttl), [Firestore billing](https://firebase.google.com/docs/firestore/pricing).

### 5.3 Wake state machine and the unavoidable crash window

```text
authenticate/capability → transaction: validate + quota + claim attempt
                       → FCM submission OUTSIDE transaction
                       → transaction: completion CAS(attempt ID, token generation)
```

Concurrent claims must not send twice merely because two instances received the same request. A stale `UNREGISTERED` response must not erase a freshly rotated token: compare the claimed token generation/ciphertext before marking dead. Revoke-before-claim denies the claim. A revoke cannot retract an FCM request already in flight; document this boundary and test its ordering.

No Firestore transaction can atomically commit with FCM. If FCM accepts and the process crashes before completion, a retry may submit the same content-free wake again. Choose bounded **at-least-once submission attempts**, consistent with byte-identical retry and phone wake replay protection, and record that choice in the ADR. A claim lease prevents duplicate concurrent live claimants; recovery/takeover can still duplicate a previously accepted send. Inject crashes immediately before and after provider acceptance and before completion. Refusing all uncertain takeovers would instead lose delivery attempts. Neither choice promises FCM delivery. Preserve the machine's existing deadline; do not introduce an unbounded gateway queue or send a command through push.

A globally exact registration limiter can become a hot Firestore document. Start with strict correctness and test contention. Sharded approximate counters or preallocated permits change admission semantics and require an explicit design; do not silently trade abuse limits for benchmark throughput.

### 5.4 Cloud identity, encryption and serving

Use a dedicated Cloud Run runtime identity with minimum Firestore, selected-secret and FCM/Play Integrity permissions, distinct from deployer and relay identities. Keep keyless Google credentials. Existing GCE deployment had a specific Play Integrity OAuth-scope requirement; attaching the same service account to Cloud Run does not itself prove the real API call works. A legitimate staged attestation/FCM test is mandatory. [Service identity](https://docs.cloud.google.com/run/docs/securing/service-identity).

Load stable versioned encryption material through Secret Manager integration or a narrowly scoped SDK fetch with in-memory cache. Never regenerate on cold start, put it in a container layer, print it, or rely on an instance-local adjacent key file. Record key version beside ciphertext; implement decrypt-old/encrypt-new rotation and tested backup restoration. Startup/readiness must fail closed if key or database is unavailable.

Initial staging configuration: request-based billing, minimum instances 0, maximum 3 instances, 1 vCPU/512 MiB, concurrency 8, a 30-second outer request timeout with shorter bounded downstream calls. These are **starting test values**, not sizing results. Measure startup boost and larger memory only if needed; cost any adjustment. Instance caps and budget alerts are not hard monetary caps, particularly under arbitrary external traffic.

Preserve `https://push-swarm.dsfactory.org`. Android's `PhoneRuntime.kt` injects it at construction, release Gradle validates it, and machine `PushBinding` persists it as part of custody. Re-registering the phone with another URL does not migrate every machine's binding. DNS may direct the same hostname to a new backend; HTTP redirect or different-host fallback is not equivalent.

Firebase Hosting must forward every existing API method/path/query/body and authentication header unchanged. Set `Cache-Control: private, no-store` for the whole API, validate error/status behavior, and test the retained hostname from an actual release build. It requires a billing-enabled Blaze project; quote both Hosting and Cloud Run usage rather than assuming a free proxy.

**P1 blocker:** prove public direct `run.app` access is disabled, or establish the same unspoofable source identity/quota bucket across both origins. Capture only synthetic-test peer/forwarding data, spoof every forwarded header and alternate origins, and verify identical Host/CORS/cache/auth/rate behavior. Shared global counters alone do not prevent doubling per-source buckets. Neither `Origin` nor client-supplied `X-Forwarded-For` is authority. If no documented trustworthy end-client source survives this integration, redesign admission controls or reject this front door; do not preserve IP-based security claims by assumption. Hosting rollback may change code routing, never rewind durable authorization state. [Cache behavior](https://firebase.google.com/docs/hosting/manage-cache).

## 6. Migration, rollback and recovery

### 6.1 One authority at a time

No dual writers, probabilistic DNS-only fence, or automatic fallback to a stale database. Every mutable operation—including consent, revoke, token rotation and allocation—participates in a durable migration fence. A current source-only latest-wins connection rule is not a cross-backend fence.

Migration manifest contains source/target schema, canonical home, migration epoch, serving/frozen mode, mailbox incarnation, sequence high-water/floor, item counts and checksums, identity/grant/retired-ceremony data, effective retention/quota configuration, and key-version references. It contains only the minimum required protected state; never put secrets in a CI log or public evidence artifact.

### 6.2 Relay migration sequence

1. Ship new clients able to use either existing v1 self-hosted relay or the explicitly selected v2 backend. Persist an authenticated transport/home/epoch record on both endpoints. Do not migrate an active binding until both peers support it. Transport-version fallback within one authority is different from authority fallback.
2. Deploy a legacy fence-capable relay first. Quiesce the entire selected authority scope, reject new mutations and close or drain in-flight sessions deterministically. Persist the freeze before exporting.
3. Export an immutable logical snapshot under the fence. Do not read a running bbolt writer with a backup approach that only works on a stopped store. Verify checksums, all sequence and grant facts, and encrypted data counts before import.
4. Import idempotently into a target in frozen mode. Verify the manifest, reject conflicting import state, and preserve incarnation for a planned live migration. Target ownership cannot be created from a replayed endpoint standing consent.
5. Activate exactly one target epoch, deliver/persist the authenticated transport selection, reconnect and drain. Legacy accepts no writes for migrated routes even while DNS/old sockets remain reachable. Prevent old binaries that do not understand the fence from being restarted against writable state: fence ingress and deployment permissions as well as database metadata.
6. Observe, then expand by whole authority scope. Retain old state encrypted and read-only for the agreed rollback window. A phone session may show a brief reconnect during a planned migration; do not promise zero interruption.

Before any target mutation, a verified abort may unfreeze the source. After **any** target mutation, an old snapshot is no longer a valid rollback. Freeze/export the target's current state and perform a forward-compatible return migration, or roll code back while retaining the current authoritative store/schema. Revocation is a mutation even if no ciphertext append occurred.

### 6.3 Push migration sequence

Migrate push independently of relay. First run the bbolt and Firestore repository conformance suites, stage real Google identity and retained-hostname compatibility, and rehearse token-key restore. At cutover, freeze all old API mutations, drain bounded in-flight provider sends, export installations/addresses/tombstones and required encryption material through an approved secure path, import/verify, then switch the retained hostname to the new authoritative backend.

Existing volatile state needs a **reviewed secure handoff**, not an empty-cache restart. Add freeze/export support for all nonce, idempotency and rate state with absolute expiries and its exact old keying semantics; export atomically under the old authority fence and import with a migration epoch. Let the compatibility reader honor remaining old records until their original expiry while applying the separately approved new registration-conflict policy. Audit every volatile horizon, not just the named cache TTLs. This temporarily durable protected export also requires the privacy amendment and prompt purge.

Do not pause push for ten minutes merely to expire old caches: accepted machine wake obligations last five minutes, so that would deliberately exhaust some. The brief fenced transition must be rehearsed against outstanding deadlines and current retry behavior. Draining wakes can still bind an address, mark a token dead and update caches, so export follows that drain; it is not concurrent with supposedly frozen mutations. If secure state handoff cannot meet the agreed interruption budget, stop cutover. An alternative planned outage requires explicit approval, first preventing new upstream obligations and serving existing ones to their deadline while old push remains authoritative. That is a contract-affecting operational choice, not the default migration.

Do not split existing production push traffic between bbolt and Firestore for a canary. Canary synthetic installations in isolated state first; mixed revisions may share one authoritative Firestore adapter only after schema compatibility is proven. DNS caches/direct old IPs remain fenced. Any post-cutover rollback retains current token generations and revocations.

### 6.4 Backup and disaster restore

Preserve at least the current daily recovery cadence and 14-day operator recovery window unless explicitly amended. Keep mailbox ciphertext, authorization retirement state and push token encryption key versions recoverable together. Test an independent export as well as provider recovery; disaster recovery must not depend on the same broken deployment credentials alone.

Cloudflare SQLite PITR supports a longer provider window, but is not a substitute for an application-consistent, revocation-safe restore, and local development cannot prove it. Restoring historical state can resurrect deleted permissions. Keep serving fenced until reconciliation against a non-rewound authority record succeeds; otherwise fail closed and require re-pair/re-registration. Apply the existing disaster mailbox-incarnation reset policy, rather than importing stale ACKs into a restored store. [SQLite recovery API](https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/).

Set and test restoration objectives in P0; proposed maximum service recovery time is one hour for the first production pilot, and recovery point is the existing daily snapshot baseline unless stronger funded guarantees are selected. These are proposed operational targets, not current measured achievements. State any possible history loss within that disaster window separately from the zero-loss normal append/reconnect guarantee.

## 7. Delivery phases and acceptance gates

The rows are implementation work packages, not completed production work. Assign one accountable owner per package in Beads; let transport and push implementation run independently after P0, with a separate reviewer for each.

| Phase | Deliverable / likely files | Exit gate | Planning effort |
|---|---|---|---|
| P0 — contracts and stop/go | ADRs, schema/metadata amendments, region choice, invoice baseline, exact quota table, home-identity proof, endpoint migration design | Reviewed threat model and owner accepts metadata/region/recovery tradeoffs | 3–5 engineer-days |
| P1 — provider staging | Staging runtime auth spike; Cloud Run+Firestore+Hosting+stable secret; reproducible deployment definitions | Real hibernation, Google API/IAM, front-door/source-identity and cold-start gates pass | 4–7 engineer-days |
| P2 — push repository | `internal/pushgw` domain repository, bbolt/Firestore adapters, shared replay/quota state, key rotation | Same API conformance plus multi-instance race/failure suite; privacy amendment accepted | 5–8 engineer-days |
| P3 — relay v2 | Worker/home/rendezvous actors, schema, auth, bounded delivery, alarms, client subscription adapter | Full old relay behavior matrix + cross-runtime fuzz/negative tests | 8–13 engineer-days |
| P4 — endpoint compatibility | Phone/machine profile migration, reconnect, v1 self-host support, release artifact tests | Old-server/new-client and new-backend/new-client matrix; no wrong-authority fallback | 4–7 engineer-days |
| P5 — migration and recovery | Fences, secure export/import, runbooks, live/disaster restore drills | Fault injection at every transition; old binary/ingress cannot mutate | 4–6 engineer-days |
| P6 — handset and economics | Physical-device campaign, staged canary, measured billing and soak | Product budgets, zero safety violations, approved cost forecast | 5–10 engineer-days plus calendar soak |

Total initial planning range: **33–56 engineer-days** (roughly 7–12 engineer-weeks), not a quote. Two implementation lanes can reduce elapsed calendar time, not total work or the need for review/soak. P1 is a paid, tightly bounded go/no-go checkpoint; do not fund the full port if provider constraints or measured latency invalidate it. Re-estimate after P1. A mobile repository split is additional and excluded.

Recommended release order: P0 → P1 → independent P2/P3 → P4/P5 → push cutover → observation → relay canary → expansion → legacy retirement. Do not combine backend migration, repository split and unrelated phone UX release in one rollout.

### Real-device performance gate

Use the existing playbook budgets without weakening them:

| Measurement | Required result |
|---|---|
| Foreground send | p50 ≤150 ms; p95 ≤300 ms |
| Visible foreground update | p95 ≤300 ms |
| Usable network → reconciled | p95 ≤5 s |
| Normal background FCM submission → observed callback | p95 ≤5 s; observation target, not a provider guarantee |
| Notification open → reconciled | p95 ≤2 s |
| Duplicate semantic effects, automatically replayed uncertain input, lost acknowledged retained items, push plaintext | Zero |

Run at least 200 Wi-Fi and 200 cellular foreground samples, 50 normal-background samples per handset, and 20 per Doze/standby/battery-saver case. Include network switching, airplane mode, app/process death, machine restart, late/dropped/repeated wake, multiple phones/machines, old retained history and revocation during reconnect. Compare identical payloads and routes against the current backend; separate cold and warm distributions and report failures as well as successful-request latency. No local loopback mean substitutes for a handset p95.

### Hosted validation matrix still required

| Test | Measurement / release-blocking failure |
|---|---|
| 0/1/10/100 quiet homes for a day | Measure active handler/request-time union and all rows/alarms/messages; hibernation-eligible idle is not duration-billed even before eviction |
| Idle eviction then append/revoke | Prove constructor reconstruction and attachment reauthorization; no dropped retained items |
| Active bursts and slow reader | Bounded memory/rows/queue; no catch-up scan proportional to sequence high-water |
| Cloud Run cold/warm register/wake | End-to-end distribution incl Hosting, secret/Firestore init, attestation and FCM |
| Firestore production concurrency | Shared nonce/cap/rate correctness and retry/lock contention under multiple instances |
| Dependency failure | Secret/Firestore/FCM timeout returns bounded retryable error; no fail-open auth |
| Front-door bypass | No auth/capability/rate bypass via headers, alternate hostname, run.app or old IP |
| Recovery and deployment | N/N−1 code with current schema; restore does not resurrect retired authority; old-profile clients remain on unmigrated authority or show explicit unsupported/re-pair, never stale-authority fallback |
| Seven-day retention soak | Real deletion occurs without reconnect; earlier expiry is not postponed by new traffic |
| Billing export | Forecast all services from measured counters; include attack/reconnect burst sensitivity |

Start synthetic → one consenting real pairing → small whole-home cohort → 25% → all. These are provisional rollout stages; promotion requires the gates, not a timer. Observe at least seven days through retention/cleanup and then a complete billing period before declaring the monthly target achieved. Keep a documented manual abort and forward-return procedure available throughout.

## 8. Monthly cost and economic stop rules

**Low-usage planning budget: $10/month**, consisting of a $5 Cloudflare paid base plus a $5 contingency for usage and ancillary services. This is a target after old VM/disk/IP resources are retired, **not a measured quote or hard cap**. At the earlier modeled $48.44 baseline, $10 would reduce recurring allocation by about 79%; $5 would reduce it by about 90%. Taxes, domain renewal and engineering are outside those numbers.

| Cost line | Model and check |
|---|---|
| Cloudflare Workers/DO | $5 account paid base; meter Worker ingress separately from DO requests, duration, rows, storage and alarms |
| Cloud Run | Request/compute over applicable remaining allowances; no warm minimum assumed |
| Firestore | Actual reads/writes/deletes/index/storage; only the first eligible database receives free quota; TTL/backup/PITR extra |
| Firebase Hosting | Current included transfer 360 MB/day and storage 10 GB, subject to shared project/plan consumption; overage $0.15/GB transfer, $0.026/GB storage; quote retained-domain setup and both proxy/backend accounting |
| Secrets/images/build/logs | Version/access count, retained image GB, build minutes, bounded log ingestion/retention |
| Recovery | Scheduled backup/export storage, restore operations and any non-rewound authority record |
| Migration overlap | Old infrastructure plus staging/new infrastructure until safe retirement |

Provider pricing references: [Workers](https://developers.cloudflare.com/workers/platform/pricing/), [Durable Objects](https://developers.cloudflare.com/durable-objects/platform/pricing/), [Cloud Run](https://cloud.google.com/run/pricing), [Firestore](https://firebase.google.com/docs/firestore/pricing), [Firebase Hosting](https://firebase.google.com/pricing), [Secret Manager](https://cloud.google.com/secret-manager/pricing), [Artifact Registry](https://cloud.google.com/artifact-registry/pricing).

Model dimensions, not just users: monthly new connections, incoming WebSocket messages, actual active actor seconds, rows read/written including indexes/ACK/deletion, retained ciphertext GB, registration/rotation/wake calls, transaction retries, image/backup storage and egress. Add cold-start Secret Manager/KMS fetches and retry amplification explicitly; test per-instance key caching/fail-closed behavior. Apply current provider billing rounding and the account's remaining shared allowances. Quiet users and continuously streaming users have very different costs. P1 must demonstrate Hosting/domain and all ancillary costs fit the contingency or revise the $10 target before proceeding.

The supplied sensitivity check demonstrates why repeatedly persisting empty 25-second waits is the wrong target design: at three writes per wait, ten always-waiting endpoints imply 103,680 writes/day before business traffic. It is a hypothetical implementation, not measured Swarm utilization. Subscription removes that synthetic idle work, but eight appends/second sustained for an hour still produces 28,800 appends. Measure real writes/append before projecting scale.

Budget alerts at $5, $10 and $20 are suggested operator signals, not shutdown controls. Enforce admission quotas before expensive durable allocation/provider calls, bounded logs and retained artifacts, and an operator kill switch for abusive registration. Do not automatically disable legitimate revocation or required deletion when an alert fires. Any Redis VM, SQL instance, public load balancer, NAT gateway or keep-warm cron changes the cost model and requires a new decision.

At $10/month, one year of infrastructure savings against $48.44 is about $461.28 before exclusions. Engineering cost is `engineer-days × agreed day rate`; the full port will not normally repay itself from today's small hosting bill alone. The decision should explicitly value scale/operations, or select the lower-change hosting option instead.

## 9. Private mobile repository decision

Recommendation: **do not move the mobile app merely for security concealment or hosting savings**. Private hosting can be free, but source access is not the phone's runtime security boundary. Public history and released APK contents remain inspectable. If future UX/IP or contributor/release access separation is the goal, a private Android-shell repository consuming an exact signed/provenance-verified AAR is a reasonable independent project.

A robust Android-only split is estimated at 2–4 additional engineer-weeks, mainly build/release/compatibility work. Backend monthly runtime cost is unchanged. Illustrative private Linux CI costs are $6/month for 100 × 30-minute builds or $42 for 300 × 30-minute builds if the full 2,000-minute Free allowance remains available; $18/$54 if it is already exhausted. Storage, account plan, extra jobs and artifact distribution are additional. These build durations are assumptions, not measured current CI usage.

Keep package ID, Play signing identity, Firebase identity, versionCode continuity, Keystore namespaces and backup/lifecycle behavior stable. Do not combine the split with this transport migration. The full comparison, evidence, cost assumptions and acceptance gates are in [the private-repository decision](../research/mobile-private-repository.md).

## 10. Completion definition and authorization boundary

The **planning task** is complete when this plan, source-mapped findings and reproducible local probes are reviewed and published on the planning branch. Production implementation is complete only when P0–P6 and existing release gates pass, migration is verified, old billable resources are deliberately retired, and measured billing confirms the target.

This work did not deploy provider resources, send real push notifications, change IAM/DNS, migrate live data, change repository visibility or move the app. Those require the approved implementation workflow and staging/production authority. Outstanding region/privacy/recovery choices are decision gates, not silently chosen settings.
