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

## Re-audit round 2 — five more findings, and one of them was INTRODUCED by round 1's fix

The round-1 remediation was itself re-reviewed (cross-model, isolated worktree). It found two
blocking defects in work that had already passed two reviews and my own reading.

| # | Finding | Class | Disposition |
|---|---|---|---|
| B1 | **The public-key binding was bypassable.** `openKeyStore` fell back to adopting a raw 128-byte `device.key` on nothing but its length, and that layout has **no public half** for `checkPublic` to verify against | introduced surface, still open | FIXED `47809f0` — **deleted** |
| B2 | The lock purge was **fail-open**: the memory clear, which cannot fail, was gated behind the durable write, which can | (iv) | FIXED `47809f0` |
| B3 | An **epoch rotation** was still inferred from an all-zero key, carrying the old epoch's sealed key forward under the new epoch id | (ii) | FIXED `47809f0` |
| B4 | The purge was **resurrectable** by a Save built from a pre-purge `State` snapshot — a direct consequence of round 1's own fix | (iv) | FIXED `47809f0` |
| B5 | Decrypted replies survived the purge, and were **refilled into the cache by the purge's own rebind** | PB-KEY-7 clause | FIXED `47809f0` |

### B1: deleted, not authenticated

A format with no public half **cannot** be authenticated, so the only honest fix was removal. Both
premises were verified before acting, and the producer claim is stronger than first stated: outside
the frozen `internal/remote/crypto`, **every** reference to `NewFileKeyStore`/`OpenFileKeyStore` is
in a `_test.go` file save one comment — and because the package is frozen, a new producer cannot
appear without an ADR. No Phase B phone app has shipped, so there is no installed base (the same
reasoning this file already uses for the absent v2 -> v3 migration). All seventeen S14a test names
were checked for one whose *subject* was the legacy migration: none.

Two fixtures outside the package changed, **including the PB-SEC-1 acceptance gate**. That is the
change most deserving of suspicion, so the record is explicit: no assertion was relaxed. The gate now
lets the core generate material and recovers it through the production writer, instead of hand-seeding
a format that no longer exists — the old fixture was itself a small instance of testing a path
production does not take. Round 1's public-binding fence still fires on all four subtests with
`checkPublic` neutered, and an independent review later confirmed the gate was measurably
**strengthened**: with the content sealer bypassed so the seam seals nothing, the new gate FAILS
where the parent's gate PASSES.

> **CORRECTION (round 3 review, and it is worse than the first correction).** An earlier version of
> this paragraph claimed a `sealDeviceKeys` that also drops an unwrapped copy beside the container
> "still fires both byte-level fences". **That was false**, and I propagated it into the commit
> message too. `android/gate/keycustody_test.go:148` searched the **raw** needle only, while its
> sibling at `:200` and the in-package `s14aFindMaterial` search raw **and** base64 — so a JSON field
> holding base64-encoded privates passed the acceptance gate untouched. The base64 arm is added
> (`4d8a37d`).
>
> **Even with the arm, do not read a green `android/gate` as "no cleartext privates".** Under a leak
> of all four device privates as one base64 field, the arm catches exactly **one** of them —
> `RelayAuth` — because it sits at offset 96 of the concatenation, which is both 3-byte aligned and
> terminal. `NoiseStatic`, `Recipient` and `CommandSign` are invisible to **both** arms in the very
> same file that holds them in the clear. A 32-byte needle's padded base64 only appears inside a
> longer field's base64 when it happens to be aligned. The fence fires on an accident of layout.
>
> **What actually covers this property is the positive half** — that the material went through an
> injected sealer — not the byte search. Both byte-level fences now carry a comment saying so.

**Why it mattered more than it looked**: while the app ships `InsecureCleartextSealer` the container
is forgeable anyway, which masked this. Once S14 lands a real Keystore KEK and the container becomes
unforgeable, this was the **only remaining unauthenticated ingress to the device identity** — the
seam's entire purpose defeated by a file length.

### B3: scoped to the epoch, rather than a second destroy verb

`sealedTier` gained an `epoch`, and carry-verbatim now requires `prev.epoch == epoch`. Both
alternatives were considered and rejected on stated grounds: a second destroy verb makes rotation a
**caller obligation** that the next rotation path will forget, and clearing `opened == false` "once
the sealer demonstrably works" requires probing the content KEK — the one thing that legitimately
refuses, so a failed probe is indistinguishable from a locked tier. Scoping to the epoch states the
actual invariant (an epoch key is meaningless outside its epoch) and covers rotation paths that do
not exist yet.

### B4: a stale Save loses its key material, rather than being refused

`State` carries an unexported purge stamp; a Save built from a pre-purge snapshot has its key
material and decrypted caches dropped while the rest of the write proceeds. Refusing the Save
outright was rejected because it would satisfy the security assertion while bricking every
subsequent write — the same acceptance-test-green-product-broken shape this slice keeps producing.

This required touching PB-STATE-1's reflective fence, which is why it is recorded here rather than
left in a diff: reflection cannot call `.Interface()` on an unexported field at all, so skipping them
is **forced**, not chosen. The exemption was converted into a stated property by adding the converse
assertion — the bookkeeping must NOT survive a restart, since a restored purge count would make a
fresh process refuse the first Save of every legitimate caller. Exported fields, the only ones any
caller can set, are covered exactly as before.

## Re-audit round 3 — no live defect, three fences that could not fail

The third independent review found **no live defect in the shipped code** and listed thirteen
attacks that failed: the frozen package untouched, B8 holding, B1's deletion complete with no other
unauthenticated ingress, no brick constructible from B3 or B4, and no existing test weakened. What it
found is that **three of round 2's five fixes were unfenced in the direction that matters** — the
project's own #1 recurring class, reproduced inside the remediation for it.

| # | Finding | Disposition |
|---|---|---|
| F1 | The facade purge test passed **with its own fix reverted** — both assertions read the CORE's caches, never `a.journal`, the field the fix protects | FIXED `4d8a37d` |
| F2 | The carry-verbatim fence ran at epoch 0, so after round 2 added an epoch predicate it only ever compared `0 == 0`. A mutation restricting the carry to epoch 0 ships a permanent content-key brick with five packages green | FIXED `4d8a37d` |
| F3 | The purge-stamp converse assertion **cannot fail** — its fixture never purges. The harmful variant it names is a silent permanent brick | FIXED `4d8a37d` |
| F4 | The in-memory pre-purge race: a Save whose rebind read pre-purge state rebinds the router to the **purged** content key after the purge returned | FIXED `4d8a37d` |
| F6 | A vacuous assertion inside round 2's own new test — the fixture never populated what it asserted was cleared | FIXED `4d8a37d` |

### F4's discipline, recorded because a later reader will need it

`Core.rebindMu` spans rebind's **read** of durable state and its **application** to the derived
components, so no rebind can land between another's read and apply. Either interleaving with a purge
ends purged, because the purge updates state under `mu` *before* rebinding and a losing Save's rebind
re-reads. Lock order is `rebindMu -> mu`, total and never inverted: rebind is entered with `mu`
released at all three call sites, and nothing holding `mu` takes `rebindMu`. Verified under a
16-writer/4-purger `-race` stress run.

**A trap for anyone re-deriving F4's test**: the first version used `sync.Once` for the hook and
**passed against the defect**. `Once.Do` blocks later callers until the first body returns, and the
purge's own rebind runs the hook — so the purge was parked behind the Save under any implementation.
It must be a CAS. A reviewer who "simplifies" it back to `Once` silently stops measuring anything,
which is precisely the failure mode this round exists to correct.

### One property that cannot be fenced

`Session.Need` is not observable after a purge: the core's session cache is empty, so the facade
returns "no session in the roster" whatever `a.needs` holds, and a post-purge roster record
repopulates both together. `a.journal` is the whole measurable surface of the facade purge fix.
Recorded rather than fenced.

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
  rather than just told a number moved. It matches any `*ast.CallExpr` anywhere rather than only
  assignment and expression statements, so it closes the other fence's **`var`-initializer** gap —
  there is no legitimate call here to bound the match to, so the broad match costs nothing.

  *Corrected by re-review*: this is NOT gap-free, and an earlier draft of this file overstated it.
  The matcher keys on a call whose function is **named** `InsecureCleartextSealer`, so binding it as
  a function **value** evades the fence entirely — `var mint = phonecore.InsecureCleartextSealer`
  followed by `mint()` in a third package stays green, mutation-confirmed. The honest claim is "no
  var-initializer gap", not "no evasion gap".

  *Granularity, stated precisely*: there are two **files** but **four** call expressions —
  `mobile/app.go:119` and `:120` (one sealer per tier) and two in the conformance harness. The fence
  is at file granularity deliberately: a third call inside an already-listed file is not new
  exposure, a third file is, and a count-of-expressions fence would fire spuriously on a reformat.
- **With the content tier LOCKED the three content publics cannot be checked**, because the material
  to check against is exactly what the lock withholds. The pairing ordering holds — re-review
  verified that `mobile/pairing.go:91-96` hoists `NoiseStatic()` and returns on error *before* the
  literal at `:97-108` that reads the other publics, and `phonecore/command.go:45-50` is safe the
  same way — but that is an **ordering dependency, not a structural guarantee**, and it is unfenced.

  *Corrected by re-review*: pairing is **not the only reader**, so the earlier framing here was
  incomplete. `mobile/app.go:799-801` `deviceID()` reads `CommandSigningPublic()` with **no**
  preceding content unseal, and `mobile/commands.go:129-134` `RevokeThisDevice` consumes it with the
  tier locked. Impact is low today — the value lands in a durable refusal record rather than on the
  wire — but "every content operation refuses in that state anyway" is false as stated: the errorless
  accessors are consumed by callers that are neither pairing nor content operations. S14 item.
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
- **A pre-seam `device.key` is now a named refusal**, not an adoption: "clear the app's data to
  discard it, then pair again". There is no installed base to migrate, and no way to authenticate
  the format if there were.
- **PB-KEY-7's purge is memory-first at all three layers**, and the `Store` contract now states
  explicitly that a returned error means **the blobs at rest survived** — never that nothing was
  purged. The App-facing half of that contract is in `mobile/doc.go`. Dropping the tier records even
  on a failed write is deliberate: the next Save that succeeds finishes the purge instead of
  resurrecting the blob.
- **`mobile/commands.go` `resolveSend` copies the content key into a `sendCtx` and seals with it
  afterwards**, so an operation already in flight continues past a concurrent purge. In-memory and
  in-flight, not a durable resurrection, and out of scope for the custody rounds — but it is the one
  place a purged key is still used. **Owner: whoever next touches the facade send path.**
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

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** (ADR-007 B129). One row
per requirement, verdict `DERIVED` or `NOT DERIVED`, and for `DERIVED` the mutation that was made
to fail, in the same row. `internal/remote/crypto` was not touched.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-KEY-9 | DERIVED | the seam's fail-closed default, mutated at BOTH of its production sites at once — `openKeyStore` and `OpenStore` substituting an inline identity `Sealer` instead of returning `ErrNoSealer` -> caught by `TestS14A_NoSealerIsNotSilentlyCleartext`. Failability half: `sealedKeyStore.SignCommand` memoizing the unsealed tier -> caught; `OpenSealedBox` swallowing the refusal -> caught by `TestS14A_LockedContentTierRefusesEverySigningPath`. Sealing half: `sealDeviceKeys` bypassing `content.Seal` / `wake.Seal` -> both caught by the `android/gate` PB-SEC-1 pair reading the bytes on disk |

### The one-site mutation, and why the row is still DERIVED

Substituting the identity sealer at **`openKeyStore` alone** — leaving `OpenStore`'s check in
place — survives `internal/phonecore`, `android/gate` and `mobile` except for one test:
`TestS14A_TheCleartextSealerHasNoCallSitesLeft`, which is a **text scan for the identifier
`InsecureCleartextSealer`**. Spell the same behaviour with a different type name and that fence is
blind, and the behavioural test (`TestS14A_NoSealerIsNotSilentlyCleartext`) does not fire because
`OpenStore` still refuses first and returns the named error the test accepts.

Both sites removed together does fail the behavioural test, which is what the row's verdict rests
on. Recorded because the shape is the same as S18's FINDING E: two layers enforce one property,
the outer one is what every test observes, and the inner one is fenced by its own name rather
than by its behaviour.
