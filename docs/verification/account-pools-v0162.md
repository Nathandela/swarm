# Account pools v0.16.2 verification

Status on 2026-10-05: all revised-source local gates pass. Android identity is **0.16.2**, version code **55**. Signed official publication, installation and authenticated Gaston continuation remain pending. The VM remains on v0.16.0.

## Scope and deterministic regression

The [v0.16.1 record](account-pools-v0161.md) retains the installed launch defects, native stock admission, ownership/recovery boundaries, independent Astra probes and every failed or cancelled gate. That immutable tag was not published or installed. v0.16.2 preserves its production implementation and configuration compatibility requirement **4**.

The successor makes the owner-protocol positive fixture independent of ambient `XDG_CONFIG_HOME`. A separate regression saves a nonempty unsupported XDG selector, clears the live environment, and verifies that an omitted launch environment still retains and rejects the saved selector before profile/projection/session writes or native child startup. The production selector capture and characterized-origin guard remain intact.

## Frozen source and validation

The revised inventory contains 1,708 Go files, with mapping digest `ed531c1e285d5893fddb78ff81a33476c9067bcfa145dcef1ae5afc9a30a264b`, and eight separately hashed build inputs. The only Go changes from the unpublished v0.16.1 source are the owner-protocol test fixture and Android release assertion. All five focused owner-protocol tests pass under the race detector with both empty and nonempty ambient XDG selectors.

Build (70.46 s), vet (28.46 s), and lint (33.24 s) pass. The first full normal run completed all 85 packages in 743.30 s, but failed one accounts fixture when `/tmp` could not create a directory because the root filesystem was full. All other packages passed, including skeleton. The failed log and receipt are retained separately; the sequential wrapper did not start the race suite. No source assertion or production guard change is justified by this storage failure.

After exact cleanup of 147 unused task-generated binary folders recovered 3.31 GiB on the root filesystem, both full suites pass with one package at a time: normal **815.86 s**, race **1,645.78 s**. Each completes 77 tested packages and eight packages without tests, with zero failures. Both use a nonempty synthetic ambient XDG selector; all assertions, race instrumentation and package coverage are retained. Independent post-cleanup verification preserves the original discussion, process, account and configuration identities. No module downloads, source, histories or populated live build cache were removed by this cleanup.

Astra verified that completed task-generated processes and binaries were disjoint from user discussions before their exact cleanup. Standard `go clean -cache` subsequently reclaimed only the native Go build cache after a privileged host scan found no active Go tools or cache references. Source, module downloads, credentials, histories, installed binaries, all 160 discussion identities, eight original live shim identities, seven accounts and original rotation preferences remained intact. Runtime test paths retain `/tmp` semantics because moving them below the owner's home would change ancestor configuration discovery.

## Acceptance boundary

Three Astra specialists review release provenance, native/backend continuation and operational custody. The installed check requires a completed assistant response, a guarded move to a distinct eligible account, the same native conversation and model, and a second completed response recalling a value omitted from its prompt. Echoed input or a launch receipt cannot establish acceptance. One bounded attempt per provider retains immutable receipts and restores original rotation preferences independently of cleanup.

The prepared v0.16.2 helper has SHA-256 `3badd20f44b7dbabeaec8d1d244ae50b44331cd79ec6c21deed2eb92b8676c1e`; 62 pure checks pass, and its direct RPC dependency and Python startup chain are recorded separately. Astra also checked actual Claude 2.1.289 transcript structures: the unchanged parser recognizes 42 completed-answer shapes with real ancestry after text redaction and rejects 252 altered negative cases. These establish format compatibility, not fresh authentication or rotation acceptance.

Claude's stale quota observations can hold target selection even after a successful authenticated first turn. A guarded manual move does not certify automatic switching under actual provider quota exhaustion. Original settings, histories, discussion identities and live process identities must be verified after installation and cleanup.
