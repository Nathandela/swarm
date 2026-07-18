# Perf + lint baseline — 2026-07-18 (item 1.4, agents-tracker-4rp)

Item 1.4 of the swarm performance implementation plan (`.claude/tmp/perf-implementation-plan.md`):
in-repo benchmarks (R1.4.1), local golangci-lint reproducibility (R1.4.2), and
the firstpaint gate p95 capture (R1.4.4). This is a measurement baseline, not
a regression gate — later items (2.x, 3.x) are compared against these numbers.

## Environment

- Git commit: `632d5c1c241a49f3395e5f967f8e971ee732621d` (branch worktree-perf-audit)
- `go version`: go1.26.1 darwin/amd64
- `go env GOOS GOARCH`: darwin / amd64 (this machine is an Apple M1; the Go
  toolchain installed here is the amd64 build, so benchmarks run under Rosetta
  2 translation, not natively on arm64 — recorded as-is per instruction, no
  cross-build was forced)
- `cpu:` line reported by `go test -bench`: `VirtualApple @ 2.50GHz` (Rosetta's
  presented CPU string; the physical CPU per `sysctl -n machdep.cpu.brand_string`
  is `Apple M1`)
- Command: `go test -run='^$' -bench=. -benchmem -benchtime=1s ./internal/...`
  (run per-package below; single run each, as authorized)

## R1.4.1 benchmark baseline

New files (one per package, internal test files so they can reach unexported
state — hub, distribute, clientConn, etc.):

- `internal/vt/feed_bench_test.go` — (a) Feed throughput, (b) Snapshot build+Marshal, DecodeSnapshot
- `internal/wire/frame_bench_test.go` — (c) frame round-trip over net.Pipe
- `internal/shim/hubfeed_bench_test.go` — (d) hub.feed fanout
- `internal/protocol/fanout_bench_test.go` — (e) status-fanout distribute path
- `internal/persist/save_bench_test.go` — (f) Save latency

Run: `go test -bench=. -benchmem -run='^$' ./internal/...` (all other packages
have no benchmarks, so this is equivalent to targeting the five above).

| Benchmark | ns/op | throughput | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Feed_Plain_80x24 | 554,484 | 3.55 MB/s | 235,262 | 1,944 |
| Feed_Plain_200x50 | 3,699,339 | 2.73 MB/s | 1,268,161 | 10,050 |
| Feed_Styled_80x24 | 624,431 | 7.15 MB/s | 235,248 | 1,944 |
| Feed_Styled_200x50 | 3,602,896 | 6.33 MB/s | 1,268,107 | 10,050 |
| Snapshot_80x24 (build+Marshal) | 833,572 | — | 218,594 | 1,867 |
| Snapshot_200x50 (build+Marshal) | 4,278,356 | — | 1,143,208 | 9,853 |
| DecodeSnapshot_80x24 | 1,722,867 | 41.87 MB/s | 473,072 | 2,048 |
| DecodeSnapshot_200x50 | 8,940,215 | 42.24 MB/s | 2,056,753 | 10,267 |
| FrameRoundTrip_256B | 1,945 | 131.62 MB/s | 580 | 3 |
| FrameRoundTrip_32KiB | 15,098 | 2,170.31 MB/s | 81,925 | 3 |
| HubFeed (fanout, 1 subscriber) | 22,380 | 2.68 MB/s | 6,446 | 51 |
| Distribute_1Sub | 178 | — | 208 | 2 |
| Distribute_16Subs | 2,529 | — | 3,328 | 32 |
| Distribute_128Subs | 23,777 | — | 26,624 | 256 |
| Save (persist.Store.Save) | 7,820,862 | — | 4,230 | 29 |

Notes:

- `Feed_Styled_*` is faster in wall-clock ns/op than `Feed_Plain_*` at 80x24
  despite doing more parsing work per byte (SGR transitions every 4 cells):
  the styled payload is smaller per row (interleaved escape sequences replace
  literal glyph runs at the same visual width), so MB/s (which normalizes by
  payload bytes) is the more informative column there, not ns/op.
- `Distribute_*` scales close to linearly with subscriber count (~155ns and
  2 allocs per subscriber beyond a small fixed cost), consistent with the
  per-subscriber `Control`+`SessionView` allocation in `server.go:294`. The
  benchmark drains each fake subscriber's queue synchronously after every
  `distribute()` call rather than via a background goroutine racing the timed
  loop — an unthrottled tight loop (unlike production's serial `fanoutLoop`,
  which is paced by real event arrival) can otherwise outrun the drain
  goroutines and trip the S9 wedged-subscriber eviction path mid-benchmark,
  silently degrading later iterations into iterating an emptied `s.subs` map.
- `HubFeed` covers the shim-side single-subscriber fanout (`hub.feed`,
  `internal/shim/server.go`): emulator Feed + transcript Write + one bounded
  channel send, all under `h.mu`.
- `Save` includes a real `fsync` (`tmp.Sync()` in `persist.go`); it is disk-
  bound, not CPU-bound — the ~7.8ms/op is consistent with this machine's disk
  fsync latency, not marshal cost.
- All five benchmark files also passed `go test -race -bench=. -benchtime=1x`
  (one iteration each) with no data races and no panics, and the full
  `go test ./...` / `go test -race ./...` suites pass unchanged.

## R1.4.2 golangci-lint

CI pins `golangci-lint-action@v6` at `version: v1.64` (`.github/workflows/ci.yml`),
which resolves to the latest v1.64.x patch. No repo `.golangci.yml` exists, so
CI runs golangci-lint's default ruleset.

Local install (version-matched):

```
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
```

Verified: `golangci-lint --version` → `golangci-lint has version v1.64.8 built
with go1.26.1`.

Run at repo root: `golangci-lint run ./...` → clean (exit 0, no findings),
both before and after adding the five benchmark files above. No stricter
`.golangci.yml` was added — R1.4.2(c) treats that as an optional, separate
decision, deferred (not made in this item).

## R1.4.4 firstpaint gate p95

Command: `go test -v -run TestFirstPaintGate ./internal/tui/`

Result: `TestFirstPaintGate_RealDaemon_FiftySessions_P95` — PASS.
First-paint p95 over 25 runs @ 50 real sessions: **3.179417ms** (raceEnabled=false),
against the N-1 budget of 100ms.
