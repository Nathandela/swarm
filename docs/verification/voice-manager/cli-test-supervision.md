# CLI test scratch ownership

The CLI package now uses the existing `internal/testbin.Supervise` TestMain, matching the repository's process-owning test packages. This gives helper re-execution and integration builds one canonical, per-run scratch root and cleanup owner.

Seven CLI refresh/usage-hook tests failed unchanged on the clean `62e85aac` baseline because macOS's `/var` temporary-path alias did not satisfy their strict native-login path checks. They passed in 1.754 seconds with canonical private scratch. After adding the standard TestMain, the targeted tests passed in 3.128 seconds and the full CLI package passed in 123.855 seconds, with the six inherited Swarm session/hook environment variables removed. Independent static review approved the change; later bounded manager CLI checks also passed.

This changes only test supervision. It does not weaken native-login path validation or certify the full voice feature.

The broad clean-checkpoint gate found the same alias issue in the unchanged persistence package: `TestCLIContentFingerprintRejectsMetadataPreservingTamperAndOldReaders` failed before any production changes with `TMPDIR=/tmp`. Applying the same standard TestMain to `internal/persist` made the complete package pass under the race detector in 5.437 seconds with that same initial alias. The supervisor creates canonical per-run scratch; production fingerprint checks remain strict.
