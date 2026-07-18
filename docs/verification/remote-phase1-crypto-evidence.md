# Remote Phase 1 — Crypto Foundation — Evidence

**Slice**: `internal/remote/crypto` (agents-tracker-v40), the Phase-1 critical-path cryptographic
foundation every other remote component depends on. Implements R-CRY.1-.16 + R-PAIR.4 (SAS) per
ADR-007 D2/D3/D4 and plan amendments D.0-A5/A10/A14/A15.

## What it delivers

- Machine + device identity: two X25519 keys per party (Noise-static + sealed-box recipient, A14),
  a device Ed25519 command-signing key (R-CRY.16 / D4), an Ed25519 relay-auth key (relay never sees
  the X25519 identity). `KeyStore` interface exposes DH / sealed-box-open / Noise-static handle / sign
  but never raw private-key export (no shared-secret oracle, ADR-007 D2).
- Noise XX live transport (`Noise_XX_25519_ChaChaPoly_SHA256`): fail-closed static pinning (constant-
  time; a live session requires a 32-byte pin, unpinned is mechanically pairing-only via a required
  PSK), route/role-bound length-prefixed prologue, fresh handshake per connection (no resumption),
  enforced rekey (Encrypt refuses past the byte/time threshold until a coordinated Rekey()).
- Async epoch envelopes: byte-exact 62-byte header + XChaCha20-Poly1305, `recipient_key_id` OUTSIDE
  the AAD (identical-ciphertext fan-out, A5), authenticated `issued_at`, per-epoch authenticated seq
  with replay/reorder/gap detection and a `SeedHighWater` snapshot-cursor seed. Two epoch keys per
  epoch — a wake key (NSE-readable, type-0x02 only) and a content key (biometric-gated, type-0x01
  only), non-interchangeable typed keys (A15/F10). EpochGrant is sealed to the recipient key with the
  coordinates authenticated inside + an Ed25519 machine signature + a restart-seedable per-device
  replay tracker.
- SAS: HKDF-SHA256 over the Noise channel binding into a fixed 64-emoji table (mirror byte-identical
  in Swift). Device command signatures over a length-prefixed domain-separated canonical tuple.

## TDD evidence (GG-5)

Failing-first RED captured in `remote-phase1-red/crypto-red.txt` (40 tests, 168 undefined-symbol
errors) and `remote-phase1-red/crypto-fix-red.txt` (remediation fix-wave RED). Commits:
`ed4e411` RED tests -> `47b879f` GREEN impl (44/44) -> remediation `444c38c` (67/67) -> round 2
`1d93e0a` (70/70) -> `a6b392a` F10 close (71/71). All under `go test -race`.

## Committee (cross-model, security-critical per R-TDD.5)

Independent role separation: test-writer (opus), implementer (opus), reviewer (opus) + codex cross-
model — all distinct instances. The green 44-test implementation was REJECTED by codex+opus review
(optional Noise auth, private-scalar leak via fmt, forgeable epoch grants, un-seedable replay, non-
enforced rekey, ...) — the audit catching what green tests missed. Two remediation rounds (RED-first
per finding) closed all 14 findings F1-F14; the orchestrator finished them inline when the subagent
hit the account session limit, including fixing an introduced pin-timing bug that had been aborting
every valid handshake. codex final verdict: **YES — sound enough to build pairing/relay/gateway on,
no critical/high issue remaining, no new defect introduced.** One rejected finding on cross-examination:
`box.SealAnonymous` exists in x/crypto (no hand-rolled primitive needed).

Reviews: `docs/verification/audit-003-remote-control-plan.md` (plan), plus the crypto review transcripts
under the session job dir. Honesty carried forward: the 4 derive-and-pin KATs are self-generated
regression pins; cross-language (Swift/CryptoKit/libsodium) interop is an explicit ON-DEVICE RELEASE GATE.

## Quality gates (GG-4)

`go build ./...`, `go vet ./internal/remote/crypto/...`, `gofmt -l` (clean), `go mod tidy`, and
`go test -race -count=1 ./internal/remote/crypto/...` (71/71) all green. Deps added: `flynn/noise
v1.1.0`, `golang.org/x/crypto v0.48.0` (pinned to keep the module at go 1.24.2). golangci-lint not on
this PATH; the implementer's earlier run reported it clean.

## Integration notes / follow-ups (for the slices that build on this)

- The gateway/phone MUST durably persist and reseed the mailbox `SeedHighWater` cursor and the
  `NewGrantReceiverAt` (epoch, grant_seq) high-water; using the un-seeded constructors after restart
  forfeits replay protection (by design).
- Rekey time-threshold enforcement is send-side refusal (`ErrRekeyRequired`); the transport layer must
  perform the coordinated `Rekey()` on both ends.
- On-device cross-language KAT verification (Swift/CryptoKit/libsodium interop of the SAS table, the
  sealed-box vector, the envelope/channel-binding KATs) is a pre-ship release gate.
