# Epic 1 — Evidence

**Epic**: Foundations — binary, status kernel, persistence, fake agent (`agents-tracker-je2`)
**Commits**: 92a7051 (bootstrap), 973efcc (status + fakeagent), 4798a13 (persist), e4492b1 (review fixes r1), afaa532 (artifact removal), 3ba3c8b (review fixes r2).

## TDD evidence (GG-5)

Failing-first (red) runs recorded by each test designer before any implementation existed; excerpts committed under [epic-01-red/](epic-01-red/) (bootstrap, status, persist, fake). All four show missing-implementation compile failures (`undefined: dispatch/Process/Store/Parse...`), not test bugs — independently confirmed by both reviewers.

## Criterion walk (E1.1 – E1.9)

| Criterion | Evidence |
|---|---|
| E1.1 single distributed binary, role dispatch | `cmd/swarm` with testable `dispatch()`; daemon/shim/hook routed to stubs; `swarm-fake-agent` is a separate dev binary (never shipped) |
| E1.2 CGO_ENABLED=0 cross-compile, static | CI `build` job: 4-target matrix; linux binaries asserted "statically linked" via `file`; `build-darwin` job asserts otool shows system libraries only. Interpretation recorded in ci.yml: darwin cannot be fully static (no static libSystem) — fully static ELF on linux, system-libs-only on darwin |
| E1.3 status kernel, derivation 1:1 | `internal/status`: 36-row table-driven test (spec has 4 interaction values — the test designer caught the 4th, `unknown`, missing from the initial API; pinned: running∧idle∧interaction-unknown → ready_for_review since the spec's Needs-input set is exhaustive), wire-string constants pinned, completeness sweep |
| E1.4 atomic meta, crash model, isolation | temp+rename same-dir with fsync; SIGKILL helper-process injection test (15 cycles, readiness-gated, kill/wait errors checked, old-or-new asserted — vacuity objection resolved in r2); corrupt session isolated by Scan; decode-time validation rejects `{}`/id-mismatch (r1) |
| E1.4b all S-2 fields round-trip | `TestSaveLoadRoundTripAllFields`: DeepEqual on every field + 14 snake_case on-disk keys |
| E1.5 schema_version + migration primitive | migration registry `map[int]func(*Meta)` applied in ascending order; gap in the chain errors loudly (r2); future version errors; Save stamps current version unconditionally (r1) |
| E1.6 roster rebuild-by-scan | `TestRosterRebuildableByScan` — no index file needed |
| E1.7 retention/delete + resumed_from | `TestDeleteRemovesSessionDir`, `TestResumedFromRoundTrips` |
| E1.8 env allowlist | Normative list documented in `env.go`; FilterEnv tested (drops injection vectors + unrelated secrets, passes PATH/HOME/locale/venv/provider keys); enforced as a Save choke point per ADR-004 (r1) |
| E1.9 fake agent | print/ask/idle/exit; in-package suite + exec-level binary smoke; stdin-script+ask rejected with clear error (r1); fall-off-the-end = exit 0 (pinned contract) |

Additional pinned contract (ADR-004): session ids validated `^[A-Za-z0-9._-]{1,128}$`, no `.`/`..`/leading `-`; symlinked session dirs rejected in Save/Load/Delete and skipped by Scan (r1).

## Quality gates (GG-4)

`gofmt -l .` empty · `go build ./...` · `go vet ./...` · `go test ./... -race -count=1` — all green at close (44 test functions across 5 packages). CI: lint (pinned golangci-lint), vet, race tests, 4-target build matrix + darwin dylib job, docs gate.

## Review outcomes (protocol steps 2/5 — cross-model required for Epic 1)

- **Opus (independent, saw no implementation)**: APPROVE; 5 LOW notes, all addressed or recorded (vet step added to CI; stdin-ask fixed; FilterEnv wired into Save; case-collision + fsync scope notes below).
- **codex GPT-5.6 sol (cross-model), round 1**: FIX REQUIRED — 7 HIGH / 3 MEDIUM. All fixed in e4492b1 except four carried to round 2.
- **codex round 2**: 6/10 OK; residuals (vacuous crash test, silent migration gaps, darwin static claim, GG-5 evidence) fixed in 3ba3c8b + this file; new MEDIUM (committed build artifact) fixed in afaa532.
- **codex round 3 (final confirmation)**: see addendum below.

## Accepted deferrals (recorded, with rationale)

1. **Case-insensitive id collisions** (codex M8/Opus L3): store is internal; ids are generated, not user input. Deferred to Epic 5's id generator (lowercase-only) — acceptance test noted on the Epic 5 bead. Condition set by codex accepted.
2. **No parent-directory fsync** (codex M1/Opus L2): ADR-003's crash model is process crash, not power loss; temp file is fsynced before rename. Codex round 2: "deferral is acceptable."
