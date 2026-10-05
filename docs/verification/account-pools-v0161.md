# Account pools v0.16.1 hotfix verification

Status on 2026-10-05: local gates passed, but official publication was stopped after main CI exposed a non-hermetic regression fixture. The immutable v0.16.1 tag remains at `98d6626b1f09a2d50796f263353e99906080ba0d`; this version was neither published nor deployed. The correction is tracked in [v0.16.2 verification](account-pools-v0162.md). The deployed v0.16.0 release is signed and its official workflow passed all 17 jobs; that release preserved 160 discussion records, seven accounts and all eight live shim process identities.

## Installed defects and corrections

Actual installed managed-launch attempts exposed defects that empty synthetic account profiles did not cover:

| Boundary | Correction and evidence |
|---|---|
| Owner protocol ingress | Capture original native configuration selectors before environment filtering. Preserve omitted versus explicitly empty environment semantics and derive resumed configuration from the source discussion. Remote clients cannot inject owner origins. Regressions cross owner RPC, core configuration preparation and a real shim/synthetic child; the v0.16.0 source fails them. Focused normal/race checks pass and Astra independently reviewed the correction. |
| Confined Codex `.tmp` | Admit the exact owned, real, non-world-writable native runtime directory inside the anchored private 0700 profile. Native 0775 is supported without chmod. Strict general private directory checks remain. Native-layout positive, unsafe profile/path/type/ownership negatives and post-install drift pass focused race checks. |
| Native bundled skills | All three enrolled Codex 0.160.0 profiles contain the same native-generated `skills/.system` tree. Exact stock is retained before an owner alias is installed, under stopped-writer and durable custody fences. Source-absent native generation is also supported. Unknown or modified candidate customization remains held. Independent native, crash/retry and full local gates pass. |

## Independent Astra native evidence

Three Astra specialists reviewed the full launch path, enrolled native assets and backend continuation. Their isolated probes made no model requests and used synthetic authentication. Native Codex 0.160.0 creates a 72-node stock skills tree containing 49 files and 307,566 bytes. Its complete path/type/content digest is `8feb5b0889075ff695465a068fbeec422e3d27c8074a48af16185f92e8ac406c`; a matching marker alone is insufficient because native initialization retains modified files when the marker matches.

Retaining exact stock in a private sibling and aliasing owner skills preserves the full native catalog across reopening, including repository/user/system precedence and disabled skills. With no owner skills source, native initialization creates stock in the private profile; repeated preparation must accept that exact stock. All three real account asset trees remained unchanged during investigation.

Independent product acceptance passes all three required layouts: owner-present enrolled, owner-absent fresh, and owner-absent enrolled. Each crosses actual native configuration/skills initialization and a successful empty thread start, then a second preparation and both context/public projection revalidation. Recorded stock inodes and configuration generations remain stable. Eight interrupted adoption phases recover; seven invalid states hold without writes. These are synthetic, isolated native checks with no authenticated model turn. All probe descendants were reaped.

The installed native backend starts, initializes and resumes retained synthetic history through its remote TUI. Conversation ID, model and network policy remain intact, and copied Gaston configuration retains its four enabled/trusted hooks. These probes justify no backend compatibility patch. They do not explain two historical degraded sessions whose early native stderr was discarded, nor establish an authenticated assistant turn.

Private raw receipts are under the session's `machine-account-rotation-impact/native-release-v0161` directory. Signed installation and fresh managed Gaston turn/continuation acceptance remain pending under v0.16.2. Real quota exhaustion is not manufactured or claimed.

## Official execution and successor

Official release run `37364545756` first completed with eight successful jobs, seven cancelled jobs and two skipped publication jobs. GitHub's annotations confirm that every cancellation had no assigned runner or executed step and failed hosted-runner acquisition. Astra independently verified all seven annotations. One failed-jobs rerun retained the successful gates and passed two more, including Android.

The simultaneous main CI race job `111946490389` executed and failed only `TestAccountConfigurationOwnerProtocolNilUsesSavedOriginEmptyDoesNot`. The positive fixture left `XDG_CONFIG_HOME` inherited when the daemon saved its environment. A nonempty value correctly triggers the native characterized-origin guard. Astra reproduced the exact error with a synthetic nonempty XDG value and confirmed the same unchanged test passes with an empty value. This is a fixture defect, distinct from the runner cancellations. The remaining release attempt was explicitly cancelled before publication. The successor isolates the positive fixture and tests rejection of a saved unsupported selector after the live selector is cleared; production behavior is preserved.

Three Astra reviewers also corrected the temporary installed canary's persisted status/provider readiness checks and Python 3.10 parsing of Go nanosecond timestamps. The final v0.16.1 helper digest is `3ff39cace3026d55fbe9b22aeb2621f75d87695d195422fcd357b7abda2267a3`; 62 pure checks pass, and all 160 existing discussion timestamps parse. Read-only owner RPC confirms distinct fresh Codex targets. All four Claude accounts have stale usage observations, which can hold movement after a successful first response. No installed canary was run for v0.16.1.

Host inspection found 42 abandoned synthetic test processes. Astra proved their exact PID/start/executable identities, synthetic state paths and disjointness from all live user discussions. A pidfd-fenced cleanup stopped those processes with TERM only and reclaimed six generated binary folders. Independent verification preserved all 160 discussions, eight original live shim identities, seven accounts and both original disabled rotation settings. This task cleanup neither removed histories nor cleared shared caches.

## Frozen source and local gates

Astra independently reconciled all 1,708 Go files and seven other inputs. The final Go inventory digest is `410eadf04789250f3c5bfd4b34eac52ea33d57bfbd50907df2b4d71e19c0de2b`. Build (3.73 s), vet (7.18 s), lint (32.05 s, zero issues), the corrected full normal suite (84.81 s), and the full race suite (713.35 s) pass. Both full suites completed all 85 packages without failures. Source hashes remained unchanged through the gates.

The initial full normal run failed only because the Android release identity test expected 0.16.0/code 53 while the prepared Gradle metadata was 0.16.1/code 54. All other packages passed. Luna updated that existing assertion; Astra verified that this was the sole Go change and that all reviewed native production code remained unchanged. The original 579.69-second failed run and prior source inventory are retained separately. The corrected Android check and repeated full normal suite pass against the resealed source.

## Upgrade and recovery boundary

Native stock custody requires account configuration compatibility **4**. Existing schema-3 projections retain their earlier requirement. The new compatibility scan includes private candidate and unreferenced profiles, so an interrupted transfer cannot hide behind an empty registry or unpublished projection. It checks bounded custody metadata and recorded profile/tree identities without reading credentials or following owner asset aliases.

An interrupted stock transfer also uses the context-update filename understood by older account readers. Those readers hold native reuse and credential erasure on that pending state. The new reader distinguishes the stock sentinel from existing global configuration refresh journals and completes only the recorded phases. Astra tested the actual v0.16.0 credential-erasure implementation through a source overlay: the new marker alone permits erasure of synthetic credentials; the recognized outer journal refuses it and preserves the exact bytes. Focused compatibility race checks, the full upgrade package and final integrated local release gates pass.

## Bounded follow-ups

After an initial retained-stock adoption, removing owner skills and allowing native stock regeneration is supported. Recreating owner skills again can produce a duplicate-stock hold. Both native trees and all owner customization remain preserved; repeated relocation is tracked in `swarm-2xm.42`. The hotfix does not overwrite or discard retained trees to bypass the hold. Sanitized early-exit diagnostics for historical backend failures are separately tracked in `swarm-2xm.43`.
