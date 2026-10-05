# Account pools v0.16.2 verification

Status on 2026-10-05: the initial successor source passed all local gates, but publication was held after Claude automatically changed from qualified **2.1.289** to **2.1.290** during acceptance preparation. The added managed-only retained qualified Claude implementation passes focused normal and race regressions and is frozen for final review and release gates; the earlier full passing gates do not cover that new code. Android identity is **0.16.2**, version code **55**. Signed official publication, installation and authenticated Gaston continuation remain pending. The VM remains on v0.16.0.

## Scope and deterministic regression

The [v0.16.1 record](account-pools-v0161.md) retains the installed launch defects, native stock admission, ownership/recovery boundaries, independent Astra probes and every failed or cancelled gate. That immutable tag was not published or installed. v0.16.2 preserves its production implementation and configuration compatibility requirement **4**.

The successor makes the owner-protocol positive fixture independent of ambient `XDG_CONFIG_HOME`. A separate regression saves a nonempty unsupported XDG selector, clears the live environment, and verifies that an omitted launch environment still retains and rejects the saved selector before profile/projection/session writes or native child startup. The production selector capture and characterized-origin guard remain intact.

## Managed Claude runtime retention

The added change retains the qualified Linux portable Claude **2.1.289** executable from the owner's canonical `HOME/.local/share/claude/versions` installation in a private, atomically published version directory. Managed enrollment, availability checks, launches, successors and restart recovery verify the selected executable's bounded content digest and ownership. Ordinary unbound launches continue using the owner's installed CLI. Custom launchers stay in place; alternate installer layouts do not gain a portability guarantee.

The new compatibility capability is **account worker 3**. Retained enrollment configuration uses schema 2; existing progress and stopped-writer custody schemas remain intact. Admission also checks retained cache, session and recovery references, including the case where cache files are missing, so an older target cannot silently downgrade execution verification. Account configuration compatibility remains 4.

Account details and the sign-in method show the managed qualified version and the last observed newer installed version. Presentation uses the last fully verified executable observation; actor admission still performs content verification. An empty daemon with no managed Claude intent does not allocate a retained executable simply by starting.

Astra's isolated relocated-executable controls passed with updater settings unchanged and no model submissions. They establish local startup and configuration behavior. Native housekeeping begins after the first submitted turn, so the idle probe did not exercise vendor garbage collection; that failed probe assertion is retained rather than treated as a passing dynamic test. Installed authenticated acceptance remains required.

## Revised source and focused validation

The new frozen inventory contains **1,713 Go files**, mapping digest `0bd26010111aec1a3c38eecfb7ab728a32f25fce81452ce0df4417f0e072dd4a`, with the same eight separately hashed build inputs. Twenty-five Go files differ from the initial successor seal. Sol's focused normal and race checks pass across persist, enrollment, upgrade, shim and skeleton, including an actual copied synthetic ELF child, ambient updater swaps, vendor-file removal, restart, concurrent publication and presentation, abandoned staging directories, metadata-preserving content tampering, last-moment startup refusal, first enrollment and older-target admission without cache files. The final strict-manifest controls also pass normally and under the race detector.

Two behavioral failing controls preserve the previous code's actual failures: the old ambient resolver rejects managed launch after the installed version changes, and the old final shim check starts a native child despite detecting changed retained bytes. An initial overlay compilation error and a later no-op fixture permission change are preserved separately and are not counted as behavioral controls.

Required final-source gates use local build, vet, lint and full normal tests, plus the official exact-source CI's full `go test -race ./...` and integration gates. The repository requires green gates rather than local-only execution. The immutable release tag follows successful final-source gates, and publication reuses the complete official release workflow. Installation and authenticated continuation are separate acceptance steps.

## Initial successor source and validation

The revised inventory contains 1,708 Go files, with mapping digest `ed531c1e285d5893fddb78ff81a33476c9067bcfa145dcef1ae5afc9a30a264b`, and eight separately hashed build inputs. The only Go changes from the unpublished v0.16.1 source are the owner-protocol test fixture and Android release assertion. All five focused owner-protocol tests pass under the race detector with both empty and nonempty ambient XDG selectors.

Build (70.46 s), vet (28.46 s), and lint (33.24 s) pass. The first full normal run completed all 85 packages in 743.30 s, but failed one accounts fixture when `/tmp` could not create a directory because the root filesystem was full. All other packages passed, including skeleton. The failed log and receipt are retained separately; the sequential wrapper did not start the race suite. No source assertion or production guard change is justified by this storage failure.

After exact cleanup of 147 unused task-generated binary folders recovered 3.31 GiB on the root filesystem, both full suites pass with one package at a time: normal **815.86 s**, race **1,645.78 s**. Each completes 77 tested packages and eight packages without tests, with zero failures. Both use a nonempty synthetic ambient XDG selector; all assertions, race instrumentation and package coverage are retained. Independent post-cleanup verification preserves the original discussion, process, account and configuration identities. No module downloads, source, histories or populated live build cache were removed by this cleanup.

Astra verified that completed task-generated processes and binaries were disjoint from user discussions before their exact cleanup. Standard `go clean -cache` subsequently reclaimed only the native Go build cache after a privileged host scan found no active Go tools or cache references. Source, module downloads, credentials, histories, installed binaries, all 160 discussion identities, eight original live shim identities, seven accounts and original rotation preferences remained intact. Runtime test paths retain `/tmp` semantics because moving them below the owner's home would change ancestor configuration discovery.

## Acceptance boundary

Three Astra specialists review release provenance, native/backend continuation and operational custody. The installed check requires a completed assistant response, a guarded move to a distinct eligible account, the same native conversation and model, and a second completed response recalling a value omitted from its prompt. Echoed input or a launch receipt cannot establish acceptance. One bounded attempt per provider retains immutable receipts and restores original rotation preferences independently of cleanup.

The prepared v0.16.2 helper has SHA-256 `3badd20f44b7dbabeaec8d1d244ae50b44331cd79ec6c21deed2eb92b8676c1e`; 62 pure checks pass, and its direct RPC dependency and Python startup chain are recorded separately. Astra also checked actual Claude 2.1.289 transcript structures: the unchanged parser recognizes 42 completed-answer shapes with real ancestry after text redaction and rejects 252 altered negative cases. These establish format compatibility, not fresh authentication or rotation acceptance.

Claude's stale quota observations can hold target selection even after a successful authenticated first turn. A guarded manual move does not certify automatic switching under actual provider quota exhaustion. Original settings, histories, discussion identities and live process identities must be verified after installation and cleanup.
