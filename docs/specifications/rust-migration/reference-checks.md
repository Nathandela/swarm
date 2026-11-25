# Reference observations from the planning pass

Recorded locally on 25 November 2026. The runtime checks below used macOS 27.2.1 (26B131), Go 1.26.5, GOHOSTOS/GOHOSTARCH darwin/amd64. These are observations of the Go references. The measurements are single suite invocations on a working development machine. Use the paired controlled runs in the [qualification plan](qualification.md) for Rust performance claims.

| Check | Result |
|---|---|
| Main revision | Remote main and release v0.29.22 resolve to `f26648d69086e329132ac7849954d2f29a895387`. Local source build SHA-256 `dd1c72a067ff7dbd51d31413a6d51be10c3fb381173145e52ab9d1a509ec354e`. |
| Call reference | Remote integration head `da7b9f2dfa7e2c6960bd860a72459c0448845c82`. Independent local source build SHA-256 `dc4f267601edde65989656466216db614446782a2d89a60ad0540abc6482dbe3`. |
| CLI inspection | Built both with the ordinary Go compiler. Main reports `swarm dev (go1.26.5)` as expected without release linker stamps. Both binaries printed Discord help successfully. Only the call reference advertised `stop-call`. A frozen qualification run also needs the installed release and matching media helper. |
| Codec checks | Complete `internal/wire` and `internal/shimwire` suites passed. |
| Targeted storage and Discord checks | Two migration-order/missing-step tests and four tests for uncertain creation, conflicting admission replay, concurrent operation binding and wait-notice nonce expiry passed. |
| Keystroke echo | Real client → daemon → shim → PTY, p95 **0.127 ms** across 1,000 post-warmup samples. The Go gate of <10 ms passed. |
| Warm-daemon first paint | 50 real sessions, 25 trials, p95 **8.449084 ms**. The <100 ms requirement passed. One missing shim-socket diagnostic appeared during the passing run. Its cause was not established. Carry the diagnostic into M0 calibration before treating the fixture as quiet. |

To reproduce scoped checks in independent pinned checkouts, use the shared Go caches and the test supervisor's private scratch. Build flags, paths and metadata can change binary digests. Always record the digests of the actual executables used.

```sh
go build -o /tmp/swarm-main-reference ./cmd/swarm
/tmp/swarm-main-reference --help
/tmp/swarm-main-reference version
/tmp/swarm-main-reference discord --help
go test -count=1 ./internal/wire ./internal/shimwire
go test -count=1 ./internal/persist -run '^TestApplyMigrations'
go test -count=1 ./internal/discord -run '^Test(OwnerUncertainCreateIsNeverRepeated|AdmissionDurableReplayConflictAndExactContext|ConflictingConcurrentClaimsAndOperationBinding|UnconfirmedWaitNoticePastNonceWindowGoesPassive)$'
go test -count=1 -v ./internal/e2e -run '^TestE2E_LiveShimKeystrokeEchoLatencyP95$'
go test -count=1 -v ./internal/tui -run '^TestFirstPaintGate_RealDaemon_FiftySessions_P95$'
```

In the call reference checkout build to a different path and exercise `discord --help`. No live Discord event, native provider inference, rollback, long soak, screenshot pair or Rust candidate was exercised in this planning pass.
