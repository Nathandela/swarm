# Wave R4 — multi-machine foundation (agents-tracker-hggx.5 + agents-tracker-u37c)

Date: 2026-08-16. Role: GREEN (round 1) over the dated RED inventory in
`docs/verification/r4-red/` (go-red.txt, android-red.txt, both 2026-08-16).
Strict TDD: every assertion below was written RED-first by the RED slice and none
was weakened; the GREEN slice is implementation plus the enumerated ledger edits
this file records.

## 1. What was delivered, per playbook deliverable

### D1 — transactional migration into a machine registry (MM6)

- `internal/phonecore/machineregistry.go` — `MachineRegistry` (New:105 / Open:122 /
  AddMachine:166 / RemoveMachine:191 / Entries / MachineDir / Root),
  `MigrateSingletonToRegistry`:281 with the `MigrationConfig.Kill` seam and the five
  `MigrationKillPoint`s, `ErrRegistryNotLive`, `ErrStateMigrated`.
- `internal/phonecore/core.go:138-146` — `Resume` over the OLD singleton dir refuses
  with `ErrStateMigrated` once the registry commit is durable (MM6 step 5: never two
  live send sequencers for one pairing).
- Crash contract proved by `internal/phonecore/r4_migration_test.go` (5 tests, kill
  matrix at :204/:249): at every pre-commit kill point the old state resumes fully
  intact and the registry answers `ErrRegistryNotLive`, retry succeeds with exactly
  one entry; after the commit the registry is fully live and the old blob stays
  byte-identical (rollback-readable, step 6); a corrupt blob refuses with the root
  directory byte-identical (step 1).

### D2 — independent per-machine loops + aggregate stream (MM2/MM3/MM4/MM7)

- `internal/phonecore/registrymanager.go` — `RegistryManager` (NewRegistryManager:80),
  the real N-entry `MachineManager` that retires `SingleMachineManager`'s
  `ErrMultiMachineNotImplemented` refusals; per-client relays qualify every aggregate
  event with its machine id.
- Isolation proved byte-level by `internal/phonecore/r4_isolation_test.go`: disjoint
  namespaces/wake addresses/seq spaces/cursors/op queues (:102); machine A's
  revocation leaves machine B's namespace hash-identical and live (:139); process
  death restores both pairings with their own ceilings (:167); removal deletes
  exactly one namespace (:211); duplicate session id "s-1" on two machines is two
  aggregate identities (:232); duplicate AddMachine refused with the first add
  standing (:290).

### D3 — add/switch/forget screen model + global inbox facade (NOT reachable UX)

SCOPE, STATED UP FRONT (round-3 honesty amendment): what exists is the manager-backed
Go facade and a PURE Kotlin screen model. `MachinesScreen.kt` is referenced by nothing
in `android/app/src/main` -- no companion View, no Surface, no navigation caller -- so
no user can reach ADD_COMPUTER, SWITCH_COMPUTER, FORGET_COMPUTER or GLOBAL_INBOX. The
playbook's literal D3 wording ("every affordance nameable by the screen model, tested
from first-run resolver state") is met; DELIVERED UX is not claimed. Composing the
switcher and inbox over `FacadeBridge` is the follow-on slice (the 6
`android/unbound-verbs.tsv` rows).

- Go facade: `mobile/machines.go` — `App.Machines`:279, `App.SelectMachine`:336,
  `App.AddMachine`:362 (runs the MM6 migration on first use, then adds BESIDE),
  `App.ForgetMachine`:442, `App.GlobalInbox`:476 (rows keyed (machine_id,
  session_id)), handles `MachineList`/`MachineInfo`/`InboxList`/`InboxItem`.
- Traceability: 8 new rows in `mobile/screen_coverage.tsv` (machines.list/.add/
  .select/.forget, inbox.global, machines.connection_health/.stale_age/.recovery);
  the same 8 elements added to `requiredScreenElements` in `mobile/coverage_test.go`
  (the list's own documented growth rule); surface golden regenerated
  (`mobile/testdata/exported_surface.golden`, `-update-surface`).
- Android: `android/app/src/main/kotlin/dev/swarm/phone/ui/screens/MachinesScreen.kt`
  — pure screen model (PB-DS-9): `destinationFor` first-run resolver (0 ->
  PAIR_ONLY, >=1 -> MACHINES), affordances ADD_COMPUTER / SWITCH_COMPUTER /
  FORGET_COMPUTER / GLOBAL_INBOX, `MachineRowModel` keyed by machine id with
  reachability / lastSyncUnixMs / needsInput / derived stale. JVM suite
  `MachinesScreenTest.kt` (RED slice's frozen contract) now compiles and passes.
- New bound verbs with no Kotlin caller yet are ledgered in
  `android/unbound-verbs.tsv` (6 rows), per that file's uncallable-by-default rule.

### D4 — per-machine push revoke producer, end to end (u37c)

- `internal/remotegw/revokeproducer.go` — `HTTPAddressRevoker.RevokeAddress`:63
  (DELETE /v1/addresses/{base64url}, "Swarm-Revoke <cap>", empty body — never
  Swarm-Capability), `OpenRevokeObligationStore`:121 (+`Pending`),
  `NewRevokeObligationMachine`:186 / `Record`:192 / `Drive`:203 (record durable
  BEFORE first network attempt; 2xx/tombstone-204 terminal, transport+5xx/429
  retryable; request built from the obligation record alone, so epoch rotation
  cannot change a byte).
- Config plumbing: `internal/remotegw/service.go:122`
  `PushGatewayConfig.MachineRevokeCapability`; `cmd/swarm-remote/config.go:299`
  `pushGatewayFile.MachineRevokeCapability` (`machine_revoke_capability`,
  optional — a pre-producer file stays valid and resolves empty).
- PRODUCTION PRODUCER: `cmd/swarm-remote/main.go` — `produceMachineRevoke` (record +
  drive) when `Service.Run` returns `ErrDeviceRevoked`, and
  `redrivePendingMachineRevoke(stateDir)` on every process start, BEFORE the
  quiescence gate (round-2 fix: a completed revoke leaves ZERO paired devices —
  exactly the state `requireSomethingToServe` exits on — so the round-1 placement
  after the gate made the durable retry unreachable in the only state that can hold
  a pending obligation; the redrive needs only StateDir + push-gateway.json, never a
  paired device). Resolved idempotently by the PG-REV-2 tombstone. ROUND-3
  CORRECTION: the zero-device claim above was a precondition the round-2 code never
  ENFORCED — the redrive now drives only while the device registry is quiescent and
  retires a pending obligation found with a device paired again (section 6's
  blocking D4 fix).
  HonorMachineRevoke is therefore triggerable end to end; the library path is driven
  against the REAL `internal/pushgw` in
  `internal/phonecore/r4_revokeproducer_e2e_test.go:55`, and main's own ordering is
  driven through the real `main()` in a child process by
  `cmd/swarm-remote/r4_round2_test.go`
  (TestR4R2_Main_RedrivesThePendingRevokeBeforeTheQuiescenceGate).

### D5 — bounded connection concurrency + deterministic stale rendering

- `internal/phonecore/registrymanager.go` — `ManagerOptions{Cap,Now}`,
  `MarkViewed`, `ConnectedIDs`, `RecordSync`, `Rows`, `arbitrateLocked` (demotions
  strictly before promotions; the connected set is exactly the Cap highest view
  stamps, so the same history always yields the same set). Round-2 fix: `Remove` now
  stops the removed client BEFORE arbitration promotes a parked successor — round 1
  claimed the cap was a hard bound but stopped the removed client after the promote,
  holding Cap+1 live connections in between; proved and pinned by
  `r4_round2_test.go` (TestR4R2_ConnectionCap_RemainsAHardBoundAcrossRemove).
- Proved by `internal/phonecore/r4_connectioncap_test.go`: cap never exceeded and
  parked CLIENTS actually `Stop()`ed (:73); deterministic least-recently-viewed with
  re-view promotion (:99); parked rows Stale with the durable last-sync instant,
  connected rows never stale, identical `Rows()` under a frozen clock (:138).
- The documented product cap is `mobile/machines.go` `foregroundConnectionCap = 3`,
  exposed honestly via `MachineList.Cap`.

## 2. B94 ledger delta (internal/verify/phaseb_reachability_test.go)

- DELETED: all 15 `b94Allowed` MM4 rows (`NewSingleMachineAdapter`,
  `NewSingleMachineManager`, `SingleMachineAdapter.{Core,Events,ID,Running,Start,Stop}`,
  `SingleMachineManager.{Add,Close,ConnectionCap,Events,List,Remove,Select}`) — every
  one now has a production caller through `mobile/machines.go`'s manager seam, and
  the bidirectional fence confirms them reachable.
- ADDED (ledgered honestly): ONE row, `phonecore.NewMachineRegistry` — the first-run
  N-entry construction. Production phones acquire a registry only through the
  migration (every shipped install predates multi-machine); the row states the slice
  that must delete it (first-run pairing moving off the singleton layout).
- u37c's disclosure in `docs/verification/r3-green/android-green-round2.txt:222-226`
  amended: the two delivered obligations (P6 durable dual-revocation with the
  deletion tombstone; the HonorMachineRevoke producer) retired with an amendment
  note; the msg4 capability-signal deferral STAYS (not this wave's work, and
  `NewPairingWakeKey`'s B94 row stays with it).

## 3. Gate results (2026-08-16)

Go (toolchain go1.26.5, golangci-lint 2.12.2):

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `golangci-lint run ./...` — 0 issues
- `go test -race -count=1 ./internal/phonecore/ ./internal/remotegw/` — PASS
- `go test -race -count=1 ./cmd/swarm-remote/ ./mobile/ ./mobile/conformance/ ./android/gate/` — PASS
- `go test -count=1 ./internal/verify/` (TestB94 included) — PASS
- One flake observed and resolved: `./mobile/conformance` failed once while running
  in parallel with the full `./mobile` suite on this host (timing-sensitive rig
  under load; no test named in the truncated output), and passed cleanly on the
  serial rerun (196s) and again under `-race`. No assertion was changed.

Android (JAVA_HOME=/usr/local/opt/openjdk@21/..., ANDROID_HOME=
/usr/local/share/android-commandlinetools; script file
/tmp/r4_green_androidunit.sh; `--no-daemon --rerun-tasks --no-build-cache`;
test-results never deleted):

- AAR rebuilt via `android/build-aar.sh` at 11:57 local (09:57Z), BEFORE the gradle
  lane, because the facade surface grew and the artifact had to follow the
  regenerated golden; `TestPBBIND7_TheBuiltAARExportsThePinnedFacadeSurface` green
  afterwards.
- `./gradlew --no-daemon --rerun-tasks --no-build-cache lint test` — BUILD
  SUCCESSFUL; lint-results-debug.xml carries ZERO severity=Error issues; both unit
  variants ran with the R4 suite in them.
- `./gradlew --no-daemon --rerun-tasks --no-build-cache :app:assembleDebug` — BUILD
  SUCCESSFUL in 2m 27s, 39 actionable tasks: 39 executed.
- `go test -tags androidgate -timeout 60m -count=1 ./android/gate/...` (CI's
  expensive artifact assertions) — every assertion passed EXCEPT exactly one, the
  SAME pre-existing host condition all three R3 evidence rounds document
  (r3-green/android-green.txt section 3.4 and both round files):
  `--- FAIL: TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore`. This operator
  machine's ~/.gradle/gradle.properties carries the four SWARM_RELEASE_* entries
  and the keystore exists, so :app:assembleRelease legitimately succeeds SIGNED
  regardless of the scrubbed env; on CI (no gradle.properties) the refusal holds.
  Nothing in wave R4 touches signing or the release path. Verified twice (404s
  first pass, 138s confirm pass, one identical failure both times, no other
  failure in the tagged suite).

RUN STAMP CORRECTION (round 2, evidence hygiene): the ordering claim below was true
when recorded but was falsified afterwards — the AAR was rebuilt at 12:20 local
(10:20Z), AFTER this lane's end, so the artifact then in the tree was not the one
this lane validated. Round 2 rebuilt the AAR again (12:49 local, with the round-2
Go fixes) and re-ran the identical lane against it; the authoritative run stamp for
the tree as it stands is in section 5. The round-1 stamp is kept verbatim for the
record:

```
lane start:  ~2026-08-16T10:00Z (process spawn 12:00 local)
lane end:    2026-08-16T10:12:23Z
AAR mtime after the lane: Aug 16 11:57 local (09:57Z) -- PREDATES the lane's
  gradle runs: the lane did not rebuild the AAR, :app:requireSwarmAar only
  asserted its presence, exactly the RED lane's discipline.
test-result XML mtimes (REWRITTEN by this lane; the pre-lane set was 10:49):
  app/build/test-results/testDebugUnitTest/
    TEST-dev.swarm.phone.ui.screens.MachinesScreenTest.xml  Aug 16 12:07
      -> tests=6 skipped=0 failures=0 errors=0
  app/build/test-results/testReleaseUnitTest/
    TEST-dev.swarm.phone.ui.screens.MachinesScreenTest.xml  Aug 16 12:09
No test-results were ever deleted.
```

## 4. What this wave does NOT claim (disclosures)

- THE PHYSICAL EXIT IS THE OWNER'S. The three-machines-two-relays physical exit
  criterion is not claimed here. This wave's bar — per the wave instruction — is
  code-complete + gates green + the two-machine isolation tests
  (`r4_isolation_test.go`, byte-level) standing in for it.
- FACADE WIRING: the five new App verbs and `MachineList.Cap` have no production
  Kotlin caller yet; `MachinesScreen` is the pure model, and composing the switcher
  and global-inbox destinations over `FacadeBridge` is the follow-on slice. Each
  verb carries an `android/unbound-verbs.tsv` row saying so.
- `App.SelectMachine` records the viewed pairing and feeds the LRV policy; it does
  not yet re-target the App's live relay session to another machine's core.
- `App.AddMachine` creates the new pairing's registry namespace awaiting that
  machine's pairing ceremony; the ceremony itself still lands through the existing
  pairing flow, and first-run pairing stays on the singleton layout (hence the one
  new B94 row).
- `App.ForgetMachine` removes the pairing's registry row, namespace, keys and
  caches locally; the gateway-side push-address revoke by installation key on
  forget is not yet wired into this verb (the machine-side revoke arm, by
  contrast, is delivered end to end — D4).
- The per-machine clients the manager arbitrates are `SingleMachineAdapter`s whose
  Start/Stop is the manager's foreground-connection bookkeeping; independent
  per-machine CONNECTION loops (N live relay sessions) remain future wiring, and
  the aggregate stream's production clients publish no events yet
  (`drainAggregate` documents this). Round 2 closed the latent fail-open ahead of
  that wiring: `RegistryManager.relay` now consults the adapter's stopped()/
  stopSignal() halves (the same two `SingleMachineManager.relay` reads), so a
  parked client's events are dropped, never forwarded machine-qualified.
- The revoke producer's retry cadence is: once at the revoke moment, then once per
  `swarm-remote` process start — BEFORE the quiescence gate, and (round 3) ONLY
  while the device registry is quiescent: with a device paired again the pending
  obligation is retired undriven rather than presented against what may be the live
  pairing's own push address (section 6). The obligation is durable; the tombstone makes every re-presentation a 204.
  There is no in-process backoff timer, mirroring the wake-obligation redrive's
  current shape. Two round-2 store rules: `Record` never clobbers a PENDING
  obligation for a different address (refused; same-address re-record is a no-op),
  and a refusal `RevokeAddress` classifies terminal (non-2xx/5xx/429, e.g. a 401
  capability/address mismatch after re-provisioning) RESOLVES the obligation
  durably with the reason preserved in the record's `refusal` field, instead of
  re-presenting a dead capability on every start forever. The store still holds
  exactly ONE obligation — sufficient while a pairing owns one push address, and
  the refusal rule means a conflicting second revoke is loudly reported rather
  than silently swapped.
- AddSole (machine-side device registry) and ADR-011 multi-phone scope: untouched,
  per the freeze.
- `internal/pushgw`, `internal/skeleton`, `internal/shim`: untouched.

## 5. Round 2 — review fix pack (2026-08-16)

Role: GREEN (round 2) over the adversarial review of round 1. Strict TDD: every fix
below was pinned by a NEW failing test first (dated staged RED evidence in
`docs/verification/r4-red/go-red-round2.txt`; the round-1 inventory untouched, no
assertion weakened; all round-2 tests are additive files).

### Findings fixed

- BLOCKING (D4/u37c): the durable revoke retry was UNREACHABLE in production —
  main ran `requireSomethingToServe` before the redrive, and a completed revoke
  leaves zero paired devices, the exact state the gate exits on. Fix:
  `redrivePendingMachineRevoke(stateDir)` (`cmd/swarm-remote/main.go`) runs BEFORE
  the quiescence gate and needs only StateDir + push-gateway.json
  (`parsePushGatewayFile`, extracted store-free from `resolvePushGatewayConfig`).
  Pinned by `cmd/swarm-remote/r4_round2_test.go`: the redrive drives a recorded
  obligation from a 0-device state dir (in-process, TLS double via the
  `revokeHTTPClient` seam), and the ordering itself is driven through the REAL
  `main()` re-exec'd as a child process — drive report strictly before the gate
  report on stderr, quiescent SUCCESS exit.
- BLOCKING (D5): `RegistryManager.Remove` stopped the removed client only AFTER
  arbitration promoted a parked successor (peak Cap+1 live clients). Fix: Stop
  before `arbitrateLocked()`. Pinned by
  TestR4R2_ConnectionCap_RemainsAHardBoundAcrossRemove (peak-tracking clients).
- MEDIUM (D2/D5): a PARKED client's events still reached the aggregate stream.
  Fix: `RegistryManager.relay` consults the optional `stopped()`/`stopSignal()`
  surface (the same two halves `SingleMachineManager.relay` reads; the production
  client type `SingleMachineAdapter` implements both). Pinned by
  TestR4R2_ParkedClientEventsNeverReachTheAggregateStream, driven over REAL
  SingleMachineAdapters.
- MEDIUM (D4): the revoke obligation store clobbered a pending obligation and
  wedged forever on a terminal refusal. Fix in
  `internal/remotegw/revokeproducer.go`: `Record` refuses to overwrite a PENDING
  obligation for a different address (same-address re-record is a no-op), and
  `Drive` resolves a terminal refusal durably with the reason preserved
  (`RevokeObligation.Refusal`, JSON `refusal`) while still returning the error.
  Pinned by the two TestR4R2_RevokeObligation_* tests (401 double included).
- LOW: `NewMachineRegistry` refuses a root still holding an unmigrated
  phone-state.json (would brick the pairing: `ErrStateMigrated` with zero
  entries). Pinned by TestR4R2_NewMachineRegistry_RefusesARootHoldingAnUnmigratedSingleton.
- LOW: `validMachineID` refuses an id equal to `machine-registry.json` (its
  namespace directory would occupy the committed registry's path — a wedged root
  no retry can clear). Pinned by TestR4R2_ValidMachineID_RefusesTheRegistryFileName.
- EVIDENCE HYGIENE: section 3's round-1 RUN STAMP ordering claim had been
  falsified by a post-lane AAR rebuild (12:20 local). Corrected in place; the
  authoritative lane for the tree as it stands is below.

### B94 ledger delta (round 2)

None. The 15 MM4 rows stay deleted; the single `phonecore.NewMachineRegistry` row
stays with its deletion condition (round 2 only guards the constructor — no
production caller was added). `go test -count=1 -run TestB94 ./internal/verify/` — PASS.

### Gate results (2026-08-16, round 2)

Go (go1.26.5, golangci-lint 2.12.2):

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `golangci-lint run ./...` — 0 issues
- `go test -race -count=1 ./internal/phonecore/ ./internal/remotegw/ ./cmd/swarm-remote/` — PASS (all after the fixes)
- `go test -race -count=1 ./mobile/ ./android/gate/` — PASS
- `go test -race -count=1 ./mobile/conformance/` — PASS (204.9s)
- `go test -count=1 -run TestB94 ./internal/verify/` — PASS

Android (same discipline as round 1: script file `/tmp/r4r2_androidunit.sh`,
`--no-daemon --rerun-tasks --no-build-cache`, `lint test` then `:app:assembleDebug`,
`set -eu`, exit 0; JAVA_HOME=/usr/local/opt/openjdk@21/..., ANDROID_HOME=
/usr/local/share/android-commandlinetools; test-results never deleted):

- AAR rebuilt via `android/build-aar.sh` BEFORE the lane, at 12:49 local (10:49Z),
  carrying the round-2 Go fixes (12417695 bytes).
- `lint test` — BUILD SUCCESSFUL; lint-results-debug.xml carries ZERO
  severity="Error" issues; both unit variants ran the R4 suite.
- `:app:assembleDebug` — BUILD SUCCESSFUL in 2m 17s, 39 actionable tasks: 39 executed.

RUN STAMP (round 2; script exit code 0 under `set -eu`). ROUND-3 CORRECTION of the
AAR line below: as written it was falsified for the tree as it then stood -- the
tagged `androidgate` suite was run LAST, and that suite REBUILDS the artifact by
design (`android/gate/artifact_androidgate_test.go` TestPBTOOL2 runs
`./android/build-aar.sh`), leaving the AAR mtime at 13:03 local, ~51 s AFTER the
recorded lane end (byte size identical, 12417695). The claim "PREDATES the lane's
gradle runs" was true of the gradle lane itself but not of the tree's final state.
Round 3's stamp (section 6) states the rebuild-by-design explicitly:

```
lane start:  ~2026-08-16T10:50Z (script spawn 12:50 local)
lane end:    2026-08-16T11:02:35Z
AAR mtime after the GRADLE lane: Aug 16 12:49 local (10:49Z) -- but see the
  round-3 correction above: the tagged suite, run after, rebuilt it at 13:03.
test-result XML mtimes (REWRITTEN by this lane):
  app/build/test-results/testDebugUnitTest/
    TEST-dev.swarm.phone.ui.screens.MachinesScreenTest.xml  Aug 16 12:57
      (timestamp 2026-08-16T10:57:19.503Z) -> tests=6 skipped=0 failures=0 errors=0
  app/build/test-results/testReleaseUnitTest/
    TEST-dev.swarm.phone.ui.screens.MachinesScreenTest.xml  Aug 16 13:00
      (timestamp 2026-08-16T10:59:47.042Z) -> tests=6 skipped=0 failures=0 errors=0
No test-results were ever deleted.
```

- `go test -tags androidgate -timeout 60m -count=1 ./android/gate/...` re-run
  against the 12:49 AAR (191s): every assertion passed EXCEPT exactly the SAME
  single pre-existing host condition round 1 and all three R3 evidence rounds
  document — `TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore` (this
  operator machine's ~/.gradle/gradle.properties carries the four SWARM_RELEASE_*
  entries and the keystore exists, so :app:assembleRelease legitimately succeeds
  SIGNED regardless of the scrubbed env; on CI, with no gradle.properties, the
  refusal holds). Nothing in wave R4 touches signing or the release path. No
  other failure in the tagged suite.

## 6. Round 3 — review fix pack (2026-08-16)

Role: GREEN (round 3) over the adversarial review of round 2. Strict TDD: every code
fix below was pinned by a NEW failing test first (dated RED evidence in
`docs/verification/r4-red/go-red-round3.txt`, 2026-08-16 11:25Z: six round-3 tests,
five failing for exactly the reviewed defect, one — the quiescent-drive guard —
passing by design to pin the round-2 behaviour the fix must not break). No existing
assertion weakened; all round-3 tests are additive files (`r4_round3_test.go` in
`cmd/swarm-remote`, `internal/phonecore`, `mobile`).

### Findings fixed

- BLOCKING (D4/u37c, the round-2 fix's own self-DoS): `redrivePendingMachineRevoke`
  ran at every start and drove the stored obligation with NO check that the pairing
  was still revoked. push-gateway.json has no writer in this tree (hand-provisioned
  scaffold), so a re-pair after a revoke keeps the SAME push address — and the
  redrive would DELETE the now-live address at the gateway, tombstoning the fresh
  pairing's wake path permanently while reporting success. Fix
  (`cmd/swarm-remote/main.go`): the redrive now ENFORCES the zero-device
  precondition its own justification assumed — it drives only while the device
  registry is quiescent, and an obligation found pending with a device paired again
  is RETIRED durably (`retireStalePendingRevoke`; leaving it pending would defer the
  same delete to the next zero-device start). Pinned by
  TestR4R3_Redrive_RefusesToDeleteALivePushAddress (zero DELETEs at the TLS double,
  obligation no longer pending) and TestR4R3_Redrive_StillDrivesWhileQuiescent (the
  round-2 contract survives: 0-device redrive still drives).
- BLOCKING (D2/MM8): one broken namespace degraded EVERY pairing —
  `registryRuntimeLocked` (`mobile/machines.go`) aborted the whole runtime on the
  first `resumeNamespace` error, so with only machine B's blob corrupt,
  `App.Machines`/`GlobalInbox`/`SelectMachine`/`ForgetMachine` all failed wholesale
  with the state-corrupt remedy ("clear this app's data"), which destroys every
  pairing — the exact opposite of the `machines.recovery` row's claim and of
  deliverable 2's isolation property (which `r4_isolation_test.go` proves one layer
  BELOW this seam). Fix: the failed pairing becomes a BROKEN ROW
  (`MachineInfo.Broken` + `MachineInfo.BrokenReason`, new exported fields; golden
  regenerated; `machines.recovery`'s TSV row now names them) — every other pairing
  keeps its client, SelectMachine over the broken row refuses with ITS OWN named
  fault (state-corrupt class, "other computers are unaffected"), and ForgetMachine
  removes the broken registry row and namespace directly. Pinned by
  TestR4R3_OneBrokenNamespaceDoesNotDegradeTheOthers, driven through the REAL App
  over a migrated two-machine registry in production's exact key world (ONE at-rest
  KEK pair shared across namespaces — see the disclosure below).
- MEDIUM-HIGH (D1/MM7, forgotten keys): three closures in phonecore. (1)
  `MachineRegistry.AddMachine` clears the namespace directory before use, so
  RemoveMachine's commit-then-delete crash window can no longer hand a re-added
  machine id the forgotten pairing's sealed key material
  (TestR4R3_AddMachine_AdoptsNoResidueFromAForgottenPairing). (2)
  `OpenMachineRegistry` purges namespaces the committed registry does not name
  (best-effort by design — AddMachine's clear is the hard guarantee;
  TestR4R3_OpenMachineRegistry_PurgesNamespacesTheRegistryDoesNotName pins that the
  orphan goes and the NAMED namespace is untouched). (3) `RegistryManager.Remove` no
  longer returns early on a Stop error: the durable row and namespace are removed
  regardless and both errors are joined
  (TestR4R3_RegistryManagerRemove_ForgetsDurablyEvenWhenStopFails) — the latent
  fail-open on a generic MachineClient is closed.
- NOTE promoted to a fix (D4 durability hole): `machineRevoke(record=true)` returned
  BEFORE `Record()` when `machine_revoke_capability` was empty, so a pre-producer
  provisioning's revoke moment left NO durable obligation and a later capability
  provisioning could never drive the delete that was owed. Fix: the obligation is
  recorded UNCONDITIONALLY (Record needs no revoker; the store's parent dir is
  created if missing) and only the drive is skipped, with the report saying the
  obligation stays durable. Pinned by
  TestR4R3_RevokeMoment_RecordsTheObligationEvenWithoutACapability.
- LOW (evidence hygiene, recurrence): round 2's authoritative AAR stamp was
  falsified for the tree as it stood — the tagged `androidgate` suite runs
  `./android/build-aar.sh` (TestPBTOOL2) and was run LAST, leaving the AAR mtime
  ~51 s after the recorded lane end. Corrected in place in section 5; this round's
  stamp below runs the tagged suite last and SAYS it rebuilds the artifact by
  design.
- LOW (D3 scope honesty): section 1's D3 heading and body amended — what is
  delivered is the manager-backed facade and a pure screen model with NO reachable
  Android UX (`MachinesScreen.kt` has no View, Surface or navigation caller); the
  wave summary no longer reads as delivered UX.

### Disclosures added (round 3)

- KEY-WORLD DIVERGENCE in the byte-level isolation proof:
  `internal/phonecore/r4_isolation_test.go` gives each machine its OWN wake and
  content sealer, whereas production (`mobile/machines.go` `resumeNamespace`) shares
  ONE at-rest KEK pair across every namespace — the phonecore proof runs over a
  stronger key world than production has. Two bounds keep this honest:
  `OpenStore`'s different-machine discard bounds a swapped blob, and round 3's
  TestR4R3_OneBrokenNamespaceDoesNotDegradeTheOthers drives the mobile seam in
  production's exact shared-KEK key world.
- The redrive's retirement rule is deliberately conservative: with any device paired
  again, a pending obligation is retired UNDRIVEN and the retirement reason is
  preserved in the store's `refusal` field. If a future provisioning flow ever
  rotates the push address on re-pair, the retired record says why the old delete
  was not presented.

### B94 ledger delta (round 3)

None. The 15 MM4 rows stay deleted; `phonecore.NewMachineRegistry` keeps its row and
deletion condition; `NewPairingWakeKey` stays with the msg4 deferral. The new
`MachineInfo.Broken`/`BrokenReason` fields are fields, not fenced symbols.
`go test -count=1 -run TestB94 ./internal/verify/` — PASS (680 symbols examined, 57
unreachable and all accounted for).

### Gate results (2026-08-16, round 3)

Go (go1.26.5, golangci-lint 2.12.2, PATH=$HOME/go/bin):

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `golangci-lint run` — 0 issues
- `go test -race -count=1 ./internal/phonecore/ ./internal/remotegw/ ./cmd/swarm-remote/ ./mobile/ ./android/gate/` — PASS (all after the fixes)
- `go test -count=1 -run TestB94 ./internal/verify/` — PASS
- `go test -race -count=1 ./mobile/conformance/` — PASS (205.3s)

Android (same discipline: script file `/tmp/r4r3_androidunit.sh`, `set -eu`,
`--no-daemon --rerun-tasks --no-build-cache`, `lint test` then `:app:assembleDebug`;
JAVA_HOME=/usr/local/opt/openjdk@21/..., ANDROID_HOME=
/usr/local/share/android-commandlinetools; test-results never deleted):

- AAR rebuilt via `android/build-aar.sh` BEFORE the lane, at 13:31 local (11:31Z),
  carrying the round-3 facade (`MachineInfo.Broken`/`BrokenReason`; 12427801 bytes,
  grown from round 2's 12417695 by exactly the surface change). The untagged
  `./android/gate` suite (PB-BIND-7 surface pin included) passes against it.
- `lint test` — BUILD SUCCESSFUL in 9m; lint-results-debug.xml carries ZERO
  severity="Error" issues; both unit variants ran the R4 suite.
- `:app:assembleDebug` — BUILD SUCCESSFUL in 2m 24s.

RUN STAMP (round 3; script exit code 0 under `set -eu`):

```
lane start:  2026-08-16T11:34:03Z (13:34 local)
lane end:    2026-08-16T11:45:30Z
AAR mtime before AND after the gradle lane: Aug 16 13:31 local (11:31Z),
  12427801 bytes -- PREDATES the lane: the gradle lane did not rebuild the AAR,
  it validated the round-3 artifact.
test-result XML mtimes (REWRITTEN by this lane; pre-lane set was 12:57/13:00):
  app/build/test-results/testDebugUnitTest/
    TEST-dev.swarm.phone.ui.screens.MachinesScreenTest.xml  Aug 16 13:40
      (timestamp 2026-08-16T11:40:30.867Z) -> tests=6 skipped=0 failures=0 errors=0
  app/build/test-results/testReleaseUnitTest/
    TEST-dev.swarm.phone.ui.screens.MachinesScreenTest.xml  Aug 16 13:43
      (timestamp 2026-08-16T11:42:37.927Z) -> tests=6 skipped=0 failures=0 errors=0
No test-results were ever deleted.
```

- `go test -tags androidgate -timeout 60m -count=1 ./android/gate/...` run LAST,
  twice (11:45:50Z-11:50:02Z with a 40-line tail, then 11:50:54Z-11:53:31Z with FULL
  output to enumerate failures): every assertion passed EXCEPT exactly one, the SAME
  single pre-existing host condition rounds 1-2 and all three R3 evidence rounds
  document — `TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore` (this operator
  machine's ~/.gradle/gradle.properties carries the four SWARM_RELEASE_* entries and
  the keystore exists, so :app:assembleRelease legitimately succeeds SIGNED
  regardless of the scrubbed env; on CI, with no gradle.properties, the refusal
  holds). Nothing in wave R4 touches signing or the release path. No other failure
  in the tagged suite either run.
- AAR AFTER the tagged suite: Aug 16 13:51 local (11:51Z), 12427801 bytes —
  POSTDATES the gradle lane BY DESIGN: the tagged suite's TestPBTOOL2 runs
  `./android/build-aar.sh` and rebuilds the artifact from the same tree, to the
  same byte size as the 11:31Z artifact the gradle lane validated. This is the
  rebuild-by-design statement round 2's stamp lacked (section 5's correction).
