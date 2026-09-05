# Swarm push gateway — API, `WakeV1`, and wake-obligation contract

**V2 amendment (2026-09-05):** [ADR-027](../adr/ADR-027-clean-remote-control-v2.md)
supersedes the former process-local-only replay/idempotency requirement (PG-RET-4),
closed durable metadata set (PG-RET-10), and legacy migration/transport requirements
(PG-MIG). V2 uses fresh Firestore state and bounded shared transactional authority;
there is no old-client or old-origin fallback. Existing authentication, attestation,
capability, encrypted-wake, expiry and log-safety guarantees remain binding. `WakeV1`
and `/v1/` may remain the sole push format/API, not a compatibility mode. Implementation
and hosted verification status are tracked in the
[replacement plan](remote-scale-to-zero-plan.md).
Registration additionally requires the installation-key proof in PG-AUTH-15; the former
unsigned-registration exception is removed. This does not replace Play Integrity or
owner admission, and no unsigned fallback is supported.

**Spec ID**: 0003
**Status**: Draft — binds at Wave R3 implementation
**Author**: Nathan Delacrétaz (with Claude)
**Created**: 2026-08-15
**Binds**: the Swarm-operated push gateway service; the Android installation/registration path
(`PushRegistration`, playbook `:482-483`); `swarm-remote`'s wake submitter
(`internal/remotegw`); the phone-side wake receiver (`internal/phonecore`); and the pairing
transcript fields that carry the address, capabilities and wake key.
**Decided by**: [ADR-015](../adr/ADR-015-push-gateway-split.md) (the push-gateway split, P1-P12).
Companions: [ADR-016](../adr/ADR-016-web-pki-relay-tls.md) (Web-PKI TLS — relay-scoped; the
gateway's TLS ruling is ADR-015 P4, reached the same way and not inherited),
[ADR-017](../adr/ADR-017-terminal-fallback-capability.md), [ADR-018](../adr/ADR-018-multi-machine-pairings.md)
(MM5: one installation credential, N per-machine addresses), and
[ADR-007](../adr/ADR-007-remote-access.md) B144 for the annotation discipline this document is
filed under.
**Source of obligation**: `docs/specifications/remote-control-product-playbook.md` §6.6 (`:512-561`),
the R1 deliverable "Define push-gateway OpenAPI/schema, threat model including relay+gateway
collusion, retention policy, `WakeV1`, wake-obligation state machine, and migration fixture"
(`:697-698`), §5's trust table (`:348-359`), §11.3 (`:915-933`), §12's `push_transport` migration
(`:955-962`), §13's gateway retention rows (`:979-981`), §14's gateway responsibilities
(`:1000-1012`), §15's non-goals (`:1024`).
**Precedence**: where this document and ADR-015 disagree, **ADR-015 wins** and the divergence is
recorded in §14. Where ADR-015 and the playbook disagree, ADR-015 has already recorded the
divergence and this document follows ADR-015 (the `expires_at` case, §5.2). **One carve-out, stated
here rather than left to be discovered six pages later**: on the canonical AAD, this document
follows the seven-element tuple *both* records declare normative (playbook `:537-539`, ADR-015 P8)
rather than P8's accompanying sentence "every byte on the wire is AAD-covered", because those two
halves of P8 contradict each other — the tuple P8 itself writes down omits `type`. §14.1 row 2
records the choice, §5.3 argues it, and §13.2 asks the owner to close it in one direction or the
other. **One divergence is open rather than carved out**: §6.4's terminal-refusal states have no
antecedent in ADR-015 P9, which is recorded as §14.1 row 7 and put to the owner as §13.8; until it
is ruled, P9's literal text is the one that binds an implementer.

**Nothing in this document is evidence.** Not one wake has left this repository toward Google
(ADR-015, Notes). Every "the gateway does X" below is a design commitment binding R3, not a
measurement. The Firebase facts in §12 are the only observed facts here.

---

## 0. Scope

### 0.1 What this defines

The wire contract between three parties that do not trust each other symmetrically:

- **Android** (`dev.swarm.phone`) → gateway: register, rotate token, allocate address, revoke address.
- **`swarm-remote`** → gateway: submit wake, revoke address.
- **gateway** → **FCM**: one data-only high-priority message carrying the opaque `WakeV1` bytes.

Plus the `WakeV1` envelope itself, which the gateway forwards and never opens, and the durable
obligation on `swarm-remote` that makes "a wake was owed" survive a crash.

### 0.2 What this does not decide

- **Where the gateway runs.** Deferred to the owner (§12).
- **The gateway's internal storage engine, language, or process topology.** Only its observable
  fields (§8), its refusals (§4), and its ordering guarantees (§6) are contract.
- **Anything about the relay.** The relay holds no push credential, no token map and no push
  transport after ADR-015 P1. A relay appears in this document only as an adversary in §7.
- **iOS/APNs.** Not the active target (ADR-015 P3); no APNs vocabulary appears below.
- **Notification rendering.** Content-free generic notification is PB-PUSH-4's contract, unchanged.

### 0.3 Requirement labels

Requirements are `PG-<AREA>-<n>` and carry an EARS class, matching `interaction-schema.md`'s
convention. **SHALL/MUST** is normative; **SHOULD** is a defaulted recommendation an implementer may
depart from only with a recorded reason; **MAY** is permission.

---

## 1. Transport, versioning, and request bounds

- **PG-TR-1** (Ubiquitous) Every operation SHALL be reached over HTTPS on the ordinary Web PKI.
  The gateway is a public Swarm-operated host and has none of the self-hosted relay's
  certificate-provisioning excuses (ADR-015 P4). TLS 1.3 SHALL be the floor; TLS 1.2 MAY be
  accepted only if an R3 measurement shows a supported Android version requires it, recorded as a
  deviation.
- **PG-TR-2** (Ubiquitous) The API SHALL be versioned in the path: every operation lives under
  `/v1/`. A request to an unknown version prefix SHALL be refused with `404` and code
  `version_unsupported`. Versioning is a decision-record property, not taste: it is what makes
  ADR-015 P12's compatibility window expressible — an old installation and a new gateway must be
  able to name the same contract.
- **PG-TR-3** (Ubiquitous) Request bodies SHALL be bounded **before parsing**. Every gateway
  operation is reachable by an unauthenticated caller at least as far as its size check, and the
  component behind that check is the only background wake path ADR-007 B16 left. Limits:

  | Operation | Max body | Enforcement |
  |---|---|---|
  | `POST /v1/installations` | 12 KiB | `Content-Length` refused above the cap; body read through a hard-limited reader regardless |
  | `PUT /v1/installations/{id}/token` | 8 KiB | same |
  | `POST /v1/installations/{id}/addresses` | 1 KiB | same |
  | `DELETE /v1/addresses/{addr}` | 0 bytes | any body is `malformed_request` |
  | `POST /v1/wakes` | **exactly 74 bytes** | `Content-Length != 74` refused before reading; absent or unusable `Content-Length` read through a reader hard-limited to 75 octets (§5.1) |

  **The register row is 12 KiB and not 8 because §3.1's schema, honoured at its declared maxima, must
  fit under it.** The arithmetic: `installation_public_key` is exactly 87 characters (§3.1's
  pattern), `fcm_token` is capped at 4096, `attestation.token` at 6144, and `attestation.kind` is the
  14-character constant `play_integrity` — 10341 octets of field content. The five field names with
  their quotes and colons (67), the two brace pairs (4), the three commas (3) and the eight
  value-delimiting quotes (8) add 82 more, so the largest schema-legal registration body is 10423
  octets, and every octet of it is inside 12 KiB (12288), with 1865 octets of slack for a client that
  pretty-prints. At 8 KiB (8192) it was not: a client that filled the fields to the maxima
  this document publishes would have received `413 body_too_large` for a body §3.1 declares valid,
  and §3's exhaustiveness discipline gives a schema reader no way to discover the contradiction. The
  cap binds the pre-parse check, not the schema — the two `maxLength` keywords still refuse an
  oversized field after parsing, and are unchanged.

  **The wake body is the one row whose failure is never `body_too_large`.** A `POST /v1/wakes` body
  that is not exactly 74 octets — over, under, or unmeasurable — SHALL be refused `400`
  `wake_malformed`, and `413` SHALL NOT be returned on this operation at all. The reason is §3's
  exhaustiveness rule: §6.4 drives the obligation machine off codes, and a `413` here would be a
  status §3.5 does not declare and §6.4 has no transition for. Two consequences are spelled out
  because both are easy to get wrong. First, `body_too_large` is a bound *violation* code for the
  four JSON/empty operations only (§4). Second, a request that omits `Content-Length` — HTTP/2, or
  chunked transfer — is not undefined here: the reader is hard-limited to 75 octets, a 75th octet
  or a short read is `wake_malformed`, and no oversized wake is ever buffered.

  The wake row is the **sole normative statement of the 74-byte bound on the wire**. The body is 74
  raw octets, not text and not base64: the OpenAPI block of §3.5 therefore declares only
  `contentMediaType: application/octet-stream` and carries no length keywords, because JSON Schema's
  `minLength`/`maxLength` bound a *string* and would pin a 74-character encoding (~55 octets) rather
  than 74 octets. The base64 encoding that does exist happens one hop later and in one direction
  only: the gateway base64-encodes the 74 received octets into the single FCM data key (PG-SUB-1).

- **PG-TR-4** (Ubiquitous) Requests SHALL NOT be compressed. `Content-Encoding` on any request is
  `malformed_request`. A compressed 74-byte fixed shape is a variable-size shape.
- **PG-TR-5** (Unwanted) IF a request carries an unexpected `Content-Type`, THEN it SHALL be refused
  with `malformed_request` before authentication. JSON operations require
  `application/json`; `POST /v1/wakes` requires `application/octet-stream`.
- **PG-TR-6** (Ubiquitous) The gateway SHALL expose `GET /v1/health` — unauthenticated, static,
  carrying service and provider-reachability state and **no** installation-scoped, address-scoped or
  per-caller datum. This is playbook `:548`'s delivery health. It is **not** one of the five
  operations of §3, and it SHALL never claim that FCM acceptance proves handset display.

---

## 2. Authentication

Three credential kinds, deliberately distinct, never interchangeable.

### 2.1 Installation-control signature (registration uses the distinct proof in §2.5)

- **PG-AUTH-1** (Ubiquitous) An installation-control request — rotate token, allocate address,
  revoke address by owner — SHALL carry a signature by the installation private key over the
  canonical string

  ```text
  swarm-pg-v1|<METHOD>|<path>|<body_sha256>|<nonce>|<expiry>
  ```

  where `METHOD` is the uppercase HTTP method; `path` is the **full origin-relative request path
  including the `/v1` version prefix and excluding scheme, host and query** — literally
  `/v1/installations/{installation_id}/token`, not `/installations/{installation_id}/token`. The
  prefix is explicit because the OpenAPI document carries `/v1` in `servers` and not in `paths`
  (§3), which leaves the signed string undefined to anyone deriving it from the schema; the signed
  form is the one that appears on the wire, and it includes the version PG-TR-2 makes contractual.
  `body_sha256` is base64url-unpadded SHA-256 of the exact request bytes (**the empty body hashes to
  SHA-256 of zero bytes; it is never omitted**), `nonce` is base64url-unpadded of 16 CSPRNG bytes,
  and `expiry` is a decimal Unix-seconds integer. The domain-separation prefix is part of the signed
  bytes, not a header.

  Concatenation with `|` is unambiguous because no component can contain `|`: the method is
  alphabetic, the path vocabulary is fixed and its variable segments are base64url, and the
  remaining three are base64url or digits. That argument holds **only** because the query string is
  excluded, and excluding it from the signature while allowing it on the wire would leave it
  unauthenticated. Therefore: **a signed request carrying a query string SHALL be refused with
  `malformed_request` before signature verification.** None of the five operations defines a query
  parameter, so the ban costs nothing today and closes the one hole in the unambiguity argument — the
  query is the single component whose alphabet the gateway does not control, and an attacker-supplied
  `?a|b=c` would let two distinct requests share one signed pre-image. If a query parameter is ever
  added, the fix is to length-prefix every component of the canonical string, not to relax this.

  Headers: `Swarm-Nonce`, `Swarm-Expiry`, `Swarm-Signature: p256-sha256 <base64url>`.
  The installation id is **in the path**, deliberately, so it is signature-covered; an id carried
  only in a header would be outside the signed bytes.

- **PG-AUTH-2** (Ubiquitous) The installation key SHALL be **ECDSA P-256 with SHA-256**, generated
  in Android Keystore, hardware-backed where the device offers it. The signature SHALL be the
  IEEE P1363 fixed-width 64-byte `r||s` encoding with `s` normalized low; a DER signature or a
  high-`s` value SHALL be refused. *This is a spec-level choice, not an ADR ruling*: ADR-015 P5 and
  playbook `:131-133` say "Keystore-generated installation public key" without naming a curve, and
  P-256 is the algorithm with broad hardware-backed Keystore support across the Android versions
  this product targets. See the open question in §13.1.
- **PG-AUTH-3** (Unwanted) IF `expiry` is in the past, or more than **120 seconds** in the future,
  THEN the request SHALL be refused with `request_expired`. That response — and only that response —
  SHALL include the gateway's `server_time`, so a handset with a skewed clock can correct and
  re-sign rather than fail permanently. Note the word: **re-sign, not retry**. The body carries
  `retryable=false` (§4), because `expiry` is inside the signed pre-image and the identical bytes are
  expired forever; the recovery is a new request with a new `expiry`, a new `nonce` and a new
  signature.
- **PG-AUTH-4** (Ubiquitous) The gateway SHALL keep an authenticated-request nonce cache keyed
  `(installation_id, nonce)` for at least the maximum acceptable expiry horizon, and SHALL refuse a
  repeat with `nonce_replayed`. This is what makes ECDSA malleability non-exploitable independently
  of PG-AUTH-2's low-`s` rule: a re-encoded signature over the same canonical string reuses the
  nonce.
- **PG-AUTH-5** (Ubiquitous) Any request that authenticates successfully under PG-AUTH-1 SHALL reset
  the installation's inactivity clock (§8, 180-day row). No sixth operation exists for refresh:
  the app's periodic `PUT .../token` with its **current** token is the refresh, and is idempotent.

### 2.2 Submit capability (the wake path)

- **PG-AUTH-6** (Ubiquitous) `submit_capability` SHALL be **32 CSPRNG bytes**, transported as
  base64url-unpadded, presented as `Authorization: Swarm-Capability <value>`. It is returned exactly
  once, in the allocate-address response, and is reusable until the whole address is revoked
  (playbook `:519`).
- **PG-AUTH-7** (Ubiquitous) The gateway SHALL store only `SHA-256(capability)` as the verifier and
  SHALL compare in constant time. A slow KDF is not required and SHALL NOT be used: the secret is
  256 bits of CSPRNG output, so an offline attack on the verifier is not the threat, and a slow
  comparison on the wake path is a self-inflicted latency budget on the only background wake path.
- **PG-AUTH-8** (Ubiquitous) The submit capability SHALL NOT authorize any operation other than
  `POST /v1/wakes`. In particular it SHALL NOT revoke, rotate, allocate, or read.

### 2.3 Machine-revoke capability

- **PG-AUTH-9** (Ubiquitous) `machine_revoke_capability` SHALL be 32 CSPRNG bytes, stored as a
  SHA-256 verifier, distinct from the submit capability (playbook `:527-529`, §11.3 `:921`), and
  SHALL authorize exactly one operation: `DELETE /v1/addresses/{push_address}` for its own address.
  Presented as `Authorization: Swarm-Revoke <value>`.
- **PG-AUTH-10** (Ubiquitous) The two revocation paths are two parties holding two different
  credentials, not two routes to one operation (ADR-015 P6): the **phone** revokes with the
  installation key (it may have discarded the capabilities); the **machine** revokes with the
  machine-revoke capability, after local epoch rotation, under a durable retry (§6.6).

### 2.4 App attestation (registration only)

- **PG-AUTH-11** (Event-driven) WHEN an installation registers, the request SHALL carry Play
  Integrity evidence bound to the request: the integrity verdict token whose `requestHash` is

  ```text
  requestHash = SHA-256( JCS( registration body with attestation.token replaced by "" ) )
  ```

  The carve-out is normative here, not merely descriptive in §3.1, because the naive reading —
  "SHA-256 of the registration body" — is **circular and unsatisfiable**: the body contains
  `attestation.token`, and minting that token requires the hash. Setting the field to the empty
  string before hashing breaks the cycle while keeping `installation_public_key` and `fcm_token`,
  the two fields that actually matter, inside the binding.

  `JCS` is RFC 8785 JSON Canonicalization Scheme. Pinning a canonical serialization is not
  fastidiousness: the gateway must **recompute** this hash to verify the verdict, and two JSON
  encodings of the same object — different key order, different spacing, different string escapes —
  hash differently, so an unpinned serialization is a verifier that fails on a client library
  change. `additionalProperties: false` (§3.1) closes the field set JCS runs over, so the
  canonical form is fully determined by this document.

  The gateway SHALL verify the token with Google, SHALL require the package identity of the
  Play-signed application, SHALL recompute the hash above from the received body and refuse on
  mismatch, and SHALL refuse registration with `attestation_invalid` when the app-recognition
  verdict is not the licensed Play-signed build.
  The v2 production verifier accepts a verdict at most two minutes old, with at most
  30 seconds of future skew, measured against server time. Their sum SHALL remain shorter
  than PG-REG-2's ten-minute idempotency window: once that record expires, the exact saved
  verdict cannot authorize a second installation. Completed retries are resolved before
  attestation re-verification; the phone SHALL NOT replace the token inside a pending body.
- **PG-AUTH-12** (Ubiquitous) Attestation is an **authenticity and abuse signal, never an identity**
  (playbook `:560-561`). The gateway SHALL NOT persist the integrity token, any device identifier it
  contains, or any Google account identifier. It MAY persist a boolean verdict class and a timestamp
  on the installation record and nothing else. RC-D3's accountless product (`:73`) is unweakened: an
  installation id is a routing handle with a 180-day inactivity expiry, not a user.
- **PG-AUTH-13** (Unwanted) IF attestation is unavailable or fails, THEN registration SHALL be
  refused (`attestation_invalid` / `attestation_unavailable`) and the app SHALL present the honest
  degraded state — "foreground updates only" — rather than enrol a durable identity anyway
  (ADR-015 P5, playbook `:148-149`).
- **PG-AUTH-14** (Ubiquitous) Debug builds SHALL target a separate non-production gateway and a
  separate non-production Firebase project (playbook `:561`), so no test handset can spend or poison
  production quota. This is a build-configuration obligation on R3, stated here because the API is
  where its absence would first be invisible.

### 2.5 Registration proof of installation-key possession

- **PG-AUTH-15** (Ubiquitous) Every `POST /v1/installations`, including a completed
  idempotent retry, SHALL carry exactly one `Swarm-Registration-Proof` header:

  ```text
  Swarm-Registration-Proof: p256-sha256 <base64url-unpadded signature>
  ```

  The installation key named in the body signs the following exact UTF-8 bytes using
  the SHA-256/P-256, fixed-width 64-byte, low-`s` P1363 format of PG-AUTH-2:

  ```text
  swarm-pg-register-v1|<Idempotency-Key>|<body_sha256>
  ```

  `body_sha256` is raw base64url SHA-256 of the **exact final request body**, including
  the attestation token. This is deliberately different from the JCS attestation
  request hash: attestation is prepared first, then the finished body is signed.
  The domain is exclusive to registration; it cannot authorize a control operation.
  The fixed idempotency-key alphabet and body-hash alphabet exclude `|`. Duplicate
  idempotency headers and query strings are malformed requests, not unsigned extensions.
  Missing, duplicated, malformed, noncanonical, wrong-key or high-`s` proofs are
  `401 unauthorized`; proof material is never echoed, logged or persisted.

  After bounded request/key parsing and the cheap owner allowlist check, verification
  SHALL precede every registration-state lookup, shared quota operation, attestation
  verification and allocation. Removing a key from the active admission policy also refuses completed
  retries. Knowing an admitted public key alone therefore cannot spend those resources.

  The phone SHALL save the existing prepared body and idempotency key before sending,
  and sign those same bytes again for each POST, including after restart. A retry may
  have different ECDSA signature bytes; the signature is not part of idempotency identity.
  No new durable signature, nonce or clock field is introduced. A local signing failure
  cannot erase an earlier outcome-unknown registration. There is no handset-clock expiry
  on this proof; existing attestation freshness and shared idempotency lifetimes still
  apply. The proof is not a single-use invitation or a one-installation-per-key rule:
  legitimate key holders can deliberately prepare fresh registrations, still subject to
  admission, attestation and global/source quotas.

---

## 3. The five operations

The blocks below are fragments of **one** OpenAPI 3.1 document; §3.6 carries the shared
`components`. Schemas are JSON Schema 2020-12. `additionalProperties: false` everywhere is
deliberate: an unknown field on a gateway request is either a client bug or an attempt to smuggle a
locator, and §15 of the playbook bans locators "even encrypted" (`:1024`).

Each operation's `responses` map is **exhaustive against §4**: every code §4 makes reachable on that
operation is declared on it, including the ones a reader might assume away — `400` on a `DELETE` that
takes no body, `413` on a request bounded at 1 KiB, and `500` everywhere. This is load-bearing rather
than tidy: §6.4 drives the obligation machine off codes, so an undeclared-but-reachable status is an
input the machine has no transition for. The rule cuts both ways, and `POST /v1/wakes` is where it
does: `413` is **absent** from that operation's map because PG-TR-3 makes it unreachable there — a
wake body of any other length is `400 wake_malformed` — rather than because it was overlooked.

One code of §4 is deliberately in **no** operation's map: `404 version_unsupported`. PG-TR-2 refuses
an unknown path version *before* dispatch, so it is a refusal of the router rather than a response of
any of the five operations, and declaring it per-operation would claim the operation was reached. It
is still a status the wake submitter can receive, which is why §6.4 carries a transition for it — the
exhaustiveness rule is about the obligation machine's input alphabet, and that alphabet is closed at
§6.4, not only at the responses maps.

```yaml
openapi: 3.1.0
info:
  title: Swarm push gateway
  version: "1.0.0-draft"
  summary: Opaque push-address to FCM-token mapping and content-free wake forwarding.
  description: >-
    Five logical operations (playbook 6.6). The gateway receives no session traffic, no relay
    URL, no relay-auth key, no pairing secret, no content key, no device command key, no daemon
    credential and no wake key. It cannot open a WakeV1 envelope and never attempts to.
servers:
  - url: https://{host}/v1
    description: Host is deferred to the owner (see section 12).
    variables:
      host:
        default: gateway.invalid
```

### 3.1 Register installation

Caller: Android. Purpose (playbook `:518`): with valid app-attestation evidence and installation-key proof, exchange an FCM
token and a Keystore-generated installation public key for an opaque installation id; **allocate no
machine address yet**.

```yaml
paths:
  /installations:
    post:
      operationId: registerInstallation
      summary: Exchange an attested, installation-key-proven FCM token for an opaque installation id.
      security:
        - registrationProof: []
      parameters:
        - $ref: '#/components/parameters/IdempotencyKey'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [installation_public_key, fcm_token, attestation]
              properties:
                installation_public_key:
                  type: string
                  description: >-
                    ECDSA P-256 public key, SEC1 uncompressed point (65 bytes), base64url unpadded.
                  pattern: '^[A-Za-z0-9_-]{87}$'
                fcm_token:
                  type: string
                  description: >-
                    Provider-issued registration token. Encrypted at rest; excluded from every log,
                    trace and error body (playbook :531).
                  minLength: 1
                  maxLength: 4096
                  writeOnly: true
                attestation:
                  type: object
                  additionalProperties: false
                  required: [kind, token]
                  properties:
                    kind:
                      const: play_integrity
                    token:
                      type: string
                      description: >-
                        Integrity verdict token whose requestHash is PG-AUTH-11's value:
                        SHA-256 of the RFC 8785 (JCS) canonicalization of this request body with
                        attestation.token replaced by the empty string. Never persisted.
                      maxLength: 6144
                      writeOnly: true
      responses:
        '201':
          description: Registered. No machine address is allocated by this operation.
          content:
            application/json:
              schema:
                type: object
                additionalProperties: false
                required: [installation_id, refresh_before]
                properties:
                  installation_id:
                    $ref: '#/components/schemas/InstallationId'
                  refresh_before:
                    type: string
                    format: date-time
                    description: >-
                      The 180-day inactivity floor (section 8). Any successfully authenticated
                      installation request moves it.
        '400': { $ref: '#/components/responses/BadRequest' }
        '401': { $ref: '#/components/responses/Unauthorized' }  # registration proof
        '403': { $ref: '#/components/responses/Forbidden' }     # attestation_invalid, beta_closed
        '409': { $ref: '#/components/responses/Conflict' }      # idempotency_conflict
        '413': { $ref: '#/components/responses/TooLarge' }
        '429': { $ref: '#/components/responses/Throttled' }
        '500': { $ref: '#/components/responses/Internal' }
        '503': { $ref: '#/components/responses/Unavailable' }
```

- **PG-REG-1** (Ubiquitous) Registration SHALL allocate no address. Address allocation is a separate,
  installation-key-signed operation (§3.3), because the two objects have different lifetimes
  (ADR-015 P5).
- **PG-REG-2** (Ubiquitous) A proven, still-admitted registration with the same `Idempotency-Key`
  and byte-identical body inside the key's retention window SHALL return the same
  `installation_id` rather than mint a second one, without another attestation verification
  or quota debit. The same key with a different, validly proven body is
  `409 idempotency_conflict`; an invalid proof is refused before that lookup. Without
  this, a response lost on a flaky handset network yields two durable installations for one app
  install, and the abandoned one holds a live token until its 180-day expiry.
  A received HTTP response alone does not establish success or prove that an earlier attempt
  never committed. The phone SHALL retain the exact prepared body/key after transport loss,
  an unreadable or invalid success response, or an ambiguous server response. Once an attempt
  may have committed, later refusals (including expired attestation) SHALL NOT cause automatic
  fresh registration. Only a bounded, contract-valid `201` resolves that pending identity.
  Recovery of the original result is guaranteed only within the idempotency retention window;
  unresolved attempts outside it require explicit recovery rather than silent re-enrollment.
- **PG-REG-3** (Ubiquitous) Registrations SHALL be bounded per source and globally (playbook
  `:559-560`); the refusal is `quota_exceeded`, never a silent success.

### 3.2 Rotate token

Caller: Android. Purpose (playbook `:520`): **replace the FCM token without changing every machine
pairing**. This is the reason installation identity exists as a separate object (ADR-015 P5); do not
"simplify" the two objects into one.

```yaml
paths:
  /installations/{installation_id}/token:
    put:
      operationId: rotateInstallationToken
      summary: Replace the FCM token. Touches no address, no capability, no pairing.
      security:
        - installationSignature: []
      parameters:
        - $ref: '#/components/parameters/InstallationIdPath'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [fcm_token]
              properties:
                fcm_token:
                  type: string
                  minLength: 1
                  maxLength: 4096
                  writeOnly: true
      responses:
        '204':
          description: >-
            Token replaced, or re-affirmed unchanged. Idempotent: submitting the current token is
            the app's inactivity refresh (PG-AUTH-5). Every address of this installation keeps its
            push_address, submit capability, machine-revoke capability and wake key.
        '400': { $ref: '#/components/responses/BadRequest' }
        '401': { $ref: '#/components/responses/Unauthorized' }
        '409': { $ref: '#/components/responses/Conflict' }      # nonce_replayed
        '413': { $ref: '#/components/responses/TooLarge' }
        '429': { $ref: '#/components/responses/Throttled' }
        '500': { $ref: '#/components/responses/Internal' }
        '503': { $ref: '#/components/responses/Unavailable' }
```

- **PG-ROT-1** (Ubiquitous) Rotation SHALL NOT invalidate, re-issue, or renumber any address,
  capability, wake key or wake sequence. A rotation that touched pairings would turn a
  provider-scheduled event into an N-way re-pairing ceremony, which is exactly what this operation
  exists to prevent.
- **PG-ROT-2** (Event-driven) WHEN FCM reports `UNREGISTERED` for the stored token (`internal/remote/push/fcm.go:195-215`
  classifies it on the structured `errorCode`, never on the 404 status), the gateway SHALL mark the
  mapping dead, SHALL **delete the token bytes**, and SHALL refuse subsequent submits for that
  installation's addresses with `push_token_unregistered`. The addresses and their verifiers SHALL
  survive, so a later rotation restores delivery without re-pairing: what dies is the token, not the
  pairing. Deleting the bytes is not optional tidiness — playbook `:532` requires deleting revoked and
  expired registrations, PG-RET-2 requires actual deletion of a token mapping rather than a tombstone
  that retains it, and a provider token Google has already declared `UNREGISTERED` has zero delivery
  behind it and full breach value in front of it. The dead-mapping marker is the address→installation
  binding with no token attached, and the next `PUT .../token` (§3.2) fills it.

### 3.3 Allocate address

Caller: Android, authenticated by the installation key. Purpose (playbook `:519`): return
`{push_address, submit_capability, machine_revoke_capability}` **once**, for one pending pairing.

```yaml
paths:
  /installations/{installation_id}/addresses:
    post:
      operationId: allocateAddress
      summary: Allocate one unbound per-machine push address and its two distinct capabilities.
      security:
        - installationSignature: []
      parameters:
        - $ref: '#/components/parameters/InstallationIdPath'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              description: >-
                Deliberately empty. The gateway learns nothing about the machine being paired:
                no hostname, no machine id, no relay URL, no label.
              properties: {}
      responses:
        '201':
          description: >-
            Allocated and UNBOUND. The three values are returned exactly once and never again.
          content:
            application/json:
              schema:
                type: object
                additionalProperties: false
                required: [push_address, submit_capability, machine_revoke_capability, unbound_expires_at]
                properties:
                  push_address:
                    $ref: '#/components/schemas/PushAddress'
                  submit_capability:
                    type: string
                    description: 32 CSPRNG bytes, base64url unpadded. Shown once. Never logged.
                    pattern: '^[A-Za-z0-9_-]{43}$'
                    writeOnly: true
                  machine_revoke_capability:
                    type: string
                    description: 32 CSPRNG bytes, base64url unpadded. Distinct from submit. Shown once.
                    pattern: '^[A-Za-z0-9_-]{43}$'
                    writeOnly: true
                  unbound_expires_at:
                    type: string
                    format: date-time
                    description: >-
                      issued + 10 minutes. An allocation that has never had a wake accepted is
                      deleted at this instant in every case (playbook :979).
        '400': { $ref: '#/components/responses/BadRequest' }
        '401': { $ref: '#/components/responses/Unauthorized' }
        '409': { $ref: '#/components/responses/Conflict' }      # nonce_replayed, address_limit_reached
        '413': { $ref: '#/components/responses/TooLarge' }      # PG-TR-3's 1 KiB pre-parse bound
        '429': { $ref: '#/components/responses/Throttled' }
        '500': { $ref: '#/components/responses/Internal' }
        '503': { $ref: '#/components/responses/Unavailable' }
```

- **PG-ALLOC-1** (Ubiquitous) `push_address` SHALL be 16 CSPRNG bytes, gateway-minted, opaque, and
  SHALL encode nothing about the installation, the machine, the pairing order, or the allocation
  time. It appears in the clear inside `WakeV1` (§5) and is therefore read by both the gateway and
  FCM; §7.4 states that disclosure rather than arguing it away.
- **PG-ALLOC-2** (Ubiquitous) An allocation is **unbound** until the gateway accepts its first
  submit-wake. Binding is inferred from that first acceptance and requires no sixth operation.
  **This definition is invented here — no record supplies one** (§14.2 A6), and §13.9 asks the owner
  to rule it, because the obligation in the next sentence is product-visible and the alternative
  reading of ADR-015 P6 ("unbound" = not yet attached to a pairing) does not produce it.
  **Every pairing SHALL therefore send a successful gateway test wake before it is complete** — not
  only a migrating one. Playbook `:955-958` requires the test wake before a pairing may leave
  `legacy_relay`, but that is the migration instance of the rule, not its whole extent: a *fresh*
  pairing has no `legacy_relay` state to leave, so if the test wake were migration-only, a newly
  paired machine would produce no qualifying event inside ten minutes, its address and both
  verifiers would be swept below, and push would die silently while the pairing believed it
  succeeded. The test wake is a real `WakeV1` at `wake_seq` 1 under the pairing's wake key; the phone
  treats it as an ordinary wake and PG-WAKE-16's high-water absorbs it. It is an ordinary obligation on
  the machine too, so PG-WAKE-7's five-minute expiry bounds it and PG-OBL-6's second clause re-mints
  it once when that expiry arrives with the allocation still unbound — which is what gives this
  ten-minute window two attempts rather than one. PG-MIG-2 restates this as one
  of the four migration preconditions.

  An allocation still unbound ten minutes after issue SHALL be deleted with all its verifiers, and
  the pairing must allocate again (§8.1 row 1).
- **PG-ALLOC-3** (Event-driven) WHEN a pairing fails or is abandoned, the phone SHALL delete the
  allocation immediately with the installation credential (§3.4) rather than wait for the ten-minute
  sweep (playbook `:136-139`).
- **PG-ALLOC-4** (Ubiquitous) One installation holds N independent allocations for N machine
  pairings (ADR-018 MM5). No object is shared between them: not the address, not the wake key, not
  either capability, not the wake sequence, not the phone's high-water. The count SHALL be bounded
  per installation (`address_limit_reached`); §13.4 asks the owner for the number.
- **PG-ALLOC-5** (Unwanted) IF a capability is suspected exposed, THEN the whole address SHALL be
  revoked and a fresh allocation rebound through an authenticated pairing-update. There is **no
  in-place verifier swap**, because a swap leaves both generations momentarily usable
  (playbook `:137-139`).

### 3.4 Revoke address

Caller: Android **or** `swarm-remote`. Purpose (playbook `:521`): delete one address using the
installation credential **or** its distinct machine-revoke capability.

```yaml
paths:
  /addresses/{push_address}:
    delete:
      operationId: revokeAddress
      summary: Delete one address, its verifiers, and its token binding. Idempotent.
      security:
        - installationSignature: []   # phone-side "forget this computer"
        - machineRevokeCapability: [] # machine-side "revoke this phone"
      parameters:
        - $ref: '#/components/parameters/PushAddressPath'
      responses:
        '204':
          description: >-
            Deleted, or already deleted. Idempotent by construction: a bounded revocation tombstone
            (section 8) lets a durable machine-side retry receive 204 rather than 401 after the
            verifier it presented has been destroyed.
        '400': { $ref: '#/components/responses/BadRequest' }    # any body at all: PG-TR-3's 0-byte row
        '401': { $ref: '#/components/responses/Unauthorized' }
        '409': { $ref: '#/components/responses/Conflict' }      # nonce_replayed, on the signature arm only
        '429': { $ref: '#/components/responses/Throttled' }
        '500': { $ref: '#/components/responses/Internal' }
        '503': { $ref: '#/components/responses/Unavailable' }
```

- **PG-REV-1** (Ubiquitous) Revocation is **whole-address**: the address, both verifiers, the
  address→installation binding and any in-flight idempotency state are deleted together
  (ADR-015 P6). There is no partial revocation.
- **PG-REV-2** (Ubiquitous) Revocation SHALL be idempotent for both credential kinds. For the
  installation-key path this is free (the credential outlives the address). For the machine-revoke
  path the successful delete destroys the verifier being presented, so the gateway SHALL retain a
  bounded tombstone — the hashed machine-revoke verifier plus a revoked-at timestamp, no address
  content — long enough for a durable retry across an ADR-011 M5 process exit to receive `204`.
  §13.3 asks the owner whether that tombstone lives under the 7-day diagnostics row or gets a row
  of its own; this document places it under the 7-day row and says so.
- **PG-REV-3** (Ubiquitous) The gateway SHALL NOT distinguish "no such address" from "not your
  address" in its response. Both are `204` for a valid installation signature and `401` for an
  unverifiable capability. An existence oracle over a 16-byte opaque space is cheap to avoid and
  free to leak.
- **PG-REV-4** (State-driven) WHILE ADR-015 P12's compatibility window is open, revocation attempts
  **both** legacy relay deletion and gateway deletion idempotently, and the per-pairing
  `push_transport` state plus the wake sequence forbid double delivery (playbook `:959-962`).

### 3.5 Submit wake

Caller: `swarm-remote`. Purpose (playbook `:522`): present the unguessable submit capability and a
bounded `WakeV1`.

```yaml
paths:
  /wakes:
    post:
      operationId: submitWake
      summary: Forward one 74-byte WakeV1 envelope to FCM, unchanged and unopened.
      security:
        - submitCapability: []
      requestBody:
        required: true
        description: >-
          Exactly 74 raw octets (section 5.1); the length is normative in PG-TR-3, not here, because
          a JSON Schema cannot express a raw-octet length — minLength/maxLength bound a string, and
          this body is not a string. Any other length — over, under, or unmeasurable — is 400
          wake_malformed; 413 is deliberately absent from this responses map and unreachable on this
          operation (PG-TR-3). The gateway parses only version, type and push_address from the
          first 18 bytes for routing; it holds no wake key and never attempts the AEAD.
        content:
          application/octet-stream:
            schema:
              contentMediaType: application/octet-stream
      responses:
        '200':
          description: >-
            FCM accepted the byte-identical request. 202 is deliberately NOT used: "Accepted"
            connotes a queue, and ADR-015 P9 forbids the gateway from having one.
            Acceptance does NOT prove handset display (playbook :548).
          content:
            application/json:
              schema:
                type: object
                additionalProperties: false
                required: [status]
                properties:
                  status:
                    const: provider_accepted
        '400': { $ref: '#/components/responses/BadRequest' }   # wake_malformed; malformed_request (PG-TR-4, PG-TR-5)
        '401': { $ref: '#/components/responses/Unauthorized' }
        '410':
          description: >-
            Terminal for this obligation. address_revoked or push_token_unregistered. The machine
            stops retrying and surfaces the repair path; it does not roll back the mailbox event.
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Error' }
        '429': { $ref: '#/components/responses/Throttled' }
        '502':
          description: >-
            Provider unreachable or refusing. `upstream_unavailable` is retryable and the obligation
            survives; `upstream_refused` is not (section 4).
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Error' }
        '500': { $ref: '#/components/responses/Internal' }
        '503': { $ref: '#/components/responses/Unavailable' }
```

- **PG-SUB-1** (Ubiquitous) The gateway SHALL forward the envelope **unchanged**: the 74 bytes it
  received, base64-encoded into the single FCM data key, with `android.priority` high, data-only, to
  the device token and never to a topic — the three properties `internal/remote/push/fcm.go:140-158`
  already argues in place. It SHALL NOT re-seal, pad, truncate, annotate, or add a second data key.
- **PG-SUB-2** (Ubiquitous) The gateway SHALL return success only after FCM accepts the
  byte-identical request (playbook `:551-552`). It SHALL NOT acknowledge and then send.
- **PG-SUB-3** (Ubiquitous) The `push_address` used for routing SHALL be the one inside the
  envelope, and the presented submit capability SHALL be the verifier of **that** address. A
  capability that verifies against a different address is `unauthorized`; the gateway SHALL NOT
  route on any address supplied outside the AAD-covered bytes.
- **PG-SUB-4** (Ubiquitous) The gateway MAY hold a bounded idempotency cache keyed
  `SHA-256(the 74 received octets)` for at most the wake's five-minute expiry, and MAY answer a
  byte-identical retry from it. Hashing the whole body rather than parsing `(push_address, wake_seq)`
  out of it is deliberate on two counts: it needs no parse beyond the 18 routing bytes the gateway
  already reads, and it makes "byte-identical retry" literal rather than approximate — PG-WAKE-12's
  seal-once rule guarantees that a retry of one obligation is the same 74 octets, so the hash is
  exactly the identity the cache should key on. It SHALL NOT populate that cache with anything other
  than an outcome already returned by FCM — a cached success without a prior FCM acceptance is the
  hidden queue P9 forbids, wearing a different name.
- **PG-SUB-5** (Ubiquitous) The gateway SHALL NOT parse, store, or index `wake_seq` **at all**. It
  reads the first 18 bytes (`version`, `type`, `push_address`) for routing and shape-checking, and
  treats bytes 18-73 as opaque. Replay defence is the phone's durable high-water (§5.5), not the
  gateway's memory, and after PG-SUB-4's move to a whole-body hash the gateway has no remaining
  reason to look at the counter. **It is a discipline on an honest gateway, not a security boundary**:
  the 74 octets are in its hands and `wake_seq` is cleartext in them, so §7.3's first bullet states
  what a compromised gateway reads anyway. §14.2 A5 records it as an addition no record makes.

### 3.6 Shared components

```yaml
components:
  securitySchemes:
    registrationProof:
      type: apiKey
      in: header
      name: Swarm-Registration-Proof
      description: >-
        Exactly one p256-sha256 signature over
        swarm-pg-register-v1|Idempotency-Key|body_sha256 (section 2.5), verified using the
        installation_public_key in the final body. body_sha256 covers the exact body,
        including attestation.token. Canonical raw base64url of 64-byte low-s P1363.
        Does not authorize installation control, replace attestation, or bypass admission.
    installationSignature:
      type: apiKey
      in: header
      name: Swarm-Signature
      description: >-
        p256-sha256 over swarm-pg-v1|METHOD|path|body_sha256|nonce|expiry (section 2.1), with
        Swarm-Nonce and Swarm-Expiry carrying the two non-derivable components. `path` is the full
        origin-relative path INCLUDING the /v1 prefix this document carries in `servers` rather than
        in `paths`, and EXCLUDING any query string — a query string on a signed operation is
        refused as malformed before verification. The installation id is in the path so it is
        signature-covered.
    submitCapability:
      type: apiKey
      in: header
      name: Authorization
      description: >-
        `Authorization: Swarm-Capability <value>` (section 2.2) — 32-byte unguessable capability,
        base64url unpadded. Authorizes POST /wakes and nothing else. Declared as apiKey rather than
        http + scheme because OpenAPI defers `scheme` to the IANA HTTP Authentication Scheme
        registry, which carries no Swarm entry; the wire form is fixed normatively in section 2.2
        either way, and this declaration states it without claiming a registration that does not
        exist.
    machineRevokeCapability:
      type: apiKey
      in: header
      name: Authorization
      description: >-
        `Authorization: Swarm-Revoke <value>` (section 2.3) — 32-byte unguessable capability,
        base64url unpadded, distinct from the submit capability. Authorizes DELETE of its own address
        only. apiKey rather than http + scheme for the same registry reason as above.
  parameters:
    InstallationIdPath:
      name: installation_id
      in: path
      required: true
      schema: { $ref: '#/components/schemas/InstallationId' }
    PushAddressPath:
      name: push_address
      in: path
      required: true
      schema: { $ref: '#/components/schemas/PushAddress' }
    IdempotencyKey:
      name: Idempotency-Key
      in: header
      required: true
      schema:
        type: string
        pattern: '^[A-Za-z0-9_-]{22}$'
        description: 16 CSPRNG bytes, base64url unpadded. Client-generated, retained 10 minutes.
  schemas:
    InstallationId:
      type: string
      description: 16 opaque gateway-minted bytes, base64url unpadded. A routing handle, not a user.
      pattern: '^[A-Za-z0-9_-]{22}$'
    PushAddress:
      type: string
      description: 16 opaque gateway-minted bytes, base64url unpadded. Carried in the clear in WakeV1.
      pattern: '^[A-Za-z0-9_-]{22}$'
    Error:
      type: object
      additionalProperties: false
      required: [code, message, retryable]
      properties:
        code:
          type: string
          description: Stable machine-readable code from the closed vocabulary of section 4.
        message:
          type: string
          description: >-
            Human-readable, non-secret. Never contains an FCM token, a capability, an envelope, a
            signature, or a provider error body.
          maxLength: 200
        retryable:
          type: boolean
          description: Whether an identical retry can succeed later. Drives the obligation machine.
        retry_after_seconds:
          type: integer
          minimum: 0
        server_time:
          type: string
          format: date-time
          description: Present only on request_expired, so a skewed client can correct.
  responses:
    BadRequest:
      description: Malformed, oversized-field, or unparseable request.
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    Unauthorized:
      description: Credential not accepted. Deliberately non-discriminating (section 4).
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    Forbidden:
      description: Admission refused — attestation or closed beta.
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    Conflict:
      description: Replayed request nonce, registration idempotency conflict, or a bound reached.
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    TooLarge:
      description: Body exceeded the pre-parse bound.
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    Throttled:
      description: Quota exceeded. Always explicit; never a silent drop.
      headers:
        Retry-After: { schema: { type: integer } }
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    Unavailable:
      description: Gateway unavailable. Retryable.
      headers:
        Retry-After: { schema: { type: integer } }
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
    Internal:
      description: >-
        Unclassified gateway fault (code `internal`, retryable). Declared on every operation:
        the code is in section 4's closed vocabulary and section 6.4 transitions on it, so an
        operation that omitted it would be a hole in the obligation machine's input alphabet.
      content: { application/json: { schema: { $ref: '#/components/schemas/Error' } } }
```

---

## 4. Error vocabulary

- **PG-ERR-1** (Ubiquitous) `code` is a **closed, stable vocabulary**. Adding a code is a minor
  version bump; changing a code's meaning requires a new path version. An unknown code is still a
  gateway error body, and `retryable` is required on every error body (§3.6), so a client SHALL take
  the `retryable` field as authoritative even when it does not recognise the code. The status-based
  fallback exists for exactly one case and is scoped to it: **no parseable error body at all** — a
  proxy-generated or truncated response — in which case the client SHALL treat 429 and 5xx as
  retryable and every other status as not. That fallback is the only reading of a status permitted
  anywhere in this document (PG-ERR-3).
- **PG-ERR-2** (Ubiquitous) An error body SHALL never contain an FCM token, a capability, an
  envelope, a signature, an attestation token, or a verbatim provider error body (§8.2).

| Code | Status | Retryable | Meaning and the reason it is or is not distinguished |
|---|---|---|---|
| `version_unsupported` | 404 | no | Unknown path version prefix. Distinguished so an old installation gets a real answer during P12's window. |
| `malformed_request` | 400 | no | Unparseable body, wrong content type, `Content-Encoding` present, disallowed field. |
| `wake_malformed` | 400 | no | `POST /wakes` body is not exactly 74 octets — over, under, or unmeasurable — or its version/type bytes are not `WakeV1`'s. Refused before any routing lookup. **This is the only length refusal on the wake path**: `body_too_large` is never returned there (PG-TR-3). |
| `body_too_large` | 413 | no | Exceeded the pre-parse bound of PG-TR-3 on one of the four JSON/empty-bodied operations. Unreachable on `POST /wakes`. |
| `unauthorized` | 401 | no | **Deliberately non-discriminating**: unknown installation, missing/invalid registration proof, bad control signature, unknown or revoked capability, capability/address mismatch. Splitting these would build an enumeration oracle out of an error code. |
| `request_expired` | 401 | **no** | Signed `expiry` is past or beyond the 120 s horizon. `retryable` is `false` because the field means *an identical retry can succeed later* (§3.6) and an identical retry cannot: `expiry` is inside the signed pre-image, so the byte-identical request is expired forever. The recovery is a **different** request — re-signed against the `server_time` this error carries — which is why the code is distinguished at all. Safe to distinguish: computed from the presented request with no lookup. |
| `nonce_replayed` | 409 | no | Reached only after a signature verified, so it discloses nothing an authenticated caller does not already know. |
| `idempotency_conflict` | 409 | no | Registration's idempotency key already names a different body; checked only after admission and valid registration proof. |
| `attestation_invalid` | 403 | no | Play Integrity evidence absent, unbound to the body, or not the licensed Play-signed build. |
| `attestation_unavailable` | 403 | yes | Google's verification endpoint could not be reached. The handset retries; it is never enrolled unattested. |
| `beta_closed` | 403 | no | Closed-beta admission control (playbook `:559-560`). Carries the appeal path §14 of the playbook requires. |
| `address_limit_reached` | 409 | no | Bounded allocations per installation (PG-ALLOC-4). |
| `address_revoked` | 410 | no | Terminal for a wake obligation. Only reachable by a caller presenting a capability that once verified, so it is not an oracle. |
| `push_token_unregistered` | 410 | no | FCM reported `UNREGISTERED` (`fcm.go:195-215`). Terminal for this wake; the address survives for a later rotation (PG-ROT-2). |
| `quota_exceeded` | 429 | yes | Per-address, per-source or global admission limit (§9). Always explicit. |
| `upstream_unavailable` | 502 | yes | FCM transport failure or 5xx — "the provider having a bad moment" (`fcm.go:96-101`). |
| `upstream_refused` | 502 | no | FCM refused with a non-retryable 4xx that is not `UNREGISTERED`. Non-retryable because retrying is quota spent to reproduce a refusal. |
| `service_unavailable` | 503 | yes | Gateway shedding load or in maintenance. Carries `Retry-After`. |
| `internal` | 500 | yes | Unclassified gateway fault. |

- **PG-ERR-3** (Ubiquitous) The mapping from this table to the obligation machine is exactly
  §6.4's transition table. `retryable` is the contract; the machine SHALL NOT infer retryability
  from the status code alone, **except** in PG-ERR-1's single bodyless case, which §6.4 handles as a
  transport failure rather than as a code.

---

## 5. `WakeV1`

Normative shape. **This section must agree exactly with ADR-015 P8 and its three deltas**; where it
does not, ADR-015 wins and §14 records the difference.

### 5.1 The wire object — 74 bytes, one pinned constant

| Offset | Bytes | Field | Encoding |
|---:|---:|---|---|
| 0 | 1 | `version` | `0x01` |
| 1 | 1 | `type` | `0x03` — a **new** value, distinct from `TypeMailbox` 0x01 and the legacy `TypePushWake` 0x02 (`internal/remote/crypto/envelope.go:15-19`) |
| 2 | 16 | `push_address` | the raw 16 opaque bytes of PG-ALLOC-1 |
| 18 | 8 | `wake_seq` | uint64 big-endian |
| 26 | 8 | `issued_at` | int64 big-endian, Unix milliseconds |
| 34 | 24 | `nonce` | XChaCha20-Poly1305 nonce |
| 58 | 16 | `tag` | Poly1305 tag over an **empty** plaintext |
| | **74** | | |

- **PG-WAKE-1** (Ubiquitous) The AEAD SHALL be XChaCha20-Poly1305 under the per-pairing wake key,
  over a zero-length plaintext. There is no ciphertext, only a tag.
- **PG-WAKE-2** (Ubiquitous) The size SHALL be exactly 74 bytes, pinned as one constant with schema
  tests that fail when it moves, and derivable from this table so a reviewer can recompute it rather
  than trust it (ADR-015 P8). `PushWakeEnvelopeSize` moves from 78 to 74
  (`internal/remotegw/push.go:20-29`); its doc comment's argument — that a conceded size disclosure
  "is benign only while it is CONSTANT" — is untouched, and this is a one-time move of the pin, not a
  licence for the size to depend on anything.
- **PG-WAKE-3** (Ubiquitous) The type byte SHALL be checked and the three shapes separated **before**
  any AEAD is touched, exactly as `crypto.OpenWake` refuses a type-0x01 envelope today. The domain
  string in the AAD is the second, cryptographic half of that separation and is not a substitute for
  the first.
- **PG-WAKE-4** (Ubiquitous) `WakeV1` is added **beside** the frozen mailbox envelope, never by
  editing it. `crypto.EnvelopeHeader`, its `aad()` and that function's deliberate
  `recipient_key_id` exclusion (`envelope.go:43-68`), `SealMailbox`/`OpenMailbox`, the XChaCha nonce
  rules and the mailbox seq discipline are not edited by anything in this document.
- **PG-WAKE-5** (Ubiquitous) There are **no key-id fields** on this shape, so B20's "key ids zeroed"
  clause has no subject here. Parking the address in reusable key-id bytes is specifically
  forbidden: `aad()` excludes `recipient_key_id`, so half the address would sit outside the
  authenticator, mutable by relay, gateway or provider without breaking the tag — and an
  unauthenticated address selects which high-water coordinate the phone compares against, which is
  the pin-the-window lever `internal/phonecore/wake.go:87-95` exists to deny.

### 5.2 `expires_at` is derived, not carried

- **PG-WAKE-6** (Ubiquitous) `expires_at = issued_at + 300000` milliseconds. It is computed
  identically by both sides, bound in the AAD, and **absent from the wire**.
- **PG-WAKE-7** (Ubiquitous) Five minutes, not ten. This narrows `WakeMaxAge`
  (`internal/phonecore/wake.go:34-42`) from `10 * time.Minute` to `5 * time.Minute` and matches the
  five-minute FCM TTL the playbook sets for the high-priority data wake (`:303`). The comment above
  that constant survives verbatim at five minutes: narrowing the bound strengthens exactly the
  property it defends. The persisted counter is unchanged — it is what actually rejects a replay;
  the expiry is only the outer bound.

  *Recorded divergence*: playbook `:535-536` lists a five-minute expiry among the fields `WakeV1`
  "carries". ADR-015 P8 keeps every item of that list on the wire **except** `expires_at`, pins the
  size at 74 rather than 82, and records the deviation as one. This document follows ADR-015. If the
  owner prefers it carried, the size becomes 82, the AAD is unchanged, and §5.1's table gains a row
  at offset 34.

### 5.3 Canonical AAD

```text
AAD = "swarm-wake-v1"          (13 bytes, ASCII, no terminator)
   || version                  (1 byte)
   || push_address             (16 bytes)
   || wake_seq                 (8 bytes, uint64 BE)
   || issued_at                (8 bytes, int64 BE, ms)
   || expires_at               (8 bytes, int64 BE, ms — derived, PG-WAKE-6)
   || nonce                    (24 bytes)
                               = 78 bytes, fixed
```

- **PG-WAKE-8** (Ubiquitous) This tuple is the playbook's and ADR-015's canonical AAD
  `(swarm-wake-v1, version, push_address, wake_seq, issued_at, expires_at, nonce)` (playbook
  `:537-539`), with each element's byte encoding fixed here. Every element is fixed-width except the
  leading constant, so concatenation is unambiguous.
- **PG-WAKE-9** (Ubiquitous) The AAD SHALL NOT be `crypto.EnvelopeHeader.aad()` with fields renamed.
  It adds a domain-separation string and `expires_at`, and it **binds the nonce**, which `aad()`
  deliberately omits because the nonce is the AEAD's own parameter.
- **PG-WAKE-10** (Ubiquitous) The `type` byte is the one wire byte not literally in the tuple. It is
  bound in effect rather than in form: `type` and `version` jointly select the domain string, so a
  mutated type byte either fails PG-WAKE-3's pre-AEAD shape check or is opened under a different
  domain string and fails the tag. A path attacker therefore gains only a refusal, which it could
  obtain by dropping the packet. *This is a precision correction to ADR-015 P8's sentence "Every byte
  on the wire is AAD-covered", recorded in §14; the canonical tuple is normative in both records and
  is not widened here.* §13.2 offers the owner the alternative.

### 5.4 Nonce

- **PG-WAKE-11** (Ubiquitous) The nonce SHALL be 24 CSPRNG bytes and SHALL be unique under the wake
  key. Uniqueness is by construction of the generator, not by derivation from `wake_seq`.
- **PG-WAKE-12** (Ubiquitous) A wake is sealed **once**, at obligation creation, and the sealed 74
  bytes are what the obligation persists. Every retry re-sends those exact bytes — same nonce, same
  `issued_at`, same tag. This is what makes P9's "retries the byte-identical wake" literally true and
  makes PG-SUB-4's idempotency cache safe. Re-sealing on retry would mint a second nonce and a second
  authenticator for one logical wake, and would silently extend the expiry the obligation is bounded
  by.

### 5.5 Phone side — verify, then persist the high-water, then route

- **PG-WAKE-13** (Ubiquitous) The receiver's order SHALL be:

  1. parse; require `version` 0x01 and `type` 0x03 before touching the AEAD;
  2. select the per-pairing wake key by `push_address`; if the pairing holds none, refuse with the
     waiting verdict (`ErrNoWakeKey`), never "invalid request";
  3. **open under that wake key**, which authenticates every field above;
  4. only then compare `wake_seq` against the persisted per-address high-water, and
     `now - issued_at` against the five-minute bound;
  5. only then **atomically persist** the advanced high-water — before routing.

  Step 3 before step 4 is the whole contract. A receiver that advanced the coordinate before
  authenticating would hand any party on the path a one-packet permanent denial of service: an
  unopenable envelope carrying seq 2^63 pins the window at the top and every genuine wake afterwards
  is refused as a replay (`internal/phonecore/wake.go:67-79`). The address is a routing field, as the
  epoch id was; it selects **which** coordinate to compare, and comparing is still step 4. Selecting
  on it is safe only because it is fully AAD-covered (PG-WAKE-5).
- **PG-WAKE-14** (Ubiquitous) The old step 2 — "require the wake to name the epoch this phone holds a
  key for" (`wake.go:64-73`, `:130-133`) — is **removed**, not retargeted. Its defence at `wake.go:87-95`
  rests on "the wake key is per epoch and a revoke rotates it", which ADR-015 P7 makes false. The
  property it protected is preserved by whole-address revocation instead: a revoked address is deleted
  at the gateway and its successor is a *different* `push_address` with its own high-water, so no
  coordinate is shared across generations for a stale wake to pin.
- **PG-WAKE-15** (Ubiquitous) `State.WakeReplay` becomes **one coordinate per `push_address`**. With
  ADR-018's N pairings that is a table, and one machine's wake SHALL NOT advance another machine's
  coordinate.
- **PG-WAKE-16** (Ubiquitous) `wake_seq` SHALL be per-pairing, durable on the machine, and strictly
  increasing. It starts at 1; the phone's high-water starts at 0; acceptance requires
  `wake_seq > high_water`. Gaps are legal, and a gap SHALL NOT be treated as loss or as a repair
  trigger. Two things create them, and **coalescing is not one of them**: PG-OBL-5 rules that a
  trigger coalescing into a live obligation consumes no `wake_seq`, and PG-OBL-4's 30-second
  per-session window mints none either, so both layers of §6.2 leave the sequence untouched by
  construction. What does create a gap is an obligation that **consumed** a sequence and never
  delivered it: (1) PG-OBL-6's re-mint, which abandons the expired obligation's `wake_seq` and takes
  a fresh one — the ordinary case, since the replacement exists precisely because the first was not
  delivered; and (2) any obligation that ends in `expired` with nothing re-minted, or in `abandoned`
  under §14.1 row 7's open reading.
- **PG-WAKE-17** (Unwanted) IF the wake key is unavailable because the device is locked, THEN the
  envelope SHALL be stored and SHALL cause **only** a generic notification. It SHALL NOT select a
  machine, deep-link, connect, or mutate state until unlock and validation (playbook `:545-547`,
  `:307-310`).
- **PG-WAKE-18** (Ubiquitous) The payload SHALL carry no session, interaction, cursor, provider,
  category, repository, prompt, tool or approval locator, **even encrypted** (playbook `:543-544`,
  `:1024`), and no session name, hostname, agent name or Group label (ADR-007 B10). There is no
  plaintext to put one in, and adding one is a shape change that PG-WAKE-2's pinned constant fails on.

---

## 6. The wake-obligation state machine (`swarm-remote`)

The obligation lives on the machine, not the gateway. ADR-015 P9: the gateway has **no hidden
delivery queue**.

### 6.1 The record

- **PG-OBL-1** (Ubiquitous) The obligation SHALL be keyed `(push_address, wake_seq)` and SHALL
  persist: the sealed 74 bytes (PG-WAKE-12), `issued_at`, the derived `expires_at`, the state, the
  attempt count, and the last outcome code. It SHALL NOT persist the wake key, the submit capability,
  or any session identifier.
- **PG-OBL-2** (Ubiquitous) The obligation SHALL be recorded **before or atomically with** mailbox
  publication (playbook `:550-551`). "After the append succeeded, in memory" is the specific failure
  this rule forbids: a crash in that gap publishes an event the phone is never told about.
- **PG-OBL-3** (Ubiquitous) A failure anywhere in this machine SHALL never roll back, delete, or hide
  the published mailbox event (playbook `:555-556`). The existing rule that a failed wake never fails
  a record (`internal/remotegw/push.go:180-198`) is the same rule and stays.

### 6.2 Coalescing

Two layers, both required, neither replacing the other:

- **PG-OBL-4** (Ubiquitous) **Per session, 30 seconds**: at most one wake per session per
  `DefaultPushWindow` (`internal/remotegw/push.go:14-18`), pinned by
  `TestPBPUSH0_CoalescesRepeatTransitionsWithinTheWindow` (`push_trigger_test.go:399`). R3 keeps this
  green against the new submitter rather than re-deriving it.
- **PG-OBL-5** (Ubiquitous) **Per address, at most one outstanding obligation.** WHILE an obligation
  for a `push_address` is non-terminal, a new trigger for that address SHALL coalesce into it and
  SHALL NOT consume a `wake_seq`. This is sound precisely because the wake carries no locator: the
  pending wake, once delivered, already causes the phone to reconcile everything that happened. It
  bounds the outstanding set to N (one per pairing) and bounds the per-address submit rate to the
  delivery round trip.
- **PG-OBL-6** (Event-driven) WHEN an obligation reaches `expired` with triggers coalesced into it
  that were never announced, a **fresh** obligation SHALL be minted with a new `wake_seq` and a fresh
  `issued_at`. Otherwise the phone is never woken for work that is still waiting.

  **The same re-mint SHALL happen, once and with no coalesced trigger required, when the expired
  obligation was the binding wake of an allocation that is still unbound** (PG-ALLOC-2). A test wake
  has nothing coalesced into it, so the first clause alone would not reach it, and the arithmetic is
  unforgiving: PG-WAKE-7 bounds the obligation at five minutes and PG-ALLOC-2's unbound window is ten,
  so a single expiry would leave the second half of that window with nothing driving the allocation
  before the sweep destroys the address and both verifiers — a pairing the user completed, losing its
  push binding after one bad five-minute stretch of connectivity. One re-mint, not a loop: the
  replacement's own five minutes end exactly at the sweep, and an allocation that has failed twice
  across the whole window is genuinely unreachable, which is the case the sweep is for.
- **PG-OBL-7** (Ubiquitous) Sender-side preference suppression is unchanged and fails closed on doubt
  (`push.go:443-466`); a suppressed interaction wake is deferred, not dropped (`push.go:329-359`); a
  seeded roster still stops a gateway restart firing n stale wakes (`push.go:156-178`). A suppressed
  trigger creates **no** obligation — suppression is at the sender, which is what PB-PUSH-8 requires
  and what keeps token/timing/size off the wire entirely.

### 6.3 States

```text
                 trigger (post-coalescing)
                        │
                        ▼
                   ┌─────────┐    submit attempt    ┌───────────┐
                   │ pending │──────────────────▶   │ in_flight │
                   └─────────┘                      └───────────┘
                     ▲    │                           │  │  │  │  200 provider_accepted
   retryable outcome │    │                           │  │  │  └──────────────┐
                     └────┼───────────────────────────┘  │  │                 │
                          │      now > expires_at        │  │                 │
                          │   ┌──────────────────────────┘  │                 │
                          │   │   terminal refusal          │                 │
                          │   │   (any retryable=false)     │                 │
                          ▼   ▼                             ▼                 ▼
                       ┌─────────┐                    ┌───────────┐     ┌───────────┐
                       │ expired │                    │ abandoned │     │ delivered │
                       └─────────┘                    └───────────┘     └───────────┘
```

`pending`, `in_flight` are non-terminal. `delivered`, `expired`, `abandoned` are terminal.

**`abandoned` is this document's invention and neither record has it — read §14.1 row 7 before
building either half of this machine.** ADR-015 P9 says without qualification that "timeout,
transport failure, non-2xx and non-final FCM responses leave the obligation retryable", and playbook
`:555` says "Restart retries the byte-identical wake until success or declared expiry". Read
literally, a `410 address_revoked` is a non-2xx and therefore retryable: the obligation would retry a
wake for an address the gateway has deleted until its five minutes elapsed, then reach `expired`.
The argument for `abandoned` is that the refusals routed to it are *decided* rather than transient —
a revoked address, an unregistered token, a malformed envelope, a malformed request, a rejected
capability, an unserved path version and a provider refusal that is not `UNREGISTERED` will each
return the identical answer to the byte-identical
request, so retrying spends quota to reproduce a refusal and delays the repair path §6's degraded
state exists to surface. The set is not an enumeration this document maintains by hand: it is
**every refusal the gateway marks `retryable=false`**, which is what §6.4's residual row says and
what keeps the machine total against PG-ERR-1's minor-version code additions. The argument is an
engineering argument, not a recorded ruling, and this
document does not get to promote it to one: §13.8 asks the owner to amend ADR-015 P9 or to strike
`abandoned`. **Until that is ruled, P9's literal text binds** and an implementer who routes every one
of them back to `pending` and lets expiry end them is following the record, not this section. The two
readings differ only in quota and in how fast a user sees "push is broken" — never in whether the
mailbox event survives, which PG-OBL-3 fixes identically in both.

The expiry edge leaves **both** non-terminal states, matching §6.4's `pending` / `in_flight` row.
An obligation whose five minutes elapse while a submit is in flight expires too: the wake it carries
is already past the FCM TTL PG-WAKE-7 pins the expiry to, so completing the round trip would spend
quota to deliver something the phone's `issued_at` check would refuse on arrival. PG-OBL-6 then mints
a fresh obligation if triggers were coalesced into the expired one, or if it was the binding wake of
an allocation that is still unbound.

### 6.4 Transitions

| From | Event | To | Notes |
|---|---|---|---|
| — | trigger survives §6.2 coalescing | `pending` | Sealed once here (PG-WAKE-12); recorded before/with the mailbox append (PG-OBL-2) |
| `pending` | submit begins | `in_flight` | Persisted before the request leaves, so a crash mid-request recovers as `in_flight`, not as "never tried" |
| `in_flight` | `200 provider_accepted` | `delivered` | FCM accepted the byte-identical request. **Not** proof of handset display |
| `in_flight` | timeout / transport failure | `pending` | Retryable (playbook `:552-553`, ADR-015 P9). A response with **no parseable error body** is handled here, not as a code — the single status-reading exception PG-ERR-1 scopes |
| `in_flight` | `429`, `502 upstream_unavailable`, `503`, `500` | `pending` | Retryable; honour `Retry-After` |
| `in_flight` | any non-2xx the gateway marks `retryable=true` | `pending` | The `retryable` field is the contract, not the status (PG-ERR-3) |
| `in_flight` | non-final FCM response relayed as retryable | `pending` | Explicitly retryable per playbook `:552-553` |
| `in_flight` | `410 address_revoked` | `abandoned` **(§14.1 row 7)** | The pairing's push binding is gone; surface repair, do not retry. **Unrecorded**: under P9's literal reading this is a non-2xx and stays `pending` until expiry |
| `in_flight` | `410 push_token_unregistered` | `abandoned` **(§14.1 row 7)** | The handset must rotate its token; the address survives. Same unrecorded status as the row above |
| `in_flight` | `400 wake_malformed`, `401 unauthorized`, `502 upstream_refused` | `abandoned` **(§14.1 row 7)** | Non-retryable by construction; retrying is quota spent to reproduce a refusal. Same unrecorded status |
| `in_flight` | `404 version_unsupported` | `abandoned` **(§14.1 row 7)** | Reachable on every operation, including this one, while PG-TR-2's path version and ADR-015 P12's compatibility window disagree. `retryable=false` (§4): the repair is a submitter that speaks a served version, not another attempt. Same unrecorded status |
| `in_flight` | `400 malformed_request` | `abandoned` **(§14.1 row 7)** | Reachable on the wake path through PG-TR-4 (`Content-Encoding` present) and PG-TR-5 (wrong `Content-Type`) — a submitter defect that the byte-identical retry reproduces exactly. Distinct from `wake_malformed`, which is the length/shape refusal. Same unrecorded status |
| `in_flight` | any other non-2xx the gateway marks `retryable=false` | `abandoned` **(§14.1 row 7)** | The residual row, and the mirror of the `retryable=true` row above. It exists so the machine's input alphabet is **total**: PG-ERR-1 lets a minor version add a code, and a code this table has no row for is exactly the hole §3's exhaustiveness rule exists to close. Same unrecorded status |
| `pending` / `in_flight` | `now > expires_at` | `expired` | Bounded by PG-WAKE-6's five minutes, which is also the FCM TTL — an expiry longer than the TTL is a replay window with no delivery behind it |
| `pending` / `in_flight` | process restart | same state, re-driven | PG-OBL-8 |
| `delivered` | response lost after FCM acceptance | (no transition) | A duplicate may reach the handset; the authenticator and high-water reject it harmlessly (playbook `:553-555`) |

**On the citations in the rows above.** Playbook `:552-553` is one sentence — "Timeout, transport
failure, non-2xx, and non-final FCM responses leave the obligation retryable" — and it covers the
six `abandoned` rows exactly as much as it covers the four `pending` rows that cite it. Citing it
only on the rows it agrees with would be the quietest way to launder a divergence into an inherited
ruling, which is what §14 exists to prevent. It is cited on the `pending` rows because it is their
source, and named on the `abandoned` rows as the text they contradict.

- **PG-OBL-8** (Event-driven) WHEN `swarm-remote` restarts, it SHALL load every non-terminal
  obligation and re-drive it — the byte-identical wake, until success or declared expiry
  (playbook `:555`). This is the same crash-consistent shape ADR-015 P6 requires of the machine-side
  revocation obligation across an ADR-011 M5 epoch-rotation exit, and R3 owes both the same
  fault-injection bill: crash before append, after append, before submit, after gateway commit, and
  before local acknowledgement (playbook `:741-742`).
- **PG-OBL-9** (Ubiquitous) Retry SHALL be bounded by expiry, not by an attempt count, and SHALL back
  off. `internal/remote/push`'s in-request retry loop (`fcm.go:102-131`) lives at the **gateway**
  after ADR-015 P2 and is not a substitute for this obligation: it bounds one HTTP call, this bounds
  one hand-off.
- **PG-OBL-10** (Ubiquitous) The obligation SHALL be observable in machine health: a pairing whose
  last obligation reached `expired` or `abandoned` is a visible degraded push state (playbook `:313`),
  sitting beside — not replacing — B143's unconditional Doze/battery-saver sentence.

---

## 7. Threat model

### 7.1 Parties

| Party | Operated by | Holds | Sees |
|---|---|---|---|
| Phone (`dev.swarm.phone`) | User | Installation private key (Keystore), N wake keys, N addresses, N capability pairs, content keys | Everything it is sent |
| Machine (`swarm-remote`) | User | Per-pairing wake key, address, submit and machine-revoke capabilities, content keys | Its own sessions |
| Relay | **The user or their organization** (RC-D2) | Nothing push-related after ADR-015 P1 — no credential, no token map, no transport | Routing ids, timing, sizes, presence, retention metadata (playbook `:350`) |
| Push gateway | **Swarm** | FCM sender credential, token↔address mapping, installation public keys, hashed verifiers, timestamps, minimal diagnostics | Opaque push address, submit-source IP, timing, the fixed wake size, the FCM outcome (playbook `:351`) |
| FCM / Google | Google | The device token and the app/project routing | App/project routing, device token, opaque address, clear wake counter and time, timing (playbook `:352`) |

**The "Sees" column is declared observation scope, not bytes physically held, and the gateway row is
the one where those differ.** The playbook `:351` row this document reproduces describes what an
honest gateway looks at. What it *holds* is the whole 74-octet envelope, in the clear, for as long as
the request is in flight: PG-SUB-5 forbids it to parse, store or index `wake_seq`, but PG-SUB-5 binds
an honest implementation and constrains nothing about a compromised one. The keyless-readable
cleartext of a wake is **address, counter, issued-at, and size** (ADR-015 P11, restating ADR-007
B77's "claim no less exposure than exists"), and every one of those four is in bytes the gateway
receives and forwards. §7.3 states the consequence; this note exists so the table is not read as
denying it.

ADR-007 B133's boundary listed four in-scope items: the relay, the network path, FCM/Google, and any
MITM. **The gateway is the fifth entry and the fourth named party** — reading "party" as an operator
with a name and a contract rather than a position on the wire (ADR-015 P4).

### 7.2 Relay + gateway collusion (playbook `:918`)

The obligation the security review owes is the **colluding pair**, not each in isolation, because
RC-D2 makes the relay operator a different party from Swarm.

What the pair holds, combined: relay-side routing ids, presence, ciphertext sizes and timing; plus
gateway-side FCM token, push address, submit-source IP, wake timing and size.

- **They cannot decrypt content.** Session confidentiality is the content key, which the phone and
  machine hold and neither party ever sees. The relay is ciphertext-only by construction (RC-D7); the
  gateway receives no session traffic at all.
- **They cannot forge a command.** Remote operations are signed by the phone's device key and
  verified by the machine. Neither party holds a signing key. A gateway that mints bytes and a relay
  that delivers them still produces a signature failure at the daemon.
- **They cannot forge a wake.** The wake authenticator is the per-pairing wake key, phone-generated
  and conveyed inside the authenticated pairing transcript (ADR-015 P7). The gateway never receives
  it (playbook `:922`); the relay is not in the push path at all. This is why "give the gateway the
  wake key so it can validate the envelope" is rejected outright.
- **They cannot resolve an address to a machine or session identity from stored fields**
  (playbook `:919-920`): the address is 16 opaque bytes minted by the gateway with no structure, and
  the gateway stores no machine identifier, hostname, relay URL or session id to join it against.

**What the collusion does buy**, stated rather than argued away: correlation. The relay knows *which
routing ids are active when*; the gateway knows *which push address was woken when, from which source
IP*. Joined on timing, the pair can link a machine's relay routing id to its gateway push address and
to the developer's egress IP, and thereby to the handset's FCM token. That is a metadata join across
two parties that the previous topology kept inside one. **E2EE and signed commands — not network
separation — are what prevent content access and command forgery** (RC-D7, playbook `:77`,
`:355-359`). The gateway split is a credential-custody decision. It is not a security mechanism and
must not be described as one.

### 7.3 What a compromised gateway can do

- **Read every cleartext field of every wake it forwards.** It holds all 74 octets, and ADR-015 P11's
  list of the keyless-readable fields — **address, counter, issued-at, and size** — is exactly what
  is readable in them without any key: bytes 0-33 are the cleartext header (§5.1), of which
  `wake_seq` and `issued_at` are 16 octets no authenticator hides. PG-SUB-5's "SHALL NOT parse,
  store, or index `wake_seq` at all" is a **discipline on an honest gateway** — it removes a reason
  to look, not the ability — and this bullet exists because a threat model that quoted PG-SUB-5 at a
  compromised operator would be claiming less exposure than exists, which is precisely what ADR-007
  B77 forbids. Concretely, the counter is a per-pairing monotonic activity odometer: it discloses how
  many wakes a machine has ever produced, and gaps in it disclose wakes this gateway did not see
  (another transport, or a suppressed trigger). What stays hidden under compromise is unchanged and
  is the point of the empty-plaintext design: no session, interaction, cursor, provider, repository,
  prompt, tool or approval locator exists in the bytes to read (playbook `:543-544`).
- **Spam.** Submit wakes it has captured (it cannot mint new ones) or drive its own FCM sender at a
  handset's token: battery drain and generic notifications. It cannot make any of them route,
  deep-link, or mutate state, because the phone requires a valid authenticator (PG-WAKE-13) and a
  captured wake is rejected by the high-water on the second delivery and by the five-minute bound
  thereafter.
- **Suppress.** Drop every wake. This is the sharpest power it has, because B16 made push
  load-bearing: a dropped push is a missed hand-off with no fallback. The user-visible mitigation is
  §6's degraded state plus foreground reconciliation, not a second transport — ADR-015 P10 records
  that the "machine went silent" wake lost its transport and that no replacement is designed here.
- **Correlate.** Wake timing per address is an activity trace: "the content is hidden; the rhythm is
  not" (ADR-007 B77). Add the submit-source IP and the gateway sees *the developer's machine*, which
  is a genuinely new observation with no antecedent in the relay-hosted design.
- **Count a handset's machines.** Under ADR-018's N pairings, N distinct addresses converge on **one**
  FCM token. Either the gateway or FCM can therefore count how many machines a handset has paired and
  separate their wake streams — an inference the token alone did not support, since the token is one
  value for all N. ADR-018 reaches the same verdict for the analogous relay-side case and calls it "a
  designed disclosure, not an incident" (`ADR-018:123`); the difference here is that the relay is the
  operator's own service and the gateway is Swarm's.
- **What it cannot do**: mint a wake authenticator, submit a valid command, read a conversation, learn
  the relay URL or relay-auth key, learn a pairing secret, or turn a wake into a daemon command
  (playbook `:919`, `:355-359`).

### 7.4 The disclosure that widened, stated in the same breath as the design

B77's honesty rule binds: **claim no less exposure than exists.** Under B20 the wake carried two
*zeroed* key-id fields and conceded nothing about identity. `WakeV1` carries the opaque
`push_address` in the clear, and ADR-015 P7 makes it *more* stable than what was withheld, because it
is not epoch-scoped — only whole-address revocation rotates it. It is in the payload, so both the
gateway and FCM read it.

Not conceded, and still true: the address is opaque and gateway-minted; it names no machine the
provider can otherwise resolve; no endpoint key id or public-key-derived identifier is in the clear;
the plaintext is empty; the size is one pinned constant. The price is paid because the gateway cannot
route without it, and paying it in an AAD-covered field is strictly better than paying it in a mutable
one. `docs/operations/metadata-disclosure.md` carries the operator-facing form of this per ADR-015
P11's three edits; **this document does not edit that file** and does not claim it has been edited.

### 7.5 Residual risks accepted here

| Risk | Why accepted | What bounds it |
|---|---|---|
| Gateway availability is a product dependency the self-hoster does not control | B16 made push load-bearing and no alternative background path exists | §6 degraded state, honest "foreground updates only" copy, playbook §14's published availability status |
| Swarm now holds a join key (token ↔ address ↔ source IP ↔ timing) that N independent relay operators previously fragmented | The routing cannot be done without it | §8 retention, encryption at rest, log exclusions, metadata disclosure |
| A leaked submit capability lets an attacker wake one handset for one pairing | Capabilities are per-pairing and revocable; the wake still cannot be forged without the wake key | Per-address quota, whole-address revocation, no in-place swap |
| A leaked machine-revoke capability lets an attacker delete one address | Denial of push for one pairing, recoverable by re-allocation | Distinctness from submit, §9 quotas, visible degraded state |
| Play Integrity wrongly refuses a legitimate device | An abuse control the personal-only build never needed | The appeal/recovery path playbook §14 requires |

---

## 8. Retention

### 8.1 Gateway rows — playbook §13 (`:979-981`), reproduced exactly

| Store | Default retention and bound | Expiry behavior |
|---|---|---|
| Push gateway unbound allocation | 10 minutes. | Delete address and all verifiers; pairing must allocate again. |
| Push gateway delivery diagnostics | 7 days, no payload/capability/token. | Operational aggregates may remain only if irreversibly non-addressable. |
| Push installation mapping | Until revoke or 180 days without an authenticated app refresh. | Delete FCM token mapping and addresses; app shows foreground-only until it registers again. |

- **PG-RET-1** (Ubiquitous) These periods SHALL be published **before the first real token is
  collected** (playbook `:534`), as product behaviour rather than as an implementation default.
- **PG-RET-2** (Ubiquitous) Deletion SHALL be actual deletion of the token mapping and the verifiers,
  not a tombstoned row retaining them.
- **PG-RET-3** (Ubiquitous) The bounded revocation tombstone PG-REV-2 requires — a hashed
  machine-revoke verifier plus a revoked-at timestamp, and nothing else — lives under the 7-day
  diagnostics row. **That placement collides with the row's own wording**, which reads "7 days, no
  payload/capability/token": the tombstone's whole content is derived from a capability. This
  document's position is that a SHA-256 verifier is not a capability — it cannot be presented,
  cannot authorize anything, and is exactly what PG-AUTH-7 already stores for live addresses under
  the same row's regime — so the row's ban on capabilities is not violated by a one-way hash of one.
  The position is arguable, the row is reproduced verbatim from playbook `:980` and cannot be
  reworded here, and §13.3 puts both the placement and this wording collision to the owner.
- **PG-RET-4** (Ubiquitous, amended by ADR-027) Replay, idempotency and quota authority
  SHALL be shared durable metadata, not process-local caches. Logical expiry is checked
  at use; physical cleanup is bounded and does not extend authority.

  | Shared metadata | Horizon | Permitted contents |
  |---|---|---|
  | Request nonce claims (PG-AUTH-4) | 120 s, PG-AUTH-3's expiry horizon | Digest of installation id plus nonce; expiry |
  | Wake attempts (PG-SUB-4, ADR-027) | 5 min, bounded by the original wake deadline | Request/target digest, installation/address ids, token generation, state, attempt count, lease id/deadline, expiry and bounded gateway response status/body; never the wake payload |
  | Registration attempts (§3.6, PG-REG-2) | **10 minutes** | `HMAC-SHA256(Idempotency-Key)` document id; exact-body SHA-256 digest, installation id, refresh-before, expiry, state, lease id and lease-until |
  | Quota windows (§9) | The configured bounded rate window | Bucket digest, count and expiry |

  Registration's separate HMAC key is stable injected secret material. No raw
  idempotency key, request body, attestation token or registration proof is stored by
  the gateway. Registration attempts use a 30-second provider-owner lease inside the
  ten-minute identity window; a takeover does not create a new identity window.
  Provider work is outside retryable transactions. Wake claim/complete state permits
  bounded at-least-once submission, not an exactly-once provider guarantee.

### 8.2 What may never be stored, logged, or traced

- **PG-RET-5** (Ubiquitous) FCM tokens SHALL be encrypted at rest and SHALL be **excluded from logs
  and traces** (playbook `:531`).
- **PG-RET-6** (Ubiquitous) Capabilities — submit and machine-revoke — SHALL never be logged, traced,
  echoed in an error, or written anywhere in plaintext. Only their SHA-256 verifiers exist at rest.
- **PG-RET-7** (Ubiquitous) `WakeV1` payloads SHALL never be logged or traced, in whole or in part.
  A diagnostic may record `push_address`, an outcome code and a timestamp; it SHALL NOT record the
  nonce, the tag, or the bytes.
- **PG-RET-8** (Ubiquitous) Attestation tokens, request signatures and request nonces SHALL never be
  logged.
- **PG-RET-9** (Ubiquitous) The gateway SHALL NOT collect app version, OS version, device model,
  locale, or any advertising or account identifier. Nothing in the API of §3 carries one, and adding
  one is a change to this document rather than a diagnostic improvement.
- **PG-RET-10** (Ubiquitous) The stored field set is closed: token mapping, installation public key,
  opaque address, hashed capability verifiers, creation/last-use timestamps, an attestation verdict
  class, and minimum delivery diagnostics (playbook `:527-528`). ADR-027 adds token generation,
  encryption-key version and the bounded shared replay/idempotency/quota/attempt metadata
  enumerated in PG-RET-4. These are durable fields, not a cache exception. A field beyond
  this set requires an amendment; plaintext tokens, raw capabilities, request signatures,
  attestation tokens and session content remain forbidden. Registration proof adds no
  stored field.

---

## 9. Quotas and abuse

No-account abuse protection starts with unguessable capabilities, quotas, token validity, bounded
registrations per installation, IP and global admission limits, and a closed beta (playbook
`:558-560`).

- **PG-Q-1** (Ubiquitous) Wakes SHALL be quota'd **per address** and **per source** (playbook `:526`).
  The per-address bucket is the one that matters: it is what stops a leaked submit capability from
  draining a handset's battery, and it is charged to the address, not to the target's aggregate, so
  one pairing cannot silence another.
- **PG-Q-2** (Ubiquitous) A refused wake SHALL be refused **before** any provider call and SHALL
  return `429` with `Retry-After`. It SHALL NOT be silently dropped: a silent drop turns a
  load-bearing hand-off into an invisible one.
- **PG-Q-3** (Ubiquitous) Registration and allocation SHALL be bounded per source IP and globally,
  and allocations SHALL be bounded per installation (PG-ALLOC-4).
- **PG-Q-4** (Ubiquitous) Quota accounting SHALL key on the validated external source address behind
  whatever proxy topology R3 deploys, on the same trusted-proxy principle the relay bundle uses
  (playbook `:501-503`). A spoofed forwarding header consuming another caller's budget is a defect,
  not a configuration.

**Proposed default numbers, for R3 to confirm or replace with measurement.** These are not carried by
the playbook or ADR-015; §13.4 puts them to the owner.

| Bucket | Proposed default | Reasoning |
|---|---|---|
| Wakes per address | 20 per 5 minutes, burst 5 | PG-OBL-5 bounds one address to one outstanding obligation, so sustained legitimate rate is one per delivery round trip, not one per session |
| Wakes per source IP | 600 per hour | One developer machine with many pairings, with headroom |
| Registrations per source IP | 10 per hour | A handset registers once per install |
| Allocations per installation | 20 total live | ADR-018's N pairings; §13.4 asks for the real N |
| Signed requests per installation | 60 per hour | Rotation, allocation and revocation are rare and deliberate |

---

## 10. Migration and the R1 fixture

### 10.1 `push_transport`

- **PG-MIG-1** (Ubiquitous) Each pairing SHALL persist exactly one of `legacy_relay`, `gateway`,
  `foreground_only` (playbook `:955-957`).
- **PG-MIG-2** (Event-driven) WHEN a machine leaves `legacy_relay`, it SHALL do so only after **all
  four**: Android gateway registration, address allocation, authenticated pairing-update
  acknowledgement, and a **successful gateway test wake**. The fourth is the migration instance of
  PG-ALLOC-2's binding event, which every pairing owes; this requirement adds the other three and the
  atomicity, not the test wake itself. The transition SHALL be atomic; rollback selects one
  transport, never both.
- **PG-MIG-3** (State-driven) WHILE the compatibility window is open, revocation attempts both legacy
  and gateway deletion idempotently, and the wake sequence plus the selected state forbid double
  delivery.
- **PG-MIG-4** (Ubiquitous) `foreground_only` is a user-choosable state with honest copy: the app
  SHALL say "foreground updates only" and SHALL NOT imply reliable background delivery
  (playbook `:148-149`).

### 10.2 The fixture R1 owes

R1's deliverable list ends with "migration fixture" (playbook `:698`). This document **specifies** it;
it does not contain it, and no part of it has been generated or run.

- **PG-FIX-1** (Ubiquitous) The fixture SHALL live at `docs/verification/fixtures/push-gateway/`,
  beside the existing `spike-*` sets under `docs/verification/fixtures/`, and SHALL be consumed by
  tests in both `internal/remotegw` (producer) and `internal/phonecore` (receiver).
  (`docs/verification/r1-codex-fixtures` sits one level up, at `docs/verification/`, and is a
  sibling of the `fixtures/` directory rather than of this path.)
- **PG-FIX-2** (Ubiquitous) It SHALL contain, at minimum:

  | File | Content | What it pins |
  |---|---|---|
  | `wakev1-golden.json` | A fixed wake key, `push_address`, `wake_seq`, `issued_at`, nonce, and the resulting 74 hex-encoded bytes | §5.1's layout and §5.3's AAD, byte-for-byte, in both directions |
  | `wakev1-aad-golden.txt` | The 78 AAD bytes for the same vector, hex | That a re-implementation derives the same AAD without reading the sealer |
  | `wakev1-mutations.json` | One mutated copy per header field, each expected to fail to open, plus a type-byte mutation expected to fail the pre-AEAD shape check | Playbook `:540-541`'s mandatory header-mutation test |
  | `wakev1-replay.json` | Two wakes at seq N and N-1 against a high-water of N | Rollback and replay refusal |
  | `transport-migration.json` | A pairing record in each of the three `push_transport` states, plus the four-precondition transition and a rollback | PG-MIG-1..3, including that rollback selects exactly one transport |
  | `legacy-78.json` | One legacy type-0x02 78-byte wake with zeroed key ids | That the legacy shape still parses under `legacy_relay` and is rejected by the `WakeV1` parser |
  | `errors.json` | Every code of §4 with its status and `retryable` | PG-ERR-1's closed vocabulary and §6.4's transition mapping |

- **PG-FIX-3** (Ubiquitous) The golden vectors SHALL be generated by an implementation and then
  frozen; a fixture regenerated from the code under test pins nothing. Until R3 produces them, every
  size and offset in §5 is a design claim, not a verified one.

---

## 11. Conformance obligations

Playbook `:540-541` makes four tests mandatory; the rest follow from the rulings above.

- **PG-TEST-1** Header mutation: every field of §5.1 mutated in turn fails to open (fixture
  `wakev1-mutations.json`).
- **PG-TEST-2** Nonce reuse: two wakes sealed under one wake key never share a nonce; a test asserts
  the generator, and PG-WAKE-12's seal-once rule is asserted by re-driving an obligation and
  comparing bytes.
- **PG-TEST-3** Rollback: a wake at or below the persisted high-water is refused, and the high-water
  never regresses across a persist.
- **PG-TEST-4** Tag/sequence race: an unopenable envelope carrying a maximal `wake_seq` does **not**
  advance the coordinate (the step-3-before-4 property of PG-WAKE-13).
- **PG-TEST-5** Size pin: exactly one constant, 74, failing on movement, on both the producer
  (`TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize`) and the channel side, re-hosted
  against the gateway rather than the relay per ADR-015 P8.
- **PG-TEST-6** Key inventory: `PushConfig` still carries no content key
  (`TestPBPUSH0_PushConfigCarriesNoContentKey`, `internal/remotegw/push_trigger_test.go:478`), and
  `EpochID` is **gone** from it (ADR-015 P8 delta 2). Leaving the epoch stamp while re-keying the
  phone's coordinate is the specific half-migration forbidden: it ships a machine authenticating a
  field no receiver reads.
- **PG-TEST-7** Content-free schema: inspect the **raw FCM request** and assert one data key, no
  `notification` block, high priority, token not topic — against the gateway's sender after ADR-015
  P2's relocation.
- **PG-TEST-8** Obligation crash injection at all five boundaries (PG-OBL-8).
- **PG-TEST-9** Log and trace exclusion: a negative control asserting no token, capability, envelope,
  signature or attestation token appears in any emitted log line (§8.2).
- **PG-TEST-10** Idempotency: repeated registration under one `Idempotency-Key`, repeated revocation
  under both credential kinds, repeated byte-identical submit.

- **PG-TEST-11** Wake-length refusal: 73, 75 and 0-octet bodies, and a chunked body with no
  `Content-Length`, are each refused `400 wake_malformed` — never `413` — and no oversized body is
  buffered past 75 octets (PG-TR-3).

None of these have been written. They are R3's, and no row above may be cited as evidence.

---

## 12. Deployment

- **The hosting decision is deferred to the owner.** The deferral is an **out-of-band owner ruling of
  2026-08-14**, recorded in the session that launched this wave: hosting is proposed and decided
  during R1/R3 design. **No playbook line carries it and none is cited here** — the playbook's Wave R1
  section says nothing about where the gateway runs, and neither does ADR-015. This
  document fixes the contract, not the host. Nothing in §3 depends on a provider, region, or runtime,
  and the OpenAPI `servers` block carries `gateway.invalid` deliberately so that no implementer reads
  a placeholder as a decision.
- Whatever the host, it must satisfy the rulings already made elsewhere: Web-PKI TLS on a public name
  (PG-TR-1, ADR-015 P4), egress to FCM v1, a scheduled job for each of §8.1's three retention rows,
  §8.2's log exclusions in whatever logging pipeline it inherits, and §9's source-identity rule behind
  whatever proxy it sits behind.
- **The bound Firebase project is `swarm-8404f`; the Android app is `dev.swarm.phone`, registered
  2026-08-14; the sender/project number is `733314021126`; FCM v1 is enabled.** The client config sits
  at `android/app/google-services.json`, present locally and deliberately untracked
  (`.gitignore:60-62`: "Present locally so Wave R3 can apply the google-services plugin; stays out of
  the repo until R3 decides how config ships"). No production token has been collected and no Google
  Services plugin is applied.
- **Debug builds use a separate non-production gateway and a separate non-production Firebase
  project** (playbook `:561`, PG-AUTH-14), so no test handset can spend or poison production quota.
- Before the first real token is collected, playbook §14 requires published stored fields and
  retention, the FCM/Google metadata disclosure, deletion and revocation behaviour, availability
  status, abuse limits with an appeal path, and a credential-incident runbook. Those are documents
  this specification does not write and does not claim exist.

---

## 13. Open questions for the owner

1. **Installation key algorithm.** PG-AUTH-2 fixes ECDSA P-256/SHA-256 for hardware-backed Keystore
   support. Ed25519 is the cleaner primitive but its Keystore availability is narrower. Confirm
   P-256, or accept Ed25519 with a documented minimum Android version.
2. **The type byte and the canonical AAD.** PG-WAKE-10 keeps the canonical tuple exactly as both
   records state it and binds the type byte through domain separation and a pre-AEAD shape check,
   which makes ADR-015 P8's "every byte on the wire is AAD-covered" true in effect but not in form.
   The alternative is to add `type` as an eighth tuple element (AAD 78→79 bytes, wire unchanged at 74)
   and amend both records. Rule.
3. **The revocation tombstone: its row, and that row's wording.** PG-REV-2 needs a bounded tombstone
   to make machine-side revocation idempotent across an ADR-011 M5 process exit. Two questions, not
   one.
   *(a) Which row.* §8 places it inside the 7-day diagnostics row rather than adding a fourth
   retention row, because the mandate was to reproduce playbook §13's gateway rows exactly. Confirm,
   or authorize a fourth row.
   *(b) The wording collision.* That row reads "7 days, no payload/capability/token" (playbook
   `:980`, reproduced verbatim in §8.1), and the tombstone stores a hashed machine-revoke capability.
   PG-RET-3 argues a SHA-256 verifier is not a capability. If the owner reads the row's ban
   literally, the tombstone needs its own row regardless of (a), and the privacy notice playbook §14
   requires must describe it — this is flagged now precisely so it is ruled here rather than
   discovered while drafting that notice.
4. **Quota numbers and the pairing bound.** §9's table is proposed, not decided. In particular: the
   maximum number of live machine pairings per installation (ADR-018 MM3 has a connection cap; this is
   a different, larger number), and whether the per-address wake quota is the one a support case will
   most often hit.
5. **`expires_at` on the wire.** ADR-015 P8 derives it and pins 74 bytes; the playbook's field list
   says it is carried. This document follows ADR-015 (§5.2). If it should be carried, the pin is 82
   and both records change.
6. **Closed-beta admission mechanics.** Playbook `:559-560` requires a closed beta; nothing states how
   an installation is admitted without becoming an account. A single-use invite consumed at
   registration is the obvious shape, and it is one more secret to distribute and revoke. Rule before
   R3 builds registration.
7. **Delivery-health surface.** PG-TR-6 defines `GET /v1/health` as static and caller-agnostic.
   Playbook `:548` says the gateway "exposes delivery health"; if the owner wants per-installation
   delivery health, that is a sixth operation with its own authentication and its own disclosure, and
   it must be ruled rather than added.
8. **Does a wake obligation ever stop retrying before its expiry?** ADR-015 P9 and playbook
   `:552-555` have no terminal-refusal concept: every non-2xx is retryable and restart re-drives the
   byte-identical wake "until success or declared expiry". §6.3/§6.4 introduce `abandoned` for the
   decided refusals — every code the gateway marks `retryable=false`, which today is
   `410 address_revoked`, `410 push_token_unregistered`, `400 wake_malformed`,
   `400 malformed_request`, `401 unauthorized`, `404 version_unsupported` and `502 upstream_refused`,
   plus any `retryable=false` code a minor version adds under PG-ERR-1 — because retrying any of them
   spends quota to reproduce the identical answer and delays the repair path. Three ways to close
   this, and one of
   them has to be chosen before R3 writes the machine: **(a)** amend ADR-015 P9 to read "non-2xx
   responses the gateway marks `retryable=true`", making `abandoned` an inherited ruling; **(b)**
   strike `abandoned` and let them all retry to expiry, which costs at most one wake's worth of
   attempts per five minutes per address and keeps the record literal; **(c)** split it — terminal
   only on the two `410`s, where the address or token is provably gone, and retryable on the rest.
   §14.1 row 7 records the state of this until it is ruled. Note that no reading touches the mailbox
   event (PG-OBL-3) or the phone's replay defence; the disagreement is entirely about quota and about
   how quickly §6's degraded state appears.
9. **When does an allocation stop being "unbound"?** ADR-015 P6 says "an unbound allocation expires
   after ten minutes in every case — abandoned pairing, failed SAS, crashed app", and playbook `:979`
   gives the retention row, but no record says what ends the unbound condition. PG-ALLOC-2 rules that
   it is the first accepted submit-wake, and derives from that a product-visible obligation: **every**
   pairing, not only a migrating one, must land a successful gateway test wake within ten minutes or
   lose its address and both verifiers. The alternative reading of P6 — "unbound" meaning *not yet
   attached to a pairing*, which the three examples it lists all are — has a materially different
   support consequence: under PG-ALLOC-2 a successful pairing whose machine cannot reach the gateway
   or FCM for ten minutes silently loses its allocation and must re-allocate, which is a repair a
   user will experience as "pairing worked and push did not". §3.3 argues why the alternative reading
   leaves a fresh pairing with no qualifying event at all; the argument is sound but the definition is
   invented, so the owner rules whether binding is "first accepted wake" (as written) or "pairing
   acknowledged", and if the latter, what still sweeps the address of a pairing that never wakes.
   §14.2 A6 records the invention.

   **One interaction the owner should see before ruling.** PG-ALLOC-2's ten-minute unbound window and
   PG-WAKE-7's five-minute obligation expiry do not divide evenly, and PG-OBL-6's first clause does
   not close the gap: a binding test wake carries no coalesced trigger, so on expiry nothing re-drives
   it and the second half of the window passes with no further attempt. PG-OBL-6's second clause
   exists only for that: it re-mints an expired binding wake **once**, so the machine gets two
   attempts inside the window instead of one. That clause is a consequence of PG-ALLOC-2's invented
   definition, not an independent rule — if the owner rules binding to be "pairing acknowledged", the
   clause loses its subject and is struck with the rest of the obligation.

---

## 14. Recorded divergences and additions

Two kinds of entry. A **divergence** contradicts a record and must be resolved. An **addition** says
something no record says; it contradicts nothing, but it is listed anyway, because the failure mode
this section exists to prevent is a later reader mistaking a spec-level choice for an inherited
ruling and defending it as one.

### 14.1 Divergences

| # | This document | The record it diverges from | Resolution |
|---|---|---|---|
| 1 | `expires_at` derived, not carried; 74 bytes | Playbook `:535-536` lists it among carried fields | ADR-015 P8 already recorded this deviation and wins; §5.2 restates it rather than inheriting it silently |
| 2 | The `type` byte is bound through domain separation and the pre-AEAD shape check, not by literal inclusion in the AAD tuple | ADR-015 P8: "Every byte on the wire is AAD-covered" | The canonical tuple is normative in both records and is not widened here; §13.2 puts the alternative to the owner |
| 3 | `type = 0x03`, `version = 0x01` fixed as constants | ADR-015 P8 requires "a new value, distinct from 0x01 and 0x02" without naming it | This document fixes the value, as a spec is entitled to; if R3 finds a reason to differ it amends here, not in the ADR |
| 4 | ECDSA P-256 fixed as the installation key algorithm | Playbook `:131-133` and ADR-015 P5 say "Keystore-generated" without a curve | Spec-level choice, §13.1 |
| 5 | The revocation tombstone sits under the 7-day diagnostics row | Playbook §13 has three gateway rows and no tombstone row | §13.3 |
| 6 | §9's quota numbers | Playbook `:526` requires per-address and per-source quotas without numbers | Proposed defaults, explicitly labelled, §13.4 |
| 7 | **§6.3/§6.4's terminal `abandoned` state**: every refusal the gateway marks `retryable=false` ends the obligation and retry stops — today `410 address_revoked`, `410 push_token_unregistered`, `400 wake_malformed`, `400 malformed_request`, `401 unauthorized`, `404 version_unsupported` and `502 upstream_refused`, plus any `retryable=false` code PG-ERR-1 lets a minor version add, which §6.4's residual row covers without re-listing | ADR-015 P9, unqualified: "timeout, transport failure, non-2xx and non-final FCM responses leave the obligation retryable"; playbook `:552-555`, same sentence, plus "Restart retries the byte-identical wake until success or declared expiry". **Neither record has a terminal-refusal concept**, and under their literal text every one of these is a non-2xx response that stays retryable to expiry | **Open — this is the one unresolved divergence in this document.** §13.8 puts three closings to the owner (amend P9, strike `abandoned`, or split at the `410`s). Until it is ruled the header Precedence clause governs and ADR-015 P9's literal reading binds an implementer; §6.3 states that in place. Resolving it requires an ADR-015 amendment, not an edit here |

### 14.2 Additions — not divergences

| # | This document adds | What the record actually says | Why it is additive |
|---|---|---|---|
| A1 | The `swarm-pg-v1` domain-separation prefix inside the signed canonical string (PG-AUTH-1) | Playbook `:529-530` specifies a signature "over method, path, body hash, nonce, and expiry" — five components, no prefix | Strictly more binding, not different binding: all five named components remain, in order. The prefix stops a signature minted for this API being replayed against any other Swarm surface that ever signs the same five fields |
| A2 | `GET /v1/health` (PG-TR-6) | ADR-015 P4 and playbook `:514` define **five** logical operations; `:548` requires the gateway to expose delivery health without saying how | A sixth *route*, not a sixth *operation*: unauthenticated, static, caller-agnostic, carrying no installation- or address-scoped datum. §13.7 asks the owner whether per-installation health is wanted, which **would** be a sixth operation |
| A3 | PG-OBL-5: at most one outstanding obligation per `push_address` | Playbook `:550-556` requires the obligation be durable and coalescible, without bounding the outstanding set | Strictly stronger than "coalescible", and sound only because the wake carries no locator. It is what makes §9's per-address quota numbers derivable rather than guessed |
| A4 | PG-AUTH-3's 120-second expiry horizon, and PG-AUTH-4's nonce cache sized to match | The signed `expiry` is required by playbook `:530`; no record gives it a maximum | Without a maximum, a signed request is replayable until its own `expiry`, which the client chooses — so the bound has to exist somewhere, and 120 s is a spec-level choice trading clock skew against replay window. `server_time` on `request_expired` is what makes it recoverable |
| A5 | PG-SUB-5: the gateway SHALL NOT parse, store, or index `wake_seq` **at all** | Playbook `:351` and ADR-015 P11 describe what the gateway observes; no record forbids it to read a field it physically holds | A **discipline**, not a capability claim. It removes the gateway's reason to look at the counter (PG-SUB-4's whole-body hash leaves none) and makes "did not retain it" auditable in an honest implementation. It binds nothing about a compromised one, which is why §7.1's note and §7.3's first bullet state that the gateway holds all 74 cleartext octets and can read `wake_seq` and `issued_at` regardless. Listed here so PG-SUB-5 is never cited as a security boundary |
| A6 | PG-ALLOC-2: an allocation is **bound** by its first accepted submit-wake, so **every** pairing — not only a migrating one — must land a successful gateway test wake inside ten minutes | ADR-015 P6 and playbook `:979` fix the ten-minute sweep of an *unbound* allocation and enumerate the cases (abandoned pairing, failed SAS, crashed app); **no record defines when an allocation ceases to be unbound**. Playbook `:955-958` requires the test wake only as a `legacy_relay` migration precondition | The definition is invented here, and unlike the other additions it has a **support-visible consequence**: a successful pairing whose machine cannot reach the gateway for ten minutes loses its address and both verifiers. §3.3 gives the argument (a fresh pairing otherwise emits no qualifying event and push dies silently while the pairing believes it succeeded); §13.9 asks the owner to rule the definition rather than let it arrive as an inherited one |

## 15. What is unrun

Everything. There is no gateway, no OpenAPI document generated from this text, no fixture, no test,
no production token, and no wake that has ever left this repository toward Google. This specification
is a contract for R3 to build against and for R3's evidence to be measured by; it is not evidence of
anything, and no row in §11 may be cited as a passing gate.
