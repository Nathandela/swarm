# PB-SEC-2 — the per-use tier, wired (ADR-007 B51 and B59)

> ## WITHDRAWN 2026-07-31 (ADR-007 B133) — READ THIS BEFORE CITING ANYTHING BELOW
>
> **PB-SEC-2 is VOID, and every mechanism this file records has been deleted from the product.**
> The trust boundary is now the wire between the phone and the computer. Both endpoints are
> trusted, and all phone-side user authentication was removed *with its code* — `keys/PerUseGate.kt`,
> `keys/BiometricPrompts.kt`, `keys/BiometricPolicy.kt`, `keys/TimedTierGate.kt`,
> `runtime/ContentLock.kt`, and the four `android/gate/s20_pbsec2_*` fences (commits `e0e644d`,
> `11f0517`, `52b8abf`, `7863f8c`).
>
> **What that costs this file, stated plainly rather than glossed: nothing below can be
> re-demonstrated.** Every mutation in §4 targets a file that no longer exists; §2's claim —
> "the action runs only after the platform releases a key for that one use; each use prompts
> again" — is a claim about a feature that no longer ships; §7's last two bullets describe a
> handset-capability price the product no longer pays. There is no substitute measurement and
> none is offered here.
>
> This is not a finding that the work was wrong. It was correct against the decision it was
> written under, and B133 replaced the decision. **The body is left unedited below this banner as
> the dated record of what was demonstrated on 2026-07-26.** Do not read it as current evidence.
>
> **Three things in it are about method rather than about the gate, and they survive:**
>
> 1. **§5 is still live and still true.** The `androidx.biometric:biometric:1.1.0` dependency, its
>    lockfile row, its `verification-metadata.xml` component and `android/dependency-inventory.tsv:37`
>    are all **still in the tree**, still justified by this now-void requirement. The
>    corrupted-checksum run that proved verification bites is a PB-SEC-14 result and is unaffected.
>    Removing the dependency is a supply-chain action needing a reviewed lockfile and metadata
>    regeneration, so it is its own slice, not cleanup collateral — see the de-auth plan's
>    follow-up section. `TestB133_TheAppImportsNothingFromAndroidxBiometric` in
>    `android/gate/s16_ui_test.go` now fences that no Kotlin file imports it.
> 2. **§4.4's defect-in-the-fence finding** — a fence a KDoc sentence could satisfy, then a regexp
>    that walked back over blanked comments — is about how source-scanning fences fail. It does not
>    depend on what was being scanned.
> 3. **§4.3's S1** recorded a mutation that initially PASSED because a cross-file name search was
>    satisfied by a call inside a function nothing invoked. The gate it fenced is gone
>    (`PhoneRuntime.kt:172` now carries only a comment naming
>    `refuseAHandsetThatCannotHoldTheContentKek` as removed), but "a call inside a dead function
>    satisfies a name search" is a standing lesson about `bodyMustName`-style fences.

Evidence for closing ADR-007 B51: PB-SEC-2's per-use authorization tier, found **entirely
unimplemented** by the round-2 audit, and ADR-007 B44's missing in-app re-authentication.

Date: 2026-07-26. Branch: `worktree-remote-control-research`.

---

## 1. What was wrong

`KeystoreSpecs.forOperation` — the per-use `CryptoObject` spec for REVOKE, KILL_SWITCH, LAUNCH and
KILL — was referenced only from `src/test/`. `AuthorizationLedger.beginPrompt`/`endPrompt`/`consume`
had no production caller. There was no `BiometricPrompt` in the app; `androidx.biometric` was not a
dependency.

So those operations were gated by exactly what typing is gated by: the content KEK's 60-second
**timed** window. `BiometricGateTest` was green throughout, **because its subject was unreachable**.

B44 made it bite rather than merely latent: the content KEK carries `AUTH_BIOMETRIC_STRONG` and
nothing else, B44's trigger drops the key on every screen-off, and the resume path asserted the
Keystore "will answer". On refusal there was no in-app way to authenticate.

## 2. What is claimed, and what is NOT

**Claimed.** The per-use tier is *reached* from production; the action runs only after the platform
releases a key for that one use; each use prompts again; every refusal names a way forward except the
one where there is none; the content tier's refusal now has an in-app exit on both the ready and the
startup paths.

**Not claimed, anywhere, by any file added here.** That a real `BiometricPrompt` was shown, accepted
or refused on any device. That a real Keystore withheld a real key from an unauthenticated user. That
`setUserAuthenticationParameters(0, AUTH_BIOMETRIC_STRONG)` behaves as documented on any handset.

PB-E2E-5 is deferred (ADR-007 B31). **ADR-007 B56 makes the entire `androidTest` tier unexecutable**:
the emulator's keymint reports `SECURITY_LEVEL_SOFTWARE`, PB-KEY-8 fails the app closed, and no screen
renders — so an instrumented test cannot reach a prompt at all. `BiometricPrompts.kt` is executed by
**no test in this repository**. It is confined to one file and fenced out of every unit test precisely
so that the untestable surface is auditable by reading it.

## 3. TDD — the failing-first run (GG-5)

Tests written before any production symbol existed. First compile of
`PerUseGateTest.kt` + `ContentUnlockTest.kt`:

```
e: PerUseGateTest.kt:66:27 Unresolved reference 'PromptAvailability'.
e: PerUseGateTest.kt:70:9  Unresolved reference 'PerUsePrompt'.
e: PerUseGateTest.kt:94:9  Unresolved reference 'PerUseCipherSource'.
e: PerUseGateTest.kt:104:38 Unresolved reference 'PerUseRefusal'.
e: PerUseGateTest.kt:111:9 Unresolved reference 'PerUseGate'.
e: PerUseGateTest.kt:146:22 Unresolved reference 'PerUseRefusalReason'.
e: PerUseGateTest.kt:337:58 Unresolved reference 'PerUseRefusalText'.
   ... (40 errors, every one an unresolved reference to unwritten production code)
```

Every failure is a missing implementation, not a syntax error.

## 4. Mutation matrix

Each fence run against **the mutation it exists to catch** (must FAIL) and against a
**property-preserving mutation** (must stay GREEN). A mutation that does not weaken what the test
asserts proves nothing, so both columns are recorded.

### 4.1 Kotlin — `PerUseGateTest`, `ContentUnlockTest`

| # | Mutation | Meaning | Result |
|---|---|---|---|
| K1 | `proof = if (ledger.authorized(op, now())) CHALLENGE else null` | **the in-memory `authenticated = true` flag**, which is PB-SEC-2's named criterion | **FAIL** — `a_believed_authorization_does_not_run_the_action_when_the_platform_releases_no_key`, `a_crypto_object_whose_key_refuses_does_not_authorize_the_action` |
| K2 | `ledger.consume(operation)` commented out | the per-use authorization is never spent | **FAIL** — `the_authorization_is_consumed_before_the_action_runs`. `BiometricGateTest` stayed GREEN, which is the point: the pre-existing policy suite cannot see this |
| K3 | K2 plus an early `if (ledger.authorized(...)) { action(); return }` | **per-use implemented as timed** — one prompt authorizes every later use | **FAIL** — `every_use_is_prompted_for_again` + 2 others |
| K4 | `released.doFinal(...)` → `cipher.doFinal(...)` | trusts the outcome enum: uses the cipher passed IN, not the one the platform released | **FAIL** — `a_crypto_object_whose_key_refuses_does_not_authorize_the_action` |
| C1 | `offersUnlock(...) = false` | **B44's dead end restored** — the content refusal has no exit | **FAIL** — `the_unlock_control_is_offered_for_an_authentication_refusal`, `..._discriminates_...` |
| C2 | `offersUnlock(...) = error != null` | a prompt for refusals no prompt can satisfy — PB-APP-10's failure loop through the remedy | **FAIL** — `the_unlock_control_is_not_offered_for_a_refusal_no_prompt_can_satisfy`, `..._discriminates_...` |
| **K-VAC** | different challenge bytes; local renamed | property preserved: the RELEASED key is still exercised | **GREEN** (as required) |
| **C-VAC** | route on `ErrorState.REAUTH_REQUIRED` instead of `Remedy.AUTHENTICATE` | property preserved: the same refusals offer the same control | **GREEN** (as required) |

### 4.2 Go — `android/gate/s20_pbsec2_peruse_test.go`

| # | Mutation | Result |
|---|---|---|
| F1 | **the exact state B51 found**: `PerUseGate.kt` and `BiometricPrompts.kt` deleted, `PhoneSurface`/`PhoneActivity`/`PhoneRuntime` reverted | **all four checks FAIL** — including `KeystoreSpecs.forOperation`, `endPrompt` and `.consume(` reported unreached |
| F2 | `perUseButton(` → `gatedButton(` — a one-word edit that compiles, keeps every Kotlin test green, and silently returns revoke and kill to the timed tier | **FAIL**, naming both call sites and their declarations |
| F2-VAC | both button labels reworded | **GREEN** (as required) |
| F3 | `AUTH_DEVICE_CREDENTIAL` added to the **key only** — ADR-007 B59 reversed by half | **FAIL** |
| F3-VAC | `DEVICE_CREDENTIAL` added to the **prompt too** — agreement restored | **GREEN** (as required): the fence checks agreement, not a hard-coded answer |
| F4a | `import androidx.biometric.BiometricPrompt` added to `PhoneSurface.kt` | **FAIL** — production sprawl |
| F4b | the same import added to `PerUseGateTest.kt` | **FAIL** — "a Robolectric shadow driven to succeeded, read as proof the gate works" (PB-E2E-5) |
| F4-VAC | a **second** `androidx.biometric` import inside the owner file | **GREEN** (as required): the property is *which file*, not *how many imports* |

### 4.3 The startup gate (ADR-007 B59's bill)

| # | Mutation | Result |
|---|---|---|
| S1 | `refuseAHandsetThatCannotHoldTheContentKek()` commented out in `construct` | **FAIL** — `refuseAHandsetThatCannotHoldTheContentKek (not named in the body of construct)` |

**S1 initially PASSED, and that is recorded because it is the more useful fact.** The cross-file name
search still found `provisioningFor` and `deviceBiometricAvailability` in `PhoneRuntime.kt` — inside a
function nothing called. That is the limit the fence's own header documents ("a call inside a function
nothing invokes satisfies it"), observed rather than assumed, and it is the exact shape of the defect
being fenced. `bodyMustName` now pins the call inside `construct`'s body, and S1 fails as shown.

### 4.4 A defect the matrix found in the fence itself

The first draft of check (1) reported `PerUseGate` and `beginPrompt` **reached** when run against the
pre-fix sources — because a KDoc sentence elsewhere in the module names them. A fence a comment can
satisfy is a fence the next thorough comment turns off, and it is the same failure class the fence is
pointed at. `kotlinCodeOnly` now strips comments while leaving string literals intact; F1's recorded
result is from the corrected check. A second defect surfaced immediately after: with KDoc turned into
blank lines, `^\s*` in the member-declaration regexp walked back over them and reported an empty
declaration, failing check (2) over correct code. Both are documented at the site.

## 5. PB-SEC-14 — verification regenerated, and proven live

Regenerated, not disabled:

```
./gradlew :app:dependencies --write-locks
./gradlew --write-verification-metadata sha256 :app:dependencies
```

Diff reviewed: `gradle.lockfile` gained **one** row (`androidx.biometric:biometric:1.1.0`);
`verification-metadata.xml` gained **one** component, two artifacts. Nothing else moved —
`androidx.fragment` and the lifecycle modules already resolved through `androidx.appcompat`.
`android/dependency-inventory.tsv` gained the corresponding row, naming what the dependency is for,
what it pulls, and the framework alternative that was rejected and why.

**Enforcement proven live on the new artifact.** The `biometric-1.1.0.aar` sha256 was deliberately
corrupted and an ordinary compile run:

```
$ ./gradlew :app:compileDebugKotlin
> Dependency verification failed for configuration ':app:debugRuntimeClasspath'
  One artifact failed verification: biometric-1.1.0.aar (androidx.biometric:biometric:1.1.0)
    from repository Google
  This can indicate that a dependency has been compromised.
EXIT=1
```

Restored immediately; the tree is clean.

## 6. Gate runs

```
go build ./...                          EXIT=0
go vet ./...                            EXIT=0
go test ./...                           EXIT=0
~/go/bin/golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 ./...   0 issues
./android/build-aar.sh                  EXIT=0
./gradlew lint test --rerun-tasks       EXIT=0   (stale test-results deleted first)
```

Exit statuses checked, not last lines. Zero skips.

## 7. What is still open

- **PB-E2E-5 stays deferred**, and B56's consequence stands: the `androidTest` tier cannot execute,
  so `BiometricPrompts.kt` is shipped code that no test in this repository runs.
- **`App.Launch` and `App.KillSwitchEngaged` remain unbound** (`android/unbound-verbs.tsv`). Launch has
  no screen; the kill switch may never be SET by the phone (PB-SEC-6). Neither was given a production
  gate, because a fence over a path production does not take is this phase's standing defect. The
  fence covers launch prospectively and says so.
- **B44's foreground `AUTH_TIMEOUT_EXPIRED` timer** is still not built. Its recorded reason — no
  prompt exists — is now false, and `ContentLock`'s header is corrected in place rather than left
  standing. The residual now rests on the smaller claim it can carry (ADR-007 B59).
- **A handset with no enrolled Class-3 biometric cannot use revoke, kill or the content tier** until
  the user enrols one; a handset with no Class-3 sensor cannot use them at all. That is the price of
  refusing `AUTH_DEVICE_CREDENTIAL`, argued in ADR-007 B59, and both are now told what to do rather
  than refused silently — on the per-use path, on the content-unlock path, and at provisioning.
- **A pre-existing hole found while checking that claim, and closed.** A PIN-only handset did not reach
  any of those messages: it failed inside `KeyGenerator.init` during provisioning and surfaced as
  `SwarmErrorTokens.UNKNOWN` with remedy `NONE`. `DeviceCapabilities.probe` could not see it because
  USER_AUTH_PER_USE is answered from the API level. See §4.3 and ADR-007 B59.
