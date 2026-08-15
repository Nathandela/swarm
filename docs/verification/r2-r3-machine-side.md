# Waves R2 and R3 (machine/service side): evidence

**Beads:** `agents-tracker-hggx.3` (R2), `agents-tracker-hggx.4` (R3) | Orchestrated 2026-08-15.
RED evidence under docs/verification/r2-red/ and r3-red/.

## R2 — relay productization (code-complete; physical exit = owner bead)

- **Bundle:** multi-stage static OCI Dockerfile + Compose with the Caddy topology, named
  volumes, healthcheck against a loopback-only admin /healthz + /readyz (bbolt writability,
  listener, disk floor), resource limits, log rotation, generated 0600 operator secret
  (reuse path refuses wider modes). Ruling recorded in the default test: the admin surface is
  OFF until configured (a fixed default port collided every parallel test relay; ":0" is an
  undiscoverable trap for file-based operators); the shipped example carries the port.
- **Doctor:** operator-minted HMAC capability (TTL 5 min, single-use, identity-bound),
  ephemeral route, encrypted round-trip, six actionable steps. Adversarially fenced: no new
  unauthenticated surface; diag routes cannot read real mailboxes; capabilities are
  route-scoped and non-transferable. Reviewer ran its own hostile probes.
- **Backup/restore:** bbolt Tx.WriteTo hot snapshot with fsync discipline, four-stage restore
  validation, refuses a live relay; flags-before-subcommand silent-serve bug found and fixed.
- **Trusted proxy:** rightmost-untrusted XFF behind the Compose loopback CIDR, per-source
  quota isolation (spoof + isolation tests), textual IP variants canonicalized.
- **Web-PKI migration (ADR-016 W1-W9):** policy/pin separated end to end; first real
  RemoteProfileV1 publisher; pin consulted iff pinned_spki; RelayTrust reverse-bound seam with
  Go-side independent hostname+validity enforcement (a lying platform verifier cannot admit a
  peer alone; unset fails closed); migration ladder moves only on authenticated advertisement
  plus live validation, failure retains the pin visibly. Kotlin impl on
  X509TrustManagerExtensions with JVM tests. Committed with its final adversarial verdict as a
  post-commit audit (prior rounds green; main was red on a half-committed closure) — any
  findings land as follow-ups referenced from the R2 bead.

## R3 machine/service side (Android + pairing conveyance + physical exits remain)

- **Push gateway service** (cmd/swarm-pushgw + internal/pushgw) implementing
  push-gateway-api.md: five operations, installation-key request signatures (replay/expiry/
  low-s), capabilities hashed at rest and absent from logs, WakeV1 admission fixed at 74 bytes
  forwarded byte-identical, AEAD-encrypted FCM tokens, retention sweeps with the 10-minute
  unbound deadline ALSO enforced at use (the review's probe-proven escape), atomic
  tombstone+delete revocation, per-address/per-source quotas. Security-lane reviewed twice;
  the punch-list verifier mutation-tested the load-bearing guard.
- **Wake obligations on swarm-remote:** durable (push_address, wake_seq) obligations fsynced
  before the mailbox publication they announce; coalescing without sequence mint;
  byte-identical retry until success or expiry; crash injection at the five playbook
  boundaries; TransportRouter selects exactly one of legacy_relay/gateway/foreground_only,
  nil-guarded, legacy path unchanged and FUNNEL-enumerated in the PBPUSH3 fence.
- **Accepted residuals, filed:** retry driver beyond redial (hggx.4.3, with the P7 wake-key
  custody note), deferred-wake PG-OBL-2 window (hggx.4.4), PG-OBL-10 health wiring (hggx.4.5),
  pairing conveyance of address/capabilities/wake key (the R3 Android slice).
