# Scale-to-zero planning evidence

Date: 2026-09-04. Source: Swarm v0.13.27 / `f1621618`. These isolated probes support
[the proposed implementation plan](../../specifications/remote-scale-to-zero-plan.md).
They do not change production application code or deploy infrastructure.

## Result ledger

| Lane | Execution | Established | Not established |
|---|---|---|---|
| Relay | Actual local Wrangler 4.129.0/workerd, hibernating WebSocket API and SQLite-backed storage | Authenticated fixture routing, commit-before-forward, catch-up, ACK fence, revoke, staggered retention, uint64 controls | Production Ed25519 auth, forced eviction, hosted latency/billing, complete quotas/bounded catch-up |
| Push | Actual Firestore emulator 1.22.0; firebase-tools 15.29.0; client 7.11.6 | One concurrent nonce/registration winner; body conflict refusal; allocation cap; stale token CAS; transaction callback retry and duplicate side-effect negative | Real IAM/attestation/FCM, exact-once provider delivery, production contention/latency/quota behavior |
| Interop | Actual Go relay functions → Node 22 WebCrypto | Same HKDF RID, Ed25519 auth/consent verification, context/tamper refusal, framing and uint64 precision hazard | Full workerd production protocol implementation or exhaustive fuzzing |
| Migration | Deterministic JavaScript authority model | Frozen source rejection, stale epoch refusal, immutable export, post-mutation rollback refusal including revoke-only, incarnation distinction | Disk persistence, distributed atomic cutover, actual export/import or disaster restore |
| Existing Go | Selected repository tests against unchanged Go application | Backup/cursor/revoke and command/input/push-binding contracts still pass in baseline | Whole repository release gate, new backend equivalence, physical handset behavior |
| Economics | Pure asserted arithmetic | Empty-wait write amplification and illustrative private-CI/payback calculations | Actual workload, account free allowances or monthly bill |
| Runner cleanup | Real local child-process negative test | Deadline and external termination kill an owned grandchild that ignores graceful signals | Cross-platform Windows process handling; tests target POSIX |

The relay probe's **shared-HMAC ticket is a synthetic fixture**, not production Swarm
authentication. Attachment API use is not proof that workerd evicted/reconstructed the
object. The migration model's state lives in memory: comments about persistence describe
the intended protocol, not an actual durable-write measurement.

## Reproduce

Run from the repository root unless a command changes directory. Go >=1.25 and Node >=22
are required; push additionally requires Java 21. Dependencies are pinned with lockfiles.
The first dependency/emulator download needs network access. Tests use loopback only and
fake external providers; they neither need nor use live Google/Cloudflare credentials.

```sh
node docs/verification/remote-scale-to-zero/interop/check.mjs
node docs/verification/remote-scale-to-zero/cost-sensitivity.mjs
node docs/verification/remote-scale-to-zero/migration-probe/migration_fence_model.mjs
node docs/verification/remote-scale-to-zero/push-probe/test-run-bounded.mjs

cd docs/verification/remote-scale-to-zero/relay-probe
npm ci --no-audit --no-fund
npm run test:all

# In a fresh shell at repository root:
sh docs/verification/remote-scale-to-zero/push-probe/run-local.sh
```

On a restricted filesystem, set `GOCACHE` to a writable task-specific directory before
running the interop/Go commands. This investigation used
`GOCACHE=/private/tmp/swarm-plan.QDFqaJ/go-cache`. The initial default-cache attempt was
denied by sandbox permissions; the same test passed with that isolated cache.

The copied relay probe was rerun using the exact pinned Wrangler binary from the earlier
scratch install through `RELAY_PROBE_WRANGLER`; it did not copy its dependency cache into
the repository. Its ordinary `npm ci` command installs the same lockfile for a fresh run.
The copied push probe was run from its delivered directory and the emulator shut down.

One root relay rerun exposed a timing-sensitive test: both entries expired before a
250 ms-window assertion. The fixture now separates first/later TTLs (500 ms/6 s),
uses the actual automatic alarm, and passed three consecutive agent reruns plus an
independent root rerun. Caller-selected TTL exists **only in this fixture**; production
retention remains server policy. This is a test-harness correction, not evidence of
production latency or an invitation to shorten retention.

### Representative successful outputs

Root Go/WebCrypto interop:

```json
{"goWebCryptoHKDF":true,"ed25519Auth":true,"ed25519Consent":true,"wrongContextRejected":true,"tamperRejected":true,"actualGoFrameDecoded":true,"malformedFramesRejected":true,"uint64NumberLossDetected":true,"decimalStorageOrdering":true}
```

Sol's actual emulator concurrency run:

```json
{"nonce":true,"registrationIdempotency":true,"idempotencyBodyMismatchRejected":true,"allocationHardCap":4,"staleUnregisteredCAS":true,"revokeBeforeClaim":true,"transactionCallbacks":2,"unsafeDuplicateSends":2,"safeOutsideSend":1,"expiryAtUse":true}
```

`unsafeDuplicateSends:2` is an intentionally demonstrated bad design, **not an unexpected
test failure**. `safeOutsideSend:1` proves the tested callback arrangement only. It does not
resolve the crash-after-FCM-acceptance/before-completion window. `revokeBeforeClaim` checks
that ordering, not an exhaustive in-flight revocation race.

Selected existing Go commands run by Terra against the same baseline:

```sh
go test ./internal/remote/relay -run 'Test(Backup|Restore|MailboxCursorContinuity|MailboxDiscard|PBOPS5|WaitReauth)' -count=1
go test ./internal/remotegw ./internal/remote/device -run 'Test(CommandBridge_RoutesInputVsCommand|RelaySink|.*PushBinding.*|.*Push.*)' -count=1
```

Agent package results: relay PASS (12.760 s); remotegw PASS (1.750 s); device PASS
(1.121 s). Root independently reran the same commands in the deliverable worktree:
relay PASS (13.748 s), remotegw PASS (2.531 s), device PASS (1.417 s).
These are selected tests, not a full `go test ./...`, race, Android or release run.

## Bugs and mistaken assumptions exposed

1. Each new append originally overwrote the expiry alarm with its own later deadline.
   Fixed to preserve the earliest alarm; staggered retention test added.
2. Normal JSON number decoding loses uint64 cursor precision. V2 now requires decimal
   strings/BigInt and fixed20 storage ordering, with exhaustion refusal.
3. External side effects in Firestore transaction callbacks duplicate under retry.
   Provider submission must occur outside the retried callback.
4. Registration keyed by idempotency key **plus** body hash silently permits reuse with a
   different body. Key by idempotency identity, store/compare the body hash, reject conflict.
5. An exported array shared with target state made a rollback model comparison misleading.
   Export/import now clone; a separate mutation fence catches revoke-only changes too.
6. Existing phone/machine push hostname custody prevents assuming that changing an Android
   config value migrates established bindings. Retain the origin or implement authenticated
   binding migration; the plan uses a staged Hosting rewrite candidate.
7. A timeout supervisor originally exited when its npm leader exited, discarding the
   scheduled forced cleanup of descendants. It now remains alive through escalation.
   Agent and root lifecycle tests passed with `timeoutExit:124`, `externalTermExit:143`
   and `stubbornGrandchildrenGone:true`. Root also made the test independent of working
   directory and waits for the grandchild's ignore-signal handlers before signaling it.

## Remaining evidence gates

Hosted actor reconstruction/billing, real Google identity and Play Integrity scopes,
real retained-hostname forwarding, production Firestore contention, physical-handset
performance/lifecycle, full authorization conformance, migration fault injection and
revocation-safe backup restore remain **unrun**. See P0–P6 and the matrix in the plan.
The implementation estimate is a planning judgment, not inferred from probe runtime.
