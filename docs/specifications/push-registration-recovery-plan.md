# Push registration: recover an unknown result safely

Design and implementation plan, 2026-09-09. Work is tracked in
`agents-tracker-75xt`; hosted enrollment (`agents-tracker-kn18`) depends on it.
This refines the [clean replacement plan](remote-scale-to-zero-plan.md), not the
foreground relay protocol or the phone's existing pairing identity.

## Problem and evidence

The owner phone retains a prepared registration whose outcome is unknown. Its
subsequent registration requests receive `403 attestation_invalid`. The current
client correctly refuses to discard an unknown registration, but replays its
short-lived attestation indefinitely. The server currently forgets successful
registration idempotency after ten minutes while installations survive for up to
180 days of inactivity. Merely obtaining a new verdict cannot safely resolve a
lost successful response after that ten-minute window.

The phone regression uses the existing in-process gateway fixture and reproduces
an outage before any server commit, a process
restart, and refusal of the old verdict. It fails on the old client and passes
only when the same intent obtains a fresh attestation. This proves the recovery
defect, not the original cause of the phone's first unknown result. Hosted 403s
do not by themselves distinguish an expired verdict from another Play refusal.
That client fixture is not proof of Firestore receipt retention: separate actual
Firestore emulator cases are required for the production persistence semantics.

## Selected contract

Reuse `registration_attempts`: a completed row is the durable receipt for one
registration, not a ten-minute cache entry. Do not add another collection,
service, public-key uniqueness rule, deterministic installation ID, or client
identity-reset mechanism.

| Element | Contract |
|---|---|
| Logical intent | Existing `pushreg.RequestHash`: public key, FCM token and fixed `play_integrity` kind; attestation token omitted canonically |
| Idempotency identity | Existing keyed hash of the same Idempotency-Key |
| Request authentication | Admission and installation-key proof over the actual final request body, including its attestation, before recovery lookup |
| Pending attempt | Ten-minute expiry; a bounded verifier lease; completion must still own the unexpired lease |
| Completed attempt | No independent expiry; retained with its linked installation |
| Installation identity | Fresh random ID for a new registration; another idempotency key may intentionally create another installation with the same public key |
| Changed intent | Same retained idempotency key with another FCM token or public key returns conflict |
| Replay | Same intent returns the original installation without decoding Play again, including after ten minutes |
| Corrupt linkage | Missing or mismatched half of a completed receipt fails closed; never silently creates another installation |
| Physical cleanup | Delete the installation, its matching receipt and owned addresses atomically after the inactivity floor; retain revocation tombstones |

Completed recovery reads and refreshes installation activity transactionally so
retention cannot delete an installation based on its pre-recovery timestamp.
A logically expired but physically present installation is not revived by an
unsigned installation-control request or silently replaced: registration waits
for cleanup. New random IDs prevent a delayed old push-provider result from
affecting a later installation with the same signer and token generation.

Completed rows omit `expires_at_ms`. Pending-retention transactions must recheck
the field/state: a query snapshot collected before completion is not permission
to delete the now-completed receipt. Installation activity, not creation time,
governs co-retention. Firestore transactions perform reads before writes and
their retry callbacks must not invoke Play or FCM.

## Phone state transitions

1. Persist the initial prepared key and body before the first POST, as today.
2. After an unknown result, replay the saved body first. A completed receipt can
   resolve the request without another Play verdict.
3. Only `attestation_invalid` permits one refresh in that Ensure call. Validate
   the saved body, signer, FCM token and idempotency key before requesting it.
4. Preserve all logical fields and the idempotency key. Replace the attestation,
   persist the new body, then sign and send those exact bytes.
5. Bound network retries and refresh attempts. Any further failure retains the
   unresolved intent. Failed refresh or persistence must not cause an identity
   reset or a POST whose prepared state was not durably written.
6. Persist the recovered installation, then always reconcile the current FCM token
   with the existing signed token rotation before reporting readiness. A receipt
   proves identity, not current token health: a provider result could have marked
   even an unchanged token dead. Do not change the pending registration's intent.
   If rotation fails, the saved installation ID allows a normal signed retry.

No Keystore reset, app-data clear, new sealed-state format, or relay repair is
part of this fix. First definitive refusals remain foreground-only and do not
create a durable unknown result.

## TDD, implementation and review gates

Root owns the client implementation, this plan, integration review and release
decisions. Terra owns client regression tests. Sol owns backend transaction
implementation and emulator checks. A separate Sol reviewer challenges identity,
retention and deployment safety. A passing author test is not review acceptance.

| Check | Required observation |
|---|---|
| Pre-commit outage and restart | Same key and intent; one fresh verdict; one installation; durable recovered ID |
| Lost successful response beyond ten minutes | Original ID; no second Play verification or installation |
| Refreshed attestation | Actual-body proof verifies; canonical logical digest unchanged |
| Changed public key/FCM | Conflict for the same retained key; no mutation |
| Separate intentional registration | Different idempotency key, same signer produces a different random ID |
| Invalid saved state | Refused before attestation; pending state retained |
| Repeated refusal/refresh error | Bounded work; no fresh identity; pending survives restart |
| Persistence failure | No POST before the refreshed body is durable |
| Concurrent workers and stale verifier | One winning installation; expired or replaced lease cannot commit |
| Half-record corruption | Error, never a replacement installation |
| Pending GC races completion | Completed receipt survives the stale cleanup query |
| Activity refresh races GC | Active installation and its receipt both survive |
| Inactivity cleanup | Matching receipt and installation disappear atomically; other receipts are untouched |
| Delayed old push completion | Cannot affect a newly registered random installation ID |

Run focused regressions from RED to GREEN, then complete phonecore and pushgw
suites, race detection, affected builds/vet and repository-required checks.
Run new storage cases against the actual Firestore emulator, not only the
in-memory transaction fake; record skips as missing evidence, never successes.
Keep test hooks unset and all fixtures isolated from live phone/cloud state.
Review the final diff for untested branches and update the API/ADR retention
contract before shipping. Track implementation evidence in the verification
journal rather than treating this design document as a completion report.

## One-environment deployment

The existing namespace stores exact-body digests. The new logical-digest code
must not be rolled over those records and reinterpret them. The owner requested
a clean replacement without compatibility code. Use a fresh push namespace at
the same service URL only if a quiesced inspection proves there is no committed
installation or address to preserve.
New receipts carry digest revision 2; missing or other revisions fail closed.
This catches accidental old-namespace configuration without adding a legacy
conversion or compatibility branch.

Before cutover, fence old ingress and retention and verify no other principal can
write the old namespace. Record the fence time and deployed request timeout
(currently 30 seconds) and verifier lease (30 seconds). Wait beyond both windows
after the fence is effective, confirm old request completion in service logs, and
ensure no old revision or retention execution remains able to write. A timeout
alone is not proof that application work stopped; if process/request drain cannot
be established, stop the cutover. Inspect authoritative old state only after this
writer fence. Require zero installations, addresses, completed registration
receipts and wake attempts. A prior live aggregate count of zero is not sufficient.
If any committed installation, completed receipt or address exists, stop and design an explicit migration;
do not discard it. A leftover pending-only request can safely start in the fresh
namespace only after the old backend can no longer commit it.

Create the required index for the new namespace; retain the same Secret Manager
key versions, admission list, runtime service account and request-billed service.
Deploy the tested backend, update the existing retention job to the same
namespace, and verify readiness and rejection paths before restoring ingress.
Do not serve old/new digest semantics concurrently. Do not roll back to an old
binary against newly populated state. Preserve the old namespace for inspection;
no ad hoc document deletion is required for this cutover.

Build the new Android native artifact from the reviewed commit, publish a new
Play internal-test version, and update the phone normally. Verify actual hosted
201 recovery without exposing attestation/token bodies or clearing phone state.
Only then activate the existing desktop push configuration and, if required,
perform supported fresh pairing to provision the push binding. Foreground
pairing stays usable until that deliberate activation.

Acceptance requires a provider-accepted wake plus observed phone delivery and
background/LTE behavior. A local test, HTTP 201, or FCM acceptance alone does not
prove the whole delivery path. Keep enrollment, delivery and cold-open/history
results separate. If Play still refuses a fresh verdict, diagnose the issuer
configuration and response classification; do not weaken attestation or reset
the phone to make the test pass.

## Cost and scope

No additional always-on component or dependency. Storage grows by one compact
completed receipt per retained installation instead of deleting it after ten
minutes. Recovery performs bounded transactional reads/activity writes and at
most one fresh verdict per Ensure call; ordinary request billing and scale-to-zero
remain. Measure Firestore operations and hosted request behavior before assigning
a monthly amount. This is a correctness repair, not a claim of literally free
hosting or a replacement for the separate delivery acceptance work.
