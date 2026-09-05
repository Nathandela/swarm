# Push-gateway v2 operator runbook

Status: source contract; hosted setup and recovery drills remain open.
See the [deployment contract](push-gateway-deploy.md) and
[replacement plan](../specifications/remote-scale-to-zero-plan.md).

## Observe

The optional admin listener is loopback-only. Its `/healthz`, `/readyz` and `/metrics`
are not routes on the public handler. Observe aggregate status/operation counts, instance
restarts, bounded cleanup outcomes/backlog and storage growth. Include Firestore readiness
reads and cleanup operations in the cost model. No background worker heartbeat can prove
health while request-billed instances are idle.

Never log raw paths, FCM tokens, capabilities, installation public keys, attestation
tokens or request bodies. Provider failures are request-scoped; readiness must not send
test notifications or consume attestation verdicts.

## Bounded cleanup

Logical expiry is enforced at use; asynchronous physical deletion is not authorization.
Arrange an explicitly authorized, bounded cleanup invocation and verify it against the
real database before launch. Run repeated bounded passes when a backlog exists; preserve
revocation tombstones until their required lifetime expires. A successful single batch
is not proof that the whole backlog is empty. Do not use keep-warm traffic to obtain CPU.

The `retention` subcommand performs one bounded pass and exits, using the same validated
Firestore namespace, keyring and admission flags as serving. It does not open a listener
or grant a public cleanup endpoint. Schedule it explicitly in the hosted environment;
source publication has not created a job or schedule.

## Recovery and key rotation

Back up v2 Firestore state and retain all key versions needed by the encrypted tokens
and approved recovery copies. The initial policy in the plan is daily copies with a
14-day recovery window; its hosted implementation and restore drill are not complete.
Provider TTL is not a backup, and a backup without matching token keys is insufficient.

Fence serving before a restore. Restore into isolated state, recover required key
versions, and reconcile revoked/expired authority before allowing traffic. A snapshot
can resurrect revoked installations; if reconciliation cannot be proved, remain fenced
and require explicit fresh enrollment. Never silently point clients at an older store.

For rotation, publish a reviewed mounted keyring containing the new active version and
all still-needed old versions, then validate a new immutable service revision. Do not
remove an old key while live records or recovery copies still require it. Preserve the
registration HMAC key until its separate retry-state transition is explicitly reviewed.
The application reads its keyring at startup; do not assume a changed mount updates an
already running process.

## Upgrade and rollback

Review and pin the new image and secret versions; validate with isolated fresh owner
state before changing hosted traffic. Rollback means compatible v2 code with the current
v2 store, not restoration of a stale authority snapshot. Readiness alone is insufficient:
verify authenticated registration, token rotation, wake/retry, revoke and recovery.
Neither this runbook nor source publication authorizes deployment or deletion of resources.
