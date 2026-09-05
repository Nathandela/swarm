# Remote relay v2 protocol

Status: implementation contract for the first bounded Worker + Durable Object slice. It is a clean replacement: version 1 framing, hello/wait/poll and HMAC routing tickets are not accepted.

## Routing and admission

The normal endpoint is `wss://<relay>/v2/ws?machine_rid=<rid>`. `rid` is 32 lowercase hex characters. Before dispatching a Durable Object, the Worker requires the RID in the operator-configured `ALLOWED_MACHINE_RIDS`; missing, empty or malformed configuration fails closed. The allowlist bounds actor creation and is admission policy, not authentication.

The pairing endpoint is `wss://<relay>/v2/pair?ceremony=<id>`, where `id` is the QR's existing 16-byte rendezvous ID rendered as 32 lowercase hex characters. An authenticated machine first registers the ID through `PAIR_CREATE`. A singleton, bounded, short-lived rendezvous directory resolves it to an allowed machine RID before home dispatch. Resolution does not write state. The QR therefore does not gain a machine RID: it remains within the existing 80x24/version-6 budget, whose relay URL field is at most 39 bytes. A deployment may need a short custom hostname; a long `workers.dev` URL is not silently truncated.

Both endpoints require a WebSocket upgrade before the native pre-authentication limiter or either Durable Object is accessed. A valid admitted `/v2/ws` request consumes the constant `ws` key; a syntactically valid `/v2/pair` request consumes the constant `pair` key before directory resolution. Missing or failed limiter access returns 503 and denial returns 429 with `Retry-After: 60`. Attacker-selected query values never become limiter keys.

The canonical home name is lowercase hex of:

```text
SHA256(
  u32be(19) || UTF8("swarm-relay-home/v1") ||
  u32be(len(namespace_utf8)) || namespace_utf8 ||
  u32be(32) || UTF8(lowercase_machine_rid)
)
```

Only operator configuration supplies `namespace`; it is one 1–64-byte ASCII token matching `[a-z][a-z0-9-]{0,63}`. The final RID field is exactly 32 ASCII bytes, not the 16 decoded bytes. A missing machine identity creates a new RID and therefore a new home; it never inherits old consent.

## Encoding

Each WebSocket text message contains exactly one UTF-8 JSON object and is at most 1,048,576 encoded bytes before parsing. Binary messages, unknown fields, unknown types and any `v` other than `2` are rejected. There is no legacy negotiation. A future extension either adds a new explicitly specified message type whose old-server rejection is safe, or increments `v`; fields are never added with ambiguous ignore semantics.

Every message contains `v:2`, `type`, and `request_id` (`[A-Za-z0-9_-]{1,64}`). Replies copy `request_id`; delivery events use a server-generated value. Bytes are canonical unpadded base64url. RIDs and ceremonies are lowercase hex.

All uint64 fields (`generation`, `cursor`, `after`, and millisecond timestamps) are canonical unpadded decimal strings matching `0|[1-9][0-9]{0,19}` and not exceeding `18446744073709551615`. JSON numbers, signs, fractions, exponents, leading zeroes and overflow are errors. JavaScript uses `BigInt`; SQLite uses zero-padded 20-digit `TEXT` for order. Exhaustion returns `generation_exhausted` or `cursor_exhausted`; it never wraps.

Errors are `{"v":2,"type":"ERROR","request_id":"...","code":"..."}`. Callers must branch on `code`, not text.

## Relay authentication

RID derivation is unchanged from the reviewed Go primitive: HKDF-SHA256 with input key material equal to the 32-byte Ed25519 public key, salt `swarm-relay-routing-id-v1`, info `routing-id`, first 16 output bytes rendered as lowercase hex.

Authentication is:

```text
C -> S  AUTH_INIT   {role:"machine"|"phone", purpose:"control"|"stream", pub}
S -> C  CHALLENGE   {nonce, home, expires_at}
C -> S  AUTH_PROVE  {signature}
S -> C  AUTHENTICATED {rid, role, purpose, home, generation?}
```

The signature is Ed25519 over:

```text
UTF8("swarm-relay-auth-v2\0") ||
u32be(32) || nonce ||
u32be(32) || UTF8(rid) ||
u32be(64) || UTF8(home_id) ||
u32be(len(role)) || UTF8(role) ||
u32be(len(purpose)) || UTF8(purpose)
```

Challenges contain 32 random bytes, expire after 30 seconds, and are consumed before asynchronous verification so no proof can race or reuse one. The home alarm closes silent pre-authentication sockets at that deadline; a home admits at most 64 total sockets. A machine proof is accepted only when its derived RID equals the admitted route RID. This proof is the only operation allowed to initialize home schema/authority. A phone proof is accepted only for the exact public key of a live member. The role, purpose and home signature bindings prevent challenge reflection and context reuse.

Machine `control` and `stream` are independent connection purposes. Pairing and `AUTHORIZE` require `control`; mailbox operations require `stream`. A newer socket supersedes only the same `(RID,purpose)`, so a short-lived CLI pairing/revoke connection cannot evict the running gateway stream. Phones use only `stream`.

During pairing, the existing Noise XXpsk0/SAS transcript authenticates the machine endpoint profile (relay URL/TLS policy and pin) before the phone releases consent. Subsequent phone connections use that protected profile. The relay never receives the QR secret or Noise plaintext.

The native Go client applies the existing `relay.Security` TLS policy before dialing; routable plaintext is refused and loopback plaintext requires the existing test-only opt-in. Every dial and request has a ten-second client-side ceiling even when its caller supplies `context.Background()`, at most 64 requests may await replies, and the write gate is context-aware. Its only reader never blocks on consumers: delivery/pairing queues have frame and one-MiB aggregate byte bounds, and overflow, malformed/duplicate/unknown response fields, non-canonical values or an unsolicited response close the underlying socket so reconnect owns recovery. TLS dials retain the peer SPKI digest for the existing protected-profile comparison after Noise msg2.

## Pairing and membership

An authenticated machine may hold at most eight live 60-second ceremonies:

```text
PAIR_CREATE {ceremony} -> PAIR_CREATED {ceremony, expires_at}
PAIR_CLAIM  {ceremony} -> PAIR_CLAIMED {ceremony}
PAIR_SEND   {ceremony, ciphertext} -> peer PAIR_FRAME; sender PAIR_SENT
PAIR_FINISH {ceremony} -> PAIR_FINISHED
```

Only a live, pre-created directory entry reaches a home. Only one phone claims it. A duplicate create cannot reset or extend a slot, a different machine cannot overwrite a live directory mapping, and finish is machine-RID-bound. Each side may forward at most eight frames and 256 KiB total per ceremony. Frames are opaque Noise handshake/transport bytes, limited to 256 KiB each, and require the other inbound socket to be present. No Durable Object owns an outbound socket or timer.

After both people accept the SAS and the phone durably commits, the machine sends:

```text
AUTHORIZE {phone_pub, consent}
  -> AUTHORIZED {phone_rid, generation}
```

`consent` is the existing binary credential encoded as base64url: `u32be(ceremony_utf8_len) || ceremony_utf8 || ed25519_signature`. The signature verification message remains byte-for-byte `ConsentMessage(ceremony, machineRID)`:

```text
UTF8("swarm-relay-consent-v1\0") ||
u32be(len(ceremony_utf8)) || ceremony_utf8 || UTF8(machineRID)
```

The canonical machine home makes that grantee RID select exactly one home in an operator namespace. A fresh ceremony supersedes the old one, increments the binding generation, retires the prior ceremony and purges prior-generation relay data atomically. Replaying a retired consent returns `consent_retired`. Retirements are capped at 64 per phone/home; at the cap new authorization fails closed. Revocation itself is never refused by that cap.

## Mailbox subscription

One binding has two independent streams, machine-to-phone and phone-to-machine. The authenticated sender cannot supply a target role: `peer_rid` plus its proven role determines sender and recipient.

```text
APPEND {peer_rid, generation, msg_id, ciphertext}
  -> APPENDED {peer_rid, generation, cursor, deduped}

SUBSCRIBE {peer_rid, generation, incarnation, after}
  -> SUBSCRIBED {peer_rid, generation, incarnation, after}
  -> zero or more DELIVER {peer_rid, generation, incarnation, cursor, msg_id, ciphertext}

ACK {peer_rid, generation, incarnation, cursor}
  -> ACKED {peer_rid, generation, incarnation, cursor}

DISCARD {peer_rid, generation, incarnation}
  -> DISCARDED {peer_rid, generation, incarnation, cursor}

REVOKE {peer_rid, generation}
  -> REVOKED {peer_rid}
```

`msg_id` is `[A-Za-z0-9_-]{1,64}`. Ciphertext is an opaque canonical base64url envelope. An append transaction rechecks live membership/generation after digest calculation, then checks quota/cursor space, inserts the item and receipt, advances high-water, and only then replies or attempts live delivery. The receipt key is authenticated stream + generation + `msg_id`; the stored SHA-256 of decoded ciphertext makes same-ID/same-body return the original cursor and same-ID/different-body return `id_conflict` while the receipt is retained.

Receipts are kept for every unacknowledged retained item plus the most recent 256 acknowledged items, subject to the same seven-day ceiling. The acknowledged window slides instead of refusing healthy throughput. A retry older than that window may be appended again. This is intentionally bounded transport retry suppression, not exactly-once delivery; endpoint semantic operation IDs and raw-input uncertainty rules remain authoritative. The Go vertical slice must show that a delayed retry beyond this window creates no duplicate semantic effect before this policy is called integrated.

`incarnation` is a random 16-byte base64url value created with a stream. The first subscription uses empty incarnation with `after:"0"` and receives the current value. When that blank checkpoint is below durable ACK, `SUBSCRIBED` still echoes `after:"0"`, but delivery starts above the durable ACK; an explicit incarnation checkpoint below ACK is refused. Later subscribe/ACK/discard messages must match the current value. `after` may otherwise be between the durable ACK and stream high-water inclusive, supporting a reconnect where endpoint progress was durable but its ACK was lost. ACK is monotonic, generation/stream/incarnation-bound, and cannot exceed that socket's eligible/sent high-water. Endpoint implementations must durably commit local progress before sending ACK. Missing ACK causes replay, never accepted-work loss.

`DISCARD` is the explicit destructive-mailbox recovery operation. It is available only to the authenticated recipient, atomically advances ACK to current high-water, rotates incarnation and makes old cursors/ACKs unusable. The latest pre-rotation incarnation and discard cursor are retained only to replay an exact lost response: a live binding retry with that one prior incarnation returns the current incarnation and original cursor without another mutation; all other stale incarnations fail. Physical item deletion remains batched. Clients invoke it only after a user-legible recovery decision; it is not an automatic decrypt-failure fallback.

One subscription is allowed per socket; a second `SUBSCRIBE` returns `already_subscribed` instead of resetting the delivery accounting. At most 64 delivery frames and 1 MiB estimated bytes are in flight; more data remains durable until ACK opens the window. Slow readers therefore do not create an application queue. Delivery queries use the `(recipient,sender,generation,cursor)` index and stream their cursor until the byte limit rather than materializing 64 maximum-size rows.

Revocation by the machine or the phone itself atomically marks the member revoked, advances generation when possible, retires live consent, and purges both streams and receipts. It then closes the phone and binding-subscribed sockets. Every later handler rechecks durable membership and generation, so queued events and stale sockets cannot revive access.

## Bounds, expiry and lifecycle

- At most 32 phone members per home and 10,000 unacknowledged retained items per direction/generation. Transactional per-stream item counters avoid an O(backlog) `COUNT`/`SUM` scan on every append. A proposed deployment safety ceiling additionally limits estimated retained item bytes across the home to 8 GiB, leaving headroom below the platform's 10 GB SQLite object limit. This is not a claim of capacity equivalence with the old relay: it allows 10,000 items only below roughly 839 KiB average stored size, and actual gateway frame-size distribution still needs measurement before production cutover. Hitting either bound returns an error; it never drops accepted data.
- Seven-day server-selected item and receipt retention; callers cannot choose TTL.
- Logical expiry is filtered on reads. At-use cleanup removes an expired matching message ID before receipt lookup, so a stale receipt outside a cleanup batch cannot suppress a retry and its stale item cannot collide in the unique index. Each alarm deletes at most 256 expired items plus 256 acknowledged items, and at most 256 expired receipts plus 256 excess acknowledged receipts; rendezvous deletion is capped at 256. It then reschedules the earliest remaining expiry. On quota pressure, one bounded global expiry batch is reclaimed; if more expired physical rows remain the append returns `cleanup_pending` and schedules another batch rather than scanning the whole backlog or misreporting permanent `mailbox_full`.
- Rendezvous directory: 1,024 live entries globally; resolve is read-only; machine homes permit eight each. Cleanup examines the entire bounded directory before selecting the next alarm.
- One socket accepts at most 600 authenticated operations per wall-clock minute. This is a conservative initial abuse ceiling, not a measured active-stream budget; tune it only after the owner vertical-slice traffic experiment.
- Before Durable Object dispatch, the native Workers Rate Limiting binding initially allows 60 `ws` and 60 `pair` route calls per 60 seconds per Cloudflare location. Its counters are permissive and eventually consistent, not global or an exact accounting/cost control. The separate constant keys stop one route from consuming the other, but an attacker who knows an admitted public machine RID can exhaust a location's `ws` bucket and temporarily deny its owner. The value is initial tuning, not an active-phone capacity claim.
- No polling/wait messages, application timers, persisted heartbeats or actor-owned outgoing sockets. The implementation uses hibernation WebSocket attachments and storage alarms only.
- Presence is deliberately omitted from this foundation rather than represented by a false durable heartbeat. Add event-driven online/disconnected/unknown only with a consumer and tests.

The local SQLite cursor cost probe runs 100 sequential append → deliver → ACK cycles against a growing acknowledged-receipt history. It reports 3,000 rows read, 1,600 rows written and 2,900 SQL statements total. More importantly, the first and hundredth operations are flat: both appends read 11/write 11 rows, and both ACKs read 19/write 5 rows. The earlier offset-scan design measured 18,650 rows read for the same probe and was removed. These workerd cursor counters support the absence of backlog-proportional reads on the healthy append/ACK path; they are not hosted billing or latency evidence.

The alarm probe compares one retained item in one stream with 300 retained items spread across the maximum 64 live directional streams. The respective workerd counters are 14 and 203 rows read, zero rows written, and nine statements in both cases. Cleanup iterates at most the bounded stream set and uses indexed cursor ranges; each table computes its own indexed minimum expiry before JavaScript selects the earliest. The counters establish a stream-count bound rather than an O(1) claim, and the test separately requires an earlier rendezvous expiry to remain the exact next deadline.

## Public vectors

Seed bytes `00..1f` produce:

```text
machine_pub = A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg
machine_rid = 88564c8ede170d2ed321e21e61354184
namespace   = local-test
home        = cc634f54c634813fc554848c78763e63b3dbdff50975c0d789de730e5570beaa
nonce       = AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8
role/purpose = machine/control
auth_sig    = OiTc45XPlww4GfLjrjS4vc0kq_t2FhKn_sx921soEFkPBVetoIiZtIffpncEuJN9qez7Rq-gM1UgooB86ztBCA
```

The full auth message in base64url is:

```text
c3dhcm0tcmVsYXktYXV0aC12MgAAAAAgAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8AAAAgODg1NjRjOGVkZTE3MGQyZWQzMjFlMjFlNjEzNTQxODQAAABAY2M2MzRmNTRjNjM0ODEzZmM1NTQ4NDhjNzg3NjNlNjNiM2RiZGZmNTA5NzVjMGQ3ODlkZTczMGU1NTcwYmVhYQAAAAdtYWNoaW5lAAAAB2NvbnRyb2w
```

Phone seed bytes `20..3f`, ceremony `11111111111111111111111111111111`:

```text
phone_pub          = Kay64UG8yvCyLhqU000LxzYeUm0L_hLIl5S8kyKWbdc
phone_rid          = 6019466df50bcada1f8bcd23f7a9e4ee
consent_credential = AAAAIDExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExnV_7guYGTRlpw7afUj-IZOxBbxDEiii7AQqM2rWoP2tsezLJ9bUBmVxx1Jz21XyOOmrbn9uXUsdWkNinXKOCAA
```

These are public fixtures, never production credentials.

## Evidence boundary

`services/relay/test/protocol.mjs` runs against Wrangler 4.129.0's actual local workerd and SQLite Durable Objects. Test-only variables are passed on the command line; `wrangler.toml` contains no public-fixture admission or shortened production lifetimes. A separate fresh-workerd control consumes the configured 60 `pair` calls and observes the next call's 429; this proves the local native binding path, not hosted counter accuracy or global enforcement. The suite covers Worker pre-dispatch admission, KDF vector, Ed25519 challenge freshness/role/home/purpose binding, silent-socket reclamation, consent, control/stream coexistence, mailbox commit-before-delivery, canonical-base64/dedupe conflict, uint64 rejection, sent-high ACK and incarnation/discard fences, transient pairing routing, duplicate-create refusal, revoke socket closure and old-consent resurrection refusal.

The same bounded run executes `internal/remote/relayv2` against that Worker. It completes the existing Noise XXpsk0/SAS ceremony through the native transient pairing transport, authorizes the resulting consent, exchanges existing XChaCha mailbox envelopes in both directions, authenticates/decrypts in memory before ACK, resumes an offline delivery from an incarnation/cursor checkpoint, rejects an older-than-256 transport retry at the endpoint sequence fence without incrementing the semantic-effect count, and observes revocation closing the phone stream. A second short-retention workerd run disables alarms only for the test and proves a matching expired item/receipt beyond a 256-row cleanup batch cannot suppress its retry.

Local workerd still cannot prove durable endpoint checkpoint-before-ACK, production eviction/attachment restoration, billing, edge latency/capacity, hosted counter accuracy, recovery from a real backup, actual frame-size distribution, or the full command operation-ID/uncertain-input behavior of production callers. Pairing now authenticates and durably commits the namespace beside the machine relay-auth public key, but the production phone reconnect path does not yet construct its `relayv2.Profile` from those durable inputs. That caller wiring and the hosted gates remain incomplete; this document does not label them complete. There is one live environment, not a standing staging deployment; its admission remains closed until those active-use gates pass.
