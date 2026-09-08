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
accepted deadline can be seven minutes from gateway receipt. At this checkpoint,
transaction retries still used process timestamps; the later server-clock review below
replaces that wake-path limitation, without claiming registration clocks are complete.

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

## Dependency security checkpoint

The push container scan on `97a436f9` rejected gRPC v1.67.3 with three advisories.
Terra upgraded to v1.83.1, the patched version for
[CVE-2026-84304](https://github.com/advisories/GHSA-vp52-pcj8-j9qc), also beyond the
fixes for [GO-2026-4762](https://pkg.go.dev/vuln/GO-2026-4762) and
[GHSA-hrxh-6v49-42gf](https://github.com/grpc/grpc-go/security/advisories/GHSA-hrxh-6v49-42gf).
Root checked the upstream advisories/release. This is dependency remediation;
the scanner result alone does not prove all affected server paths were exposed
by this application. No ignore rule or security-check bypass was added.

The ordinary Go minimum-version graph also selects Firestore v1.21.0:
gRPC v1.83.1 requires gax-go/v2 v2.17.0, whose genproto requirement selects
Firestore v1.21.0. This was not a blanket latest-version upgrade. The repository
Go floor remains 1.25.0. Terra passed module verification, whole-repository build
and vet; the clock revision's tests were still in progress at this checkpoint,
so clean-main package/emulator and the new container scan remain separate results.

## Server-clock wake review

After the dependency update, root independently passed clean-main push, command and
native relay race tests (6.070 s / 4.555 s / 2.656 s) and the actual Firestore v1.21
emulator race suite (12.042 s). The push-container workflow `33953707223` then passed,
including its vulnerability scan. The dedicated local-service CI job in `33953202959`
also passed; that run's overall suite still failed an existing disconnected-peer
fixture and is not recorded as wholly green.

Sol's next wake revision first reproduced stale-clock claims/completions in the actual
emulator. Wake expiry, future timestamp skew, unbound allocation expiry, token state
and quota checks now use the latest transaction snapshot's server ReadTime. The v2
handler no longer rejects a valid server-time wake using a skewed process clock or a
stale installation/token pre-read. Invalid capabilities still stop after their address
read, before attempt, installation or quota reads.

Direct production-method tests force an aborted first commit through the actual SDK
against a small gRPC test server: retry-local deadline reset, fresh-read expiry on
claim/completion, abandoned first-attempt writes, bad-capability read count, and a
lease already expired at commit. The real emulator separately verifies ReadTime on
missing snapshots and skewed callers. These are distinct evidence sources; the gRPC
test server is not presented as the Firestore service.

Provider and completion calls share one monotonic operation deadline derived from the
server budget minus the entire local claim round trip. Tests cover process clocks
24 hours behind/ahead and identical provider/completion deadlines. Claim CommitTime
additionally suppresses provider submission if the durable lease or original obligation
has already expired. Root independently passed the final full push package with the
actual emulator and race detector (17.525 s); Sol's run passed in 19.812 s, and the
independent Sol review found no remaining must-fix after its focused race run (6.088 s).

The exact guarantee is authority checked at latest transactional ReadTime plus bounded
client execution, **not** an atomic wall-clock predicate at Firestore commit. A client
timeout cannot prove a commit did not land, and rejecting provider ownership after a
late claim commit does not undo attempt/quota writes. Registration/other repository
clock paths, real IAM and FCM remain separate gates. The memory repository now exists
only in the Go test build; it is not an alternate production backend.

Root then passed the implementation worktree's full build, vet and lint (zero issues).
The broader local main run still did not pass as a whole: skeleton completed in
520.604 s and mobile/conformance in 250.377 s, but daemon launch timed out and the
transport package was terminated at 660 s. Both failed packages passed isolated
reruns (daemon test 4.42 s, transport package 12.538 s); the transport hang's cause
was not established. These reruns are not a full-suite success claim.

Hosted CI `33953202959` exposed a separate ordering flaw in the existing mailbox
discard fixture: client CloseNow/Done does not acknowledge the server's session
removal. Root reused `awaitGatewayDrop`, which observes removal under the server
lock, before the unchanged deletion-refusal and retained-item assertions. The
original failed in CI but passed 50 local repetitions; after synchronization,
100 race-enabled repetitions passed (13.492 s). No relay production behavior or
compatibility policy changed in that fixture correction.

## Phone foundation branch checkpoint

The registry-only phone changes are checkpointed separately from main. This is
deliberate: the current additional-machine UI still inserts an unauthenticated typed
ID without a ceremony, while Android opens with an empty selected ID. Two entries
therefore make the new constructor refuse an ambiguous restart. Selection also does
not retarget the live App, and Android stores only one relay URL. These production
caller gaps remain real; the foundation is not a working multi-machine release.

The tested foundation uses fresh schema-3 registry state with independently named
namespaces and a random persisted bootstrap namespace. It refuses old layouts rather
than importing them. First authenticated pairing commits the exact namespace before
ACK, and an existing pairing-attempt record durably gates unresolved completion across
restart. Record writes use file sync, rename and directory sync; a shared mutex prevents
stale progress writes from erasing a pending-ACK marker. Last-machine removal commits
a newly generated bootstrap namespace in the same authority write. RegistryManager
retains live bookkeeping on precommit failure but respects a committed removal after
post-rename or cleanup errors. The latter manager-boundary tests were added after the
fix, not represented as an observed RED.

Fixture changes provision registry namespaces only when a scenario begins already
paired. Fresh scenarios no longer Start before pairing. PBPAIR5 now starts its receive
loop after the first authenticated pairing and before awaiting delivered authority;
PBAPP9 directly checks the new NotPaired Start classification. The removed pre-pair
normal-transport assertions describe a state the fresh lifecycle cannot enter; their
post-pair online and delivered-pin checks remain. No conformance test function was
deleted in this fixture revision.

Sol's full non-race mobile run passed (mobile 101.248 s, conformance 271.388 s) using a
writable module cache and a 600-second package timeout. Root passed focused bootstrap
race tests (3.869 s), registry/removal race tests (5.851 s), and the complete verification
package (15.952 s). The reachability gate caught the now-stale NewMachineRegistry
exemption; it is removed on this branch. Nine now-unreachable old manager exports are
listed individually only because their source deletion was separately refused and is
still paused. They are not a v2 fallback, and this exception cannot certify P4.

Terra's final full phone race gate also passed: phonecore 44.265 s, mobile 71.413 s,
and mobile/conformance 251.283 s, with exit zero. This used the sterile hook environment,
checked-in Android toolchain environment and writable module/build caches; no Gradle
or AAR rebuild was part of that run. The earlier unusable runner invocation is not
counted as evidence. This final race result still does not resolve the caller gaps above.

The phone foundation is local commit `5c130460`. Its push was explicitly rejected by
the safety reviewer because publishing mobile internals to the public repository was
not covered by the general push approval. A fresh scoped approval was requested, and
no alternate push/merge route was attempted. Reviewed main remains at `33608f35`.

The next coherent caller slice needs atomic active-machine/per-pairing relay custody,
authenticated additional-pairing staging, and Android selection/rebuild. Merely picking
an arbitrary registry row on restart would hide the problem and could dial the wrong
relay. Full concurrent per-machine event/connection wiring remains beyond that slice.

## Completed-main CI and deadlock-fixture ordering

Main `33608f35` CI `33954039671` finished with one failure, in the existing skeleton
deadlock regression harness: the child returned nil/PASS before the parent's next
readiness poll. The dedicated remote-v2 job, Android job and all other jobs passed;
both container workflows (`33954039677`, `33954039685`) passed, including the push
vulnerability scan. This is not an overall green CI result.

The harness now checks its durable readiness marker when the child result arrives
first, accepting only a present marker plus a successful exit. Missing readiness and
failed child exits still fail; the 30-second setup and 3-second production deadlines
are unchanged. No production code changed. Sol independently reviewed the actual diff
and found no must-fix. The original passed 20 local non-race repetitions (78.736 s),
while the correction passed 20 race repetitions (75.220 s) with the race runtime's
artificial exit sleep disabled.

A temporary scheduling experiment delayed readiness polling until after the setup
deadline, forcing the completed-child result branch. The original reproduced the
exact nil/PASS failure (9.916 s package time); the correction passed (5.281 s). The
ordinary 20 ms poll was restored afterward; the temporary schedule is not committed.
This isolates the harness ordering bug without weakening its deadlock assertions.

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

The owner subsequently explicitly approved all three held source actions: publishing
the phone checkpoint to the public repository, deleting the obsolete manager and its
tests while retaining shared primitives, and switching production push startup to
Firestore with the injected keyring. Implementation/review resumed under that authority.
This approval does not include deployment, infrastructure deletion or phone resets;
hosted/device validation and the phone caller gaps above remain separate open gates.

## Approved source cleanup: phone manager

The approved phone checkpoint and merged main evidence were published on
`plan/remote-scale-to-zero` as `70950f8a`. Main `2beeefb9` subsequently completed all CI
jobs successfully. The branch checkpoint exposed two additional deterministic fixture
failures: Android's source scanner treated an internal pending-ACK storage marker as a
public screen state, and seven CLI recovery tests started a fresh unpaired app and used
the old flat state path. Those are separate follow-up corrections, not a flaky-main claim.

Terra first observed RED from a new guard finding all five obsolete singleton symbols,
then removed the unused manager, adapter alias/constructor and refusal error. Shared
MachineClient/MachineManager interfaces, CoreMachineClient and RegistryManager remain.
Lifecycle, stop-signal and aggregate-stream tests now exercise the retained registry.
Root review requested restoration of the exact four-event/order/all-field comparison
and widening the guard to every non-test phonecore source file; both were applied.

Terra passed full phonecore tests (24.973 s), race tests (41.656 s) and vet; after the
review amendments, its focused race run passed (4.802 s). Root independently passed
five focused race repetitions including those amended assertions (11.923 s).
The nine paused-manager reachability exemptions were deleted with the symbols.
These checks do not complete the phone's multi-machine production caller wiring.

## Approved source cutover: Firestore command and operational bundle

Sol implemented and a second Sol independently reviewed the sole production command
path through NewFirestoreRepository/NewFirestoreServer. Startup requires an explicit
project and canonical namespace, the default Firestore database, a strict mounted
versioned keyring and a bounded nonempty owner key allowlist. Secrets are checked before
ADC; the same token source with FCM/Play/datastore scopes is passed to all clients.
Production refuses emulator configuration and forwarded-header trust; development
requires explicit emulator opt-in. The command honors validated PORT unless -listen
overrides it; the non-root image defaults PORT/EXPOSE to 8080.

The old database flag, backup/restore commands and retention interval/ticker are gone.
One-shot `retention` performs a bounded pass with a 30-second operation deadline;
no listener, public cleanup endpoint or schedule is created. Readiness uses the same
five-second store check as startup, with a one-document-limit query rather than treating
all NotFound responses as a harmless absent sentinel document. The actual-SDK gRPC
test distinguishes an empty collection from a missing database. This read is not a
write-IAM or FCM/Play reachability proof. Serving metrics no longer invent a zero-valued
cleanup timestamp for a separate job.

Observed REDs covered the absent admission loader, background-worker-dependent readiness,
and reused HMAC/AES material. Tests now require the exact unknown-command/undefined-flag
errors for retired command surfaces, not any error from incomplete configuration.
Sol's full emulator race passed (command 5.639 s, internal 16.368 s); the independent
reviewer passed in 5.814 s / 20.128 s and reported no remaining must-fix. Root separately
passed full build, vet and lint (zero issues), and full emulator race in 8.748 s /
20.803 s. The last test-only tightening/import-group correction was followed by a
focused command check; no production logic changed after that full emulator run.

Root's operational-source guard first failed on the four old VM manifests and obsolete
runbook commands (0.845 s), then passed after their source-only removal. Docker hardening,
scanner and release gates remain; CI now runs the full command and push packages inside
the Firestore emulator. Active docs describe only v2. The retained GCP VM inventory is
explicitly an operator safeguard, not a v2 deployment instruction. Removed source files
remain recoverable in git; no existing VM, database, key, disk or backup was touched.

A separate attempt to delete the unused bbolt backup/restore implementation, its tests
and interrupted-restore guard was refused by the safety reviewer. Nothing in that patch
was applied; exact owner approval was requested and those sources/guards remain intact.
The new test expectation for that unapproved deletion was withdrawn, not counted green.
Backup/Restore have precise paused-source reachability entries and no command route.
NewServer separately remains only for old push/phonecore test fixtures; porting those
fixtures and removing the remaining old handler branches is unfinished P4 work.

The allowlist is not a secret invitation or registration proof of possession. A licensed
client reusing a known allowed public key can create inert duplicates and consume bounded
storage/provider/quota work; it cannot authenticate later operations without that key's
private half. Real onboarding hardening, Google IAM/attestation/FCM, scheduled cleanup,
hosted ingress/latency/cost and revocation-safe restore remain open launch gates.

## Registry-only CI fixture reconciliation

Terra reproduced the CLI fixture's pre-pair Start refusal locally (RED 1.745 s), then
made the existing actual owner QR/SAS/confirmation ceremony finish before Start. The
corruption fixture resolves the sole authenticated registry entry and corrupts its
namespaced state, explicitly refusing a flat-root fixture. All PBSTATE10 tests passed
(5.225 s); root independently passed three race repetitions (17.475 s).

The pending-ACK storage constant was renamed outside the public `pairX` naming convention;
its persisted bytes and App.PairingState-to-failed mapping are unchanged. Android's
public-state parity scanner required no exclusion or unreachable Kotlin UI state.
Root passed the bootstrap-focused mobile race suite (3.802 s) and the full Android source
gate (12.073 s). No local AAR, Gradle or device rebuild was part of those checks.

## Full-suite release inventory follow-up

The reviewed Firestore cutover was published as `1431618f`. The broader local suite
caught a missed relative-path consumer: the release-version gate still opened the two
removed push-gateway Compose/env examples. Terra reproduced that exact missing-file RED,
removed only those two obsolete inventory entries, and passed the full release package
(0.782 s). Sol independently traced the remaining examples, dynamic image tags, digest
manifest and provenance checks, found no active stale references and passed the package
(0.562 s). Root reviewed the two-line change and passed every deploy package with race
detection (1.987–2.304 s). The four active release-version examples remain checked.

The sandboxed whole-repository run also reported denied macOS process inspection and
related daemon/child-process failures. An approved, isolated rerun outside the sandbox
passed all seven reported daemon tests in 8.572 s without source changes. That separates
those environment failures from the real release-inventory regression; it is not a
whole-suite green claim. At this checkpoint the remote Worker/Firestore emulator lane,
lint, macOS checks, fuzzing, build matrix, release dry-run and both container workflows
passed; the full Go and Android jobs were still running. No hosted or device validation
is inferred from those CI results.

## Registration proof: TDD, caller integration and review

The next authorized slice (agents-tracker-wjp4.3) adds mandatory P-256 registration
proof using the existing installation signer. The exact transcript is
`swarm-pg-register-v1|Idempotency-Key|base64url(SHA256(final request body))`.
The final body includes the attestation token; Play's existing JCS request hash does
not change. One canonical low-S P1363 proof header is required even on a completed
retry, after owner admission and before shared lookup, quota, attestation or allocation.
No crypto dependency, mobile public interface or persistent field was added.

Sol observed server RED: absent/forged proofs reached attestation instead of returning
401. The client and Terra's mobile bridge tests separately observed zero signer calls
before registration, HTTP despite a signer error, and lost-response uncertainty not
surviving a later signing failure. The implementation re-signs the existing saved
body/key for each POST. An ephemeral prior-outcome flag preserves a possibly committed
attempt across later local failures, while a never-sent first signing failure leaves
no pending registration. Restart replay is checked against the exact server-minted ID
captured before the first response was deliberately lost, with one phone attestation.

Root review restored an existing rotation assertion that registration signing could
otherwise satisfy, removed unbounded proof-channel waits, tightened the one-field domain
mutation and noncanonical base64 cases, and required missing/wrong-key/high-S proofs to
leave the repository, quotas and verifier untouched. Actual Firestore checks cover zero
records for a non-holder, exact ID on signed replay, unchanged quota and one attestation;
the stored-data scan now also excludes raw registration proof and idempotency key.
Both Sol agents independently reviewed the opposite implementation and found no remaining
runtime/security must-fix. API/retention and operational documentation now agree with
the proof and shared durable metadata, without retaining an unsigned-registration rule.

Final local evidence: full phonecore race 38.928 s; Terra's full mobile/conformance/
Android source-gate race 84.461 / 252.367 / 109.666 s. Root independently passed three
focused caller-race repetitions (phonecore 6.319 s, mobile 5.378 s), full verification
(24.476 s), all deploy gates, whole build/vet and lint (zero issues). Root's separate
Firestore emulator passed full push/command/pushreg race suites in 16.889 / 6.674 /
2.909 s and shut down cleanly. These checks do not prove Google IAM: the local emulator
allows test reads/writes and uses no real Play/FCM or phone Keystore.

The prepared-proof verification benchmark measured 162,958–174,783 ns/op over three
Sol runs, with 1,632 bytes and 26 allocations per operation; a separate short reviewer
run measured 121,835 ns/op. These are local verifier microbenchmarks, not end-to-end or
handset/hosted latency claims. Admission still permits a legitimate key holder to create
fresh registrations subject to attestation and quotas; there is no per-key uniqueness
constraint or invitation-secret lifecycle. Hosted ingress/device/recovery/billing gates,
the remaining mobile multi-machine caller work and the separately paused bbolt recovery
source deletion remain open. The immutable proof checkpoint `0dc1801d` subsequently passed
all 14 jobs in CI run `33960749099`, including full Go race, Android AAR/Gradle and the
remote-v2 runtime/emulator lane; both container runs `33960749114` and `33960749070` passed.

## Registration response uncertainty: follow-up before main integration

A further read-only review found a pre-existing caller bug, unchanged by the proof commit:
any received HTTP response was treated as definitive. A truncated or malformed `201`, or
an ambiguous `500/503` after commit, could discard the saved request and orphan the minted
installation. A later refusal could even make pending replay start a fresh registration
in the same call. The decoder also accepted empty/noncanonical installation IDs.

Root held main integration and assigned the bounded correction to Sol under
agents-tracker-wjp4.4. TDD reproduced these failures against responses captured after a real
gateway commit. Review added duplicate/wrong-case fields, missing/invalid refresh time,
oversized bodies, content type and canonical base64 cases. A small standard-library flat
decoder now accepts only the two required response fields and a valid 16-byte ID; no
dependency or general-purpose JSON framework was added. A first definitive refusal can
still clear a never-committed attempt, but prior uncertainty survives every later failure.
Root removed the now-unreachable pending-refusal-to-fresh-registration branch.

Root's independent HTTP experiment sends only half the declared Content-Length after the
gateway commits. Actual `net/http` returns `io.ErrUnexpectedEOF`; the phone retains its
attempt and, after restart, recovers the captured original ID using identical body/key
and one attestation. This supplements, rather than replaces, the mutated-response tests.

Review also challenged retry after the ten-minute shared record expires. The production
verifier already rejects verdicts older than two minutes, allowing 30 seconds of future
skew. A saved final body therefore cannot authorize a fresh installation after its result
expires. The controlled-clock test uses the real verifier with only Google's decode seam
faked, and runs against both memory and actual Firestore emulation: cached replay returns
the original ID without another decode; expired replay is refused, leaving one original
installation. The command gate pins the production freshness/window relationship. No
handset-clock field, new durable schema or invitation lifecycle is needed. This does not
promise result recovery after ten minutes; unresolved attempts remain pending for explicit
recovery instead of silently re-attesting and creating another identity.

The stricter decoder exposed noncanonical IDs in two existing mobile fake gateways.
Terra reproduced seven failing caller tests and corrected only the fixture ID plus its
matching paths/assertions, preserving production validation and the pairing safety gates.
Root independently passed three repetitions of the affected mobile races (9.687 s), three
registration proof/outcome/TCP-fault repetitions (19.148 s), and whole build/vet/lint (zero
issues). Sol passed full phonecore race (43.065 s). Root's full separate Firestore emulator
race passed push/command/pushreg in 15.171 / 5.948 / 2.386 s; after tightening the verdict
to maximum future skew, three more actual-emulator expiry repetitions passed in 9.376 /
3.803 s. Sol's final full emulator push/command races passed in 19.681 / 5.326 s.
The initial broad caller run passed conformance (251.650 s) and Android source gates
(107.016 s), failing mobile only on the now-corrected fixture IDs. Its final rerun and
CI for this follow-up checkpoint are pending at source publication. Hosted Google/handset,
explicit unresolved-registration recovery, and billing gates remain open.

## One live environment and first-upload preflight

The owner explicitly removed a standing staging environment: use one live v2 target,
develop/review/test locally, integrate on `main`, and deploy that same target directly.
The plan and ADR now say so. Local workerd/emulator stores remain isolated; a hosted
restore drill may need a separately approved disposable target, not a permanent second
backend. No compatibility or security guarantee was removed by this simplification.

Cloudflare account email verification and the specifically approved Wrangler OAuth
grant completed. A read-only `whoami --json` verified the intended account and exactly
`account:read`, `workers_scripts:write`, and `offline_access`. The named profile
`swarm-staging` is only the existing local login label, bound to the relay checkout;
there is no staging Worker. Credentials use an encrypted file backed by macOS Keychain
with owner-only `0600` permissions. No token or encryption key is recorded here, and
the default auth profile was not replaced.

Terra's TDD config check failed on the old Worker name, then passed with the sole live
name `s`, explicit `workers_dev = true`, preview URLs disabled, two SQLite Durable
Object bindings, and no environment, custom route, admission or local-test variables.
The permanent WSS origin is `wss://s.nathan-delacretaz.workers.dev`: 37 bytes, within
the existing 39-byte QR field. No custom hostname, account-subdomain change or extra
deployment path is required. The runbook pins the local Wrangler binary, explicit
profile and nonempty operator-held account ID; it separates offline validation from
an authorized public upload and specifies complete WebSocket-upgrade refusal probes.

Sol added a direct Worker characterization test with throwing Durable Object binding
getters: missing/empty/malformed/partly malformed allowlists or a missing namespace
return `503` on both v2 routes before either binding is accessed. The public root is
state-free; an unknown machine with valid admission configuration gets `403` before
home dispatch. Removing the admission guard in an isolated temporary mutant made the
test fail. Runtime Worker code did not change.

Verification for this slice:

- Terra's full local relay suite on reserved port 8793 passed. Root's independent
  suite on 8792 passed config/admission, actual workerd protocol/cost/alarm tests and
  native Go integration (1.887 s) plus expired-receipt regression (4.307 s). The first
  root run hit a sandbox denial opening a Go build-cache file; the permitted rerun
  passed without weakening tests or changing code.
- Root's fresh no-variable workerd on reserved port 8807 returned `200` at `/` and
  `503` for complete WebSocket upgrades to well-formed `/v2/ws` and `/v2/pair` requests.
  It declared only the two expected Durable Object bindings and stopped cleanly. An
  earlier attempted root listener collided with Terra's 8793 test; its responses were
  discarded and are not evidence for the no-variable configuration.
- Root's pinned Wrangler offline dry-run passed: 52.74 KiB bundle / 11.07 KiB gzip,
  two expected bindings, no upload. Sol independently passed a dry-run with an
  intentionally unreachable Cloudflare API endpoint and telemetry disabled. This
  proves local bundling/configuration, not hosted name availability, entitlements,
  migration acceptance or TLS.
- Root's pairing and terminal-QR race suites passed (21.780 s / 2.333 s); shell syntax
  and diff whitespace checks passed. Sol independently reviewed config, tests, plan,
  ADR and protocol changes with no remaining must-fix.

No cloud Worker, Durable Object namespace, public route, paid upgrade or admission
activation was created by this preflight. First public-resource creation remains an
explicit operator action. Hosted abuse controls, authenticated namespace/home carriage
in the real clients, real-device/Google calls, recovery, capacity and billing evidence
remain open; `200/503` alone is not production readiness.

## First live relay deployment (admission closed)

The owner explicitly authorized the single public Worker and two SQLite Durable
Object namespaces, keeping the existing Free plan and admission disabled. Read-only
preflight returned Cloudflare error `10007` for Worker `s`, confirming there was no
existing deployment to overwrite. Root uploaded the reviewed `70e9f33a` Worker/config
with the explicit approved auth profile and account, telemetry disabled, no variable
override, no automatic configuration and no paid upgrade.

Cloudflare accepted version `cc7584c7-6932-4103-93b9-2ff37ead35b0` on
2026-09-05 at 12:30:44 UTC. Read-only version metadata confirms migration tag `v1`,
`has_preview: false`, and exactly the `HOMES`/`RelayHome` and
`RENDEZVOUS`/`RendezvousDirectory` namespace bindings, with no admission, test or
secret bindings. The migration tag is a provider schema identifier, not v1 protocol
support. The sole origin is `https://s.nathan-delacretaz.workers.dev` (WSS for clients).

Root's public, certificate-verified HTTP/1.1 smoke at 12:31:40–41 UTC returned the
expected banner with `200`, then the exact `relay admission is not configured` body
with `503` for complete WebSocket upgrades to well-formed machine and pairing routes.
No real machine, phone, fixture identity or command was admitted. This is hosted
TLS/routing/refusal evidence, not a pairing, hibernation, usage-meter or performance
measurement. Provider Durable Object request/row metrics have not yet been measured.

The deployed source checkpoint also completed all 14 jobs in
[CI 33965898472](https://github.com/Nathandela/swarm/actions/runs/33965898472), and
both container workflows (`33965898483`, `33965898474`) passed. The container jobs
build and scan locally in CI; they do not publish a push image or deploy Cloud Run.

The separate GCP preflight was read-only and limited to the existing `swarm-8404f`
project. Required APIs and the push runtime service account exist, but no Firestore
database, Cloud Run service, Artifact Registry repository or Secret Manager secret
was found. Existing project-level runtime grants demonstrate FCM send, not the needed
Firestore/secret access. No GCP resource, IAM or billing change was made. The owner's
ignored Android Firebase config exists in the owner checkout, not the temporary
worktree; no credential values were requested in chat.

## Authenticated namespace carriage and native pre-authentication limits

Under agents-tracker-wjp4.6, Sol added one required operator namespace from explicit
`remote init --relay-namespace` configuration through authenticated Noise msg2 to
the same durable phone-state mutation that pins the machine relay-auth public key
before the pairing ACK. The QR is unchanged. One dependency-free scalar validator
enforces the same bounded ASCII grammar in Go; the Worker checks the same grammar.
The first attempted shared-package placement pulled forbidden legacy dependencies
into the mobile closure, so review moved only the validator to `relayhome` and
documented its narrow allowance. No unused profile factory or new dependency was added.

Phone schema 22 requires a canonical namespace on active paired state at both Load
and Save, including older schema inputs. Root caught an initial load-then-save path
that could brick the next restart. Regression tests now refuse that old pairing on
first load and prove a rejected save changes neither disk bytes nor memory; disowned
and unpaired state remain writable. The actual Noise test proves invalid msg2 fails
before verification callbacks, SAS, consent or commit. Retired wire shapes are refused,
not assigned a guessed namespace. Terra updated affected valid caller fixtures;
the full mobile gate found an additional disk-backed publication seed omitted by the
first focused selection. After correction, all 20 named Publication/Composer race
tests passed (8.270 s). Sol independently reviewed the complete trust chain.

Under agents-tracker-wjp4.7, the Worker now checks route, admission and WebSocket
upgrade before the native rate limiter and either Durable Object. One binding has
constant `ws` and `pair` keys, initially 60 calls per 60 seconds per location. Missing,
throwing or malformed limiter access fails closed with 503; denial returns 429 and
`Retry-After: 60`. Throwing binding getters in the tests prove rejected requests do
not reach Durable Objects. This is a permissive per-location abuse control, not a
global counter, availability guarantee or exact monetary limit.

Root's independent full local suite on 8901 passed protocol/cost, native Go integration
(2.287 s), bounded alarm work, expired-receipt handling (4.468 s) and the separate fresh
native control observing 60 pair lookups followed by a 429. The production-shaped
offline dry-run reports 53.82 KiB / 11.35 KiB gzip, two Durable Objects and one 60/60
rate binding, with no admission/test variables. Chrome's unfiltered account inventory
showed exactly one application, `s`; its current version had no limiter binding, so
namespace ID 1001 is reserved for this sole current Worker, not claimed historically
unused across the account.

Earlier full-run attempts are not green evidence: one hit the outer deadline and
another exposed Wrangler's shared default debugger port despite separate HTTP ports.
The test runner now requests an ephemeral debugger port without changing its deadline
or adding a production bypass. A separate transient native-process startup stall
also interrupted tooling; a one-second sample of root's hung version diagnostic stayed
at `_dyld_start`. It recovered without changes to security controls, installations or
user shell configuration. Subsequent commands use non-login shells.

These are foundation changes, not an active client cutover. Production callers still
need the relay-issued generation, home-bound durable cursors/incarnations, retained
delivery retry semantics, revocation obligations and selected-machine reconnect wiring.
Admission remains closed; real phone/Google, hosted hibernation, recovery and billing
gates remain open. Final broad local gates and CI are recorded below when complete.

Root's final independent race gates passed: full mobile (51.419 s), conformance
(257.687 s), phonecore (40.273 s), relayhome (1.887 s), relaycfg (2.245 s), pairing
(21.461 s), machine sidecar (38.116 s) and supervisor (13.544 s). The serialized
command initially failed only mobile's missed publication seed; its separate full
rerun above is the corrected result. Full command CLI race passed earlier (91.772 s),
and Android source gates passed (340.912 s). Final whole build/vet and lint passed
with zero issues. Shell syntax and whitespace checks passed. The full skeleton rerun
and remote CI are pending at this source checkpoint; no real handset result is claimed.

## Native limiter deployed; namespace checkpoint verified

The final full skeleton race rerun passed in 489.532 s. Source checkpoint
`e442649c43ee1c8949533163dcf967322267643d` is committed and pushed to `main`.
Root verified all 14 jobs in [CI 33969436512](https://github.com/Nathandela/swarm/actions/runs/33969436512)
and both [relay container](https://github.com/Nathandela/swarm/actions/runs/33969436477)
and [push container](https://github.com/Nathandela/swarm/actions/runs/33969436502)
workflows succeeded for that exact SHA. These container workflows still do not
publish an image or deploy Cloud Run.

Root uploaded that committed relay source to the same Worker `s`, with the approved
profile/account, telemetry disabled and no variable override. Cloudflare accepted
version `ff45fa08-db01-4ec6-bae7-01792fb90c20`, created at
2026-09-05T13:41:32.181542Z, without a paid upgrade or new permission scope.
Read-only version metadata confirms `has_preview: false`, migration tag `v1`,
the same two Durable Object namespace IDs as the first deployment, and exactly
one added `RATE_LIMITER` binding with namespace `1001`, limit 60 and period 60.
There are no admission, test, variable or secret bindings. This updates the sole
live environment; the local auth-profile name does not create a staging target.

At 13:43:46 UTC, root's certificate-verified HTTP/1.1 probes returned `200` with
`swarm relay v2` at the public root and `503` with
`relay admission is not configured` for complete WebSocket upgrades on both
well-formed v2 routes. No identity or command was admitted. Admission is checked
before the limiter, so these probes verify hosted configuration, TLS and refusal,
not hosted counter behavior, Durable Object usage, hibernation, capacity or billing.
The per-location limiter remains an abuse reduction, not a global spending cap.
Active client wiring and the previously recorded hosted/device/recovery gates remain open.

## Phone connection retains authenticated generation

The first agents-tracker-wjp4.8 slice retains the phone generation only after `Dial`
validates the complete `AUTHENTICATED` identity, role, purpose, home and canonical
nonzero generation. `PhoneBinding` returns that value; machine or uninitialized
phone connections return no binding. Phone Append/Subscribe/Revoke now reject a
different binding locally. Machine control-to-separate-stream binding transfer
remains valid. A binding is relative to its connection's authenticated home, not a
standalone namespace-bearing credential or a replacement for durable home fencing.

Sol recorded RED with the missing accessor, implemented the three-file slice and
passed focused/full package races. Independent Sol review found no must-fix. Root
tightened the fake-server test to require the actual generation error and a bounded
context, then independently passed the full package race (2.375 s), package vet,
and a fresh actual-workerd integration race on reserved port 8912 (3.056 s).
The latter checks real Noise pairing, wrong-key authentication refusal, equality
with machine-issued authorization, bidirectional mailbox delivery, reconnect,
endpoint replay rejection and revocation. The temporary listener was stopped.
An initial package-vet invocation lacked the explicit writable cache setting and
hit a sandbox cache denial; the correctly configured rerun passed.
Whole build/vet and lint also passed with zero issues; lint was repeated with its
own writable cache to remove cache-write warnings from the first successful run.

This is a native client API foundation, not the production mobile/gateway cutover.
The broader agents-tracker-wjp4.8 task remains in progress.

## Remote-v2 work-branch checkpoint

The following reviewed commits are on `work/remote-v2-generation`, not `main`:
`04cfee0a`, `9bc0ddc5`, `8db6eaa8`, `fb4c8f6d`, `79f4afa2`, `5d6af07c`,
`89ba1cae`, `bef8ad70`, and `fede1961`. They bind inbound checkpoints to a
server-authorized relay home/phone/generation; move gateway custody, discard recovery,
mobile pairing and gateway generations to relay v2; remove the corresponding reachability
exemptions; make phone checkpoint activation and pairing commit atomic; and add a
non-destructive relay-mail probe before recovery.

Root independently observed `go test -race` results of 35.681 s for `internal/remotegw`,
2.639 s, 2.384 s and 2.365 s for relay-v2 checkpoints, and 39.895 s and 48.016 s for
phonecore checkpoints. The isolated local-workerd suite was green, including actual
pairing, discard/retry recovery, the destructive-recovery probe, alarm, expiry and rate
controls. These are local worker/process checks, not hosted evidence.

Review found and fixed stale-authority checkpoint reuse, missing full-retained-batch
accounting, idempotent-discard replay gaps, a dynamic pairing dial that defeated the
reachability check, and a custody-store fallback that could restore a retired checkpoint.
The tests now cover the corresponding authority, batch, retry, static-call and wrapped-store
cases.

This does not claim a production cutover: the mobile relay-v2 stream/data plane and the S19
end-to-end suite remain open, as do hosted deployment/CI evidence and a connected-device
test. No hosted deployment or physical-device result is recorded for this work-branch
checkpoint.

## Native mobile relay-v2 data plane

The phone production path now has one relay-v2 connection/subscription. Its startup order is
Dial, authenticated PhoneBinding, durable ActivatePhoneBinding, then Subscribe from the exact
durable incarnation/cursor. Deliveries enter only through AcceptPhoneDelivery and ACK the exact
subscription that delivered them. Phone publications use Append with a canonical base64url
SHA-256 ciphertext id. Pairing serializes stop/join, atomic authority commit and conditional
restart with Start, Stop and Close; no old delivery can ACK a replacement generation.

Explicit roster recovery wakes only the current Recv context and PROBEs the same live connection.
A destructive recovery first persists its token, exact old incarnation and authenticated stale-head
cursor. DISCARD is wire-bound to that cursor, atomically rotates the incarnation and removes only
through that head; later queued frames retain their cursor, receipt and byte accounting in the new
incarnation. An idempotent old-incarnation retry must repeat the identical cutoff. The current checkpoint
equaling that old incarnation means DISCARD is still owed; after atomic adoption changes it, a
crash retry sends only the token-bearing replacement-roster command and cannot delete the fresh
mailbox a second time. State schema v26 persists this phase; pre-v26 pending destructive intent is
retired rather than assigned a guessed cutoff. Pre-v23 relay-v1 checkpoints are also retired at load;
no relay-v1 resume is attempted. Native Resync joins the old subscription, performs the binding-fenced
checkpoint reset, reconnects from blank, persists the server-returned effective ACK floor, then
publishes the repair request. An immediate stop/start with no intervening delivery therefore remains
an exact valid reconnect.

The compiled relay-v1 phone path and its hello/wait/poll/presence/token/cursor-reset fields were
removed. The entire `mobile/conformance` package was also removed because every executable test
was transitively coupled to its in-process relay-v1 Server/Client harness. This was not replaced
with a fake compatibility adapter or skipped suite. Its retained invariants map to:

- native workerd mobile vertical: pairing/SAS, durable grant receive and ACK, bidirectional append,
  exact reconnect, healthy same-connection PROBE, encrypted facade repair commands, native Resync,
  immediate post-Resync reconnect, malformed authenticated plaintext followed by valid delivery,
  stale authenticated head with preserved fresh tail, exact DISCARD retry/adoption, post-adoption
  roster-only retry, stale generation refusal, durable revoke/reopen/re-pair, and a faulted phone
  pairing commit that yields neither machine outcome nor relay authorization;
- mobile lifecycle/unit gates: pairing-vs-Stop serialization, pairing-vs-Close serialization,
  old delivery/ACK custody through session join and binding retirement, close refusal, bounded
  panic-isolated event dispatch, concurrent Start/Stop/Close, exact pre-confirm origin dial gating,
  pin install/clear, backoff, live-send ordering, trust/pin, publication and error taxonomy;
- phonecore gates: binding activation/retirement, pairing commit atomicity, delivery transaction,
  checkpoint/discard crash retry, v26 cutoff persistence/merge, replay, grant, stale-stream,
  capability, interaction, wake and custody behavior;
- relay-v2 Worker/protocol gates: authentication, home/generation isolation, queue bounds,
  dedupe, Subscribe/Recv/ACK/PROBE/DISCARD, revoke, expiry, alarms, admission and rate limits.

RED evidence included the initially missing relay-v2 ciphertext digest helper and missing durable
discard-phase accessor, followed by the post-adoption double-discard and old conformance failures.
After the replacement pass, `go test -race ./mobile/... -count=1` passed in 45.619 s,
`go test -race ./internal/phonecore -count=1` in 47.532 s, and the relay-v2 race in 2.794 s.
A fresh isolated actual-workerd `npm test` passed on port 18959; the expanded mobile vertical
passed in 1.32 s and its faulted-commit negative in 2.15 s, alongside pairing, cutoff discard retry,
S19 skeleton, cost/alarm/expiry and pre-auth rate gates. These are local tests only; no hosted or
physical-phone result is claimed.

Final P1 review found two additional crash/production-wiring gaps and both are closed. A persisted
DISCARD cutoff may now expire before retry without wedging recovery: the Worker advances only when
the exact interval `(ack_cursor, through_cursor]` is empty, revalidates authority and coordinates in
the transaction, deletes only through the requested cutoff and preserves later mail. An accelerated-
retention real-Worker test covers expiry between durable intent and retry, exact idempotence and a
fresh queued tail. WebPKI reconciliation now authenticates the phone against native `/v2/ws` with a
signed, phone-only `probe` purpose. That purpose has no RPC authority and a separate supersession
identity, so it cannot replace the live stream. The real mobile Worker vertical exercises the
production `probeWebPKI` path, a denied probe RPC, the unchanged live stream and subsequent delivery.

Root independently passed the complete isolated Worker suite on port 18991, including both new
cases, and package races for mobile (45.729 s), phonecore (49.074 s) and relay-v2 (2.635 s). Focused
skeleton race (7.723 s), Android S25R3 (1.113 s), vet and whitespace checks also passed. Independent
Sol security re-review returned GO with no remaining P1 correctness, security or data-loss blocker.
The broad whole-repository race result is recorded separately when it completes. These remain local
checks; hosted admission stays closed and no physical-phone result is claimed.

The subsequent whole-repository race completed non-green and is not reported as a pass. Its useful
failure was B94 identifying 29 exported relay-v1 client/connection symbols with no production entry
point; removing their last operator callers and then deleting that transport is the next breaking
cleanup package. The run also exposed two verification fences still describing the retired v1
wait/rendezvous client and three budget rows pointing at deleted conformance tests. Those fences were
replaced with a non-vacuous native relay-v2 deadline audit and current replacement-test references;
their exact verifier run passes. The relay-v2 slow-consumer failure was load-sensitive only: its
isolated race test passed 10/10, and the full relay-v2 package race passed separately. Hook and process
inspection failures came from inherited whole-suite environment or sandbox-denied `ps`/`pgrep`; the
hook packages pass in isolation with the leaked socket variable absent. Several PB-STATE-10 command
tests still construct the removed relay-v1 pairing fixture and are therefore cleanup work, not green
v2 evidence. The same broad run did pass the full skeleton package in 468.232 s.

## Native operator pairing and revoke control

Operator pairing, immediate revoke and deferred purge now use relay v2 machine-control
connections exclusively. Pairing authorizes the phone generation on the same authenticated
control connection before reporting success. Revoke preserves the phone public key and exact
consent ceremony as public cleanup evidence, re-authorizes that retired ceremony to recover its
binding, then revokes only the returned generation. A later re-pair therefore cannot be touched by
an old cleanup attempt.

The cleanup ledger is write-ahead and every write has a fresh random attempt ID. Retire and resolve
operations match both routing ID and attempt ID, so a stale pairing response, immediate command or
driver snapshot cannot settle a newer same-phone obligation. Live-device obligations remain
pending and undialed. An ambiguous AUTHORIZE response leaves cleanup evidence; a positively
acknowledged AUTHORIZE is not rolled back for a later ledger I/O error; a clean token supersession
aborts pairing. Legacy relay-v1 rows without v2 evidence are resolved loudly to a retained
manual-cleanup tombstone instead of wedging the pair gate. Only `invalid_consent`, `member_limit`,
`retirement_limit` and `generation_exhausted` are permanent AUTHORIZE refusals; runtime, session,
transport, decoding and unknown errors remain retryable.

Focused race tests passed for skeleton pairing, relay-purge storage/driver, relay-v2 and the command
operator paths; relevant vet and whitespace checks passed. A fresh complete local-workerd suite on
port 19059 passed native pairing authorization, immediate revoke, response-loss retry, deferred
purge, old-consent isolation after re-pair, mobile pairing, discard recovery, expiry, alarms and
rate limiting. An earlier full-suite attempt stopped on the already isolated load-sensitive
expired-cutoff timing test; the fresh rerun passed it and the remaining suite. Independent Sol
security/crash/concurrency review returned GO with no blocking finding. These are local checks;
hosted admission remains closed and no physical-phone result is claimed.

## Relay-v1 gateway compatibility removal

The gateway now enters its native relay-v2 `MailboxWait` path directly. The unused relay-v1
`Client` compile assertions, capability hello negotiation, 500 ms compatibility polling arm and
its v1 integration test were deleted. `PollOnce` is production-private with a test-only forwarding
shim for the existing same-package batch invariants. The two remaining external-package E2Es that
called that obsolete helper through relay-v1 fixtures were deleted rather than restoring a dead
production export.

Relay-v2 `MailboxWait` blocks on a local delivery channel rather than a 25-second server long poll.
Its local 35-second deadline is therefore only a benign idle recheck: expiry immediately receives
again without latching degradation or taking the error retry backoff. The old server-wait/request
timeout composition claim and test were removed. A genuine half-open detector would require a
relay-v2 probe or heartbeat and is not claimed by this timer.

RED evidence included B94 first stopping on its obsolete relay-v1 `DialSecure` facade control,
then reporting the dead v1 surface after the control moved to relay-v2 `PhoneBinding`; the private
`PollOnce` change also exposed and removed the two external v1 fixture callers. The idle-recheck
test initially observed the 250 ms error backoff and then passed after the benign-expiry branch.
Full races passed for `internal/remotegw` (36.057 s), relay-v2 (3.029 s), `cmd/swarm-remote`
(38.233 s), and mobile (40.147 s). A whole-repository compile-only test passed. B94 now reaches its
intended verdict and remains deliberately RED on exactly 52 relay-v1 exports, with no gateway
export or obsolete allowlist row; deleting those exports is the next migration slice.

## Configured relay-v2 doctor

`swarm relay doctor` now accepts no relay URL, pin or operator-secret arguments. It loads the same
`relay.json` and machine identity as remote control, proves DNS and TCP+TLS under the configured
policy, checks the exact `swarm relay v2` edge marker, then authenticates a machine-control
connection and completes a fresh relay-v2 pairing rendezvous. Random payloads are AES-GCM sealed
locally and exchanged in both directions; the relay sees only ciphertext. The ceremony is finished
on success and by a bounded best-effort cleanup after any post-create failure, with relay expiry as
the final crash fallback. The path uses only PAIR operations, so it creates no phone membership or
retirement state and requires no Worker diagnostic RPC, persistent diagnostic phone or privileged
diagnostic credential.

Focused command race tests cover rejection of legacy arguments, missing configured state, exact
configured pin enforcement, and refusal of cross-origin and HTTPS-to-HTTP marker redirects. The
redirect cases first failed because the default client followed them, then passed after redirects
were disabled. A fresh complete local-workerd suite passed on port 19069, including
two consecutive doctor runs while a live machine stream remained connected, as well as the existing
pairing, operator-revoke, mobile, recovery, expiry, alarm and rate gates. Command/relay-v2 vet, shell
syntax and whitespace checks also passed. The root marker remains an edge/version signal rather than
a readiness or backup/restore claim. The doctor can supersede another simultaneous machine-control
ceremony, so the runbook requires an otherwise idle control plane. These are local checks; hosted
admission remains closed and no physical-phone result is claimed.

## Relay-v2 gateway coverage and latency

The gateway mailbox contract now owns relay-v2 `Item` values directly. Machine and mobile routing
IDs, dial timeout and pairing lifetime use the relay-v2 definitions, and the remaining security and
S19 vertical fixtures run against actual local Workerd rather than an in-process relay-v1 server.
Obsolete relay-v1 E2Es were deleted only after their unique retry, pairing, command, composer,
launch-policy and terminal-watch behavior was retained on the live relay-v2 path.

The first complete 3-run, 200-sample Workerd benchmark was correctly RED at p50 208.624875 ms
(p95 281.218375 ms, p99 471.299209 ms). Tracing showed `CommandBridge.Run` was applying the retired
3-reads/s relay-v1 `DrainPacer` to `MachineMailbox.MailboxWait`, even though relay-v2 `SUBSCRIBE`
already pushes `DELIVER` frames and that method only drains the client's bounded local channel.
Removing that dead pacer changes no Worker operation, quota, replay or durability rule. ACK remains
off the delivery path and coalesced to at most 1/s. A generalized exponential backoff now covers any
non-empty page that makes no authenticated cursor progress, preventing a malformed retained tail
from becoming a local CPU spin.

A submitted-line diagnostic then measured p50 159.23025 ms and exposed a separate oracle error: it
included the terminal's 150 ms anti-paste/submit behavior rather than stopping at PTY input. The
final fixture uses an explicit private raw-stdin log and 1-16 byte non-submit payloads, with a fresh
phone, machine, gateway process, terminal and checkpoint per run. Its median of three 200-sample
runs passed at p50 23.094125 ms, p95 30.503584 ms and p99 36.609291 ms; every run also proved a
non-empty production gateway inbound checkpoint.

The complete local `npm test` Workerd suite exited zero after the change. Focused relay-v2,
transport, fake-agent, skeleton and gateway replay/durability/ACK tests, including race runs, also
passed; the deterministic hostile-tail test observes exactly one receive before its first backoff.
Independent security review returned GO: Workerd meters authenticated client messages, not
server-originated deliveries or local channel drains; its 64-frame/1 MiB in-flight bounds and the
client's matching queue bounds still enforce slow-consumer backpressure. B94 remains deliberately
RED on exactly 52 relay-v1 exports, with no new relay-v2 reachability failure. These are local
Workerd/process results, not hosted or physical-phone evidence; hosted admission remains closed.

## Relay-v1 source retirement and replacement coverage

The obsolete Go relay server, RPC codec/client, bbolt mailbox store, relay binary/container
publication, VPS/TLS terminator and emulator-only pairing harness are removed. Shared TLS policy,
semantic errors and reconnect behavior remain; FCM delivery now accepts ciphertext bytes directly
without importing the deleted relay server. Operator pairing/revoke/recovery and binary pin tests
exercise the native v2 path. The Android capability and hardware-backed Keystore gates remain;
only the existence check for the removed emulator runner was retired.

GCP changes are source-only: the relay VM/disk/address/runtime-identity declarations are removed,
while Pushgw and shared infrastructure remain. Retained Pushgw IAM instance keys are unchanged,
avoiding gratuitous grant replacement in a future plan. Terraform format and validation pass.
No Terraform apply, live resource deletion, or resulting billing reduction is claimed. The
operator runbook distinguishes Durable Object storage from the retired bbolt deployment.

Independent Sol review rejected initially missing replacement coverage. The resulting tests
exercise actual `DialPair` TLS handshakes (untrusted refusal, pairing-bootstrap SPKI capture,
distinct-key isolation, defensive copies, wrong pins and TLS-to-cleartext redirect refusal),
and actual network dial/call cancellation and internal deadlines. The nightly stress selector
now targets those v2 tests and checks every expected test name before running. The local Workerd
protocol gate seeds unacknowledged mailbox data and reads its real SQLite files read-only:
items, receipts and streams must exist before revoke and be gone immediately after `REVOKED`,
before any reauthorization can mask a missing purge.

The history-page size oracle now measures v2 APPEND JSON/base64url ciphertext and both native
ciphertext and WebSocket message limits. Budget traceability recognizes Worker test files and
plural citations with a parser regression test. Machine-to-phone **visible-render latency** is
explicitly still unmeasured; the earlier phone-to-PTY benchmark is not evidence for that direction.

Local evidence during this slice: repository-wide compile and build, `go vet ./...`, full
`internal/verify` (including B94, now green without new allowlisting), Android gates, relay security,
relay-v2, relay config, push and Pushgw tests/races passed. Full gateway, mobile and remote-binary
races passed (35.462 s, 52.818 s, 38.778 s respectively). The complete local Workerd suite passed,
including operator recovery, revoke/deferred retry, mobile pairing, hostile-phone controls,
input/composer paths, doctor and bounded cleanup cost. The protected Pushgw backup files are
unchanged. This remains local evidence: hosted admission is closed and physical-phone acceptance,
live infrastructure retirement and the plan's remaining push/runtime gates are not complete.

Final review returned GO after the missing coverage was added. `golangci-lint run` reports zero
issues and `goreleaser check` passes. A Workerd repeat during whole-repository compilation/testing
exceeded the existing 12-second burst fixture deadline; the final complete repeat passed after
that heavy load subsided (including the bidirectional on-disk purge checks). The broad Go run
also exposed inherited `SWARM_SHIM_HOOK_SOCK` interference and sandbox-denied `ps`/`pgrep` in
process-integration tests. Rerunning `cmd/swarm`, `internal/e2e` and `internal/hookclient` with that
environment variable unset and approved process-inspection permissions passed all three packages
(40.560 s / 26.736 s / 3.011 s). No assertions were disabled to accommodate those environment failures.

The broad skeleton run exposed a separate raw-input fixture race: its startup prompt could
already be in the attachment snapshot while the test waited only for future frames. Terra
reproduced this failure even with the hook environment unset (two passes and one failure),
so it was not classified as an environment failure. The readiness test now accepts the initial
snapshot, exercises a subsequent live prompt, then deliberately reattaches late and proves
the prompt is in the snapshot before checking that non-submit bytes reach the raw stdin log.
Production input handling and the shared live-frame-only helper are unchanged.
The corrected test passed 20 repeated runs and five race runs (9.05 s and 7.83 s).
The clean-environment full skeleton run, started before that test fix, completed in 377.027 s
with only this same readiness failure; all its other tests passed. A single all-packages green
run after the fixture fix is not claimed: the evidence combines the broad run, the corrected
environment reruns, and the repeated/race verification of the sole remaining fixture change.

## Current-only phone checkpoints and stricter Firestore CI

The phone now refuses checkpoint schemas 1 through 25 with an explicit reset/fresh-pairing
error before opening either sealed tier or rewriting the file. Schema 26 is the first supported
v2 baseline; future versions still fail closed. Old schema conversion branches and migration-only
fixtures are removed. The current sealed fixture, durable-field-set pin, publication authority,
replay and exact discard-recovery checks remain. A mobile regression proves one old namespace
is broken and unselectable while its healthy sibling remains usable; only explicit ForgetMachine
removes the old namespace. An unused singleton-migration facade stub is also removed.

TDD evidence: the mobile isolation test failed before the version guard and passed afterward.
Independent Terra review returned GO. Root's full phonecore/mobile race runs passed (137.087 s /
160.803 s), full `internal/verify` passed (66.571 s), and focused `go vet` passed. Sol's final
phonecore suite and focused race passed, as did repeated current/legacy and mobile isolation
checks. The refusal test independently pins all 25 rejected versions and checks zero sealer
opens plus byte-identical disk contents; it does not derive its range from the production floor.

The existing Firestore-emulator CI job is strengthened, not replaced or narrowed. It still runs
the full pushgw and pushgw-command race suites and additionally requires five named emulator
tests to report PASS. Sol ran the complete emulator-backed command successfully. A fake-go
control accepts all five PASS anchors, independently rejects each missing or skipped anchor,
and rejects a nonzero command even with pass-looking output. Root repeated that control and
the bounded-runner lifecycle test: timeout 124, external termination 143, no stubborn surviving
grandchildren. CI also checks propagation of exit status 7.

Read-only inventory of project `swarm-8404f` found no Firestore database, Cloud Run service or
Secret Manager secret. The deployment runbook now identifies the composite index needed by
the actual unbound-address retention query; emulator success is not production-index proof.
No cloud resource or index was created. Hosted admission remains closed and no Android device
was connected. A public GitHub push was rejected by the publication safety gate; no retry via
another path was made and no source publication is claimed. Live IAM/FCM/attestation, handset
lifecycle, recovery and billing gates remain outstanding.

## Registry-only push and current wake format

After explicit publication approval, `47785270` was pushed non-force to public main.
GitHub CI run `34022360908` passed all 14 jobs, including Android, Workerd, Firestore,
lint and release checks; the push-gateway container run `34022360866` also passed.
This supersedes the preceding publication-blocked status, not the outstanding hosted gates.

Push now has two structural configurations: a validated registry binding constructs the
gateway sender; no binding means foreground-only. The redundant transport selector and
legacy relay sender are removed. Obsolete sidecars and the old push sequence file are
ignored without modification. The retry scheduler directly supplies the notifier's
reserve-before-append, provisional supersession and post-append drive operations.
Current encrypted WakeV1 sequences, preferences, durable obligations and retry bounds remain.
The remote sender no longer carries an unused machine-revoke capability; the authenticated
registry and daemon's self-contained revoke custody still do.

The foreground regression uses real configuration, file-backed pending custody and an HTTPS
receiver: removing the registry binding causes zero requests and byte-identical custody;
restoring that binding sends the exact stored envelope once. This stronger replacement was
added, passed and independently reviewed before the obsolete transport-store test was removed.
Root's repeated race check passed. Operator copy and current push/budget documentation now
describe the v2 routes instead of claiming the relay stores provider tokens.

The phone accepts only the 74-byte per-address WakeV1 format. A genuine, independently
constructed retired 78-byte AEAD fixture failed the new refusal test before implementation;
the new path refuses it without changing its reserved legacy replay coordinate. The old
receiver and encoder are removed, while mailbox key separation and current wake authentication,
TTL, replay and address isolation checks remain. The schema-26 legacy replay field is reserved,
not rewritten or repurposed; removing that disk field is a separate checkpoint-version change.

Canonical daemon revoke production code remains unchanged. New current-path regressions
cover pre-commit staging failure leaving registry and epoch intact, exact bodyless HTTPS
DELETE authority, 401/429/503 retaining custody across reopen, and recovery after actual epoch
rotation with byte-identical retry followed by 204 cleanup. The phone/gateway contract now
performs two actual HTTP deletes; the old second obligation drive was already done and therefore
did not test the gateway tombstone. All handset revoke/drop/restart/no-readoption assertions
remain. Independent review returned GO for these replacements; root's focused races passed
(skeleton 5.451 s, phonecore 5.095 s). Current terminal-refusal retention is deliberately
unchanged; its recovery limitation remains tracked by `agents-tracker-c2fa`.

During this slice, full crypto/phonecore/mobile/gateway/remote-binary races passed
(3.126 / 59.854 / 59.928 / 37.907 / 39.650 s), build passed and lint reported zero issues.
The local Workerd suite also passed earlier in the slice; its gated visible-latency benchmark
was not run. Final post-review gates are recorded below when available. No hosted resources
were changed, admission remains closed, and no physical-handset acceptance is claimed.

After the replacement tests passed root verification and independent review, the normal
safety gate still rejected deletion of the dormant `RevokeObligationStore`/machine tail in
`internal/remotegw/revokeproducer.go`, classifying it as removal of shared security/recovery
code beyond the approval's scope. Nothing from that patch was applied; no bypass or alternate
deletion was attempted. The live HTTP helper, old store and old store tests all remain.
Consequently B94 still reports six unreachable exports, and this slice is not integrated
into main. `agents-tracker-ppes` records the precise remaining approval boundary.

Final post-review five-package races passed (crypto 3.469 s, phonecore 46.505 s,
mobile 47.282 s, gateway 39.963 s, remote CLI 39.288 s); lint again reported zero issues.
The new locked-content test encounters an actual `ErrKeyAuthRequired` from a persisted
content blob, accepts the current per-address wake, then refuses replay after another locked
restart. Independent Terra review returned GO; existing tier tests still prove content
keys and caches are withheld. No changes were made to the protected push backup files.

The owner then explicitly approved removing the named obsolete revoke store/machine while
retaining the current HTTP revoker and daemon recovery. The normal patch succeeded: the
dormant implementation and its store-only tests are removed, with current-path replacement
tests and the live HTTP wire test retained. Full `internal/verify`, including B94 and the
budget fences, now passes (37.955 s), without allowlist or root-set changes. Build and lint
pass. Post-removal full races pass for crypto/phonecore/mobile/gateway/remote CLI
(3.736 / 61.298 / 61.802 / 52.857 / 40.127 s), and canonical daemon custody races pass
(3.685 s). This supersedes the preceding integration blocker.

The final caller audit separately identified old pairing-wire hello/consent compatibility
branches. `agents-tracker-7jvo` tracks their current-only replacement; foreground-only v2
pairing remains a supported mode, not a reason to retain old wire parsing. The overall
migration and hosted/handset gates remain open.

## Release-gate expiry fixture correction

The owner requested publishable builds. Tag `v0.13.28` at `c243a844` triggered release
run `34041177618`, but the remote-v2 gate failed before publication. The cutoff-expiry
fixture gave every item only 100 ms of retention, including the fresh item that had to
survive DISCARD, an exact retry and reconnection. A deliberate 250 ms scheduling delay
reproduced the same 12-second receive deadline failure locally.

`agents-tracker-u964` changes only the fixture: 3-second retention, a 3.5-second wait
followed by a live empty PROBE proving cutoff expiry, and a late fresh append. The
250 ms regression delay remains, as do exact DISCARD retry and delivery after reconnect.
No production timing or gate assertion is removed. Independent Terra review returned GO.
Both Sol and root ran the complete Workerd suite successfully; root's expiry case passed
in 3.84 s, the hostile-phone/input suite in 66.632 s, and full internal verification in
13.049 s. The gated latency benchmark remains unrun. The failed tag is not moved or
represented as a published release; the correction will use a new immutable release tag.

## Private push URL and owner-enrollment bootstrap

The existing Play/Firebase browser login and local publishing/signing setup were verified
without printing credentials. The published app is `dev.swarm.phone`; its existing closed
testing entry was visible in Play Console. The new production push service did not yet
exist, and the Play release build correctly required an actual provider-issued URL.

Owner admission also exposed a setup gap: the app's Keystore installation public key was
available internally, but no owner-facing action could reveal it before registration.
USB alone is not a supported way to extract app-UID Keystore state from a Play release.
`agents-tracker-929c` adds an explicit public-only enrollment display; server admission is
not weakened and no fixture identity is admitted.

To allocate the final URL without enabling registration, `agents-tracker-kn18` created
one private service `swarm-pushgw-v2` in `swarm-8404f/us-central1`. Revision
`swarm-pushgw-v2-00001-7s4` runs Google's official hello image at digest
`sha256:4229c16c0c549905376c79943d0c122a728901330d10a080ec8ceb52e3f21f3e`,
under the new `swarm-push-bootstrap` identity with no project role grants or secret mounts.
Minimum instances are zero, maximum one, CPU is request-throttled and startup CPU boost
is disabled. The service IAM policy has no invoker grants; unauthenticated HTTPS returned
403. This is a private placeholder, **not a functioning push gateway or a readiness pass**.

Cloud Run returned `https://swarm-pushgw-v2-733314021126.us-central1.run.app`; its status
also lists `https://swarm-pushgw-v2-my4glayhpa-uc.a.run.app`. The former is the explicit
build/publication origin. Keep this same service and region when replacing the placeholder
with the gated Swarm image, scoped runtime identity, Firestore and pinned secret versions.
Do not expose the placeholder. Full registration/FCM testing remains blocked until the
owner supplies the real public installation key and the real service passes its negative
and readiness gates. The first Play bundle is enrollment bootstrap only, not an invitation
for friends or evidence of working remote control.

The owner-enrollment candidate is Android `0.13.29` / version code `40`. Root ran
`:app:testDebugUnitTest :app:lintDebug :app:bundleRelease` successfully; independent Terra
inspection counted 1,751 tests across 216 XML reports with zero failures/errors, and lint
reported zero errors (33 warnings). Fresh `android/gate` and `cmd/swarm-publish` tests also
passed. The signed AAB SHA-256 is
`f2e81b47d5be3f5c16dc2c215261c00789cfb0573b54df949bfec5be07cd27a6`; schema-2 provenance
matches this artifact, the production Firebase identity and the reserved origin above.
The guarded publisher's alpha dry run uploaded code 40 and staged the track successfully
without committing edit `05361589781516772263`; this alone is not publication evidence.
The separate main CI run `34042410030` failed the journal hook-gap recovery test during
startup with a permission-denied error; desktop release remains gated pending diagnosis.

The guarded publisher then successfully committed alpha edit `06722971016813427595`
for `dev.swarm.phone` version code `40`. This confirms Play API publication, not Google
review completion or availability on the handset. The owner must update the app and use
Settings → Show push enrollment key to provide the public admission value. No private
key export, live registration, FCM delivery or end-to-end remote readiness is claimed.

## Socket-startup permission race uncovered by release CI

`agents-tracker-ujcm` traced the journal startup failure to the process-global `0177`
umask around daemon/shim Unix-socket creation. Concurrent `mkdir(0700)` could become
`0600`, denying traversal before opening the first journal segment. Both bind sites now
use `0077`: sockets remain owner-only from creation and the existing explicit chmod
still establishes exact `0600` before returning. No group/other exposure is introduced.
The regression failed both old production call sites before implementation and rejects
retaining the unsafe mask. The original failing hook-gap scenario plus the regression
passed ten repetitions (18.711 s), and their race-enabled run passed (6.783 s). Existing
daemon/shim final-mode security selectors passed. Independent Sol and Terra reviews
returned GO; root build, touched-package vet and lint passed (zero lint issues).
This later Go fix is not part of the already-uploaded Android code-40 enrollment bundle.
Desktop publication still requires the normal full release workflow; focused local tests
are not represented as completion of that gate.

## Owner-only foreground relay activation

Under `agents-tracker-h8wh`, the owner explicitly requested completion of the foreground
relay setup. Release run `34043636815` completed successfully for `1b31e835`; installed
Swarm and its Homebrew-linked gateway are both from `0.13.29`. Independent Terra
verification reran the isolated Workerd suite on port 19429 successfully, including
authentication, replay/revoke, native Noise pairing, mailbox/discard recovery, mobile
wiring, hostile input, doctor, alarms and pre-auth limits. Deployment-config and
fail-closed admission checks passed. The optional latency benchmark remained skipped.

Local preflight found zero paired devices, an empty relay-purge ledger and mode `0600`
on the existing machine key. No old phone state, grants, cursors or mailboxes were
imported. The fresh configured home namespace is `owner-v2-20260906`; the public machine
RID is `a7b386f0977e91482329bdb5157e5937`. Sol reviewed identity reuse under these
zero-device/zero-obligation conditions. `swarm remote init` configured the sole WSS
origin with explicit WebPKI and no push gateway; it retained the machine identity.
The obsolete config remains recoverable as `relay.json.pre-v2-restart-backup` and is
not read or restored into the new configuration.

The existing keyring-backed OAuth profile was bound to the current checkout; its
account matched the original deployment. No new OAuth scopes, billing upgrade or
environment was requested. Current code was first deployed with admission closed as
version `abbd3933-f40f-4e1e-abed-5741400e45a5`; the hosted owner route still returned
503 before activation. One atomic secret-bulk update then set the two public admission
identifiers as operator-managed secret-text bindings. The active version became
`0ea5acc9-2687-4074-8083-c5d76c89dd1b`, with the same script etag, two original DO
namespaces and 60/60 native limiter, exactly two secret names and no test bindings or
preview URL. Only this computer is admitted; key-possession authentication remains
mandatory.

Hosted checks returned root 200, admitted plain machine route 426, and a distinct
well-formed machine route 403. `swarm relay doctor` passed DNS, certificate validation,
edge identity and real machine-control authentication, exchanged encrypted pairing
frames both ways and retired its transient ceremony. These are bounded hosted probes,
not a synthetic phone enrollment or a user command. The daemon restarted successfully
with this configuration; doctor reports running `0.13.29`, 39 persisted sessions and
zero degraded sessions. Remote status reports identity plus relay configured, zero
paired devices and device-derived remote OFF, as expected before pairing.

The installed `swarm remote pair` then reached real hosted `pair_start`, advertised the
correct relay and produced the QR step. Its single-use QR/secret was suppressed from
the verification transcript; no device was approved. Physical-phone SAS confirmation,
foreground command/stream/reconnect acceptance, background push and P1 completion are
not claimed by this setup checkpoint. The unused CLI ceremony expired normally with
the explicit closed-window refusal (exit 1); subsequent status still showed zero
paired devices. This expected expiry is not a failed relay setup or successful pairing.

## Pairing HTTP-expiry classification

`agents-tracker-2tqi` traced one misleading phone state to the relay-v2 HTTP upgrade
boundary. The Worker answers `/v2/pair` with 404 when a ceremony is missing or expired,
but the Go client discarded that response status and mobile wrapped every failed upgrade
as `relay_unreachable`. The failing-first test observed the generic `expected handshake
response status code 101 but got 404` error and mobile state `relay_unreachable`, wanted
`expired`. `DialPair` now maps only its own HTTP 404 to the existing typed
`pairing_not_found` outcome; HTTP 403, TLS/connect failures and an ordinary authenticated
`Dial` receiving HTTP 404 remain non-expiry errors.

The focused GREEN run passed for relay-v2 and mobile (0.967 s / 1.068 s); complete package
tests passed (11.122 s / 31.303 s), the focused race run passed (2.213 s / 2.375 s), and
`go vet ./internal/remote/relayv2 ./mobile` passed. Root lint reported zero issues.
Independent Sol review reran the focused tests including TLS hardening and returned GO.

This correction does **not** explain or fix the fresh physical-phone failure: a newly
generated code confirmed after approximately 8.5 seconds still reached
`relay_unreachable`, well inside the 60-second ceremony lifetime. That live failure
remains under investigation (`agents-tracker-gxy0`); this checkpoint establishes only
that a genuine missing/expired ceremony now tells the user to obtain a fresh QR instead
of incorrectly suggesting home Wi-Fi.

## Physical-phone relay dial diagnosis

The Samsung handset independently completed a default-platform TLS handshake to the live
Worker. Its accepted leaf certificate used an EC public key; Android's
`X509TrustManagerExtensions` accepted the same chain with both the previously suspected
`RSA` authentication type and the leaf's `EC` algorithm. A separate probe loaded the
actual `RelayTrustImpl` class from the Play-installed APK and passed that same leaf-first
PEM chain through its hostname and platform-verifier path successfully. This evidence
does not support the proposed authentication-type explanation, so no TLS code was changed.

The fresh pairing attempt still failed immediately after destination confirmation, about
8.5 seconds after machine pairing began, without reaching SAS. A temporary native Android
Go probe using the production relay-v2 client, a 10-second bound and the public live-leaf
SPKI pin then failed DNS lookup against `[::1]:53`. That binary was built with
`CGO_ENABLED=0`; the shipped `libgojni.so` is cgo-linked, so the result demonstrates only
that the pure-Go diagnostic used a different Android resolver path. It is not
evidence that the Play build has the same fault and no app fix is claimed from it.

The same bounded probe has therefore been rebuilt with Go 1.26.5, `CGO_ENABLED=1` and the
repository-pinned Android NDK r27.2/API-21 arm64 compiler, matching the shipped AAR's
resolver mode. Its execution is pending restoration of authorized USB debugging. The
probe reads the ephemeral ceremony on standard input, authenticates the exact public SPKI
obtained from the phone's platform-validated TLS connection,
redacts the ceremony and URL query from failures, and closes immediately after websocket
upgrade without sending `PAIR_CLAIM`; it neither relaxes authentication nor claims a
rendezvous. Until that differential run completes, the actual Go Android dial failure
remains unresolved.

## Stale native library identified in Play code 40

After USB debugging returned, the matching cgo-enabled Android probe passed the live
relay-v2 WebSocket upgrade with the platform-validated SPKI, closing without a claim.
The installed app still failed with fresh codes confirmed after 9.3 and 9.8 seconds,
including after an app-process restart that preserved its data. A bounded, redacted
Worker tail showed the app requesting `/` (HTTP 200), not `/v2/pair`.

The Play-installed arm64 `libgojni.so` has SHA-256
`56fe2b8d6f54aa38490b0ed9b57bcd076f3db64b462a4de2b7fefd5846170ff9`.
It is byte-for-byte identical to the local ignored AAR built on September 5, before
the September 6 bundle. It contains legacy `relay.DialRawSecure` / `relay.DialSecure`
symbols and no `relayv2` client symbols. Current source uses `relayv2.DialPair`.
Thus the published Android version changed while its native transport remained old;
the existing Gradle check proved only that an AAR existed. Independent Sol review
confirmed the artifact comparison. No TLS or DNS source change is warranted.

The correction under `agents-tracker-gxy0` is a mandatory native-library producer in
the release build graph and a fresh Play upload. The prior shipped-JNI scratch probe
was stopped without a result once this artifact evidence established the cause.
Phone pairing and foreground acceptance remain open until the corrected app is installed
and exercised; finding the cause is not a successful end-to-end test.

The failing-first release regression gate rejected the old existence-only dependency.
Release packaging now consumes its own generated AAR through a Gradle `builtBy`
producer that always rebuilds; the debug artifact remains separate, preventing a
combined debug/release build from racing over one file. The builder's optional output
argument is absolute-only and its default path is unchanged. The artifact gate now
selects that exact default path rather than scanning both intentional outputs.
Independent Sol review returned GO. The code-41/version-0.13.30 identity test was
observed RED against code 40 and GREEN after the bump.

Root passed the complete Android source gate (12.157 s), Android gate lint (zero
issues), and the tagged native rebuild/ABI test (30.036 s). The newly built arm64
library contains `relayv2.DialPair`. Complete relay-v2/mobile/Play/publisher tests,
race tests and vet also passed. Signed-bundle construction and physical acceptance
are tracked separately from these source and artifact checks.

The authoritative combined `lint test :app:bundleRelease` invocation succeeded in
9m18s. Both debug and release variants ran 1,751 tests with zero failures/errors;
lint passed. The executed graph rebuilt the generated release AAR before its native
consumers, then signed the bundle and regenerated its schema-2 provenance sidecar.
The signed bundle SHA-256 is
`e28a1ce77c763d0970c4bc1f385970e347e595a8336a8bd1d320e669a7e5eada`.
Its arm64 native library exactly matches both newly built AARs at SHA-256
`a716fc7549abe61c9f9f2b2cc2d20b1999e359089a17202fab82c62907b1770d`
and contains `relayv2.DialPair`. `jarsigner` verified the JAR signature (with the
self-signed upload-certificate and ZIP entry-order warnings); Google Play's guarded
dry run accepted code 41 on `alpha` without committing the edit. That rehearsal is
not publication or evidence that the handset has updated.

CI run `34054071507` passed on source commit `a3fdff07`, including full Go tests,
performance/soak checks, remote-v2 Workerd/Firestore, Android builds and tagged artifact
assertions. The guarded publisher then committed code 41 to `alpha` in edit
`09234984996869814072`. Play Console showed that alpha release under review.

The handset's Play listing identified its account as an internal tester, whereas the
existing internal track still served code 30 / 0.13.15. Internal testers receive the
internal-track build, not closed-alpha builds. Root reused the already uploaded and
provenance-verified code 41 through Console's **Add from library** control; no second
AAB upload or tester-enrollment change occurred. The review showed only code 41 / 0.13.30
and non-blocking missing deobfuscation/native-debug-symbol warnings. Root published it
to the existing internal track, and Console confirmed **Accessible aux testeurs internes**
for release 24 at 21:30 local time on September 6. Actual handset update and pairing
acceptance remain separate gates. [Google's testing-track rules](https://support.google.com/googleplay/android-developer/answer/9845334),
[reusing an uploaded bundle](https://support.google.com/googleplay/android-developer/answer/9859348).

### Physical owner pairing and foreground connection, September 6

Play installed code 41 / 0.13.30 on the Samsung A26. The preserved phone data initially
produced an INTERNAL startup refusal. Under the approved clean migration, root cleared
only `dev.swarm.phone` app data; this irreversibly removed its old local settings and
keys, without uninstalling the Play-signed package or changing the computer identity.
Fresh startup immediately restored onboarding. The exact old private blob was not
inspected: legacy state refusal is reproducible in synthetic tests, but opening an old
app alone does not necessarily persist such a blob, so that precise on-device cause
is inferred rather than directly observed.

The local ADB pairing harness initially captured only the first 80-character wrapped
CLI line. That attempt's malformed-code error was a harness defect, not a relay failure.
Requesting a 200-column CLI display and waiting for the complete line corrected it.
Root then confirmed the expected Worker destination, compared all six displayed emoji
on phone and computer, and approved both matching displays. The real CLI exited zero;
`swarm remote status` reported ON and exactly one enrolled phone.

The phone next waited for sync because the desktop gateway refused the old schema-2
`remote/inbound-state.json`. Independent review confirmed this checkpoint belonged to
the retired mailbox population and lacked v2 relay-authority binding. Root moved only
that file to `remote/inbound-state.json.pre-v2-20260906` (recoverable, same directory).
Identity, new device registration, outbound sequence files/outbox and purge obligations
were preserved. Supervisor retry created schema 3 with `relay_authority`. The phone
then displayed MacBookPro online and the real session list. An actual app force-stop
and reopen retained the pairing and restored the online list; opening a live session
reached its detail screen and composer without sending input to the existing agent.
The normal Refresh control left the machine online and advanced the gateway's inbound
cursor from 7 to 9, providing evidence of authenticated phone-to-computer traffic;
this is not a mutating session-command acceptance test.

The installation's public enrollment key is now available through the normal Settings
control. No private key was exported. Push deployment, a harmless command round trip,
live content streaming, network handover and background/Doze acceptance remain separate
unproven gates; this evidence does not close full physical acceptance.

The accompanying recovery-message fix maps both legacy reset sentinels to the existing
state-reset remedy, with conditional revocation only if the phone is actually listed.
No automatic reset or compatibility reader was added. Failing-first classification
tests and a real saved-bootstrap schema-26-to-25 fixture check the class, sentinel
identity and unchanged refused bytes. Independent Sol review returned GO. Final root
gates passed: complete mobile race suite (47.878 s), vet, lint (zero issues), and full
Android source gates (11.963 s). The affected Kotlin `PhoneStartupRoutingTest` passed
through Gradle (1m14s). This message fix is source-only; the tested phone remains the
already published code 41 rather than a newly rebuilt release.

### Cellular delivery: stale subscriber isolation

The owner's next cellular send exposed a gateway reconnect loop at cursor 11 with
`relay v2: unsolicited response`. USB diagnostics confirmed the phone's default route
was validated cellular, not Wi-Fi. A deterministic probe against the preceding Worker
reproduced a dead subscriber throwing during delivery after an APPEND success: the
handler emitted both APPENDED and ERROR for one request. The strict Go client correctly
rejected that second terminal reply. This is a reproduced failure mechanism; the exact
exception inside the original live Worker was not logged.

The shared delivery pump now skips non-OPEN subscribers and isolates only a delivery
send exception, closing that subscriber without advancing its persisted cursor. Cursor
conversion, SQL and attachment persistence remain outside the catch. No protocol
validation, durable receipt, acknowledgement or retry fence was weakened. Cloudflare
documents that [getWebSockets can include closing sockets](https://developers.cloudflare.com/durable-objects/api/state/).
Regression checks cover the closing filter, OPEN-to-closed send race, unchanged failed
subscriber state, continued delivery to another subscriber and propagation of storage
and attachment failures. Hosted deployment and physical round-trip results follow
separately; local reproduction alone is not proof that the owner's send works.

The implementation lane's final full Workerd suite passed on port 8794, and independent
Sol review returned GO. Root's relay-v2 race suite passed (12.775 s); deployment-config
and offline Wrangler upload checks passed with the existing bindings. The stale-send
handler regression observes exactly one APPENDED response after the fix versus the
archived Worker's APPENDED plus ERROR. No client binary or Android rebuild is required
for this server-side change.

Commit `b1e0596b` was pushed to main and deployed to the same Worker `s` as version
`5574e6a0-69a0-4018-b7a4-039f2c1322e0` (created 20:12:01 UTC). Version inspection
confirmed the original two Durable Objects, native limiter and exactly the two existing
operator secret bindings; no preview, new namespace, admission change or phone rebuild.
The second independent full Workerd harness also passed on port 8795. Root's parallel
suite on 19431 passed, although its initial protocol read preceded the final extra test
assertions; the two final implementation/review lanes cover those assertions.

Root created only disposable session `ep-da00228d/cdwhc6ydzfxymzk3`, named
`Cellular acceptance 20260906`, with a no-tools one-line-response prompt. ADB observed
validated cellular as the default route, with Wi-Fi off. At 20:13:05 UTC root tapped
Send once for `Reply exactly CELLULAR_PONG_20260906_2013`. The desktop reported remote
activity at 20:13:06.060779 UTC; the exact reply appeared in the desktop session and
the phone transcript (observed by 20:13:22 UTC). These are observation bounds, not a
latency percentile or an instrumented end-to-end benchmark.

Root then enabled Wi-Fi and verified it became the validated default network, disabled
it again and verified a new validated cellular default. Without re-pairing or restarting
the app, one Send at 20:14:47 UTC requested `HANDOVER_PONG_20260906_2015`. The matching
reply appeared on both desktop and phone. Each unique prompt and reply appeared once
in the inspected disposable conversation. The gateway log stopped growing after one
old expired-mailbox-frame notice at 20:12:41 UTC and did not resume the unsolicited
response loop during either exchange or the Wi-Fi/cellular transition. This supports
two successful foreground smoke samples, not the >=200-sample PH-NET latency gate.

The phone still displayed `Missing messages · Reload`, including after a normal Reload
press; initial cold-open had shown no messages until new activity loaded history.
That remaining transcript-history issue is tracked separately and is not dismissed as
cosmetic. Root stopped only the disposable session afterward, returned the phone to its
inbox, and left Wi-Fi off as initially found. The existing pairing remains ON with one
phone. Background push/Doze, broader history/stream tests and full migration acceptance
are not closed by these two successful sends.

### September 8: history classification and private push runtime

The history investigation found a false boundary at the birth of the disposable Codex
session, before the cellular exchange. Current Codex can create its rollout before the
observer's initial resume; immediate resume success is not proof of earlier missed turns.
Launch now snapshots the provider conversation identity before go-ahead. Fresh launches
do not invent prior history; explicit resumes retain a boundary on both immediate and
delayed subscription success. Delayed completion carries the original instance fence.
The fresh-rollout and delayed-resume regressions were executed RED against isolated
pre-fix source, then GREEN with the fix. Sol and Terra reviewed the complementary paths.
Android removes the permanent gap's misleading Reload action, not the durable gap;
the separate conversation refresh still reads retained history. Live cold-open acceptance
is still pending: ADB reported no device on September 8. No phone reset or pairing edit
was performed in this work.

Root provisioned the existing project's previously absent `(default)` Firestore database:
Native, Standard, `us-central1`, delete protection enabled, free-tier eligible, PITR off.
The fresh serving namespace is `push-v2-owner-pilot`; collection-scoped composite index
`CICAgOjXh4EK` is READY (`bound ASC`, `unbound_expires_ms ASC`). Daily backup schedule
`35b38175-a37d-4951-b9d8-cd5e69074ac3` retains copies for 14 days. Configuring a schedule
does not prove a completed backup or a revocation-safe restore.

The new runtime identity is `swarm-push-v2-runtime@swarm-8404f.iam.gserviceaccount.com`.
An initial project-wide datastore grant was rejected by the safety reviewer; the applied
grant instead conditions `roles/datastore.user` on the exact newly created database
resource name. This is database isolation, not collection-level IAM. The identity also
has the existing custom role containing only `cloudmessaging.messages.create`, plus
secret-level read access to the two new secrets. Old runtime identities received no
new grants. Google documents this [database-scoped IAM condition](https://firebase.google.com/docs/firestore/manage-databases).

`swarm-push-v2-token-keyring:1` holds independently generated AES and registration-HMAC
keys; they were generated in memory and sent to Secret Manager through stdin, never
printed or written to the checkout. `swarm-push-v2-admission:1` contains the owner's
installation public key verified through Settings on September 6. Reconfirm it on the
phone before activation. Mount paths are `/var/run/secrets/swarm-keyring/keyring.json`
and `/var/run/secrets/swarm-admission/admission.json`: Cloud Run rejected the initial
attempt to mount different secrets in the same directory; separate directories deployed.

Private service revision `swarm-pushgw-v2-00002-7pl` replaced the hello placeholder using
`ghcr.io/nathandela/swarm-pushgw@sha256:93e794cb572d7974d3b3dd8bb2c0d1aba16ab477bd440feb60de0e5dc3d2e902`.
The serving URL remains `https://swarm-pushgw-v2-733314021126.us-central1.run.app`.
It uses request billing, min 0/max 3, 1 CPU/512 MiB, concurrency 8, 30-second timeout,
no CPU boost, real ADC, explicit namespace and no emulator/dev mode. Platform anonymous
invocation remains disabled. Successful startup proves the mounted configuration and
initial Firestore read, not real Play Integrity, FCM or transactional write acceptance.

The `_Default` logging sink gained only `swarm-push-v2-request-paths`, matching this
service, region and `run.googleapis.com/requests`. It prevents automatic raw URL
retention while preserving application logs and platform metrics; other sink settings
were retained. This uses the provider's [request-log exclusion facility](https://docs.cloud.google.com/run/docs/logging).

The same pinned image, identity and secret versions back `swarm-pushgw-v2-retention`,
one task, zero retries, 60-second task timeout. Scheduler
`swarm-pushgw-v2-retention-hourly` invokes it at minute 17 each UTC hour, with a
60-second request deadline and no retries. Its separate identity
`swarm-push-retention-scheduler` has invoker only on that job, no data/secret grants.
Hosted execution and cleanup-backlog evidence must be recorded separately from creation.

Independent canonical verification used Java 21.0.12, Go 1.25.0 and Firestore emulator
1.22.0. Full race packages passed (`internal/pushgw` 16.189 s, command 5.457 s); all five
mandatory shared-store/retention tests explicitly passed. The anti-vacuity shell checks,
bounded child-process tests and transaction-retry experiment also passed. The experiment
observed two transaction callbacks but one provider send outside them. No real phone
attestation or push delivery is inferred from these emulator results.

Root's complete skeleton package passed in 380.500 s; the focused R7R4/subscription
race gate passed in 53.580 s, with vet and lint clean. The first full-package attempt
hit its five-minute test timeout and sandbox module-cache warnings; a normal-access
rerun with a 15-minute limit passed. Retention execution
`swarm-pushgw-v2-retention-9jf4x` completed successfully at 19:45:57 UTC. This verifies
one real bounded pass over initially empty state, not seeded backlog deletion or a
recovery drill. The private service IAM policy contains no invoker grants.
