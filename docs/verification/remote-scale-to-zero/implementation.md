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
