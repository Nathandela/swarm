# ADR-027: Clean remote-control v2 replacement

## Status

Accepted architecture by the owner. Implementation and hosted release gates remain open;
this decision is not a production-readiness certificate.

## Date

2026-09-05

## Context

The remote-control system has no current users. The owner explicitly permits breaking
changes and requires removal of backward compatibility. Price and efficiency take
priority over geographic residency; bounded shared metadata is acceptable, but secure
pairing, revocation, encrypted content and safe command execution remain required.
The first audience is the owner and friends, not unrestricted public enrollment.

The [replacement plan](../specifications/remote-scale-to-zero-plan.md) replaces the
earlier live-migration proposal. Existing cryptography and endpoint execution guarantees
are useful implementation assets, not reasons to retain the old deployment.

## Decision

1. Use one native v2 WebSocket relay on Cloudflare Workers and SQLite Durable Objects.
   Both peers connect inbound to the actor; no polling fallback or actor-owned outgoing
   socket. Define canonical home routing and role/home-bound Ed25519 authentication
   independently: a routing hint never grants authority. Owner-controlled machine
   admission bounds allocation but does not replace authentication. Keep the compact
   QR and resolve its short-lived, machine-created rendezvous reference at the relay.
2. Use request-billed Cloud Run with minimum instances zero and colocated Firestore for
   push. Firestore is the sole production push store. Supply stable, versioned token
   encryption material through Secret Manager; never generate it on runtime startup.
   The release and publisher explicitly agree on the direct HTTPS service origin.
3. Start with fresh phone registry, pairings, installation records and cloud stores.
   Do not import v1 state, preserve origins, negotiate old protocols, or retain an old
   backend as a fallback. Remove obsolete callers and adapters as their v2 replacements
   are integrated and verified. This does not authorize erasing unrelated developer
   sessions or conversation history.
4. Retain sound cryptographic formats and application guarantees. The existing
   `WakeV1` encrypted format and push HTTP API paths may remain the sole supported
   format; calling the product v2 does not require renaming every cryptographic domain
   or API identifier. This permission does not permit legacy runtime negotiation.
5. Replace process-local replay/idempotency/quota authority with bounded, shared
   transactional state. This deliberately amends the memory-only requirement in
   PG-RET-4 and the closed durable field set in PG-RET-10 of the
   [push API](../specifications/push-gateway-api.md). Permitted additional fields are
   token generation, encryption key version, keyed nonce/idempotency digests, request
   body digests, quota counters/windows, wake attempt identity/state/lease/deadline/
   outcome, and expiry. No plaintext FCM tokens, raw capabilities, signatures,
   attestation tokens or session content may be persisted in these records or logs.
   Existing encrypted token custody remains required.
6. Initial lifetimes are 120 seconds for nonce claims, ten minutes for registration
   idempotency, five minutes for wake obligations and seven days for revocation
   tombstones. Enforce logical expiry at use, with bounded physical cleanup. Perform
   attestation and FCM calls outside retryable transactions; complete attempts by
   compare-and-swap against attempt and token generation. A provider-accepted send
   followed by a crash remains ambiguous, not exactly-once delivery.
7. Prove the minimum secure end-to-end slice before calling the independent ports
   complete. Local workerd/emulator tests are necessary but do not prove hosted
   hibernation, actual Google identity, phone lifecycle, performance or billing.
   No public security-light demo and no claim of production readiness before the
   plan's gates pass.
8. Maintain one live environment, with direct deployment from reviewed and tested
   `main`. No standing staging resources, duplicate backend or promotion pipeline.
   Keep automated workerd/emulator tests local and isolated. Initial live relay
   admission stays closed until the active-use security and client-wiring gates pass;
   a successful upload is not a readiness certificate. A provider recovery drill may
   use a separately approved disposable target, never silently overwrite useful state.

## Consequences

### Positive

There is no live migration, cache handoff, dual implementation, old-host proxy or
compatibility campaign to build. Native hibernation and request billing remove the
always-on VM requirement. Shared transaction authority works across concurrent push
instances and cold starts.

### Negative

All existing development pairings require reset and fresh enrollment. The relay becomes
Cloudflare-specific and push becomes Google-specific. Durable metadata and recovery
have real cost and retention obligations. Actual cold-start/device measurements and
revocation-safe recovery remain release gates, not assumed platform properties.

## Alternatives considered

- A small VPS remains the P1 stop/go cost-and-experience comparison, not a second backend.
- P2P and a disposable/durable history split are deferred; neither is necessary to
  eliminate the fixed VM bill.
- A private Android repository is deferred; it does not improve runtime cost or replace
  secure key custody and release provenance.
- Compatibility-preserving migration is rejected because there are no users to migrate.
