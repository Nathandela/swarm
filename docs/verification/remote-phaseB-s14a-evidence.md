# S14a evidence — the Go custody seam (PB-KEY-9, first half)

**Commits**: `582676e` (slice), `3dfbab7` (B18(a) fence), `010876a` (re-audit fixes).
**Requirement**: PB-KEY-9. **Decisions**: ADR-007 B14, B17, B18.

> **PB-KEY-9 IS NOT YET DELIVERED.** This slice closes its first half. The closing half is S14's:
> a facade verb accepting an Android-Keystore-backed KEK, a regenerated golden, and the deletion of
> both `InsecureCleartextSealer` call sites. Until then the shipped app still writes the content key
> in the clear. See `remote-phaseB-progress.md` for the standing disclosure.

## Why this slice exists at all

It was created mid-implementation, from two blocking findings the S14 RED author produced with
machine-checkable evidence:

1. **A decided ADR item that no slice delivered.** ADR-007 **B14** made `crypto.KeyStore` failable
   and said "the signature change lands in the Go core, not the Android slice". It was never
   implemented. PB-KEY-6's criterion is "a test drives an auth-required failure and a
   key-invalidated failure through every signing path" — and **no test in `android/` can drive a
   failure through a Go function with no error return**. That is what blocked S14.
2. **Nothing on the phone was sealed at rest.** `device.key` was 128 raw bytes, the four private
   scalars concatenated; `phone-state.json` carried both epoch keys as plain base64. The second
   **collapsed PB-KEY-2's two-tier split at rest independently of anything Android does**: one file
   cannot be gated two ways, so the content key was recoverable without the biometric the entire
   tier design exists to require.

## What shipped

- `crypto.KeyStore`'s three signing/material methods became failable, with two distinguishable
  sentinels: `ErrKeyAuthRequired` (recoverable — re-prompt) and `ErrKeyInvalidated` (permanent —
  re-pair). Every call site handles the error; an AST fence rejects a `_` discard.
- An **injectable** `Sealer` at `phonecore.Resume` covering both the epoch-key state file and the
  device-key file, with the KEK supplied from outside the Go core. Omission **fails closed**
  (`ErrNoSealer`); cleartext requires typing `Insecure...` at a call site (B18(c)).
- Per-operation unsealing, never memoized, so a lock actually stops signing (PB-KEY-7).

## The crypto package is frozen — what was actually widened

Verified two ways, independently, by two models. `go doc` diff of `internal/remote/crypto` between
`582676e^` and the commit yields exactly: `+ErrKeyAuthRequired`, `+ErrKeyInvalidated`,
`+NewKeyStoreFromMaterial`, and doc text. The full source diff is the three method signatures, three
one-line bodies, the two sentinels, and one constructor that delegates to the pre-existing
`newFileKeyStore` with no new I/O. Within B14 plus B18(b). `golangci-lint` output is byte-identical
to the parent.

**Wire output is bit-identical**, as R-CRY.15 requires. Six KAT vectors (four public keys, two
signatures) are pinned as literals. Both reviewers recomputed them independently — one from a
`git archive` extraction of the parent commit, one from stdlib `ed25519` + `x/crypto/curve25519` in
a package that does not import this repo. All six reproduce bit-exactly, so they cannot have been
back-fitted to a drifted implementation.

*Recorded gap*: no KAT pins `NoiseStatic`'s DH output or `OpenSealedBox` — the two widened ops
without a wire vector. Handshake bytes are ephemeral-dependent and un-KAT-able; interop tests cover
misuse. The bodies changed by `, nil` only.

## Re-audit round 1 — five findings, two independent reviewers

B14 requires any widening of the frozen package to be re-reviewed **cross-model**. Both reviewers
returned REVISE. Both independently found the same blocking gap, unprompted.

| # | Finding | Class | Disposition |
|---|---|---|---|
| F3 | The B18(a) refusal path in `relay/client.go` was correct in source but **unfalsifiable** — every `ClientAuth.Sign` fixture in the tree returned a nil error, so the guard could be deleted and the whole suite still passed | (i) a guard that cannot fail | FIXED `3dfbab7` |
| F2 | `resealTier` returned the stored tier **before examining the caller's key**, silently discarding a content key installed after the user authenticates | live data-loss defect | FIXED `010876a` |
| F1 | The carry-verbatim branch preventing a permanent brick had **no fence at all** | (i) | FIXED `010876a` |
| F4 | A purge taken with the tier locked never reached disk, contradicting the function's own comment | PB-KEY-7 violation | FIXED `010876a` |
| F5 | The cleartext public half of `device.key` was never re-derived from the sealed material on read | **introduced attack surface** | FIXED `010876a` |

### F5 deserves emphasis: this slice introduced it

The pre-slice 128-raw-byte layout derived every public from the private material, so nothing could
disagree. The sealed container created a cleartext half that was checked only at **write** time.
`mobile/pairing.go` enrols exactly those fields, so one write to the app's private data directory
got the phone to enrol **attacker-chosen** signing and recipient keys at the next pairing — and a
forged recipient key seals every epoch grant to a key the attacker holds. Threat level is PB-SEC-1's
stated adversary (root, or a restored image).

The publics are now re-derived at the unseal and the container refused on mismatch
(`ErrPublicKeyMismatch`), using values `crypto.NewKeyStoreFromMaterial` computes anyway — no
`Sealer` widening, no crypto change. The check cannot live on the accessors: they are errorless by
design because a phone with a locked tier must still state its own routing id to receive the push
that asks for the biometric.

### The root cause under F2 and F4

One signal carried two meanings: an all-zero key meant **both** "I have nothing to write" and
"destroy this". They could not both be fixed while that held. `resealTier` now branches on the
caller's key first — a real key always wins, whatever the tier record says — and destruction became
an explicit `Store.PurgeKeys` verb that writes both tiers nil in one atomic write and drops the tier
records, so nothing survives for a later Save to carry back.

The `Store` interface was widened rather than using an unexported optional interface with a type
assertion, deliberately: a `Store` that silently no-ops a purge is the standing "requirement
satisfiable while the defect ships" class.

## Failing-first evidence (GG-5)

Every fix has a test that failed against the defect first. Highlights, verbatim:

```
TestS14A_AContentKeyInstalledAfterUnlockReachesDisk
  PB-KEY-3: the content key installed after the user authenticated was discarded and the
  PREVIOUS epoch's key was written back.
   got 5556...(epoch 1)   want c0c1...(epoch 2)

TestS14A_ALockedContentTierIsCarriedVerbatimAcrossASave   [carry branch deleted]
  PB-KEY-3: a Save taken with the content tier LOCKED destroyed the durable content key.
   got 0000...  want 5556...

TestS14A_ForgedCleartextPublicKeysAreRefused/recipient_pub
  PB-SEC-1: Resume accepted a device.key whose cleartext recipient_pub was replaced with a key
  the attacker holds the private half of ... every epoch grant is sealed to a key the attacker holds.

TestS14A_DialSurfacesASigningRefusalWithoutSendingAuthResp   [sig, _ := auth.Sign(...)]
  ADR-007 B18(a): Dial returned err relay: auth_failed, want the custody refusal itself.
```

**The F1 mutation is the load-bearing result.** Deleting the carry-verbatim branch and running the
full suite in a pristine clone failed **zero tests repo-wide** while producing a permanent brick of
the epoch content key. The implementer predicted exactly this before commit, having only tested the
behaviour with a throwaway; a reviewer confirmed the prediction. The two committed tests that ever
set an open error neither one Saved. That was the hole.

**The B18(a) fence took three mutations, not two.** Discarding the error and returning-without-
closing each trip a different assertion; the third — preserve the error AND close, but sign nil and
send `auth_resp` anyway — passes both of those fences and is caught only by the frame log. Frames
are observed with a verbatim proxy in front of the real relay, so the handshake under test stays the
real server's behaviour.

**Controls, so the fixes are not satisfiable by refusing everything**:
`TestS14A_AnHonestSealedContainerStillOpens` (untampered container opens, including with the content
tier locked) and `TestS14A_APurgeIsRecoverableNotABrick`.

## Accepted residuals

- **The shipped app still writes both keys in the clear** — the standing disclosure. The acceptance
  gate injects real sealers from Go; the Android app cannot, because gomobile cannot set a Go struct
  field and the facade is golden-pinned. Standing class (iv) in its purest form: **the acceptance
  test is green and the product is not.** Fully disclosed, never claimed as delivered. S14's to
  close.

  This residual is now **fenced rather than conventional**. It was previously bounded only by a
  grep-marker convention described in the progress doc.
  `TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites` walks every `.go` file in the repo —
  including `_test.go`, since one of the two sites *is* a test file and the other fence's test-file
  exclusion would have hidden it — and requires the set of files calling `InsecureCleartextSealer`
  to equal exactly `{mobile/app.go, mobile/conformance/harness_test.go}`.

  It fails in **both** directions, each proven: a third file means unsealed custody spread somewhere
  it was not inventoried; **fewer** means S14 landed the facade verb, PB-KEY-9 is delivered, and both
  this fence and the progress doc's disclosure section are stale. The failure message names that doc
  section explicitly, so the S14 author who trips it is pointed at the stale "not delivered" record
  rather than just told a number moved. It matches any `*ast.CallExpr` anywhere, so it does not share
  the other fence's `var`-form evasion gap — there is no legitimate call here to bound the match to,
  so the broad match costs nothing.

  *Granularity, stated precisely*: there are two **files** but **four** call expressions —
  `mobile/app.go:119` and `:120` (one sealer per tier) and two in the conformance harness. The fence
  is at file granularity deliberately: a third call inside an already-listed file is not new
  exposure, a third file is, and a count-of-expressions fence would fire spuriously on a reformat.
- **With the content tier LOCKED the three content publics cannot be checked**, because the material
  to check against is exactly what the lock withholds. Not exploitable on the pairing path today —
  `mobile/pairing.go` calls `NoiseStatic()` and returns on error *before* building the payload that
  reads the other publics — but that is an **ordering dependency, not a structural guarantee**, and
  it is unfenced. S14 item.
- **The call-site fence has an evasion gap**: it inspects only assignment and expression statements,
  so `var sig, _ = ks.SignRelayAuth(...)` inside a function passes. Recorded in the fence's own
  comment. "Sign" was deliberately NOT added to the op set — it would match
  `relay.ClientAuth.Sign`, `machineid.RelayAuthSign` and every ed25519 signer in the tree.
- **The floor was measured, not assumed.** A standalone parse-only re-implementation of the fence's
  walk run over both trees counted **4** call sites at `582676e^` and **5** at the commit, against a
  floor of 3 — so the floor never measured the new surface. Now 5, with a comment saying what it
  measures.
- **The two sentinels are not machine-readable across the gomobile boundary.** No facade verb
  classifies them, so Kotlin sees only an error string, while PB-KEY-6 wants the UI to act
  differently on each. S14's work, recorded now so it is not discovered late.
- **No v2 -> v3 migration.** The version bump is genuinely required (a v2 build would otherwise read
  v3's sealed bytes as the key; now `ErrFutureSchema` fires). Accepted only because there is no
  installed base. On the same machine, a v2 file still carrying key fields yields `ErrCorruptState`
  **permanently** — `NewApp` fails, so the advice must name an action that works; the message now
  says to clear app data rather than to re-pair, which was unreachable.
- **A cheap DoS with a destructive-only remedy**: anyone with write access to the app data directory
  can stamp `schema_version: 2` plus one key field and permanently prevent the app starting. Same
  adversary as F5. Recorded, not fixed.
- **Unsealed plaintext buffers are not zeroized after use.** Same posture as the pre-existing
  software store; hardware custody is the real fix.
- **Nothing mechanically pins the crypto package's exported surface.** The freeze is
  process-enforced; the golden pin covers the mobile facade only. Pre-existing.

## B8 holds

The `Sealer` seam is exactly `Seal`/`Open`, both `([]byte) ([]byte, error)`, with no KEK accessor —
pinned **reflectively**, so a third method cannot pass. Mutation-proven: adding `KEK() []byte` with
compliant implementations fires the test. The golden contains exactly three `[]byte` occurrences,
all **inbound parameters**; no bound method returns `[]byte`. The key crossing remains single and
inbound, and the matrix narrowed rather than widened.

## Gates

`go build ./...`, `go vet ./...` clean. `go test -count=1` and `-race` green on
`internal/phonecore`, `internal/remote/crypto`, `internal/remote/relay`, `internal/remote/machineid`.
`./mobile/...` green including `mobile/conformance` (which drives `PurgeKeys` and
`InstallContentKey` through the real facade). **`android/gate` PB-SEC-1 — S14a's acceptance gate —
both tests PASS**, having been RED by design before this slice. `golangci-lint` findings identical
to the parent; all pre-existing, none in files this slice touched.

Re-verified independently on a clean tree at `010876a` before push.

## Process notes worth keeping

- **Two models found F3 independently and unprompted.** That is the strongest signal this project
  has produced for treating a finding as real without further verification.
- **Three agents ran mutation probes on one shared worktree.** That was an orchestration error, not
  an agent error: a probe is by construction an indistinguishable-from-broken tree. Two reviewers
  disclosed `git checkout` calls in the shared tree unprompted, with evidence, and neither destroyed
  another agent's work. Reviewers now get isolated clones.
- **A known flake was mischaracterised.** `internal/remotegw TestS6B_GatewayInputLatencyIsNotPollGated`
  was briefed as "reproduces at HEAD". A pristine-clone baseline showed the **full suite green** at
  HEAD; it fails only under concurrent agent load, a condition the orchestration itself creates. The
  wrong version risked an agent dismissing a real latency regression as pre-existing.
