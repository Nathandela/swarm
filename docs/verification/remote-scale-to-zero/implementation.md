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
| Firestore first red test | Agent reported missing official Firestore module | Before implementation |

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
