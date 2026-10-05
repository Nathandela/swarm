# Account pools v0.16.1 hotfix verification

Status on 2026-10-05: final local gates and independent Astra source review pass. Android version metadata is **0.16.1**, version code **54**. Official publication and deployment are pending. The deployed v0.16.0 release is signed and its official workflow passed all 17 jobs; that release preserved 160 discussion records, seven accounts and all eight live shim process identities.

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

Private raw receipts are under the session's `machine-account-rotation-impact/native-release-v0161` directory. Signed official publication, installation and fresh managed Gaston turn/continuation acceptance remain pending. Real quota exhaustion is not manufactured or claimed.

## Frozen source and local gates

Astra independently reconciled all 1,708 Go files and seven other inputs. The final Go inventory digest is `410eadf04789250f3c5bfd4b34eac52ea33d57bfbd50907df2b4d71e19c0de2b`. Build (3.73 s), vet (7.18 s), lint (32.05 s, zero issues), the corrected full normal suite (84.81 s), and the full race suite (713.35 s) pass. Both full suites completed all 85 packages without failures. Source hashes remained unchanged through the gates.

The initial full normal run failed only because the Android release identity test expected 0.16.0/code 53 while the prepared Gradle metadata was 0.16.1/code 54. All other packages passed. Luna updated that existing assertion; Astra verified that this was the sole Go change and that all reviewed native production code remained unchanged. The original 579.69-second failed run and prior source inventory are retained separately. The corrected Android check and repeated full normal suite pass against the resealed source.

## Upgrade and recovery boundary

Native stock custody requires account configuration compatibility **4**. Existing schema-3 projections retain their earlier requirement. The new compatibility scan includes private candidate and unreferenced profiles, so an interrupted transfer cannot hide behind an empty registry or unpublished projection. It checks bounded custody metadata and recorded profile/tree identities without reading credentials or following owner asset aliases.

An interrupted stock transfer also uses the context-update filename understood by older account readers. Those readers hold native reuse and credential erasure on that pending state. The new reader distinguishes the stock sentinel from existing global configuration refresh journals and completes only the recorded phases. Astra tested the actual v0.16.0 credential-erasure implementation through a source overlay: the new marker alone permits erasure of synthetic credentials; the recognized outer journal refuses it and preserves the exact bytes. Focused compatibility race checks, the full upgrade package and final integrated local release gates pass.

## Bounded follow-ups

After an initial retained-stock adoption, removing owner skills and allowing native stock regeneration is supported. Recreating owner skills again can produce a duplicate-stock hold. Both native trees and all owner customization remain preserved; repeated relocation is tracked in `swarm-2xm.42`. The hotfix does not overwrite or discard retained trees to bypass the hold. Sanitized early-exit diagnostics for historical backend failures are separately tracked in `swarm-2xm.43`.
