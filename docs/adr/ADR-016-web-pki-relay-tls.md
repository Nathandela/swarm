# ADR-016: Web PKI is the default relay TLS policy — the pin becomes an expert policy with an authenticated rotation

**Status**: Proposed (drafted 2026-08-14 from the owner-approved playbook; pending owner sign-off)
**Date**: 2026-08-14
**Locks**: RC-D10 (`docs/specifications/remote-control-product-playbook.md:80`) and its §3.3 mandate (`:151-163`); delivered in wave R2 (`:716`).
**Amends**: `ADR-007-remote-access.md` B33 (`:1858-1879`, "The relay pin is over the SPKI, and key reuse is part of the pin"), B34 (`:1881-1911`, "The transport-security policy has no production caller (PB-NET-2)"), B45 (`:2300-2346`, "the pairing dial runs UNVERIFIED TLS; the pin cannot bootstrap itself"), B48 (`:2401-2432`, "B45's ruling is INCOMPLETE and is amended"), B54 (`:2610-2634`, "the machine's published payload is authoritative on every completed pairing"), B57/B58 (`:2732-2813`, "`paired` is published before the pin is durable, and `relay_untrusted` is terminal" / "B57 is a SHIPPING BLOCKER: the first pairing on Android is a coin toss"), and B13's pin-channel clause (`:1010-1031`, "The QR carries the relay URL; `MachineStaticPub` is NOT pinned in v1; the relay URL ceiling"). It **reaffirms** D9's closing sentence at `ADR-007-remote-access.md:78` — *"TLS is metadata defense only — E2EE confidentiality does not depend on it"* — which is the premise every ruling below rests on, not a casualty of them.
**Companions**: ADR-015 (push-gateway split), ADR-017 (capability-driven terminal fallback), ADR-018 (multi-machine pairings), [ADR-011](ADR-011-multi-device-epochs.md) (multi-device epochs — untouched by this ADR, see Blast radius).
**Surfaces**: `internal/remote/relay/security.go`, `internal/remote/relay/client.go`, `mobile/relay.go`, `internal/remote/pairing/pairing.go`, `internal/phonecore/state.go`, `cmd/swarm/remote.go`, `swarm relay doctor` (R2), `deploy/relay/Caddyfile`, `docs/operations/relay-vps-deploy.md`, `docs/operations/relay-runbook.md`.

## Context

### What ships today, and why it is a pin

`TrustRootSourceFor` returns `TrustRootsPinned` for `goos == "android"` (`internal/remote/relay/security.go:76-81`), and the comment above it states the reason without hedging: Android 14 moved the system CA store into the Conscrypt APEX (`/apex/com.android.conscrypt/cacerts`), which is not in Go's `root_linux.go` search path, "so the pool is stale or empty on a modern handset", and "user-installed and enterprise CAs are never picked up at all" (`security.go:62-75`). `EmbeddedTrustRoots()` is deliberately empty (`security.go:83-85`). B33 states the consequence in one sentence: "Android's trust-root source is `TrustRootsPinned`, **so on that platform the pin is the whole of relay TLS verification**" (`ADR-007-remote-access.md:1860-1861`). `tlsConfig`'s final switch makes it literal — `case TrustRootsPinned: return nil, ErrPinRequired` (`security.go:359-360`).

That decision has cost this repository five recorded defects, each one a consequence of the same premise rather than an implementation slip:

1. **The renewal hazard (B33, `:1870-1877`).** `PinnedSPKISHA256` was adopted because the leaf-DER pin dies on every reissue. But PB-OPS-5's claim that an SPKI pin "survives renewal at the same security level" is, in B33's own words, "**wrong as written**": certbot generates a fresh keypair per renewal by default, and "a fresh key is a fresh SPKI, which breaks an SPKI pin on exactly the cadence it was adopted to survive". The implementer refused to restate it and pinned it in the opposite direction — `TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey` (`internal/remote/relay/pin_renewal_test.go:185`) **asserts the pin failing** on a rekeyed renewal (`:1876-1877`). Key reuse is therefore *part of the pin* (`security.go:114-119`), which is why `deploy/relay/Caddyfile:38-43` carries `tls { reuse_private_keys }` (the directive itself at `:40`) under a comment (`:22-37`) beginning "IS LOAD-BEARING AND IS NOT A PERFORMANCE SETTING", against a directive Caddy itself documents as "against industry best practices" and "subject to removal in a future version".
2. **No production caller, and no channel (B34, `:1881-1911`).** B34 is "the most serious finding of the closure slice" (`:1883`): at the time of writing, "no non-test file in the repository constructs a `relay.Security` at all" (`:1886-1887`) — the pin was a fence guarding a path production did not take. It is wired now (`mobile/relay.go:522-531`, pinned by `TestPBOPS5_TheHandsetAppliesThePinItPinnedAtPairing`, `mobile/conformance/b34_handsetpin_test.go:63`). What B34 could **not** fix is the channel: a 32-byte pin is "~43 base64 characters" and `MaxRelayURLLen = 39` (`internal/remote/pairing/qr.go:46`) already puts the symbol at 133 characters of the v6-L budget (B13, `:1016-1019`). The pin therefore rides `MachinePayload.RelaySPKIPin` (`internal/remote/pairing/pairing.go:236-251`) and nothing else. B34 closes by requiring the owner "before any deployment where the relay is not the operator's own trusted host" (`:1908`).
3. **The bootstrap paradox (B45, `:2300-2310`).** The pairing dial is the dial that *fetches* the pin, so it cannot be pinned; on a pinning-only platform an unpinned `wss://` dial is not merely unverified but "**REFUSED, not merely unverified**" before a packet (`:2306-2307`). B45's ruling — the pairing dial, and only the pairing dial, runs unverified TLS (`PairingSecurity()`, `security.go:181-183`) — exists solely to break that deadlock.
4. **B45 lowered an attacker's cost (B48, `:2401-2419`).** "**B45 lowered the cost of B46's harvest from 'hold a valid certificate' to 'be on the path'**" — "a widening of the one dial that must not be widened" (`:2411-2413`). The amendment is capture-then-compare: record the presented SPKI on the unverified dial (`Conn.PeerSPKI`, `internal/remote/relay/client.go:325-341`) and compare it against `MachinePayload.RelaySPKIPin` when msg2 arrives (`DeviceVerifyFunc`, `internal/remote/pairing/pairing.go:162-183`). Note what that sentence concedes: **verified TLS on the pairing dial is a security property the product gave up, and would recover.**
5. **The first pairing on Android is a coin toss (B57/B58, `:2732-2813`).** B58 is the only entry in ADR-007 labelled a **SHIPPING BLOCKER** (`:2768`; `grep -n "SHIPPING BLOCKER"` returns that line and no other). "A fresh handset holds no pin. On a pinning-only platform an unpinned dial is refused with `ErrPinRequired`, which B45's switch maps to `connRelayUntrusted` — **terminal**" (`:2773-2774`). B58 also records why nothing caught it: "the bug is invisible on the platform the suite runs on and ordinary on the platform that ships" (`:2790-2791`). Ruling (1) is built — the verdict is non-terminal while a pairing is in flight (`mobile/relay.go:408-422`, `TestB58_TheFirstPairingSurvivesAPinningOnlyPlatform`, `mobile/conformance/b58_pairingterminal_test.go:229`) — but it is a brace against a failure the default policy manufactures.

B54 (`:2610-2634`) is the lever this ADR uses. Its ruling — "**a completed pairing adopts the machine's published payload verbatim, including the absence of a pin**" (`:2627-2629`) — was written to escape a state whose own remedy was "a **no-op**" (`:2625`), and it settles the authority question in advance: treating a missing pin as an accident "second-guesses the machine's authority over its own relay" (`:2629`). **The machine is authoritative over its own relay trust.** Migration below is that ruling applied on the hot path instead of the escape hatch.

### Why the default flips now

The playbook's product shape changes the arithmetic. RC-D2 makes the normal deployment target "an always-on public host with WSS on port 443, usually a small VPS" (`playbook:72`) provisioned from a supported Compose/Caddy bundle with "automatic TLS" (`playbook:495-496`) — a relay with a real domain and a publicly trusted certificate, which is exactly the configuration the pin was invented to work *without*. RC-D7 keeps E2EE mandatory (`playbook:77`) and §5's table confirms that a relay compromise buys "routing ids, timing, sizes, presence, retention metadata" and nothing else (`playbook:350`).

And the scan channel has moved. B141 rules that "**The PNG is the promised scan target; the terminal symbol is best effort**" (`ADR-007-remote-access.md:8501`): "qrterm's symbol therefore CANNOT be promised scannable", the PNG at 16 px/module with a four-module quiet zone "is the promised path", and "the terminal drawing stays as the zero-friction best effort" (`:8513-8519`). The QR **is** still the promised scan target; what stopped being promised is the 80x24 *terminal rendering* of it. That is the stronger fact for this ADR than the one a careless reading would take: the constraint B13 and B34 both died against was never "a QR is too small", it was `MaxRelayURLLen = 39`, an arithmetic ceiling derived from a 24-row terminal (`:1016-1019`). B141 removes that budget from the promised path, and the playbook completes the removal with a required PNG/browser rendering plus a manual relay-URL-and-short-code flow (`playbook:190-192`). See W7 — this relieves the ceiling for the pin's missing channel; it does not repeal the ceiling itself.

Against that, the pin's residual value is narrow and its costs are structural: an unattended renewal reads to the user as an interception (`docs/operations/relay-vps-deploy.md:257-267`, with a whole recovery section at `:269-273`), an operator re-running `swarm remote init` without `--relay-pin` bricks the pairing (B54, `:2620-2625`), and the ordinary first pairing races (B58).

### The obstacle this ADR must answer, not wave away

B45's rejected alternative 1 is load-bearing and is quoted here in full because every "just use Web PKI" proposal dies on it:

> (1) Verifying the pairing dial against system roots fails on Android 14+ anyway — Go cannot see the Conscrypt store, which is this ADR's own reasoning for `TrustRootsPinned` — and fails always for a self-signed relay, which is the runbook's own topology. (`ADR-007-remote-access.md:2324-2326`)

Both halves are true and neither is repealed by a decision to prefer Web PKI. The second half is answered by scope: a self-signed relay becomes the *expert* policy (W6), not the default. The first half is a **mechanism problem with no prose answer**, and W2 decides it.

## Decision

### W1. Normal Web PKI hostname validation is the default relay TLS policy, and the policy is a named, machine-authored, authenticated value

The default `relay.Security` verifies the relay's certificate chain against the platform's trust roots **and** matches the certificate against the configured hostname. There are exactly two policies and they are named on the wire:

| Policy | Meaning | Default |
|---|---|---|
| `webpki` | Chain to a publicly trusted root; leaf SAN covers the configured host; certificate in its validity window. | yes |
| `pinned_spki` | Chain and name verification replaced by an SPKI pin set (W5). Expert, opt-in, marked in the UI. | no |

The policy is a field of the machine's sealed `RemoteProfileV1` (`playbook:405-407`) and of `pairing.MachinePayload`, alongside `relay_host` — the hostname the machine itself dials. It is **never** read from the relay, from a QR field, or from a phone-side setting: B54's ruling (`:2627-2629`) gives the machine authority over its own relay trust, and a policy a relay could assert about itself would be a downgrade oracle.

**Adding these fields is a coordinated R1 act, not this ADR's alone.** ADR-017 T5 (`ADR-017:106`) rules that `RemoteProfileV1` is one shared wire struct that "no ADR owns", that "a field addition is a profile-version decision, taken once across the R1 set rather than independently per ADR", and that "each ADR that adds a field carries its **own** GG-7 field-table obligation against `docs/specifications/protocol.md` in its own commit". This ADR's three fields — `relay_tls_policy`, `relay_host`, and the pin set (W5) — are added under that rule: they land in whichever single profile version the R1 set selects, together with ADR-017's `TerminalView` and session-capability-record versions, and this ADR's own GG-7 obligation (Blast radius) is discharged in this ADR's commit and discharges nobody else's.

**The policy field and the pin fields are independent, and nothing may derive one from the other.** This is a ruling, not a detail of the CLI: W9 requires a machine to be on `webpki` *and* to publish a pin for handsets that predate the policy field, so that state has to be expressible or the migration ladder below has no first rung. Therefore:

- `relay_tls_policy` is its own value in every durable and wire artifact that carries the pin — the machine's `relay.json`, `pairing.MachinePayload`, `phonecore.State`, `RemoteProfileV1`. **A pin's presence never implies `pinned_spki` and a pin's absence never implies `webpki`.** Any reader that infers the policy from pin presence is a defect; Conformance names the negative test.
- `swarm remote init` selects the policy with `--relay-tls-policy {webpki|pinned_spki}` and with nothing else. Omitted, it is `webpki` — except that a host Web PKI cannot serve is refused before any write rather than silently demoted (W6).
- `--relay-pin` (`cmd/swarm/remote.go:149`) supplies the expert pin and keeps its meaning and its pre-write validation (`validateRelayPin`, `cmd/swarm/remote.go:473-501`). It is **mandatory** under `pinned_spki` and **refused** under `webpki`.
- `--relay-pin-compat` supplies the W9 compatibility pin and is legal **only** under `webpki`. Two spellings for one digest is deliberate. The two values mean different things — one is the whole of verification, the other is a byte published for builds that cannot read the policy field — and a single flag would let one mistyped invocation move a machine between trust models in silence, which is B54's defect shape (`:2620-2625`) with the arrow reversed.
- One legacy inference survives, and it is an inference over a *flag*, never over stored state: `--relay-pin` with no `--relay-tls-policy` selects `pinned_spki`, so an operator's existing invocation keeps its exact present meaning. `--relay-pin` together with `--relay-tls-policy webpki` is a pre-write refusal that names `--relay-pin-compat`.

### W2. On Android the *trust decision* is delegated to Kotlin over a reverse-bound seam; Go keeps the *name* check; neither half alone admits a peer

This is the mechanism answer to B45's alternative 1, and it is chosen because the repository already has the seam shape and one working precedent.

- `TrustRootSource` gains a fourth value, `TrustRootsPlatformDelegate` (`security.go:47-58`). `TrustRootSourceFor("android")` continues to return `TrustRootsPinned` (`security.go:76-81`); the delegate is selected **only** when a verifier has been installed by `relay.WithPlatformVerifier(sec, v)` — a new **production** constructor, unlike `WithTrustRootSource`, which stays test-only and inert in a release build (`security.go:196-208`, fenced by `TestPBNET2_TheTrustRootOverrideIsInertInAReleaseBuild`, `pinningplatform_test.go:135`). No verifier means `ErrPinRequired`, unchanged. **Absence fails closed; it never falls back to Go's system pool on Android.**
- The verifier is a **reverse-bound gomobile interface** — Go calls it and Kotlin implements it — in exactly the shape `KeyCustody` established (`mobile/keycustody.go:14-25` states the direction rule; the interface is at `:55-64`):

  ```text
  RelayTrust interface {
      VerifyRelayChain(host string, pemChain []byte) error
  }
  ```

  The chain travels as PEM, leaf first, because gomobile cannot bind `[][]byte` and `CertificateFactory.generateCertificates` consumes PEM directly. **The direction rule is different from `KeyCustody`'s and the difference is the point**: B8's "single crossing, inbound only" bans *key material* crossing outbound (`keycustody.go:17-22`), and a server certificate chain is public by construction. `TestS14_TheCustodySeamIsInboundOnly` guards a different invariant and is untouched.
- Kotlin implements it with `X509TrustManagerExtensions.checkServerTrusted(chain, authType, host)` over the default `TrustManagerFactory`. That is the platform's own verifier: it reads the Conscrypt APEX store — the store `security.go:62-75` says Go cannot see — and it honours the app's Network Security Config.
- **Go still checks the name itself**, on the leaf, with `x509.Certificate.VerifyHostname(host)` plus a `NotBefore/NotAfter` check against the Go clock, inside `VerifyPeerCertificate`. Kotlin is handed the host so the platform's own name and NSC rules apply; Go does not *trust* Kotlin to have applied them. **Both halves must pass. Neither is sufficient.** A verifier that returns nil for everything still cannot admit a certificate whose SAN does not cover the configured host.
- Verdicts survive gomobile's error flattening by the `KeyCustody` token convention (`mobile/keycustody.go:49-54`, constants at `:66-94`): `swarm-relaytrust/untrusted` (the chain was rejected — a real security verdict) and `swarm-relaytrust/unavailable` (no platform verifier answered — a configuration fault). They reach the user as different states (W8). An unreadable verdict is fatal, never guessed, for `keycustody.go:53-54`'s reason: "a custody verdict that cannot be read must not be guessed at".
- The call runs synchronously on the dial goroutine inside `VerifyPeerCertificate`. It must be bounded and must not re-enter the Go core: no `App` lock, no state read, no event emission.

**Desktop is unchanged.** `swarm-remote` and the CLI keep `TrustRootsSystem` and Go's own verifier (`security.go:352-363`, `MachineSecurity()` at `security.go:210-233`). This seam exists for one platform because one platform has the problem.

**Named failure modes, stated rather than discovered.**

- **Revocation is not checked, and was not checked before.** Android's default trust manager performs no OCSP/CRL check for an ordinary app, and neither does Go's system verifier. A pin checked revocation even less — it admits one key forever. So `webpki` means *chains to a trusted root, name matches, inside validity* and **not** *not revoked*; the honest mitigation is short certificate lifetimes, which the ACME topology already gives. This must be written in the runbook in those words.
- **Custom user CAs and enterprise MITM proxies are refused.** Since API 24 an app does not trust user-added CAs by default, and this ADR **does not** ship a `network_security_config.xml` that restores them. A corporate TLS-inspecting proxy therefore breaks the relay dial. That is the correct answer for a relay whose payloads are E2EE anyway, and it is reported as its own state with a repair path (W8): the remedy is the expert pin policy (W5/W6), never a global relaxation.
- **A publicly trusted CA can mis-issue for the operator's name.** Under `pinned_spki` only one key is admitted; under `webpki` roughly the whole public root program is in scope. The answer is D9 at `ADR-007-remote-access.md:78`, unchanged: a mis-issuing CA reaches *metadata* — which routing ids exist, when, how large — and cannot read a payload, cannot survive the SAS, and cannot forge a signed command. An operator who declines that trade keeps the pin (W5). CT makes mis-issuance detectable by an operator monitoring their own name; it is **not** enforced by the handset, and no copy may imply that it is.

### W3. The default pairing dial is verified; B45's exemption is scoped to the expert policy; B48's capture-then-compare stays, and a pin is read only under `pinned_spki`

Under `webpki`, the pairing dial is an ordinary verified dial. There is nothing to bootstrap: the trust root is the platform's, not a value that arrives in msg2. **B45's deadlock does not exist under the default policy and its exemption is therefore withdrawn from it** — `PairingSecurity()`'s `unverifiedTLS` flag (`security.go:138-143`, `:181-183`) is reached only when the machine's published policy is `pinned_spki`, and the fence that it is reachable from nowhere else stays exactly as written (`TestB45_OnlyThePairingDialMayUseTheUnverifiedPolicy`, `mobile/b45_pairingscope_test.go:40`; `TestB45_APinOutranksTheUnverifiedFlag`, `pinningplatform_test.go:93`).

**This is the security property B48 priced, and this ADR recovers it by name.** Under `webpki` the cost of intercepting the consent-carrying pairing dial returns from "be on the path" to "hold a certificate valid for the operator's relay" (`:2409-2413`). Record this as a *point for* Web PKI rather than a side effect.

**B48's capture-then-compare mechanism is retained in full; its comparison is scoped to the policy, never deleted.** `Conn.PeerSPKI` (`client.go:325-341`) records the presented SPKI on every dial it owns, unchanged — the *absence* of that capture is what B48 was written about, and it costs nothing to keep. `DeviceVerifyFunc` (`pairing.go:162-183`) keeps comparing the capture against `MachinePayload.RelaySPKIPin`, and the fence that it runs before the pairing completes is untouched.

**The single rule, stated once here and referenced by W4 and W9: a pin is consulted if and only if the effective relay TLS policy is `pinned_spki`.** Under `pinned_spki`, nothing changes — the comparison is the whole defense, exactly as today, and every existing test of it stays green. Under `webpki`, a published pin is *stored* and *not consulted*: not by `DeviceVerifyFunc` on the pairing dial, not by `tlsConfig` on the session dial, not by anything.

That is not the deletion this section forbids — it is the difference between removing a check and scoping it to the policy that gives it meaning — and the tempting alternative, "compare it anyway, defense in depth", is refused here by name for three reasons:

1. A `webpki` phone that also enforced the compatibility pin would fail on exactly the event this ADR exists to survive: a rekeyed ACME renewal moves the SPKI (B33, `:1870-1877`), the enforcing phone refuses every dial, and it does so while believing it had migrated off the pin.
2. The compatibility pin is published *for builds that cannot read the policy field* (W9). A build that can read the policy field is not its audience.
3. It buys no adversary class. Presenting a chain that satisfies the platform verifier **and** Go's own `VerifyHostname` for the operator's name is strictly harder than presenting the operator's own key, so the pin adds nothing to `webpki`'s guarantee — only a way to fail.

### W4. A pinned client migrates only on **advertise + prove + commit**; failure retains the pin and offers a repair path, and never disables validation

An already-paired phone holding a pin does not drop it on hearing a claim. The sequence, and every step is required:

1. **Advertise.** The machine's sealed, signed `RemoteProfileV1` carries `relay_tls_policy = webpki` and `relay_host`. A relay-supplied or unauthenticated hint is ignored.
2. **Check identity of destination.** `relay_host` must equal the host of the relay URL the phone already holds. A profile that changes the destination is not a TLS migration; it is a re-pairing question and is refused as `stale_profile`.
3. **Prove.** The phone opens a **probe dial** to that URL under the `webpki` policy — platform delegate plus Go's own hostname and validity check (W2) — on a connection separate from the live pinned one, which stays up throughout. A phone that has never successfully validated the hostname has proven nothing.
4. **Commit.** Only on a successful probe does the phone durably write `relay_tls_policy = webpki`, in one transaction, behind a `phone-state.json` schema-version bump for the reason `state.go:75-81` gives for v7: a build one version back that silently dropped the field would leave "a handset whose platform trust-root source is `TrustRootsPinned`" refusing "every dial with no way for the user to tell a lost pin from a hostile relay".

   **The commit does not clear `RelaySPKIPin` / `RelaySPKIPinNext`.** They are retained exactly as published and simply stop being read, per W3's single rule. Retention is the ruling and clearing is the defect, for two reasons that are easy to invert on a careless read. First, the reverse direction below would become a re-pairing rather than a re-adoption: a phone that deleted the bytes has nothing to fall back to when the machine reverts to `pinned_spki`. Second, clearing fights B54's verbatim-adoption rule (`:2627-2629`) — the next completed pairing adopts whatever the machine publishes, so a phone that deleted the field takes it straight back, and a reader who saw both behaviours would conclude W3 and B54 contradict each other. They do not, and the division of labour is exact: **B54 decides what the phone stores (verbatim, including absences); W3 decides whether it is read (only under `pinned_spki`).** When the machine stops publishing a pin at the close of the compatibility window (W9), the phone adopts that absence verbatim as well, and under `webpki` the absence changes nothing, because nothing was consulting the value.
5. **Fail.** Any failure **leaves the phone on `pinned_spki`** — which, by W3's single rule, is what keeps the pin enforced — keeps the working connection up, and surfaces `webpki_unavailable` with the operator-facing cause (name mismatch, untrusted chain, expired, no platform verifier). It never retries into a weaker policy, never disables verification, and never presents "we could not validate" as success. Note the shape this gives the failure: the phone is never in a state with neither a working pin nor a working verification, because the only write is step 4 and step 4 runs only after step 3 succeeded.

The reverse direction is symmetric: a machine that reverts to `pinned_spki` republishes the policy and the pin set, and the phone adopts it under B54's verbatim rule (`ADR-007-remote-access.md:2627-2629`). A fresh pairing needs none of this — it adopts the published payload as B54 already rules, which is why W4 is a migration protocol and not a new trust model.

### W5. Expert pinning requires an overlapping current/next pin set and an authenticated rotation before it may claim automatic renewal

Today's pin is a scalar and the claim it supports is conditional on `reuse_private_keys`. The expert policy replaces both:

- `Security` gains `PinnedSPKISHA256Next` beside `PinnedSPKISHA256`; either digest admits the peer, on the same terms as `PinnedCert` and `PinnedSPKISHA256` do today (`security.go:317-350`). `pairing.MachinePayload` (`pairing.go:236-251`), `skeleton.PairingConfig` (`internal/skeleton/pairing.go:71-74`, `:268`) and `phonecore.State` (`state.go:210-215`) each gain the matching `...Next` field, additive and version-bumped. **A field and not a list**, deliberately: the rotation protocol needs exactly two generations, three durable artifacts would each owe a shape migration for no gain, and `omitempty` discipline already governs these blobs (`state.go:495`).
- **Rotation protocol.** (a) The operator generates the next key and computes its SPKI; (b) `swarm remote init --relay-pin-next` publishes it — legal only under `--relay-tls-policy pinned_spki`, since a `webpki` machine's compatibility pin has one generation and one audience (W9) — and the machine advertises the pair in the authenticated profile; (c) each paired phone echoes the pin set it holds in its reconcile acknowledgement; (d) **only after every paired device has acknowledged** may Caddy be switched to the next key; (e) the operator then promotes next to current and clears next. An unacknowledged device blocks promotion and is named in `swarm remote status`.
- **Until (c) exists, no automatic-renewal claim may be made.** `reuse_private_keys` stays mandatory *for the expert policy*, `deploy/relay/Caddyfile:22-43` keeps its comment and its block with the scope narrowed to that policy, and PB-OPS-5's original claim stays amended in B33's direction. `TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey` (`pin_renewal_test.go:185`) is **not** weakened: it keeps asserting the failure for a single-pin client, and the overlap case gets its own test.
- The default policy needs none of this. Under `webpki`, Caddy's own documented default — "a new key is created for every new certificate to mitigate pinning" (`docs/operations/relay-vps-deploy.md:255-256`) — is correct again, and the directive Caddy calls "subject to removal in a future version" stops being load-bearing for ordinary deployments.

### W6. IP-literal and self-signed relays are development setups behind the expert policy, marked and never silent

A `webpki` policy with an IP-literal host is refused **before any filesystem write**, in the same shape as B13's existing pre-write URL refusals (`ADR-007-remote-access.md:1025-1028`): this deployment's ACME topology has no ordinary public-CA path for an IP literal, and a policy that cannot succeed must not be written. A self-signed relay reaches the same place one step later, at `swarm relay doctor` (W8), which fails rather than warns.

**The refusal names `--relay-tls-policy pinned_spki` together with `--relay-pin` as the supported route; it does not take that route.** Nothing demotes a policy silently — that is W1's independence ruling applied at the CLI boundary. An expert trust model is entered by typing it, so that "my relay is pinned" is never a state an operator arrived at by supplying an address.

The phone renders an expert-policy relay with a persistent marker in machine health — not a warning banner, a fact — so the trust model in force is legible before a failure rather than inferred from one. The loopback cleartext carve-outs are untouched; they are listed under **Blast radius → "Invariants that hold, unchanged"** and are a separate question from this ADR's.

### W7. The QR ceiling is relieved in the direction that matters and is not repealed

`MaxRelayURLLen = 39` stands (`internal/remote/pairing/qr.go:46`); B13's arithmetic is unchanged and no byte of QR budget is spent by this ADR. What changes:

- The pin's missing channel — B34's "**Not a one-line fix, which is why it is recorded rather than patched at closure**" and "this needs a decision about the pairing payload's size budget, not merely a call-site change" (`:1902-1906`) — is **dissolved rather than solved**: under the default policy there is no pin to carry.
- The budget that produced the ceiling was a *terminal* budget, not a QR budget: L=39 gives 133 characters and a v6-L symbol "which a standard 80x24 terminal can draw at half-block density", while L=40 "jumps to version 7 (45 modules, 49x25) and no 24-row terminal can show it" (`:1016-1019`). B141 rules that the terminal drawing is no longer the promised path (`:8501`, `:8513-8519`), so the arithmetic that produced the ceiling no longer binds the promised path — and the playbook makes the PNG/browser rendering plus manual relay-URL-and-short-code flow a requirement (`playbook:190-192`). Repealing `MaxRelayURLLen` is that program's decision to take, not this one's; this ADR only records that its motivating pressure is gone.
- B140's remembered relay-URL slot (`ADR-007-remote-access.md:8483-8487`) — "a relay URL remembered before any pairing carries no SPKI pin — exactly the B45/B48 posture the QR path already has" — becomes **dialable on a fresh Android handset for the first time**. Under the pinning default that path is `ErrPinRequired` on every retry (B58, `:2773-2776`); under `webpki` it is an ordinary verified dial.
- **The honest counter-pressure**: `webpki` requires a DNS name, and DNS names are longer than IP literals. B13's own example, `wss://swarm-relay.us-east-1.example.com:8443`, "is 44 characters and does not fit" (`:1025-1026`). Operators must own a short name, and the long-URL path the playbook requires (`playbook:190-192`) is a **prerequisite of this ADR's default**, not an independent nicety.

### W8. The failure vocabulary splits; B58's non-terminal rule stands; the doctor proves the policy machine-side

`connRelayUntrusted` (`mobile/relay.go:230`) is one string covering four different operator problems. Under `webpki` it becomes a small closed set, each with a distinct remedy and none of them a spinner — B45's defect (a) (`:2333-2340`) quotes the rule this must not break, from `android/app/src/main/kotlin/dev/swarm/phone/ui/ConnectionUi.kt:25`: *"A spinner is a promise that waiting is enough."*

| State | Cause | Remedy shown |
|---|---|---|
| `relay_untrusted` | Chain rejected by the platform verifier, or SPKI pin mismatch. | "Not the relay your machine published." Pair again / check the certificate. |
| `relay_name_mismatch` | Chain valid, leaf SAN does not cover the configured host. | Name the configured host and the names the certificate carries. |
| `relay_cert_expired` | Outside the validity window. | Operator action: renew. |
| `relay_trust_unavailable` | No platform verifier answered (`swarm-relaytrust/unavailable`). | App fault, not a relay fault. Never a security accusation. |
| `relay_insecure` | Cleartext, unchanged. | Unchanged. |

**B58 ruling (1) is retained in full**: a transport verdict reached while a pairing is in flight is not terminal (`mobile/relay.go:408-422`, `:2795-2798`). Web PKI makes the ordinary first pairing succeed rather than race, but a name-mismatched or untrusted relay reaches the same verdict during the same pairing, and removing the brace because the common case improved is exactly the regression this clause exists to prevent. `TestB58_TheFirstPairingSurvivesAPinningOnlyPlatform` (`mobile/conformance/b58_pairingterminal_test.go:229`) keeps running against the expert policy.

**The machine side owes the same verdict before a handset is asked for it.** The playbook has `swarm relay doctor <url>` prove "DNS, WebSocket upgrade, **TLS policy**, protocol compatibility" (`playbook:179-180`, contract at `:504-507`) and R2 requires "clear diagnostics for DNS/TLS/WebSocket/pin failures" (`playbook:721`). Under this ADR that is a named obligation, not a category:

- The doctor **prints which policy is in force** for the configured URL (`webpki` or `pinned_spki`, and why — flag, IP literal, or default), because a silently-wrong policy is the failure this whole ADR is about.
- The doctor dials under **the same policy the phone will**, never a relaxed one, and reports the presented chain's issuer, SANs, and validity window.
- On a `webpki` host it **fails** — not warns — when the chain does not verify, when the leaf's SAN does not cover the configured host, or when the certificate is outside its validity window. The SAN case is the one that matters: it is the misconfiguration a handset would otherwise discover as `relay_name_mismatch` after the operator has already been asked to scan.
- On a `pinned_spki` host it prints the computed SPKI and reports whether it matches the configured current pin and pin-next (W5), so an unacknowledged rotation is visible before promotion.
- The doctor's states use the same vocabulary as the table above, so the operator's terminal and the user's handset name the same fault.

### W9. Rollback and N/N-1 are decided, not assumed

- **Old app / new machine.** A phone build that does not understand `relay_tls_policy` reads only `RelaySPKIPin`, and there are two sub-cases. The second is the one this ADR's own default creates and it must be stated plainly:
  - *The machine adopts `webpki` and still publishes a compatibility pin* (`--relay-tls-policy webpki --relay-pin-compat <spki>`, expressible because W1 made the two fields independent). The old phone reads only `RelaySPKIPin`, keeps pinning, and keeps working. A migrated phone stores the same byte and does not consult it (W3).
  - *The machine adopts `webpki` and configures no pin at all — the intended end state.* The old phone adopts an **absent** pin under B54's verbatim rule (`:2627-2629`), and on Android an unpinned dial is `ErrPinRequired` on every retry, mapped to `connRelayUntrusted` (`mobile/relay.go:408-414`). **That is B58's brick (`:2768`, `:2773-2776`) arriving through the new default rather than through a race** — configuration, not interleaving, and therefore reproducible rather than lucky.
  - **Ruling, because the playbook requires downgrade-safe N/N-1 behavior (`playbook:716`, `:162-163`)**: while the compatibility window is open a machine on `webpki` **must still publish a compatibility pin** over its current relay certificate, and `swarm remote init` refuses to write a `webpki` profile with no `--relay-pin-compat` while any paired device has not yet acknowledged `relay_tls_policy = webpki`. Promotion to a pinless profile is gated on the same per-device acknowledgement W5 uses for rotation, and `swarm remote status` names the devices that block it. An operator may override, and the override must state in words that every un-migrated handset will refuse every dial.

  - **The ladder, end to end, because each rung depends on the one below and no rung may be skipped.** (1) The operator runs `swarm remote init --relay-tls-policy webpki --relay-pin-compat <current SPKI>`; W1's independence is what lets that command mean anything. (2) The machine advertises `relay_tls_policy = webpki`, `relay_host`, and the compatibility pin in the sealed, signed profile — satisfying W4.1, which no un-pinned profile could have reached. (3) Un-migrated builds ignore the policy field they cannot parse, pin as they always have, and keep working. (4) Migrated builds run W4's probe, commit `webpki`, and retain-without-consulting the pin (W3, W4.4). (5) Each migrated device echoes its acknowledgement on reconcile. (6) When `swarm remote status` shows no device blocking, the operator re-runs `swarm remote init --relay-tls-policy webpki` with no `--relay-pin-compat`; the machine stops publishing the pin, every phone adopts the absence verbatim under B54, and — because no `webpki` phone was reading it — nothing changes on any handset. That last sentence is the test: if step (6) is observable on a migrated handset, W3 has been implemented wrongly.
  - The window's own cost is inherited and named: a compatibility pin is a pin, so `reuse_private_keys` (`deploy/relay/Caddyfile:40`) stays required for the duration of the window even on a `webpki` machine, or the pin goes stale at the first rekeyed renewal (B33, `:1870-1877`) and the un-migrated handsets brick anyway.
- **New app / old machine.** No policy field means `pinned_spki` with whatever pin the payload carries, i.e. today's behaviour exactly. The default flips only on an authenticated advertisement.
- **Downgrade of the phone build.** Guarded by the state schema bump (W4.4), which fails loudly for `state.go:75-81`'s reason.
- **Rollback of the machine.** W4's reverse direction, under B54.
- One compatibility window, one stable release train, then the pinned-only path becomes expert-only. Removing it is out of scope for this ADR.

## Blast radius

**Surfaces that change** (each gated on this ADR):

| Surface | Change |
|---|---|
| `relay.Security` | `PinnedSPKISHA256Next`; `TrustRootsPlatformDelegate`; `WithPlatformVerifier` (production, exported); hostname + validity check inside `VerifyPeerCertificate` |
| `relay.TrustRootSource` | fourth value; `TrustRootSourceFor("android")` still returns `TrustRootsPinned` as the no-verifier floor |
| `swarmmobile` | new reverse-bound `RelayTrust` interface and two verdict tokens; `handsetSecurity` selects policy from state (`mobile/relay.go:522-531`) |
| `pairing.MachinePayload` | `RelayTLSPolicy`, `RelayHost`, `RelaySPKIPinNext` — additive, version-skew safe; policy independent of the pin fields (W1) |
| `RemoteProfileV1` | `relay_tls_policy`, `relay_host`, pin set — sealed and signed, GG-7 field-table obligation, in the one R1-wide profile version ADR-017 T5 (`ADR-017:106`) rules is taken once across the set (W1) |
| `phonecore.State` | `RelayTLSPolicy`, `RelaySPKIPinNext`; pin fields retained across migration and read only under `pinned_spki` (W3/W4.4); schema-version bump with migration + rollback tests (D10) |
| `cmd/swarm remote init` / `remote status` | `--relay-tls-policy`, `--relay-pin-compat`, `--relay-pin-next`; the `--relay-pin`-vs-`webpki` and IP-literal pre-write refusals; the pinless-promotion acknowledgement gate (W9) |
| `swarm relay doctor` (R2) | prints the policy in force; dials under it; fails on chain/SAN/validity under `webpki`; reports pin-set match under `pinned_spki` (W8) |
| `deploy/relay/Caddyfile`, VPS deploy, relay runbook | `reuse_private_keys` scoped to the expert policy and to the compatibility window; ACME/DNS/SAN as the default setup step |
| Android | Kotlin `RelayTrust` implementation over `X509TrustManagerExtensions`; new connection states |

**Invariants that hold, unchanged** — listed so no implementer "fixes" one on the way past:

- **E2EE and the SAS are the content trust roots; TLS is metadata protection.** `ADR-007-remote-access.md:78` survives verbatim and is the premise of W2's honest accounting of a mis-issuing CA. Nothing here makes a payload depend on a certificate.
- The cleartext refusal (`ErrCleartextRefused`), its loopback-IP-**literal**-only carve-out, the never-resolve-a-name rule, and `MachineSecurity`'s release-build loopback exception (`security.go:210-233`, `:399-405`). Web PKI admits no cleartext anywhere.
- The redirect re-check (`security.go:427-430`): every hop is re-asked, so a `wss://` dial cannot be answered with `302 -> ws://`.
- `ErrPinMalformed` decided **before** the dial, on every scheme, and the pin-withdraws-the-cleartext-carve-out rule (`security.go:247-257`).
- A pin present **in `relay.Security`** outranks the unverified flag (`security.go:294-297`), so no composition of policies can downgrade an explicit one. W3's "not consulted under `webpki`" is implemented by never populating `Security.PinnedSPKISHA256` from a `webpki` profile — not by teaching `tlsConfig` to ignore a pin it was handed. The distinction is the whole reason this invariant survives intact: no precedence rule inside `tlsConfig` is edited.
- B48's capture (`Conn.PeerSPKI`) on both policies, and B48's comparison under `pinned_spki` (W3). The mechanism is not removed on either.
- B54's ruling that a completed pairing adopts the machine's published payload verbatim, including absences (`:2627-2629`).
- B58 ruling (1): a transport verdict during an in-flight pairing is not terminal (`:2795-2798`). Ruling (2) (`Cancel` loses after the durable write, `:2800-2807`) and ruling (3) (the App-level platform seam, `:2809-2813`) likewise stand.
- `WithTrustRootSource` stays test-only and inert in release (`security.go:196-208`, `:201-208`). The new `WithPlatformVerifier` is a *different* function precisely so that fence is not loosened to make room for a production caller.
- Every ADR-011 surface: envelope format, AAD field set, nonce discipline, epoch grants, per-device inbound keys, seq buckets. **This ADR changes what a socket verifies, never what an envelope means.**
- The pin is still never in the QR (B34, `:1902-1906` — "the pairing QR has no pin field, and `MaxRelayURLLen = 39` exists because … a 32-byte pin is ~43 base64 characters"), and no QR budget is spent here. `MaxRelayURLLen = 39` is not repealed by this ADR (W7). B13's separate ruling that `MachineStaticPub` is not pinned in v1 (`:1019-1023`) is a different pin, defended by the SAS, and is untouched here.
- The relay stays untrusted and ciphertext-only. A verified certificate does not make it trusted; it makes its metadata private in transit.

## Consequences

### Positive

- The pairing dial is verified again under the default policy, which is the property B48 recorded B45 as having given up (`:2409-2413`): interception cost returns from "be on the path" to "hold a valid certificate for the operator's name".
- The ordinary first pairing on Android stops being a race that only B58's brace survives, and B140's remembered-relay-URL path becomes dialable on a fresh handset.
- An unattended ACME renewal stops presenting to the user as an interception. `reuse_private_keys` — a directive Caddy documents as "subject to removal in a future version" and this repo has had to carry a recovery procedure for (`docs/operations/relay-vps-deploy.md:263-267`, `:344-348`) — stops being load-bearing for ordinary deployments once the compatibility window closes.
- B54's unrecoverable case — "a machine that legitimately has no pin" (`:2630-2631`) — becomes the *normal* case rather than an escape hatch, once the compatibility window closes.
- Go's inability to read the Conscrypt store stops being a product constraint and becomes a delegated call, on a seam whose shape is already proven by `KeyCustody`.
- Expert pinning gets, for the first time, a rotation that does not require key reuse and does not take every handset offline.
- The operator learns about a wrong SAN from `swarm relay doctor` rather than from a handset that has already been asked to scan (W8).

### Negative

- **A new adversary class.** Under the pin exactly one key was admitted; under `webpki` any publicly trusted CA can issue for the operator's name. The answer is D9 and the expert policy, and it is a real trade, not a free win.
- **The new default can brick an un-migrated handset, and only a compatibility rule stops it.** A machine that adopts `webpki` and publishes no pin leaves every not-yet-updated Android build with no pin and therefore `ErrPinRequired` on every dial — B58's shipping blocker (`:2768`) reached by configuration. W9's compatibility pin and its acknowledgement gate exist to prevent it, which means the intended end state (no pin anywhere) is reachable only after every paired device has migrated, and an operator override can still produce the brick deliberately.
- **The compatibility window keeps the renewal hazard alive on `webpki` machines too**, because the compatibility pin is a pin — for un-migrated builds, which are the only readers of it (W3). `reuse_private_keys` remains required until the window closes, and reaching the clean end state costs a **second** `swarm remote init` run (W9 step 6) that an operator who believes they already migrated will not think to make. `swarm remote status` naming the window as open is the only thing standing between them and a stale compatibility pin.
- **A phone on `webpki` carries a pin it does not use.** That is deliberate (W4.4) and it is a legibility cost: state inspection shows a pin, the dial ignores it, and the two facts are reconciled only by the policy field. Any diagnostic that prints the pin must print the policy beside it or it will teach the wrong lesson.
- **Four flags now describe one certificate** — `--relay-tls-policy`, `--relay-pin`, `--relay-pin-compat`, `--relay-pin-next`. The pre-write refusals in W1 and W6 are what keep the combinations legible, and they are themselves surface that must be tested rather than reasoned about.
- **Revocation is still unchecked**, and saying "Web PKI" invites a reader to assume otherwise. The runbook must say so in words.
- **Two policies to test, forever**, and the expert path keeps every existing pin defect (B33's renewal hazard, B45's unverified pairing dial, B58's coin toss) alive inside it.
- **A new platform seam with a JNI call on the dial path** — a synchronous Kotlin call inside `VerifyPeerCertificate`, with the "unavailable" verdict now a reachable production state that must be distinguished from a security verdict in the UI.
- **DNS names are longer than IP literals**, so `MaxRelayURLLen = 39` bites harder under the default policy, and the long-URL/PNG/short-code path becomes a prerequisite rather than an improvement.
- **Enterprise TLS-inspecting networks break the relay dial** by design, with the expert pin as the only remedy.
- Durable-artifact migrations on three blobs (phone state, pairing payload, machine pairing config), each owing D10's versioned migration and rollback tests, plus a GG-7 obligation on `RemoteProfileV1`.

## Alternatives Considered

**Bundle a pinned Mozilla root store in the app with an update path.** Rejected, and the repo already rejected it once: `EmbeddedTrustRoots()` exists, returns nil, and `security.go:72-75` gives the reason — "Shipping an embedded CA bundle instead means shipping a trust store that rots between releases". It also gets the *wrong* answer by construction for the two populations Android handles correctly: enterprise CAs and user-installed CAs are absent from any bundle we ship, and root-program removals reach the handset only at our release cadence rather than the platform's. It is more code, more release-coupling, and a worse verdict than the platform's own.

**Conscrypt via JNI from the Go core.** Rejected as the same delegation with more surface. It reaches the right store, but it means linking a crypto provider into the `.so`, hand-writing JNI against a non-public store path, owning the version skew of an APEX-updated component, and re-implementing chain building, name matching and NSC policy that `X509TrustManagerExtensions` already applies. W2's seam gets an identical verdict from the class Android itself calls, in a shape this repository has already shipped once (`mobile/keycustody.go`). Kept on record because if the reverse-bound interface ever proves unusable (a gomobile constraint, a threading constraint), this is the fallback and not the embedded bundle.

**Keep the pin as the default and only fix the renewal path with overlapping pins.** Rejected: it fixes B33 and leaves B45's unverified pairing dial, B48's lowered harvest cost, B54's authority problem and B58's first-pairing race exactly as they are, while adding a rotation protocol the default population must operate. Overlapping pins are the right mechanism — they are W5 — for the population that chooses the pin.

**Trust user-installed CAs via a shipped `network_security_config.xml`.** Rejected: it re-admits every enterprise MITM proxy for the sake of the self-signed-relay operator, who is served correctly and explicitly by W6.

**Derive the policy from whether a pin is configured — one flag, no `--relay-tls-policy`.** Rejected, and it is the design this ADR most nearly shipped. It reads as economical and it is unsatisfiable: W9 needs a machine that is on `webpki` *and* publishes a pin for un-migrated builds, and under the derivation rule configuring that pin silently returns the machine to `pinned_spki`. The state the migration ladder starts from would not be expressible, so W4's authenticated advertisement of `relay_tls_policy = webpki` could never be issued, so no phone could acknowledge, so the acknowledgement gate could never open — and the compatibility rule is the only thing between the new default and B58's brick (`ADR-007-remote-access.md:2768`). Two independent fields and an explicit flag cost one more flag and buy an expressible migration.

**Have a migrated `webpki` phone enforce the compatibility pin as well — belt and braces.** Rejected in W3 with its three reasons, and recorded here because it is the intuitive reading of "defense in depth" and it inverts the ADR: the enforcing phone inherits B33's rekey failure (`:1870-1877`) on the exact schedule the migration was performed to escape, while its state file says it migrated. Depth that fails independently of the thing it defends is not depth.

**Let the phone decide the policy in Settings.** Rejected on B54's reasoning (`:2627-2629`): the machine is authoritative over its own relay trust, and a phone-side toggle is a downgrade oracle wearing a preferences screen.

**Drop the pin on the strength of the advertisement alone, without a probe.** Rejected: the advertisement is authenticated but says nothing about whether *this handset, on this network, with this platform verifier* can validate that host. A migration that can strand a phone with neither a pin nor a working verification is the one outcome §3.3 forbids (`playbook:158-160`).

## Conformance

The playbook's §3.3 matrix (`:162-163`), as named obligations. Each is a required gate under `playbook:873-890`.

| Case | Required behaviour |
|---|---|
| Caddy renewal, same key | `webpki` unaffected; `pinned_spki` single-pin unaffected (`TestPBOPS5_SPKIPinSurvivesRenewalWithTheSameKey`, `pin_renewal_test.go:141`) |
| Caddy renewal, new key | `webpki` unaffected; single-pin client fails — `pin_renewal_test.go:185` keeps asserting it; overlapping-pin client survives, new test; a `webpki` machine inside the compatibility window fails its compatibility pin, which is why W9 keeps `reuse_private_keys` required for the window |
| Hostname / SAN failure | Refused under `webpki` by Go's own `VerifyHostname` even if the delegate returns nil; surfaces `relay_name_mismatch`, never `reconnecting`; `swarm relay doctor` fails on the same host (W8) |
| Untrusted chain | Delegate returns `swarm-relaytrust/untrusted`; `relay_untrusted`; no fallback dial |
| No platform verifier | `ErrPinRequired`; `relay_trust_unavailable`; distinct copy from a security verdict |
| Downgrade attempt | An unauthenticated or relay-sourced policy claim is ignored; a pinned phone never leaves `pinned_spki` without a successful probe (W4) |
| Policy / pin independence (W1) | A profile with `relay_tls_policy = webpki` **and** a configured pin round-trips through `relay.json`, `MachinePayload`, `RemoteProfileV1` and `phonecore.State` with both values intact — the state W9's ladder starts from. Mutation control: a build that derives the policy from pin presence must fail this case, not pass it |
| Pin consulted only under `pinned_spki` (W3) | On `webpki`, a *deliberately wrong* published pin changes no outcome: the pairing completes, the session dial succeeds, `DeviceVerifyFunc` raises nothing. On `pinned_spki` the same wrong pin refuses, proving the comparison was scoped and not deleted |
| Compatibility-pin withdrawal (W9 step 6) | A machine that stops publishing the compatibility pin produces **no observable change** on a migrated handset; the un-migrated handset in the same fixture bricks, which is why the acknowledgement gate exists |
| Rollback | Machine reverts to `pinned_spki`, phone re-adopts under B54; phone-state downgrade fails loudly at the schema bump |
| N / N-1 | Old app + new machine **with a compatibility pin** stays connected; old app + new machine **with no pin** reproduces `ErrPinRequired` on every dial and is therefore refused at `swarm remote init` until every paired device has acknowledged `webpki` (W9), with the override path proven to state the consequence; new app + old machine; interrupted migration; both directions of policy change |
| Expert rotation | Promotion blocked until every paired device acknowledges the pin set; unacknowledged devices named in `swarm remote status` |
| Doctor | Prints the policy in force; dials under it, never relaxed; fails on chain/SAN/validity under `webpki`; reports current/next pin match under `pinned_spki` |
| Fences preserved | `TestPBNET2_TheTrustRootOverrideIsInertInAReleaseBuild` (`pinningplatform_test.go:135`), `TestB45_OnlyThePairingDialMayUseTheUnverifiedPolicy` (`mobile/b45_pairingscope_test.go:40`), `TestB48_CheckRelayPin` (`mobile/b48_relaypin_test.go:14`), `TestB58_TheFirstPairingSurvivesAPinningOnlyPlatform` all still green |

The Android delegate cannot be exercised by the desktop suite — that is B58's own lesson, "the bug is invisible on the platform the suite runs on and ordinary on the platform that ships" (`:2790-2791`). So W2's verdicts are proven twice: by a fake `RelayTrust` in the JVM/Robolectric lane against the real built AAR (`playbook:881`), and on the physical-handset matrix against a real relay with (a) a valid public certificate, (b) a wrong-name certificate, (c) an expired certificate, and (d) an enterprise proxy on the path (`playbook:892-913`).

## Notes

This ADR does not touch `docs/adr/README.md`; adding its row, and the `rg` sweep across every downstream source named in the playbook's §3 table (`:96`) — `ADR-007-remote-access.md`, pairing payload and state migrations, phone dial policy, relay and VPS runbooks, pin conformance tests — are obligations of the same R1 change that lands it. The rule from `playbook:84-85` binds: implementation "must not create a second, contradictory source of truth", and a runbook still instructing an operator to configure `reuse_private_keys` as a default is exactly that.

On what this ADR is *not*: it decides transport verification and nothing about content. Every ruling above could be reverted and the confidentiality of a session would be unchanged, because it was never a function of the certificate — which is what `ADR-007-remote-access.md:78` says and why that sentence is reaffirmed at the top rather than quietly inherited. The reason to make this change is what the pinning default has cost: five recorded defects (B33, B34, B45, B48, B57/B58), one of them ADR-007's only entry labelled a **SHIPPING BLOCKER** (B58, `:2768`) and one "the most serious finding of the closure slice" requiring an owner "before any deployment where the relay is not the operator's own trusted host" (B34, `:1883`, `:1908`) — in exchange for a property the threat model never depended on.
