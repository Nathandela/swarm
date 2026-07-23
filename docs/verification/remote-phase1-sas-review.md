# SAS Widening (item 8) — Cross-Model Review Evidence

**Slice**: `crypto.SAS` widened 24 -> 36 bits (four -> six emoji), commit `47dcb73`,
under ADR-007 amendment 2026-07-23. Closes review finding MED-1 (pairing grind).
This edits the FROZEN crypto layer, so per the plan it required cross-model review
(codex + independent opus) before being trusted.

## Panel and verdicts

- **Independent opus** (adversarial crypto review): CORRECT, closes the grind with
  margin, KAT hand-recomputed and matches, SAFE TO TRUST. One LOW doc nit (fixed).
- **codex (GPT-5.6 sol)**: derivation CORRECT and uniform (36 real bits, KAT passes),
  but raised a concern that the grind is not closed end-to-end because (a) the
  rendezvous window is unbounded after claim and (b) per-trial cost is cheaper than a
  full handshake.
- **Lead code check** (this reviewer): verified the relay code directly to adjudicate
  the disagreement.

## Adjudication — the widening DOES close MED-1 for the stated threat model

The two models disagreed; the disagreement was resolved in favor of opus by reading
the relay code:

1. **codex's "unbounded window" premise is factually wrong.** The relay enforces a
   hard 60-second rendezvous TTL (`internal/remote/relay/config.go:84`
   `RendezvousTTL: 60*time.Second`, checked at `server.go:975`
   `now.Sub(slot.createdAt) >= RendezvousTTL -> rendezvous_expired`), plus single-use
   burn on complete and a `RateLimiter` on both the machine and device pairing sides
   (R-PAIR.8) and the relay's own per-source limit. The pairing window is bounded and
   rate-limited, not open-ended.
2. **opus found the load-bearing defense codex missed: no birthday shortcut.** Role
   assignment is fixed (device = initiator, machine = responder). A MITM is therefore
   RESPONDER toward the phone (one-shot — it commits its ephemeral in msg2 before the
   phone's single msg3, so it gets exactly one phone-leg SAS) and INITIATOR toward the
   machine (offline-grindable). A birthday attack to 2^18 needs BOTH legs grindable;
   only one is. The attack is therefore a 2^36 PREIMAGE against the fixed phone-leg
   SAS, not a 2^18 collision.
3. **Per-trial cost includes an X25519 DH.** Grinding the machine leg varies the
   attacker's own static and recomputes `se = DH(s_att, e_machine)` per candidate — an
   X25519 scalar-mult (~tens of microseconds), which dominates. 2^36 such trials is
   days-to-weeks on commodity hardware, far beyond a 60-second rate-limited window;
   grind does not amortize across sessions (fresh ephemerals per pairing).
4. **KAT verified by hand-recompute.** HKDF-SHA256(salt="swarm-remote/1 sas",
   ikm=0x00..0x1f) first 5 bytes = `2c f0 d2 e8 ea` -> indices [11,15,3,18,58,14] ->
   `🐷 🐧 🐰 🦉 🔨 🐔`, byte-identical to the pinned `wantSAS`.

## Conditional-safety note (important for the record)

The 36-bit SAS is sufficient *because* the pairing window is bounded (60s TTL) and
rate-limited, and *because* the responder/initiator asymmetry blocks a birthday
shortcut. If either assumption is ever relaxed — a longer/unbounded rendezvous
lifetime, removal of the pairing rate limiters, or a handshake change that makes both
legs offline-grindable — the width alone becomes insufficient and the
**ephemeral pre-commitment** (which the ADR keeps as documented future hardening, and
which gives grind-IMMUNITY at any width) must be added. codex's concern is the correct
concern to hold *if the window guarantees change*; it does not apply to the code as it
stands.

## Release gate (on-device, not verifiable here)

The Swift/Android SAS MUST mirror byte-for-byte: salt `"swarm-remote/1 sas"` (with the
space and "/1"), empty info, a 5-byte HKDF read, the same big-endian 40-bit assembly +
shift pattern, and the identical 64-emoji table. The KAT `binding[i]=i -> 🐷🐧🐰🦉🔨🐔`
is the interop check. The Noise ChannelBinding itself must also be byte-identical
cross-platform (prerequisite, separate from this diff).

## Actions taken

- ADR-007 line 37 summary corrected ("4-emoji" -> "6-emoji"), the LOW nit opus flagged.
- Verdict: item 8 GREEN is TRUSTED for the stated threat model, with the conditional
  above recorded.
