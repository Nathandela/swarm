# Push-gateway v2 deployment contract

Status: source implementation, **not a deployed or production-validated service**.
Follow [ADR-027](../adr/ADR-027-clean-remote-control-v2.md) and the
[replacement plan](../specifications/remote-scale-to-zero-plan.md).
The old VM startup manifests have been removed from the active bundle. Their removal
does not stop, delete or migrate any existing resource.

## Identity and fresh configuration

The production project is `swarm-8404f`, number `733314021126`; Android package
`dev.swarm.phone`; Play App Signing certificate SHA-256, canonical base64url:
`hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU`.
Every operator cloud command must specify `--project=swarm-8404f`; never change or
trust an unrelated ambient project.

Use one dedicated attached runtime service account for Firestore, FCM and Play Integrity,
with only the required permissions. Keep Play publishing/upload credentials separate.
Firestore is the sole production store. Use a fresh, explicitly selected namespace;
do not import development tokens, cursors, caches or old database files.
Keep enrollment restricted to owner-approved installation public keys, in addition to
the existing attestation and signed-request checks.

The command requires explicit `-gcp-project-id swarm-8404f` and
`-firestore-namespace` (for example, a fresh `push-v2-owner-pilot`); the database is
`(default)`. Namespaces are bounded canonical lower-case identifiers, not an implicit
default or a means of importing old authority. Separate owner-pilot and recovery-test
namespaces so a validation run cannot mutate the serving state.

Supply the token keyring through a read-only Secret Manager file mount, readable by
the non-root container user. Pin its secret version for a reproducible revision.
The JSON contains `active`, `keys` (version to canonical raw-base64 32-byte AES key)
and `registration_digest_key` (a separate canonical raw-base64 32-byte HMAC key).
Never put actual values in source, images, command arguments, logs or chat.
The application must read and validate the mount before listening; a missing or
inaccessible mount is not permission to generate a replacement.
[Cloud Run secret mounts](https://docs.cloud.google.com/run/docs/configuring/services/secrets).

Pass that mount with `-token-keyring-file`. Pass a separate owner-controlled
`-registration-admission-file` containing strict JSON of the form
`{"installation_public_keys":["CANONICAL_PUBLIC_KEY"]}`. Replace the placeholder with
1–64 unique, canonical unpadded base64url SEC1 P-256 public keys. Empty or malformed
admission fails startup; there is no anonymous enrollment default.
The admission list is loaded at startup. Apply changes through a validated replacement
revision; editing the mounted file alone does not reload already-running instances.

Admission keys are public identifiers, not secret invitations or proof of private-key
possession on their own. Registration also requires `Swarm-Registration-Proof`, a
P-256 proof over the exact final body and idempotency key, before shared state, quota
or Play verification is accessed. Knowing an allowed public key alone cannot create
records. The phone re-signs its saved body/key on retry; there is no unsigned fallback.
This is not a single-use invitation or a uniqueness constraint: an admitted private-key
holder can deliberately prepare more registrations, subject to attestation and quotas.
Hosted admission/ingress testing remains required before inviting friends.

Production rejects `FIRESTORE_EMULATOR_HOST`. Local development requires both `-dev`
and `-allow-firestore-emulator` plus the emulator address, and cannot silently use
real Firestore with ambient credentials. Development FCM and attestation remain
fail-closed; this is not an alternate production store.

## Runtime and image

Build the existing non-root image from `deploy/pushgw/Dockerfile`; the release gate
scans HIGH/CRITICAL vulnerabilities and checks its non-root user. Select an immutable
image digest for hosted validation. No database disk, reverse-proxy sidecar, DNS alias
or alternate local store is part of v2.

Use request-based Cloud Run billing and minimum instances zero. Initial test settings
are maximum instances 3, 1 vCPU/512 MiB, concurrency 8 and a 30-second outer request
deadline. These are measurement inputs, not a sizing result or hard monetary cap.
Colocate Firestore and Cloud Run after comparing candidate regions.

The public application listener accepts HTTP only behind Cloud Run's HTTPS termination;
use explicit `-insecure-http` for that proxy-termination mode. Without `-listen`, the
command reads and validates `PORT`; the image defaults it to 8080, and Cloud Run supplies
the configured service port. Outside the image, an unset `PORT` falls back to 8443.
An explicit `-listen` overrides `PORT`. The fresh app/machine
configuration uses the actual provider-issued HTTPS service URL. Platform-public
invocation does not replace application authentication. Keep the optional admin listener
loopback-only; never expose its metrics or readiness through the public handler.

Do not configure trusted forwarded-header networks until the hosted ingress-spoof gate
establishes a trustworthy source identity. A conservative shared source bucket is safer
than allowing caller-selected forwarded headers to expand quota.
The command currently enforces this by rejecting nonempty `-trusted-proxies` outside
explicit emulator development.

## Retention, readiness and hosted gates

Request-based instances do not have general background CPU availability. Required
physical cleanup needs explicitly scheduled, bounded work; an hourly in-process ticker
is not a scale-to-zero cleanup guarantee.
[Cloud Run billing and CPU allocation](https://docs.cloud.google.com/run/docs/configuring/billing-settings).
Logical expiry and transactional authorization remain enforced during requests.
The deployment must prove cleanup scheduling and backlog drain before launch; no schedule
has been installed by this source change.

Readiness is not an FCM/Play send probe and must not depend on an idle worker's heartbeat.
A successful Firestore readiness read does not prove write IAM, real attestation or FCM
delivery. Keep admin reads infrequent and include them in the operation-cost accounting.

Before deployment, review exact service identity/IAM, Firestore database/namespace,
secret mount/version, enrollment admission, cleanup invocation and ingress settings.
There is intentionally no copy-and-run deployment template with guessed hosted values.
Deployment remains an operator action requiring its own authority.

Before inviting friends, pass the plan's real Google ADC/Play Integrity/FCM, header spoof,
replay/revoke, key recovery, cold/warm latency, fresh pairing and phone lifecycle gates.
Test Wi-Fi/cellular, background/Doze, network changes, process death and reboot separately.
Do not promise notification delivery to a force-stopped app; record platform behavior.
Local emulator success is not a hosted launch certificate.
