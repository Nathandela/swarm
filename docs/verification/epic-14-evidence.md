# Epic 14 — Evidence

**Epic**: Integration Verification (`agents-tracker-54z`) — the whole-system proof.
**Built as a two-wave swarm** (package-disjoint agents, orchestrator-integrated).

## What Epic 14 delivers

The whole system is proven against everything above it: all 18 spec scenarios automated, every invariant S1-S12/L1-L3 asserted and traced, the failure-injection matrix (kill-9 in every phase, disk-full, wedged subscriber, lease/PID/version skew, old×new compat matrix, 50-cycle soak), the perf budgets asserted fail-closed, active fuzzing in CI, and the three traceability artifacts. The one irreducibly-manual item (real-CLI value-name confirmation) is a gated, never-CI, human-run harness.

## Criterion walk (E14.1 – E14.8)

| Criterion | Evidence |
|---|---|
| E14.1 all 18 scenarios automated (no required test skipped/flag-disabled) | Scenario→test map in [epic-14-invariants.md] + this file's table; 14/18 pre-existing, the remaining composites closed by the L1 e2e (scenario 4), the compat matrix + soak (10/11), and the idle-CPU soak (15). No required test is `-short`-skipped out of CI; the perf gates run in dedicated non-race / integration CI lanes (below). |
| E14.2 invariants asserted + traceability list | [epic-14-invariants.md] maps S1-S12 + L1-L3 to confirmed tests. GG-2. |
| E14.3 failure injection | kill-9 during spawn (`daemon/launch_test.go` crash-injection) / attach (`e2e/skeleton_e2e_test.go:TestE2E_DaemonKilledMidAttach`) / write (`e2e/killmidwrite_e2e_test.go`); disk-full ENOSPC on meta (`persist/diskfull_test.go`, via a nil-in-prod `writeTemp` seam, proves atomic temp+rename never tears, S8); wedged subscriber (`protocol/fanout_test.go`); lease/PID/version-skew (`protocol/lease_test.go`, `daemon/daemon_fixes_test.go`, `daemon/version_test.go`); **old-shim×new-daemon compat MATRIX** (`daemon/compatmatrix_test.go` — matched interops, both skew directions detect→lost with agent+shim alive, S3, via a build-tagged shimwire.Version seam that is byte-identical in the shipped build); **50-cycle restart/upgrade SOAK** (`daemon/soak_test.go` — zero session loss, byte-intact transcripts across 50 kill/restart cycles). |
| E14.4 perf budgets (fail-closed) | N-1 first-paint p95 <100ms @50 with a REAL daemon (`tui/firstpaint_gate_test.go`; the authoritative 100ms assertion is the non-race build, run in a dedicated non-race CI lane; a 2s sanity ceiling is asserted under `-race` so the gate is never skipped). N-2 attach echo p95 <10ms (`attach/latency_test.go`). N-3 spinner-churn collapse (`transcript/transcript_test.go`) + real daemon+shims idle-CPU <5%/core over ≥60s (`daemon/idlecpu_integration_test.go`, `//go:build integration`, run in both CI integration lanes; measured mean 0.000%). |
| E14.5 fuzz in CI | A `fuzz` CI job runs all 5 targets (`wire.FuzzRoundTrip`/`FuzzReadFrame`, `vt.FuzzFeedSplitConsistency`, `adapter`+`refadapter` ExtractConversationID totality) with bounded `-fuzztime`. |
| E14.6 contracts manifest | [epic-14-contracts-manifest.md] names covering tests per CONTRACTS-UNDER-TEST row + demonstrates each row's LIGHT/MEDIUM/FULL scope. |
| E14.7 EARS traceability matrix | [epic-14-ears-matrix.md] maps every EARS id to a test / CI check / doc artifact (GG-3). |
| E14.8 L1 composite | `e2e/l1composite_e2e_test.go` chains signal→engine→fan-out→rendered TUI (bubbletea via teatest over a real protocol.Client) within 1s UNDER OUTPUT LOAD, fail-closed on >1s (measured ~20ms). |

## Carry-forwards closed

- **a7d** (shim arms signal handler before spawn): `shim/shim_signal_order_test.go` — a nil-in-prod seam delivers a SIGTERM in the arm→spawn window and proves it is buffered and still contains the agent group (S5 ordering).
- **Worktree e2e**: `e2e/worktree_e2e_test.go` — launch with the toggle → the agent's ACTUAL kernel cwd (via /proc or lsof) is `.swarm/worktrees/<id>` on branch `swarm/<id>` → delete tears it down (S-3/R-3).
- **D1/D2 real-CLI VERIFY** (Epic 11 deferrals): `internal/smoke/realcli_test.go` — a `//go:build realcli` + `SWARM_REALCLI=1` doubly-gated, never-CI, billable harness that confirms the real Claude hook value-names and the real Codex app-server event stream against live CLIs and re-records fixtures on drift. Compiles clean; **remains VERIFY-pending until a human runs it once** (documented, [epic-14-realcli-smoke.md]). This is a deliberately-gated manual confirmation, not a missing test.

## Committee & TDD

Built by a two-wave swarm (Wave 1: fuzz-CI, traceability docs, a7d, daemon soak + injections; Wave 2: compat matrix + idle-CPU, L1 composite + worktree, first-paint perf, gated real-CLI). Each agent verified its own package; the orchestrator ran the whole-module gate at each integration. TDD/failing-first where meaningful (several tasks proved fail-closed empirically with a throwaway assertion). Two nil-in-production test seams added (`shim.testHookAfterSignalArm`, `persist.writeTemp`) + one build-tagged shimwire version seam — all byte-identical in the shipped build. The whole-system audit-committee tour (codex + Opus + Fable) follows this file.

## Quality gates (GG-4)

gofmt · `go build ./...` · `go vet ./...` · `GOOS=linux go build ./...` clean. Whole module green under `-race` (28 pkgs) AND non-race (the authoritative first-paint 100ms gate), plus the `-tags integration` idle-CPU soak, plus all 5 fuzz targets actively fuzzed. Cross-tag builds (`realcli`, `compat_shim_v0/v2`, `integration`) all compile; the shipped untagged build is unchanged.
