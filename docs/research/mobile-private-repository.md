# Decision record: should the Android phone move to a private repository?

**Status:** research recommendation, not an approved migration.
**Decision:** defer a private-repository split as a cost-saving measure. If source-access separation is independently required, sequence an Android-only split first; do not begin with a full Go phone-core split.

## What problem this solves—and what it cannot

A private repository limits casual source browsing and separates Android-only contributors, CI secrets, UX experiments, and build configuration from the public tree. It is not a confidentiality boundary for a released phone application. An installed APK/AAB contains Kotlin/Dex, resources, manifest, endpoint strings, native libraries and the Go AAR; Android's official [APK Analyzer](https://developer.android.com/studio/debug/apk-analyzer) can inspect final manifests, DEX composition and resources. Existing public Git history, published release artifacts, issues, forks, caches, screenshots, documentation and protocol observations also remain outside the protection of a later visibility change. GitHub specifically says that making a public repository private leaves existing public forks detached and public ([repository visibility consequences](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/setting-repository-visibility)).

In particular, moving the Android directory does **not** hide the deployed package identifier, Firebase project/app identifiers, API hosts, the public wire protocol, or the behaviour of the shipped `libgojni.so`. It must not be sold as a way to keep a cryptographic protocol secret. The actual security boundaries remain Android Keystore custody, authenticated pairing, capability-bearing push records, TLS policy, and server-side credential isolation.

## Current coupling: evidence from this repository

The Android app is not a standalone Kotlin project today.

| Coupling | Evidence | Consequence of a split |
| --- | --- | --- |
| AAR is built from the root Go module | `android/build-aar.sh:40-79` executes `gomobile bind ... ./mobile` from the repository root | Android cannot build from source after merely copying `android/`; it needs a published AAR or access to the Go tree. |
| The public gomobile facade depends on internal core | `mobile/interactionread.go:33`, `mobile/machines.go:29` import `internal/phonecore`; `mobile/bind_test.go:101-117` deliberately enforces one facade over it | A different Go module cannot import the current core because of Go's `internal` visibility rule. |
| The AAR API is a checked contract | `mobile/testdata/exported_surface.golden`; `android/gate/aarsurface_test.go:254-263` | A binary hand-off needs SemVer/API, ABI, hash, provenance, and compatibility discipline. |
| CI tests both source and artifact | `.github/workflows/ci.yml:83-96` installs gomobile for Go tests; `:313-360` builds AAR, runs Gradle, APK and Android artifact gates | One PR no longer proves both sides unless consumer-integration CI is explicitly recreated. |
| App owns platform security and release wiring | `android/app/src/main/kotlin/dev/swarm/phone/keys/`, `push/`, `relay/`; `android/app/build.gradle.kts:10-24,161-173` requires production Firebase/push settings for release | A split must move access-controlled release configuration without weakening checks. |

The app's Firebase configuration and release keystore are already intentionally operator-provided rather than tracked (`android/app/build.gradle.kts:10-24,145-179`). That is an argument for improving secrets/permissions in CI, not proof that all mobile source needs to become private.

## Two materially different proposals

### A. Android-only private repository, consuming a signed prebuilt AAR

Move `android/` plus Kotlin, resources, Gradle wrapper, Android gates and release workflow. Keep `mobile/`, `internal/phonecore`, protocol/schema and conformance tests public. The core repository publishes `swarm.aar` from the existing bind command to a versioned artifact location (public is acceptable when the Go core is public; private only if a separate artifact-access policy requires it); the Android repository consumes an exact version plus SHA-256/provenance.

**Hides:** unshipped Kotlin/UI source, Android test code, UI/product roadmap details, local Gradle configuration and private-repo CI logs. It can also narrow who may change release automation.

**Does not hide:** the public Go phone-core source, AAR API, native Go code in released AAR/APK, package/Firebase identifiers, endpoints, manifests, protocol or shipped behaviour. A released app still reveals most Android implementation shape.

**Advantages:** lowest semantic risk; preserves public peer review of crypto/protocol code; creates a useful binary boundary for eventual iOS or another UI; permits independent Android access controls.

**Costs and risks:** every facade change becomes a release dependency; reproducibility and supply-chain controls are required; source-level end-to-end tests become cross-repository; an AAR mismatch can cause runtime/JNI failures that a single-tree change previously caught.

### B. Full private Go phone-core/mobile split

Move `mobile/`, `internal/phonecore`, likely phone-specific parts of `internal/remote/*`, and their tests into a private module. Replace current `internal` imports through a deliberately extracted API, or duplicate/adapt selected implementation behind a tested boundary; it does **not** require making the public core depend on a private module.

**Additional material hidden:** unshipped phone-state, presentation, anti-replay and pairing implementation source that currently lives in `internal/phonecore`.

**Still not hidden:** any of that implementation shipped as native code, its externally observable protocol and the historical public record. It also forces a deliberate choice: publishing a stable protocol/core API leaks its shapes by design; making it all private makes public relay/machine code depend on a private module and complicates outside builds and reviews.

**Costs and risks:** this crosses the protocol/crypto boundary. It has the greatest chance of introducing version-skew, reducing external auditability, or accidentally treating an unreviewed private implementation as security. It should only follow a written threat model that names source access itself as a material threat.

## Package, signing and release continuity

The Android package must stay `dev.swarm.phone`; changing its application ID creates a separate installed app and breaks update continuity. Preserve the same Play app-signing identity and Firebase Android client identity. Keep the operator release checks that bind package/app identifiers and push endpoint (`android/app/build.gradle.kts:67-107,161-173`) and the Play provenance checks in `cmd/swarm-publish/`.

Do not copy signing keys between repositories. Use least-privilege CI identities and an approved secret store; require protected tags and provenance attestations. The Android consumer should accept only a pinned AAR version/digest built by the protected core release workflow. An artifact repository is an availability dependency, so mirror/retain release artifacts and document emergency rollback.

## Illustrative CI cost, not a quote

For 100 builds/month at 30 minutes each, one private-repository Ubuntu 2-core workflow consumes **3,000 runner-minutes/month**. For 300 builds it consumes **9,000 minutes/month**. At GitHub's currently published baseline Linux 2-core rate of **$0.006/minute**, the **gross** usage is approximately **$18** and **$54** if the account's allowance is already exhausted. If a Free organization has its whole 2,000-minute allowance still available to this repository/account pool, the illustrative **net** is **$6** and **$42**. GitHub lists 2,000 included minutes for Free organizations and 3,000 for Team, resets the allowance each billing cycle, and bills usage to the repository owner—not the triggering user. Repository hosting itself is $0 on Free; optional paid permissions/protections, storage and runner use are separate considerations, and this repository decision changes no runtime backend cost. Public repositories on standard hosted runners are free; private repositories are not. See GitHub's [Actions billing documentation](https://docs.github.com/en/billing/concepts/product-billing/github-actions).

Those figures are intentionally not a budget forecast: account/organization plan, existing pooled consumption, runner type, cache/artifact retention, self-hosted runners, macOS/Windows jobs, concurrency and whether both repositories run validation determine the real total. A split commonly duplicates core+consumer tests during migration, so first-month minutes may rise rather than fall.

## Safe phased path if source access separation becomes necessary

1. **Write the threat model and ownership model.** State exactly which source is sensitive, who loses access, how independent security review continues, and why artifact reverse engineering/history do not invalidate the goal.
2. **Freeze and document the facade.** Generate the AAR from a tagged core revision; publish API/ABI surface, supported Android API/ABIs, license/SBOM, SHA-256 and provenance. Keep `mobile/testdata/exported_surface.golden` as a release gate.
3. **Create consumer integration without moving production.** In a disposable private test repository, consume the pinned AAR, run Kotlin lint/tests, and run compatibility fixtures against the same known pairing, push and relay contracts.
4. **Dual build.** For several releases, build the AAB from the current monorepo and the consumer repository from the identical AAR; compare package name, manifest security properties, native ABI set, facade surface, signing target and test results. Do not publish from the new path until parity is demonstrated.
5. **Move Android only.** Preserve app ID, Play signing, Firebase project, release provenance and rollback artifact. Core changes publish a release candidate AAR; Android promotion selects it explicitly.
6. **Reassess full core split separately.** Require a new threat-model decision and protocol-review gate. Do not infer approval merely because the Android split worked.

## Acceptance gates

- A new AAR is byte/provenance identified, signature/digest verified by consumer CI, and has an explicit compatibility policy.
- `gomobile bind`, Android lint/tests, APK/AAB artifact assertions, release-signing checks, and mobile/relay/push conformance fixtures all pass for the selected exact AAR.
- The new AAB retains package `dev.swarm.phone`, app-signing continuity, Firebase client identity and the same exported-component/network-security policy.
- A rollback can rebuild the prior Play-compatible AAB from retained artifacts without a source-tree emergency edit.
- The old and new pipelines never publish two different AABs for one version/tag.
- The team accepts the incremental private CI/artifact cost before the old pipeline is retired.

## Recommendation and estimate

**Recommendation:** defer the move. It does not produce meaningful operator-cost savings and offers only partial source-access concealment. If a legitimate access-control requirement emerges, perform proposal A independently after the relay/push migration stabilizes. Keep protocol, cryptography and Go phone core reviewable unless a separate threat model justifies proposal B.

Engineering ranges are planning estimates, not commitments: A requires roughly **2–4 engineer-weeks** for artifact/release contracts, consumer CI, dual-build parity and rollout if the facade remains constrained. B is a **rough, lower-confidence 6–12+ engineer-week estimate**, because it needs module/API restructuring, cross-repository conformance and security review; the range grows with compatibility obligations and does not include unforeseen Play/release incidents. It has not been implementation-tested.

## Related front-door option for the push migration

Firebase Hosting can rewrite HTTPS traffic to Cloud Run, and its official documentation explicitly lists `europe-west6` among supported Cloud Run rewrite regions ([Firebase Hosting + Cloud Run](https://firebase.google.com/docs/hosting/cloud-run)). This may preserve `push-swarm.dsfactory.org` while Cloud Run remains the backend, avoiding an unsupported direct custom-domain mapping or a separately operated load balancer. It is a routing option, not proof of zero cost or a security boundary: Hosting is a global front door and can expose request/connection metadata to that provider.

Before adopting it, run a deployed gate against the retained hostname: exact method/path/body/header forwarding for registration, allocation, submit and revoke; no caching of authenticated responses; correct status/body/error semantics; rate limits and request-size behaviour; WebPKI certificate/hostname validation; and direct `run.app` behaviour. Direct `run.app` should either be deliberately disabled/restricted or tested as a bypass that cannot create a second authority. The backend must still enforce all authentication, capability, replay/idempotency and rate controls—Hosting rewrites are not an authorization layer.
