# Native Linux lightweight-storage verification

2026-10-17 · `agents-tracker-bdp7`, `agents-tracker-nsxw`

Source tested: `a72e7fd159ea12a93604ea83844154e68e6da065`.
Final gate run: 06:44:51–07:11:06 UTC on `tf_nathan`, Linux
5.15.0-198-generic, amd64. This verifies the source; no production binary was
installed and no existing session/workspace was purged.

## Environment

Go 1.26.9, `GOTOOLCHAIN=local`, `CGO_ENABLED=1`, system C compiler,
`GOMAXPROCS=4`, `GOFLAGS=-p=2`, umask `077`. Inherited `SWARM_*` and Android SDK
selectors were removed. The pinned gobind was built privately from
`golang.org/x/mobile@v0.0.0-20260709172247-6129f5bee9d5`.

The full linter used golangci-lint 2.12.2 with Go 1.26.0 on PATH, matching CI's
stdlib-fact pin. Its official Linux archive checksum was verified as
`8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553`.

The checkout, modules, build/lint caches and compiled test binaries used one
private executable RAM-backed workspace. Both `TMPDIR` and `GOTMPDIR` used a
short RAM path so Go's test directories fit Unix socket limits. Existing short
fixture helpers also used their normal `/tmp` directories and cleanup handlers.
No shared cache was purged or expanded deliberately. Persistent volumes were
already nearly full; resource guards monitored headroom and did not fire during
the final gate run.

## Results

| Gate | Result |
|---|---|
| `go build ./...` | Pass |
| `go vet ./...` | Pass |
| Full `golangci-lint run --timeout=10m` | Pass, zero issues |
| `go test -json -count=1 -timeout=20m ./...` | Pass, 578.57 s; 78 passing packages, seven skipped/no-test package outcomes, zero failures |
| `go test -race -json -count=1 -timeout=20m ./...` | Pass, 850.83 s; same package counts, zero failures |
| `go test -tags integration -race -count=1 ./internal/engine/` | Pass; actual Linux CPU-sampler path, no skipped tests |
| `TestDaemonIdleCPUNearZero`, integration tag, non-race | Pass; daemon plus three real shims, 14 samples over 62 s; mean/max reported 0.000% of one core at sampler resolution, against 5% ceiling |
| `TestFirstPaintGate_RealDaemon_FiftySessions_P95`, non-race | Pass; p95 **6.758 ms**, 25 runs at 50 real sessions, against 100 ms |
| `TestE2E_LiveShimKeystrokeEchoLatencyP95`, non-race | Pass; p95 **0.492 ms**, 1,000 samples, against 10 ms |
| Release-style `CGO_ENABLED=0` build and native `version` execution | Pass; static Linux amd64 ELF, Go 1.26.9 |
| Strict Phase B documentation manifest | Pass; all 162 active requirements owned exactly once |

The longest package was skeleton: 407.524 s ordinarily and 479.532 s under race,
both below CI's default ten-minute per-package limit. The explicit twenty-minute
test limit was headroom for the VM run, not needed to conceal a package timeout.

Full suites include real shim/process-containment, hook-stage crash/restart,
account launch/resume, native cwd alignment, bounded identity capture,
live-only history floors, deletion-family and operation-pressure regressions.
Provider processes in those host tests are synthetic; no model calls were used.

## Failures corrected before the final run

The first complete native run exposed account fixtures that still put valid
projects inside supervisor state. Shared fixtures and connected launch/resume,
configuration-origin, worktree and owner-binary cases now use external projects;
their original auth/projection/cwd/refusal assertions remain.

The release gate test now checks the owner's actual release-tag-only/reusable
workflow policy without modifying workflows or weakening publication permissions.
Two public-file fixtures now explicitly chmod their intended unsafe permissions,
independent of umask. A no-backend test uses an input barrier so a fast clean exit
cannot outrun attach; twenty focused Mac race repetitions also passed. Long Go
temporary paths were a harness configuration error and were shortened.

These corrections are committed in `7e9823e6` and `a72e7fd1`. Independent review
found no material assertion weakening or temporary-project cleanup leak.

## Limits and remaining work

Each full suite reports 67 skipped test/subtest events. These include helper
entrypoints exercised by parent tests, plus opt-in native-provider probes,
Firestore/workerd integration and Android artifact/SDK checks. This is not
evidence for those external-service, SDK or real-provider lanes, active fuzzing,
physical power loss, or production before/after disk growth.

One existing skip is material: `TestLaunchCrashReplay_W4_LiveOrphanAgent_TODO`.
Its source documents a live-orphan launch crash window before shim PID custody,
with possible duplicate re-drive. `agents-tracker-69qr` tracks the missing real
crash harness and custody fix. Passing host gates do not close that window.

The [whole fixing plan](../specifications/lightweight-storage-plan.md) remains
open for rollout, unique-work preservation/legacy cleanup, native Claude
diagnostics, shared debug-log bounds, stopped folded-spool retirement and audit
retention. The separate Mac platform/toolchain failures remain tracked; these
Linux results do not turn the full Mac suite green.

Before deleting the RAM artifacts, the task-executable scan found no remaining
processes under either private test root. Persistent free space changed while
the machine remained live: a final observation was about 0.52 GiB on root and
0.43 GiB on data. New non-test-root Claude rows were also observed. No change in
production allocation is attributed to these source fixes or to cleanup.

At 07:18:48 UTC, both task-owned RAM directories were removed after a second
scan found no executable or cwd references. That removed the private checkout,
modules, compiler/lint caches, binaries and raw test logs; this report retains
the compact evidence. Final root/data free space was 0.472 / 0.391 GiB and RAM
storage was restored. Existing runtime data, shared caches and the production
binary were not purged or replaced.
