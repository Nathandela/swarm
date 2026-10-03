# CLI session refresh verification

Implementation base: current main / v0.14.3 (`4c660259`). The initial local checkout
was older; the feature was rebased before publication and installation to preserve
the released Codex resume and pilot behavior.

## Committee review

The requested audit committee command was read from the owner's
`.claude/commands/audit-committee.md`. Its configured GPT-5.6 sol reviewer ran as an
available agent after local CLI sandbox startup failed. Configured Claude Sonnet
and Opus reviewers could not authenticate (expired OAuth); agy no longer offered
the configured Gemini 3.5 model. Approval from unavailable members is not claimed.
Two additional independent code/test reviewers examined the design, executable
fixtures, upstream integration, and final changes. The final available reviews
recommend shipping subject to passing quality gates.

Consensus requirements were source-history retention, one durable lifecycle
authority, all-state replacement recovery, per-session installation evidence,
Codex dual-process checks, and conservative idle/interaction/input gates.

Independent review and executable regressions exposed and corrected:

* Discovery and pending-work starvation beyond eight distinct environments.
* Candidates superseded by a later installer update before a safe restart.
* Recording a destructive claim before completing the version probe.
* Turn activity changing during that probe.
* Trusting launch metadata without matching shim startup evidence.
* Declaring a degraded Codex replacement complete without conversation transport.
* Losing a CLI-owned recovery obligation when the separate auth reason clears.
* Comparing a session's unchanged saved-HOME credentials to another daemon HOME.
* Resuming a source archived at the final lifecycle boundary.
* Clearing lifecycle ownership before a completion/blocked checkpoint persisted.

The key design distinction is explicit: a successful launch or a seeded native
conversation ID is not proof of successful provider conversation adoption. Codex
also requires existing subscribed transport readiness. Claude's refresh completion
is limited to the live replacement and stable startup observation. Source history
is retained in both cases. Arbitrary wrapper dependencies and future provider
compatibility are not attested by filesystem observations.

## Executable and race evidence

`TestCLIRefreshInstalledUpgradeRealResume` drives the actual watcher, daemon core,
shim, and a disposable mock Claude executable. An atomic v1-to-v2 replacement
leaves the old process on v1; an eligible idle source is replaced with a v2 process
carrying the same conversation ID, model, environment, name, tag and lineage. Old
and new sessions report their own versions even when the daemon's cached version
is deliberately wrong. The source history remains present.

The shim integration fixture starts actual mock backend and terminal child
processes. Swapping the executable between their launches produces mixed versions
and no stable startup observation. A stable pair produces the observation.

Deterministic acceptance/review/durability tests cover saved environments,
unknown/malformed/prerelease/downgrade versions, bounded probes, candidate changes,
large-roster fairness, input drafts, owner/archive races, simultaneous auth changes,
running and ended child recovery after a crash, missing or mismatched observations,
startup deadlines, independent disable with owed recovery, launch backoff, corrupt
settings/state, and failed state-file commits.

The independent process/acceptance suites passed three repetitions under the race
detector. The separate identity/review suites passed twice under the race detector.
Latest-main resume/auth/history, Codex adapter, shim argument handling, and pilot
compatibility tests were additionally exercised under the race detector.

## Existing test stabilization

On pristine v0.14.3, `TestPilotRealCLIControlsTwoWorkersWithoutChangingOrdinaryCLI`
failed three of five race-enabled repetitions because an initial prompt could
join the exact echoed receipt line (`>got: ...`). The test now waits for each
worker's initial prompt before sending. Its exact payload assertion is unchanged;
five subsequent race-enabled repetitions passed. No production pilot behavior
was changed for this fixture correction.

## Reproduction

Final frozen-code verification on macOS, 2026-10-03, code commit `07cd55be`:

| Gate | Result |
| --- | --- |
| Full Go suite | 71 packages passed; 7,448 test/subtest passes, 63 skips, zero failures |
| Targeted lifecycle/pilot race suite | 6 packages passed; 614 test/subtest passes, 2 skips, zero failures |
| `go build ./...` | Passed |
| `go vet ./...` | Passed |
| `golangci-lint run --timeout=10m` | Zero issues |
| Linux amd64 CLI cross-build (`CGO_ENABLED=0`) | Passed |

Counts include parent tests and subtests, not independent scenarios. The new
actual watcher/mock-process upgrade test executed successfully; provider/network
and platform-gated skipped tests are not claimed as verified. Earlier runs during
rebase were discarded because nested fixture builds observed conflict markers;
the results above are from the completed, conflict-free source tree.

Run Go tests with inherited `SWARM_*` session-routing variables removed. Fixtures
create their own isolated state and fake processes. Process-inspection tests need
working `ps`/`pgrep`; sandbox refusal is an environment failure, not a passing test.

```sh
go build ./...
go vet ./...
golangci-lint run --timeout=10m
go test -json -p 4 -timeout=20m ./...
go test -race ./cmd/swarm ./internal/skeleton ./internal/shim \
  ./internal/daemon ./internal/persist ./internal/adapter/codex \
  -run 'CLI|Auth|Recycle|Owner|Resume|Backend|Version|Pilot' -count=1 -timeout=15m
```

The mock upgrade integration can use a selected installed shim executable:

```sh
SWARM_REFRESH_TEST_BINARY=/usr/local/bin/swarm \
  go test -race ./internal/skeleton \
  -run '^TestCLIRefreshInstalledUpgradeRealResume$' -count=1
```

These tests do not update the owner's provider packages, use real conversations,
or claim successful authenticated model calls. Rollback and manual recovery
constraints are documented in the [runbook](../ops/cli-session-refresh.md).
