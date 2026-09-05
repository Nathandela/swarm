# Remote control v2: clean replacement plan

Updated 2026-09-05. Source inspected: v0.13.27 / f1621618. This revision supersedes the compatibility-preserving migration plan in commit a9ef4d24.

Owner decisions: there are **no current users**; backward compatibility must be dropped and cleaned up. Use **one live environment**, developed and deployed directly from `main`; no standing staging environment or promotion pipeline. Price and efficiency take priority over geographic residency. Bounded shared metadata is acceptable; security still matters. Initial audience is the owner and friends, not an unrestricted public service.

Status: implementation underway; the sole relay is deployed with admission closed, not usable as a completed replacement. Existing local experiments are supporting evidence, not a completed v2 implementation. Follow-up: agents-tracker-wjp4. Architecture is recorded in [ADR-027](../adr/ADR-027-clean-remote-control-v2.md); current test and review evidence is in the [implementation journal](../verification/remote-scale-to-zero/implementation.md).

## 1. Decision

Build one clean v2 remote-control system, then retire the unused v1 system. This is a **replacement**, not a live-user migration.

Target:

- Cloudflare Workers + SQLite-backed Durable Objects: encrypted relay, authenticated subscriptions, bounded durable mailboxes.
- Request-billed Cloud Run, minimum instances zero: short-lived push requests.
- One Firestore persistence implementation for push, colocated with Cloud Run.
- Direct provider-issued HTTPS push URL in the new phone/machine configuration.
- Stable versioned token-encryption material in Secret Manager, separate least-privilege push identity.
- Fresh installation records, fresh pairing and fresh stores. No import of old mail, credentials, cursors, push bindings or volatile caches.

We retain useful, tested cryptography and endpoint execution code. Permission to rebuild does not make rewriting sound security primitives useful.

### Explicitly out of scope

No v1 protocol support, old-client negotiation, polling fallback, dual backend, old-origin redirects, Firebase Hosting compatibility proxy, transparent state conversion, bbolt/Firestore adapter parity, N/N−1 compatibility campaign, or gradual migration of existing user cohorts.

No P2P or disposable-versus-durable history split in the first release. They are separate optimizations, not prerequisites for removing the VM bill. Do not move Android into a private repository as part of this project.

No duplicate staging Worker, Cloud Run service, Firestore database, permanent staging
credentials or staging-to-production promotion machinery. Local workerd, Firestore
emulator and disposable test directories remain isolated from live state.

Do not build anonymous public signup or public-fleet sophistication just for a small group. Owner-controlled admission is the preferred onboarding direction. Keep current attestation until a replacement enrollment contract is deliberately reviewed; “friends only” does not itself authenticate internet callers.

### What changed from the previous plan

| Previous obligation | New decision |
|---|---|
| EU relay / Zurich push placement | No residency restriction; choose from measured price and latency |
| Keep old push hostname and binding custody | New HTTPS URL, configured at fresh setup |
| Firebase Hosting front door | Remove from architecture |
| Old and new phone/relay compatibility | One v2 protocol and registry-only fresh phone state |
| Preserve running mailbox incarnation during import | No import; new state/generation |
| Securely hand off old nonce/idempotency/rate caches | No handoff; new service starts with new enrollment |
| Keep bbolt and Firestore implementations equivalent | Firestore only; retain transaction interfaces for tests |
| Production cohort canaries | Owner end-to-end pilot, then invite friends |
| Roll back to an old deployment/state | Repair or roll v2 code back with current v2 state; never silently reactivate v1 |

These deletions reduce build and review scope. They do not turn local feasibility probes into production proof.

### One live environment, incremental activation

The delivery loop is TDD, implementation, independent review, local verification,
integration on `main`, then direct deployment and bounded hosted smoke tests of that
same live environment. "Live" names the sole deployment target; it does not certify
unfinished features as production-ready. There is no separate staging resource set.

For the first relay upload, keep admission unconfigured: v2 endpoints must reject
requests before dispatching either Durable Object. A public banner proves only routing
and TLS, not pairing or command readiness. Enable the fresh owner's machine only after
the relevant authentication, client-wiring and hosted-abuse gates pass. Never deploy
test allowlists, shortened lifetimes or disabled cleanup from the local suite.

Run initial hosted experiments on fresh owner-only v2 state. Once useful state exists,
keep synthetic tests in local/emulated stores; use an explicitly approved temporary
recovery target only when a provider restore drill cannot be done safely in place.
This is not a standing staging service. Do not reset live state merely to rerun tests.
Cloud resource creation, public exposure and paid-plan changes retain their separate
operator approval boundaries. The relay's [runbook](../../services/relay/README.md)
records the concrete single-environment setup.

## 2. Target architecture

    Phone foreground ── WSS ─┐
                              ├─ Worker routing ─ canonical home Durable Object
    Machine gateway ── WSS ──┘                     ciphertext / grants / cursors

    Phone registration ─ HTTPS ─┐
                                 ├─ direct Cloud Run URL ─ Firestore
    Machine wake ─────── HTTPS ─┘                │
                                             Google FCM ─ phone wake

The machine owns execution, semantic operation IDs, input uncertainty and pending wake obligations. The phone owns its display and local durable reconciliation. The relay sees routing metadata and ciphertext; push receives content-free wakes, not commands. A public HTTPS URL is not a credential.

Both peers establish outbound WebSockets, accepted as inbound connections by the actor. No actor-owned outgoing machine socket, always-running promise, periodic empty mailbox request or keep-warm cron.

### Region selection

Compare a small shortlist of regions supporting the required Cloud Run, regional Firestore and Google APIs—initial candidates us-central1 and a low-price European region. Validate official regional tariffs and remaining account allowances; measure cold/warm request latency from the actual owner devices. Colocate Cloud Run and Firestore.

Do not choose a distant region just for a hypothetical few cents if the owner experience worsens. No regional privacy approval is needed now. Cloudflare placement is selected for efficient routing without a required EU jurisdiction. [Cloud Run pricing](https://cloud.google.com/run/pricing), [DO placement](https://developers.cloudflare.com/durable-objects/reference/data-location/).

### Canonical home and multi-machine isolation

One deterministic home per machine relay identity contains its separately keyed phone bindings. Existing R4 phone namespaces already isolate keys, sequences, cursors and push addresses; remove the singleton migrator, not the registry model.

Define home_id as hex(SHA256(domain || operator_namespace || machine_relay_RID)), using an exact length-delimited encoding and fixed domain swarm-relay-home/v1. Pin vectors before implementation. Only operator configuration selects the namespace. Authenticated pairing binds both endpoints to this home; a URL hint only locates it.

Do not let an old consent select a clean actor and resurrect a revoked pairing. Initialize durable authority only after valid machine proof and bounded admission checks. A replaced/lost machine identity requires fresh pairing, never silent inheritance. Ordinary epoch rotation on revoke retains the machine relay-auth identity. No transparent auth-key rotation feature is required for the first release.

A home-scoped revoke affects that binding/home; whole-phone removal must enumerate independently keyed bindings. Avoid one global actor or one actor per socket.

## 3. Security and reliability that survive the cleanup

These are v2 product guarantees, not backward compatibility.

| Guarantee | Required test |
|---|---|
| Authenticated pairing and encrypted session traffic | QR/SAS, challenge freshness, wrong key/home/context and tamper rejection |
| Revocation remains effective | Old consent, old socket and another home cannot revive access |
| Durable append before success/live delivery | Crash before commit and after commit/before response |
| Durable local progress before ACK | Lost ACK/reconnect does not lose accepted retained work |
| Safe command retries | Existing semantic operation-ID rules; no blanket exactly-once promise for external effects |
| No automatic replay of uncertain raw input | Crash around checkpoint/PTY write/receipt |
| Per-machine isolation | One pairing's cursor/key/revoke cannot affect another |
| Bounded resource consumption | Size/depth/rate limits, slow reader, reconnect storm, unauthenticated allocation |
| Content-free push and isolated credentials | No session text, signing keys or capabilities in push/logs |
| Revocation-safe v2 restore | Restored state cannot silently revive expired/revoked authority |

Keep the first v2 history baseline at the existing seven-day/10,000-item limits and preserve overflow behavior unless a separate product change explicitly replaces it. No shortening of history merely because compatibility is gone.

User acceptance resolves the earlier objection to durable replay/quota metadata. Update specifications to describe fields and lifetimes, but do not reopen a residency/privacy approval loop. Secrets and capability values still must not be logged.

References: [product playbook](remote-control-product-playbook.md), [system invariants](../invariants/system-invariants.md), [push API](push-gateway-api.md). Amend obsolete compatibility requirements before deleting their tests; preserve safety tests as v2 tests.

## 4. Relay and phone implementation

### Single v2 transport

Use one authenticated WebSocket subscription protocol. One WebSocket message carries one bounded v2 envelope; no v1 byte-stream framing adapter or fallback. Specify envelope types, request correlation, delivery events, errors, size bounds and capabilities actually needed by v2. Do not carry legacy hello/wait negotiation “just in case.”

Use canonical decimal strings for uint64 cursors/sequences, BigInt for JavaScript arithmetic and fixed-width 20-digit TEXT for sortable SQLite keys. Reject numeric JSON, signs, fractions/exponents, noncanonical forms and overflow. At exhaustion, return a typed error; never wrap. The actual Go-to-Node probe demonstrates why ordinary JSON numbers are unsafe.

Enforce the selected 1 MiB envelope ceiling before JSON parsing/materializing fields, plus platform ingress limits, batch-byte limits and unknown-version rejection. Specify extension rules explicitly; an unknown version never triggers legacy behavior.

Port existing Ed25519 challenge/consent verification and HKDF RID derivation with exact vectors. Reuse crypto bytes because they are tested, not to support old clients. Verify them in actual workerd, not only Node WebCrypto. The local HMAC routing ticket is a fixture and must not become production auth.

### Storage and subscriptions

Logical schema: home metadata/schema version, authenticated membership and generations, live/retired consents, mailbox metadata, ciphertext items, bounded append receipts if needed, and quota windows.

An append transaction checks current authorization/generation, quota/depth/bytes, allocates a cursor and commits the item/high-water. Only after commit can it reply or forward. Transport deduplication compares authenticated ID plus body digest; it does not replace machine-side operation IDs.

Subscribe from durable cursor and generation using an indexed range query. Never iterate over every integer up to a 64-bit high-water. Bound frames and bytes in flight. Slow readers leave data durable rather than growing an unlimited queue.

ACK is monotonic, route/generation-bound and cannot exceed eligible durable/sent progress. Missing ACK delays compaction, not correctness. Revoke atomically changes authority and purges required data, then closes sockets; late handlers recheck durable membership.

Use hibernation WebSocket APIs, small attachments and event-driven presence. Handlers finish after registering subscriptions. Replace the independent periodic presence query with connect/disconnect/unknown transitions and honest stale state. No application timer to keep actors warm. [WebSocket guidance](https://developers.cloudflare.com/durable-objects/best-practices/websockets/), [lifecycle](https://developers.cloudflare.com/durable-objects/concepts/durable-object-lifecycle/).

Schedule real cleanup at the earliest expiry, not the most recent append's deadline. Read-time expiry and bounded physical deletion are both required. Alarms can coexist with hibernation; their work is still billable. The supplied staggered-expiry test found and covered an actual scheduling mistake.

### Source-mapped cleanup

| Area | Remove / replace | Keep |
|---|---|---|
| mobile/relay.go | waitSupport, drainWait demotion, drainPoll, old hello/capability fallbacks | Local durable checkpoint, authenticated connection ownership |
| internal/remotegw/command_loop.go | compatPollInterval, runPoll, MailboxRead fallback | Command authority, receipts, bounded execution |
| internal/remote/relay/{wire,client,server}.go | Old RPC codec/read/wait/cancel implementation from active v2 build | Ported crypto/auth, bounded mailbox and revoke behavior |
| internal/phonecore/machinemanager.go, machineregistry.go | SingleMachineAdapter/Manager, MigrateSingletonToRegistry and old-layout branches | RegistryManager, independent namespace/key isolation |
| mobile/machines.go, app.go | Singleton startup/migration/re-resume fallbacks | Fresh registry setup and current feature behavior |
| internal/remotegw/pushtransport.go, service.go; cmd/swarm-remote/config.go | legacy_relay route/default, push-gateway.json bridge/retirement reader | New explicit push mode and durable wake obligation |
| Android config / release validation | Old required push hostname and stale generated configuration | Approved HTTPS endpoint validation, signing/package/Keystore security |
| deploy/ and documentation | Old service manifests, commands, compatibility flags and active runbooks | Reproducible v2 deploy/teardown and useful historical evidence |

This table defines future code work; these files have not been deleted by this planning revision. Inventory callers before deleting an entire file: security helpers and active features can share files with obsolete adapters. A generic interface is not compatibility baggage when it encodes transactions or permits isolated tests.

The old Go server and bbolt path are not retained as alternate v2 production backends. If independent self-hosting remains a desired product feature, provide a separately scoped v2 deployment story; do not keep the old implementation alive under that label or silently promise feature parity.

## 5. Push implementation

### One transactional implementation

Keep a domain-level repository boundary in internal/pushgw, but implement only Firestore for the new runtime. Use emulator/fake implementations for tests, not a production bbolt compatibility adapter.

Domain operations include AuthenticateAndTouch, RegisterOrReturn, AllocateAddress, RotateToken, RevokeAddress, ClaimWakeAttempt, CompleteWakeAttempt and bounded expiry cleanup. Define new error/status behavior directly; no emulation of historical cache quirks.

Collections: installations, addresses, revocation tombstones, registration attempts, nonce claims, wake attempts and rate windows. Retain the existing starting lifetimes where sensible: unbound address 10 minutes, nonce 120 seconds, registration idempotency 10 minutes, wake obligation five minutes, revocation tombstone/idempotency retention seven days, installation expiry after 180 days without authenticated refresh. Tombstones are security/retry state, not optional telemetry.

Use HMAC(idempotency_key) as registration record identity; compare a stored body digest transactionally. Same key/different body is conflict, not a second installation. Authentication/replay state must be shared across instances. Recheck key generation at the commit boundary.

Firestore callbacks can retry: never send FCM or perform attestation inside a retryable transaction. The emulator deliberately reproduced duplicate external sends from that mistake. [Transactions](https://firebase.google.com/docs/firestore/manage-data/transactions).

### Wake protocol

    authenticate / capability check
      → transaction: validate + quota + claim
      → FCM submission outside transaction
      → transaction: completion CAS(attempt ID, token generation)

Use bounded at-least-once submission attempts with byte-identical retries and phone replay handling. No transaction can atomically commit with FCM: crash after provider acceptance/before completion remains ambiguous. Lease takeover may repeat that wake; refusing all takeovers may lose an attempt. Test both sides of the send boundary and keep the original deadline.

A stale UNREGISTERED result cannot erase a newer token. Revoke-before-claim denies it; a revoke cannot retract a provider call already in flight. Push never becomes command authority or an unlimited delivery queue.

### Admission, identity and ingress

Keep authenticated installation/capability requests, existing attestation and least-privilege Google identity in the first working slice. Add owner-approved, short-lived, single-use enrollment if needed to restrict onboarding. Replacing Play Integrity is a separate deliberate enrollment-security decision, not an automatic consequence of zero users.

Owner-approved installation public keys are an allowlist, not secret invitations. Require
PG-AUTH-15 registration proof using the existing P-256 signer before shared state, quota or
attestation verification. Bind the exact final body and idempotency key; re-sign the saved
attempt on retry without changing its identity. This closes non-holder key reuse but does
not impose one installation per legitimate key or replace attestation and abuse limits.

Use the actual service-issued run.app HTTPS URL in fresh configuration. No DNS migration, Firebase Hosting, old-origin alias or redirect. Platform-public access is needed for ordinary app clients; application authentication remains mandatory. A guessed URL must grant no access.

Test forwarded-header spoofing and source-rate-limit derivation even without Hosting. If reliable client IP is unavailable, change that limiter design; do not trust arbitrary X-Forwarded-For. Installation/capability/global quotas still apply. Bound unauthenticated work before expensive allocation/attestation.

Use Secret Manager for stable versioned token-encryption material; never bake it into images or regenerate on startup. Test missing-secret fail-closed behavior, key rotation and recovery. Real Google ADC/Play Integrity/FCM calls remain a hosted owner-pilot gate in the one live environment. [Service identity](https://docs.cloud.google.com/run/docs/securing/service-identity).

Initial live owner-pilot values: request-based billing, min instances 0, max instances 3, 1 vCPU/512 MiB, concurrency 8, 30-second outer deadline and shorter downstream deadlines. These are conservative starting inputs, not measured sizing. Max instances and alerts are not hard monetary limits.

Logical expiry is enforced at use. Firestore TTL is asynchronous and does not cascade into subcollections; use bounded cleanup as required. Explicitly cost transaction retries, GC, backups and cold-start key fetches. [TTL](https://firebase.google.com/docs/firestore/ttl), [Firestore pricing](https://firebase.google.com/docs/firestore/pricing).

## 6. Fresh launch, retirement and v2 recovery

### No live migration machinery

Start with a fresh v2 database/namespace and fresh owner enrollment. The phone begins registry-only. Old pairing/config formats are unsupported and must yield a clear reset/re-pair instruction or deliberate refusal—not a migration shim or automatic old-server connection.

No bbolt export/import pipeline, old cursor/incarnation preservation, old token-key handoff, nonce/cache handoff, legacy compatibility reader, dual write, or old-backend rollback. No requirement to coordinate with users who do not exist.

Before retiring old resources, inventory exact targets and retain an approved recoverable snapshot/export of any development state worth keeping. This is an operator safeguard, not a product migration feature. Do not silently erase local conversation history or unrelated developer state.

Release the new owner phone and machine together; pair fresh; verify command/stream/reconnect/push/token rotation/revoke. Then fence old remote-control ingress/startup and remove obsolete application code/deployment paths. Retire identified old VM/disk/IP/backup resources only through the explicitly authorized teardown procedure. Do not restart local working sessions merely to clean a backend.

### Recovery belongs to v2

Once owner/friends create real v2 state, backups matter. Start with daily recovery copies and a 14-day operator recovery window, costed and tested; simplify that policy only through an explicit recovery decision.

A restored database can resurrect revoked permissions. Keep serving fenced until authority reconciliation succeeds, or fail closed and require fresh pairing/enrollment. Preserve or reset v2 mailbox generation according to the restore contract; never accept stale ACKs as current authority.

Provider PITR is useful but not sufficient proof of application recovery. Exercise restore and key recovery without overwriting useful live state: start with disposable local state, then an explicitly approved temporary provider recovery target if required for hosted proof. Do not maintain a second environment just for this drill. [SQLite recovery](https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/).

An operational rollback means compatible v2 code with the current v2 store, or an explicitly approved fresh v2 reset. It never means pointing clients at a stale v1 database. No old-version support matrix is carried into the initial launch.

## 7. Work packages and go/no-go gates

These are future implementation packages. The owner has resolved the no-users, no-compatibility, location and shared-metadata decisions. Record their consequences in ADRs/specs; do not ask the owner to repeat them.

| Phase | Work | Exit gate |
|---|---|---|
| P0 — freeze v2 scope | Minimal protocol/schema, safety test matrix, cleanup caller inventory, updated contracts | No compatibility work package remains; safety invariants explicit |
| P1 — real vertical slice | Minimum secure hosted relay/push slice; fresh test phone/machine enrollment on direct URL | Authentication/replay/admission/key-custody and ingress-spoof gates pass before real command/wake; then pair, stream, reconnect, background wake and measure cold/warm latency/counters |
| P2 — finish push | Single Firestore repository, shared replay/quota/expiry, secrets, enrollment/revoke | Emulator races and real IAM/attestation/FCM pass |
| P3 — finish relay | Production auth, bounded subscriptions/storage, alarms, home isolation | Crash/revoke/slow-reader/full-cursor tests pass |
| P4 — remove legacy paths | Registry-only app, one transport/push path, obsolete flags/schema/config/tests removed | Clean checkout builds v2; no old-protocol fallback or hidden compatibility branch |
| P5 — launch/recovery | Fresh owner installation, v2 restore drill, exact legacy-resource retirement procedure | End-to-end owner use works; no double authority; recovery and teardown verified |
| P6 — friends/soak/cost | Invite friends, lifecycle campaign, retention soak, actual usage accounting | Responsive/safe use and approved forecast; retire old allocation when no longer needed |

Build P1 first. A deployed vertical slice is more informative than completing two large ports independently and connecting them at the end. P2/P3 can then proceed in parallel behind the proven integration.

P1 is not a security-light public demo. Before a real command or wake, implement bounded parsers/deadlines, real relay pairing/Ed25519/E2EE and revocation, installation signatures/capabilities, current Play Integrity verification, shared nonce/idempotency claims, conservative global admission, stable Secret Manager key custody and least-privilege runtime identity. Use fresh owner-only state in the one live environment and a fresh installation; do not import old tokens/capabilities or local fixture identities. Test direct-host/header spoofing and all exposed service/revision host forms, then select a trustworthy limiter design. Failure here stops P1. P2/P3 finish coverage, rotation/recovery, lifecycle, resource bounds and cleanup; they do not postpone the security boundary until after public exposure.

The previous 33–56 engineer-day estimate included work now explicitly removed and is withdrawn as the commitment baseline. Re-estimate after P1 using remaining source work, not a guessed percentage discount. The runtime port, Android lifecycle, security and recovery work still exist.

### Experience and safety gates

Keep the existing starting budgets: foreground send p50 ≤150 ms / p95 ≤300 ms; visible update p95 ≤300 ms; usable network to reconciled p95 ≤5 s; notification-open reconciliation p95 ≤2 s. Normal background FCM submission-to-observed-callback p95 ≤5 s is an observation target, not a provider delivery guarantee.

Before inviting friends, run the owner handset on Wi-Fi and cellular, foreground/background, Doze/battery saver, network change, app/process death and machine restart. Repeat revoke, duplicated/dropped wake, stale cursor, slow consumer and fresh multiple-machine isolation cases. A staged multi-machine UI must not claim capabilities it has not implemented.

Before claiming unchanged performance, collect the playbook sample counts: at least 200 Wi-Fi/200 cellular foreground samples, 50 normal-background per handset, 20 per power-management case. Report cold/warm distributions and failures separately. Zero unintended duplicate semantic effects, autoreplayed uncertain raw input, lost acknowledged retained items or plaintext push are required.

Measure actual hosted reconstruction and eligible-idle duration separately. Hibernation-eligible idle need not wait for physical eviction to avoid duration billing. Start with 0/1/10 quiet homes plus representative bursts; large public-fleet benchmarking is not a first-launch prerequisite. Complete a real retention window and a billing period before declaring the monthly target measured.

## 8. Economics

The low-use goal remains approximately **$5–10/month after retiring the old infrastructure**, with $10 as a planning budget rather than a quote or spending cap. Removing compatibility work mainly reduces engineering and operational complexity; it does not make active computation or abuse free.

The owner approved the initial admission-closed relay on the existing Workers Free plan;
keep that plan while measuring the owner pilot. This supersedes the earlier assumption
of an immediate $5 Paid subscription. Free-plan limits can stop service, and native
per-location rate limits do not guarantee availability or impose a global spending
cap. Revisit Paid only if measured use or a required feature justifies its base fee,
with a separate billing decision. The $5–10 total remains a planning allowance, not a
measured bill or a minimum charge. [Workers pricing](https://developers.cloudflare.com/workers/platform/pricing/), [Durable Objects pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/).

| Remaining line | Measure |
|---|---|
| Worker / DO | Ingress, request units, active duration, storage rows/index/ACK/delete/alarm work, retained GB |
| Cloud Run / Firestore | Actual regional tariffs and shared allowances, cold starts, retries, document/index/deletion/storage |
| Secrets / image / build / logs | Accesses, retained versions/images, build minutes, bounded telemetry |
| Recovery / temporary overlap | V2 backups and old resources retained during verification |
| Abuse | Unauthenticated work, reconnect bursts, admission pressure and provider quotas |

There is no Firebase Hosting, dedicated public IP, fixed load balancer, always-on Redis/SQL, NAT gateway or keep-warm service in the baseline. If one becomes necessary, reopen the cost model explicitly.

Configure conservative admission limits, bounded telemetry, budget alerts and operator teardown/disable controls. Do not disable revocation or required cleanup just because a cost alert fires. Account-wide free allowance availability remains unverified until checked.

For a small friends-only service, engineering effort can dominate the savings. Keep the simpler VPS alternative as a stop/go comparison at P1, not a second backend to maintain. If the serverless prototype fails the cost/experience test, stop the rewrite and choose the simpler deployment.

## 9. Evidence and cleanup completion

[Local verification package](../verification/remote-scale-to-zero/README.md): actual local DO APIs/storage/alarms, Firestore concurrency, cross-language crypto/numeric probes and selected Go behavior tests passed. The legacy frame/import/fence experiments remain historical evidence only; they are not v2 requirements and need not be ported into the new release suite.

Hosted billing/eviction, Google identity, fresh real-phone lifecycle, full v2 auth, real recovery and final release gates remain unrun. This revision adds no claimed test results.

Implementation cleanup is complete only when source, tests, release config, deployment manifests, active runbooks and generated artifacts agree on one v2 path. Search for legacy selectors and RPC names, classify every remaining occurrence as active feature/shared primitive or historical documentation, and remove unreachable/deprecated production branches. Passing old fallback tests is not a reason to retain the fallback.

The [private mobile repository assessment](../research/mobile-private-repository.md) remains a separate decision. Given the owner’s clarified privacy priority, defer the split; it does not improve this runtime cost model.

The planning revision itself changed no provider resources, production code, repository visibility or user data. The owner subsequently authorized implementation and direct integration on main; tool-level publication and cloud permission checks still apply. The implementation journal records actual changes separately from this plan.
