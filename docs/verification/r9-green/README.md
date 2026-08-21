# Wave R9 (automated half) - green evidence

Scope: the six automated release-gate gaps found by the R9 scoping audit of playbook
sections 10-11. The owner-physical half of R9 is parked as beads (see the commit message
and agents-tracker-hggx.10's closing note); this file records the automated half only.

## What the scoping audit found already DONE (verified, not trusted)

Section 11.1 items verified in place with evidence: vet + race-with-pinned-gomobile in
ci.yml; focused stress tests per area (shim lifecycle, cancellation, reconnect,
idempotency, callback overflow, multi-machine isolation - files named in the audit
report); input-transaction and operation-journal crash tests; Android lint + Robolectric
against the real built AAR with the androidgate job; dependency verification;
profile/signed-body/session-instance/downgrade/unknown-action negatives; WakeV1 suite;
relay backup/restore, trusted-proxy and quota tests; docs manifest + protocol.md drift
checks; release prerequisite enforced by the ACTIVE repository ruleset main-required-ci
(id 20874942, 13 required contexts) - classic branch protection 404s, which is expected.

## The six gaps closed in this batch

1. Phone 500 ms polling loop (section 10, primary path violation) -> mobile drain now
   uses relay MailboxWait; 500 ms poll only on real relay refusal. Tests:
   mobile/r9_waitfallback_test.go, mobile/conformance/r9_waittail_test.go.
2. Relay container gates -> .github/workflows/relay-container.yml (build, trivy scan
   fail-on-HIGH/CRITICAL, non-root assert), deploy/relay/docker-compose.yml hardened,
   deploy/relay/hardening_test.go, deploy/relay/.trivyignore.
3. Stress-count lane -> .github/workflows/stress-nightly.yml, six areas, elevated
   -count under -race, all -run selectors proven non-empty by go test -list.
4. Per-hook-stage daemon-kill matrix -> internal/skeleton/r9_hookstagekill_test.go,
   stage list derived from the production adapter table.
5. Production-jitter test -> internal/remote/relay/r9_productionjitter_test.go.
6. Named phone-side wake-TTL expiry test -> mobile/r9_wakettl_test.go.

Failing-first runs and mutation proofs: docs/verification/r9-red/*.txt (one file per
area). The closing reviewer re-ran four mutations by hand (drain-forced-to-poll, wait
cursor*0, reconnect ceiling cap disabled, hook fold-cursor persisted as 0), each fence
fired, restores byte-verified.

## Gate table (orchestrator run, 2026-08-21, r9_audit_go.log)

| Gate | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `golangci-lint run` (v2.12.2, = CI) | 0 issues |
| `go test -race -count=1` (owned packages) | exit 0 |
| `go test -count=1 ./...` — THE gate, whole repo | exit 0 (09:10-09:26Z) |

Machine hygiene: 0 orphans / 21 PTYs before; the run left 54 rig orphans (the known
agents-tracker-ev0w class), reaped by explicit pid after commit. The closing reviewer's
independent run agreed: build/vet 0, lint 0 issues, whole repo exit 0 (62 packages ok),
actionlint 0 on both new workflows.
