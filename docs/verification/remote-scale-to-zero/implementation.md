# Remote-control v2 implementation evidence

Implementation started 2026-09-05 from `39df5bbc` (application baseline `f1621618`,
v0.13.27). Tracking: `agents-tracker-wjp4`. Architecture:
[ADR-027](../../adr/ADR-027-clean-remote-control-v2.md).

This is an evidence journal, not a completed release certificate. The owner authorized
TDD, implementation, independent review and direct integration on main. No current
users or compatibility requirements exist; unrelated local session state remains out
of scope.

## Review method

Sol owns native relay and transactional push work; Terra owns endpoint configuration
and bounded integration tasks. Each lane first records a failing check, implements,
then self-reviews. The orchestrator reads the actual production path and tests, reruns
checks, and sends concrete findings back for regression tests and fixes. A source-text
guard is not treated as runtime evidence; local platform tests are not hosted evidence.

## Observed checks

| Check | Observed result | Boundary |
|---|---|---|
| Android/publisher first red tests | Agent reported missing endpoint validator | Before implementation |
| `go test ./cmd/swarm-publish ./android/gate -count=1` | Passed, independently rerun by root | Go behavior and source gates, not a signed phone release |
| `go vet ./cmd/swarm-publish` | Passed, independently rerun by root | Publisher only |
| `sh android/gate/release-push-origin-contract.sh` | Passed, independently rerun by root: two Gradle tasks | Both provider URL forms accepted; malformed origins, credentials, ports, paths, queries, fragments, whitespace and DNS edge cases rejected; no signed phone release |
| Native relay first red test | Agent reported missing Worker entrypoint | Before implementation |
| Native relay first workerd test | Agent reported initial auth/pair/mailbox/revoke success | Root review found security/lifecycle gaps; not accepted as final green |
| Native relay workerd checkpoint | Root independently passed `RELAY_TEST_PORT=8791 npm test` | Actual local Worker/SQLite auth, home, pairing, mailbox, dedupe, ACK and revoke; subsequent Go-client and resource-cost review remains in progress |
| Firestore first red test | Agent reported missing official Firestore module | Before implementation |
| Firestore emulator checkpoint | Root independently passed five real-SDK tests in 7.883 s: shared nonce, registration retry/body conflict, registration transaction conflict, wake lease/token-generation CAS, one registration provider owner | Local emulator, not actual IAM/FCM/Play Integrity |

The endpoint review also reproduced a rejected-credential leak through Java URI's
exception cause: the Gradle contract matrix failed with `invalid origin credential
leaked through exception cause`. Removing the raw-input parser cause made the same
matrix pass (`BUILD SUCCESSFUL`, two tasks, 36 s). The input was a public test sentinel,
not an operator secret.

The first review requested transaction-bound relay authorization, canonical base64url,
pairing/resource limits, production/test configuration separation and alarm progress.
Push review requested stable idempotency across key rotation, a unique wake lease CAS,
unchanged retry deadlines, logical-expiry fencing and concurrency-safe cleanup. These
findings must be closed with runnable tests before acceptance.

Play Integrity repeated token decoding can clear verdicts. Therefore a completed,
body-bound registration retry must resolve from shared idempotency custody before
repeating provider verification. Provider-reuse rejection is a required test double
behavior; it is not a claim that Google guarantees exactly one successful decode.
[Google's standard-request contract](https://developer.android.com/google/play/integrity/standard#automatic-replay-protection).

## Reviewed main checkpoint and broader gates

The plan, ADR and configured push-origin slice were fast-forwarded into the owner's
main checkout through `04b4457f`. The active relay, Firestore and registry work remained
separate in the implementation worktree during review; a passing main test therefore
does not certify those unfinished ports.

The first broad test found an old ignored Android AAR behind the current public Go
facade. The prior AAR and sources jar were preserved in the task's `aar-before-rebuild`
directory and `sh android/build-aar.sh` rebuilt the unsigned arm64-v8a/x86_64 library.
No phone was installed, signed or published. The Android gate subsequently passed.

Main's `go build ./...`, `go vet ./...` and `golangci-lint run` passed (lint: zero issues).
The first sandboxed full test also hit forbidden process-inspection/module-cache writes
and an inherited `SWARM_SHIM_HOOK_SOCK`; a permitted local run with that variable unset
removed those environment failures. A later complete `go test -p 4 ./...` passed.

One preceding full run exposed a real randomized test-fixture bug in
`TestSH5_StatusSurfacesTheDeferredPurgeLedger`: replacing the first byte with `aa` did
not produce a different RID when the original already began with `aa`. Root reproduced
the missing-OWED failure deterministically with that prefix, then assigned distinct
`aa`/`bb` ledger prefixes; the regression passed five consecutive runs. This changes
only the status test, not revocation behavior.

The publisher passed `-race`; the changed endpoint/config/publisher-document Android
gates passed a focused race run. A full Android source-gate race run exceeded its
120-second test budget in an unrelated screen-regex gate, so it is not recorded as a
full race pass. The epic-wide race/release gates remain open.

The owner's later commit/push approval cleared public source publication. Main was
pushed through `764e82bd`, without a release/tag or deployment. The corresponding
[CI run](https://github.com/Nathandela/swarm/actions/runs/33950158347) completed
successfully, including the Linux test suite, macOS checks, lint, fuzzing, unsigned
release artifact checks and Android AAR/Gradle/debug-APK gates. This certifies that
published checkpoint only, not the subsequent worktree changes below.

## Transactional push review checkpoint

Root independently passed the expanded real-SDK Firestore emulator suite, initially
nine integration tests (11.293 s), then eleven integration tests plus the metrics
regression (12.653 s). The latter run includes registration admission/idempotency,
nonce/quota contention, token-generation and lease CAS, bounded retention, cancelled
provider attempts and the expiry tests described below. Two clients share emulator
state; the memory fake is not the evidence for shared transactional behavior.

Root's expiry regression first failed in both the memory fake and actual Firestore:
late successful completion bound an allocation, late UNREGISTERED erased token bytes,
an allocation expiring during a send became bound, expired completed results were
returned, and a never-claimed expired wake acquired a lease. The same contract passed
after adding at-use completion/claim fences. Expiry is checked before the completed
cache, independently of physical garbage collection. A separate provider test first
failed because the supplied context had no deadline. Provider calls now use the shorter
remaining original wake/lease duration; a provider response arriving after the wake
deadline cannot extend durable authority. HTTP `provider_accepted` reports that external
fact only, not phone receipt or successful late binding. Byte-identical expired retries
are rejected without another provider call. These tests advance the injected clock
across a provider call; they do not contact FCM or prove cross-host clock agreement.

The original wake deadline remains the envelope's issued-at time plus five minutes.
The handler allows up to two minutes of future issued-at clock skew, so the maximum
accepted deadline can be seven minutes from gateway receipt. Transaction retries still
use process-supplied timestamps; shared-clock and retry-boundary review remains open.

Root also reproduced fabricated empty-store metrics: the v2 admin handler exported
zero installation/address/tombstone/database gauges without observing Firestore.
Those unavailable gauges are now omitted, with no collection scans introduced.
Actual process request/retention metrics remain. The regression constructs a Firestore
repository without a client and verifies that metrics do not query shared storage.

After these changes, root passed the push library and command package race tests
(5.497 s / 3.695 s). The production command still starts the old local repository while
the separately refused startup cutover awaits scoped approval. The new Firestore
repository/keyring are a reviewed foundation, not a deployed replacement.

## Native relay review checkpoint

Root independently reran the combined native workerd suite and the Go integration
tests: Worker negative controls, real Noise/SAS and encrypted bidirectional delivery,
reconnect/replay/revocation, and a separate alarm-disabled expiry case with more than
256 expired rows. The Go runs passed in 1.993 s and 4.388 s.

The actual SQLite cursor counters for 100 sequential append/deliver/ACK cycles were
3,000 rows read, 1,600 rows written and 2,900 statements. First and hundredth append
both measured 11 reads/11 writes; first and hundredth ACK both measured 19 reads/5
writes. This is an active-traffic microprobe, not a bill estimate: idle/cleanup alarms,
hosted eviction and retained-backlog work must also be measured.

That alarm review found global joins/grouping and a union-wide minimum expiry which
scanned retained history. Sol's real-workerd RED measured 21 reads at one retained item
versus 1,939 at 300 items across 32 streams. Indexed per-stream cleanup, the existing
acknowledged-receipt counter and three indexed minimum-expiry queries replaced those
scans. Root independently passed the stricter final fixture: one item/one stream used
14 reads, while 300 items across all 64 directional streams used 203 reads; both used
zero writes and nine statements. The exact earlier rendezvous expiry remained the next
deadline. This establishes a bounded-stream cleanup cost, not constant cost or a hosted
bill. Root's final combined Go/workerd runs passed in 1.942 s and 4.499 s.

## Ongoing review boundaries

The local Go-to-workerd slice uses real Noise/SAS and encrypted mailbox primitives,
but its replay check initially used an in-memory receiver, not the production phone's
durable checkpoint or the machine's command/PTY execution loop. It cannot certify
local-before-ACK crash recovery or uncertain raw-input behavior.

Root review is testing slow-reader/subscription bounds, at-use expiry independent of
bounded GC, and actual SQLite row work rather than inferring low cost from serverless
deployment. Receipt suppression is a bounded optimization, not exactly-once execution.
No retained-byte ceiling or operation-rate setting is evidence of unchanged capacity.

Registry review rejected both a reused fixed staging directory and a separate retirement
marker. The current direction stores a fresh bootstrap namespace in the same registry
authority commit as last-pairing removal. Reserved-path protection, crash/reopen,
uncertain directory-fsync retry and existing multi-machine startup are acceptance gates.

## Foundation integration checks

The relay and push foundations were committed as `8c930032` and `89c6c98b`.
Root's final push package/command race run passed in 7.818 s / 4.097 s;
the actual Firestore emulator suite also passed with the race detector (13.604 s).
The implementation worktree passed whole-repository build, vet and lint (zero
issues). These checks preceded the next server-clock revision and do not certify it.

A broad main-branch test run did **not** pass: its 240-second per-package limit
expired in skeleton and mobile conformance; it also exposed a credential-name
test that incorrectly treated `/private/tmp` as an environment variable name,
and the expected unreachable v2 entry points. The credential test now parses
actual environment names while retaining raw/hex/base64 secret-byte checks and
an explicit nonempty-parser assertion. The isolated old-main SAS test passed
on rerun (1.383 s); this is not a replacement for the complete suite.

The existing bidirectional reachability ledger lists each unfinished v2 public
entry point explicitly. An entry becoming production-reachable fails the gate
until its exemption is removed. Neither this temporary ledger nor integration
tests stand in for the missing phone/gateway callers or authenticated home-profile
custody; P1/P4 remain open. The memory repository is a test fake, not a selected
production backend.

CI now includes a dedicated workerd and real-SDK Firestore-emulator job, because
ordinary Go tests skip those integrations when the local services are absent.
The job has read-only repository permissions and requires no cloud credentials.
Its service commands passed locally; a hosted workflow run is still a separate
verification result.

The first hosted run of the new job (`33952727197`, commit `97a436f9`) passed
the Worker protocol checks, then failed because `rg` was absent on the Linux
runner. The shell harness now uses POSIX `grep -F` for its two log assertions
and retains the normal content-addressed Go build cache; `-count=1` and fresh
Worker storage still make each integration execution fresh. The failure was
not ignored, and the Firestore step in that run was skipped, not passed.

Root's local harness review also observed one authentication refusal during the
32-phone cost fixture under shared load, then a successful rerun. That fixture
had inherited a 250 ms challenge lifetime intended for the unrelated expiry
negative control. Cost/retention fixtures now use the production 30-second
challenge lifetime; the protocol expiry control keeps a shortened one-second
lifetime and still asserts closure. Authentication failures include their exact
response in future diagnostics. This changes test setup, not production timing.
The final stable rerun passed (Go/workerd 1.784 s / 4.542 s), with unchanged
100-cycle counters and alarm reads of 14 / 203.

The longer main test run exposed a separate daemon survival timeout: its
subprocess had launched zero of three agents at the 20-second fixture bound.
Terra's sterile isolated rerun passed (test 4.42 s, package 7.367 s). The host
re-executes TestMain's two Go builds before reaching its first launch, inside
that parent's deadline. Contention is a plausible explanation, not proof of
the failed process's exact timing; the adjacent other-instance log line was
not established as its cause. No daemon production code was changed.

## Operator access and changes

Read-only access verified the dedicated Google project `swarm-8404f` and enabled billing.
The CLI's selected default project is unrelated; every Swarm command uses an explicit
`--project=swarm-8404f`. Existing VMs, disks, addresses, snapshots, IAM and running local
Swarm sessions were not changed by these checks.

On 2026-09-05 the operator-authorized implementation setup enabled only
`run.googleapis.com`, `firestore.googleapis.com` and `cloudbuild.googleapis.com` in that
project. The service-enablement operation succeeded. This did not create a database,
deploy a service, publish an app, expose a public endpoint, or delete old infrastructure.

After API enablement, metadata-only inventory found no Firestore databases, Cloud Run
services, Secret Manager secrets or Artifact Registry repositories in this project.
No Android device was attached at the initial `adb devices -l` check.

Cloudflare CLI access was unauthenticated at the initial check; operator login/account
selection was requested. Hosted relay, real ADC/Play Integrity/FCM, phone lifecycle,
latency distributions, recovery and billing verification have not yet passed.

The safety reviewer separately paused removal of the obsolete SingleMachineManager
implementation/tests and the production push-command switch from bbolt/local-key startup
to Firestore/injected keyring. Fresh scoped owner approval was requested; these refused
mutations were not retried through another tool or applied by the orchestrator.
