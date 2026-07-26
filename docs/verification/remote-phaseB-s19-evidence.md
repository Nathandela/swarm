# Phase B slice S19 — the exit demonstration (PB-E2E-1, PB-E2E-3)

**Requirements**: PB-E2E-1 (the no-fakes end-to-end chain) and PB-E2E-3 (evidence that states what
it proves). **PB-E2E-2 is assigned separately** (the Android app has no pairing UI; its fence
`android/gate/s19_pbe2e2_test.go` is RED and stays RED until that work lands). **PB-E2E-4** is the
full-suite sweep recorded under Gates below. **PB-E2E-5 remains DEFERRED and nothing here changes
that**: no real biometrics, no real camera, no real FCM delivery, no Doze, no hardware Keystore
attestation.

**Scope of this file**: the IMPLEMENTATION half. The exit test itself was written by an
independent RED author who fixed nothing; the seam inventory lives in the header of
`internal/skeleton/s19_e2e_test.go`. What is recorded here is the four production holes the exit
test found, the tests written for each, and the PB-E2E-3 audit.

## What this proves

That the whole chain works with **no fakes and no `phonesim` seam**: a real relay, the real bound
`swarmmobile.App` over a real `phonecore.Core`, the `cmd/swarm-remote` BINARY spawned as the
separate supervised process production runs, the full `skeleton.Serve` daemon with its shim and
PTYs, and the production pairing rendezvous — pair -> observe -> launch -> take_control -> type ->
revoke, under `-race`, with the gateway subprocess carrying the race detector too.

**Four shipped requirements were unreachable in production when this slice began**, each behind a
different hole, and all four shared one shape: *the requirement was marked shipped on evidence
from a harness that grants itself the lease and never reaches the daemon's checks.* PB-INPUT-2,
PB-INPUT-3, PB-APP-4 and PB-APP-6 are the affected rows.

## What it does NOT prove

- Nothing about a physical handset. The KeyCustody is two software AES keys behind the Keystore
  interface (`s19Custody`), exactly the substitution `mobile/conformance` already makes.
- Nothing about supervision: the machine's provisioning is written by the test rather than by
  `swarm remote init`, because `runRemoteInit` also installs a launchd/systemd unit on the
  developer's machine. The BYTES on disk are identical and are read back by the production loaders
  (`loadPairingConfig`, `resolveGatewayParams`); only the supervision half is skipped, and that
  half is S4's.
- Nothing about the Android app (PB-E2E-2).

## The four holes, RED-first (GG-5)

Each was found by the exit test, given its own focused test at the layer that owns it, and only
then fixed. Verbatim RED output.

### 1. The phone had no production source for the machine endpoint id

`crypto.Command.Canonical` refuses an empty `Machine`, so **every signed verb failed before a byte
was sealed**: launch, kill, take_control and the revoke panic button. `PhoneRuntime.construct`
passes `MachineID = ""` (correctly — a handset has nowhere to have learned one), the pairing
payload carried no endpoint id so `pin()` had nothing to write, and `Core.Reconcile` receives the
machine's own name on an authenticated record but only validates it.

```
s19_e2e_test.go:125: after a real pairing the phone's machine endpoint id is "", want
"ep-5a56c05a"; the phone has no production source for this value
```

**Every fixture in the repo seeds `State.Machine` by hand**, which is why nothing caught it:
`s10FreshInstall` and `TestPBNET1_AFreshInstallsPairingSurvivesTheNextProcessStart` both open the
App with `MachineID: testMachineID` and then assert that value survives a restart — the value they
check is the value they supplied.

**Where the id legitimately enters durable state: the pairing handshake.** It is the same class of
fact as the three machine keys already pinned in msg2, and it is needed BEFORE the gateway exists
— PB-LIFE-3 starts the sidecar only after pairing, so an id delivered on the gateway's reconcile
record would arrive after the phone had already been unable to author anything.

Three tests, three links:
- `internal/remote/pairing/s19_endpointid_test.go` — `MachinePayload.MachineEndpointID` rides as a
  sixth length-prefixed field before the epoch trailer (the 2026-07-20 `MachineSignPub` amendment's
  pattern), round-trips losslessly, and reaches the device through a real handshake.
- `internal/skeleton/s19_machineid_test.go` — `loadPairingConfig` carries the id the daemon SERVES
  under (asserted against a live daemon's client-visible `EndpointID()`, not against
  `endpointID(dir)`, which would be the implementation restated), and `BeginPairing` publishes it.
- `mobile/conformance/s19_machineid_test.go` — a handset opened with `MachineID: ""` learns the id
  from a real pairing and still holds it after a restart. RED verbatim:

```
s19_machineid_test.go:77: after a completed pairing the phone names machine "", want
"ep-s10boot1". The endpoint id is signed into every mutating command ... so this phone is
paired and mute
```

*(The fixture constant `s10MachineEndpointID` was `"ep-s10boot1"` at that RED run and is now
`testMachineID`. The change is not cosmetic and is recorded: one machine has ONE name — in
production the pairing payload, `RelaySink.Machine` and the id the daemon verifies signatures
against are all `endpointID(stateDir)` — so a fixture publishing a second one has the phone refuse
every authority that machine publishes and its blob discarded on the next load. Three
`mobile/conformance` tests went red on exactly that and were the evidence.)*

`pin()` adopts the id only when the machine published one. Persisting `Machine=""` is the S9 defect
in full: the load-time filter discards a blob stamped with a machine that is not the caller's, so
overwriting a known name with nothing would throw away the pairing, the epoch, the sealed content
key, the relay cursor and the send-seq ceilings on the next process start.

### 2. `mobile.LaunchSpec` carried no terminal geometry (PB-APP-6)

`App.Launch` built a `schema.LaunchReq` with `Cols=0, Rows=0`; the daemon refuses `Cols < 1`.

```
s19_e2e_test.go:679: the machine refused the phone's launch: code="error"
message="launch: cols/rows out of range"
```

`mobile/conformance/s19_launchgeometry_test.go` covers both halves: an unset geometry still
produces a launch the daemon will run (defaulting to 80x24, the grid the daemon's own session tap
opens its emulator at), and an explicit geometry rides verbatim.

**A correction to the brief's stated reason.** The task framing was that "the gateway cannot fill
them in — `LaunchContentHash` covers them". It does not: `schema/launchhash.go` excludes Cols/Rows
in as many words ("cosmetic terminal dimensions"), so a gateway could legally have filled them
without breaking the device signature. The reason the phone must send them is different and still
decisive: only the phone knows the grid — the size of the view the user is about to watch the
session in — and a machine-side default would open every remotely launched session at a width
nobody chose.

The bound surface grew two fields, so `mobile/testdata/exported_surface.golden` was regenerated
under PB-BIND-7's review requirement. The diff is exactly `field LaunchSpec.Cols int` and
`field LaunchSpec.Rows int`: additions, no removal, so nothing the Android app compiles against
breaks. No new `screen_coverage.tsv` row is owed — the traceability fence covers entry points, and
`App.Launch` was already traced to PB-APP-6.

### 3. A successful launch reply was unattributable (PB-SYNC-2's own subject)

`internal/protocol/server.go` handleLaunch wrote `Control{Op, EndpointID, Session}` with **no
`OperationID`**, unlike `replyOK`/`replyError`, which both echo `cc.opID`. The gateway seals the
daemon's reply verbatim onto the phone's reply bucket, and `phonecore.foldContent` DROPS a
command_reply naming no operation — deliberately, because mis-keying it would attribute one op's
verdict to another. So `App.Outcome(id)` never resolved for a launch that SUCCEEDED and
`PendingOpCount` stayed >= 1 for the life of the process.

```
s19_launchreply_test.go:56: a SUCCESSFUL launch replied with operation_id "", want
"devA:01JS19LAUNCHREPLY0000000"
```

`internal/protocol/s19_launchreply_test.go` asserts the success reply and carries the refusal path
as a control — `replyError` has always echoed the id, so a test that only asserted "some reply
names the op" would have passed on the unfixed code by refusing the launch.

### 4. `App.TakeControl` minted no gate token (PB-INPUT-2/-3, PB-APP-4)

`handleTakeControl` refuses an empty `GateToken` before authorization, and deliberately does not
settle for a hash check because `SHA256("")` is a valid 32-byte hash. The facade sealed through
`sealSignedCommand`'s default branch with `ContentHash = nil` and never called
`SignTakeControl`/`SealTakeControlEnvelope`, which `phonesim` has called since A7.

```
s19_e2e_test.go:174: the machine refused the phone's take_control: code="error"
message="remotegw: lease died before it was granted"

s19_gatetoken_test.go:52: the facade sealed a take_control with no gate token ... so this
phone can never take control and every keystroke after it is dropped by the gateway
```

`mobile/conformance/s19_gatetoken_test.go` asserts the token is present, is BOUND
(`ContentHash == SHA256(token)`, via `phonecore.SignTakeControl` so the hashing rule stays in one
place), and DIFFERS between two take_controls. The requested TTL is the horizon the command was
signed for, so the signature is never outlived by the lease nor the lease by the signature.

**The token is random, not attested.** §6.0's biometric freshness is PB-SEC-2's and real biometric
backing is PB-E2E-5, which is deferred — nothing here claims a BiometricPrompt gated it. What the
token delivers is the property the daemon actually enforces: one-shot, unforgeable by the relay,
bound to this exact command.

### The diagnosability defect behind hole 4

`LeaseConn.readLoop` returned on `OpError` without reading the error it had just decoded, so six
distinct daemon refusals — absent gate token, unknown device, forged or expired signature,
insufficient capability, kill switch, consumed operation id — all reached the operator as the one
sentence `remotegw: lease died before it was granted`, which names the transport rather than any
of the causes. It is what the first `-race` run of hole 4 reported, pointing at the socket.

```
s19_leasereason_test.go:71: a refused lease reported "remotegw: lease died before it was
granted". The daemon said why -- "take_control requires a gate token" -- and readLoop decoded
that OpError and returned without reading it
```

`AwaitLease` now wraps the daemon's own words around `errLeaseDead`, so `errors.Is` still classifies
it; a death with NO explanation keeps the bare sentinel, which is then a true statement rather than
a catch-all.

## Every fence, shown failing against its mutation

Run against the fixed tree, each mutation applied and reverted:

| Mutation | Fence that caught it |
|---|---|
| `encodeMachinePayload` writes an empty endpoint id | `TestS19_MachinePayload_EndpointIDRoundTrip` **and** `TestS19_Pairing_ConveysMachineEndpointID` (the second is why the codec test is not enough) |
| `loadPairingConfig` derives a DIFFERENT id (`+"-x"`) | `TestS19_PairingConfigCarriesTheEndpointIdTheDaemonServesUnder` |
| `BeginPairing` omits the id from `MachinePayload` | `TestS19_BeginPairingPublishesTheMachineEndpointID` |
| `App.Launch` ignores `spec.Cols/Rows` | `TestS19_ARemoteLaunchCarriesATerminalGeometryTheMachineAccepts` |
| the gate token is a constant | `TestS19_TakeControlMintsAGateTokenBoundIntoItsSignature` (distinctness clause) |
| the token is carried but not signed over | same test (content-hash clause) |
| an evidence file loses a requirement id | `TestPBE2E3_EveryShippedRequirementsEvidenceFileNamesIt` |

The three remaining fixes had a natural RED against unfixed production code (quoted above) and
needed no synthetic mutation.

## PB-E2E-3 — the audit, and what it found

`internal/verify/phaseb_evidence_test.go` (written by the RED author) fences the floor: a shipped
requirement's cited evidence file must NAME it. Thirteen rows were below that floor —
PB-APP-2/-4/-5/-6, PB-DOC-5, PB-GW-5, PB-NET-4/-6/-7, PB-STATE-2/-3/-4/-5 — because their files
used range forms (`PB-STATE-1..5`, `PB-APP-1..10`) that contain no literal identifier, so an
auditor holding the row had no path to the proof.

**The fence was not edited.** Four evidence files gained per-requirement sections, reconstructed
from the tests: `remote-phaseB-s7-evidence.md`, `remote-phaseB-s6-evidence.md`,
`remote-phaseB-s2-evidence.md`, `remote-phaseB-s16-evidence.md`. Three findings came out of writing
them:

1. **PB-DOC-5 could not be substantiated at all.** Its acceptance criterion is a document change —
   the Phase A committee closure "gains a scoped note, not a retraction" — and S2 performed the
   analysis without ever amending `remote-phaseA-committee-closure.md`, which contained no mention
   of §4.6, of the disproved exploit, or of the single-gateway-run scope. The deliverable did not
   exist while the row read `shipped`, and the traceability count could not see that because a row
   is measured by whether its evidence FILE exists. The note has now been written (that file,
   section "SCOPED NOTE — the Phase B §4.6 finding"), dated to S19 rather than backdated.
2. **PB-APP-4's take-control half and PB-APP-6's launch were both unearned in S16** — they are
   holes 4 and 2 above. The S16 evidence now says so per requirement, including the sharp case:
   the Kotlin test `an unanswered launch stays pending rather than being guessed at` was, in
   production, describing the behaviour of a launch that SUCCEEDED.
3. **PB-NET-6's second half is not S6's.** "plus the PB-STATE-2 restart case" is
   `internal/phonecore`'s process-death test, which landed in S7; the S6 section says so rather
   than claiming it.

## Findings: could the shipped unit tests ever have caught these?

For each hole, **no**, and each for its own reason:

- **Hole 1**: every fixture supplies the machine id through `Config.MachineID`, so the production
  question — where does a HANDSET get this? — was never asked. `TestPBNET1_AFreshInstallsPairings
  SurvivesTheNextProcessStart` is the sharpest case: it pairs for real, then verifies the id
  survives a restart, and passes only because the config re-supplies it on every open.
- **Hole 2**: `mobile/conformance`'s machine is a mailbox reader and `remotegw`'s launch tests
  forward a `LaunchReq` built in-test. Neither reaches `handleLaunch`'s range check, so a launch
  spec no daemon would accept satisfied both.
- **Hole 3**: every launch assertion in `internal/protocol` reads the reply's Session; the
  phone-side suites resolve outcomes against replies their own fixtures seal. The two never met.
- **Hole 4**: `mobile/conformance`'s harness GRANTS ITSELF the lease (`harness.Drain` answers each
  take_control with a locally-sealed `OpLease`), so the frame's gate token is inspected by nothing.
  `remotegw`'s lease tests build the `RemoteCommand` with a token literal, and the one
  `internal/skeleton` test that reaches a real daemon signs with `phonecore` directly rather than
  through the facade.

All four are the S19 brief's defect class (v): **a fence guarding a path production does not
take.** The common repair is not more unit tests; it is that the facade and the daemon meet in one
test, which is what PB-E2E-1 is.

## Gates

```
go build ./...                                                              ok
go vet ./...                                                                ok
gofmt -l (every changed file)                                               clean
go test ./... -count=1                                                      1 FAIL: android/gate
                                                                            (PB-E2E-2 only, assigned)
go test -race ./internal/skeleton/ ./internal/protocol/ ./internal/remotegw/
  ./internal/phonecore/ ./mobile/... ./internal/remote/pairing/
  ./internal/remote/relay/ ./internal/remote/transport/ ./cmd/swarm-remote/  all ok
~/go/bin/golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 ./...  exit 0
go test -race ./internal/skeleton/ -run TestPBE2E1                          ok (22.5s)
```

Both lint caps are lifted deliberately: with the defaults the linter reports clean while
suppressing duplicates, which is a gate that cannot fail.

**Not one of the recorded pre-existing flakes reproduced** in the full sweep
(`TestS6B_GatewayInputLatencyIsNotPollGated`, `TestPBSAS2_*`, `TestPBPAIR5_*/different_machine`,
`TestPresence_TransitionsAndSilentPush`).

## Residuals

- **A machine that publishes an empty `MachineEndpointID` still produces a paired-but-mute phone.**
  `pin()` will not overwrite a known name with nothing, and the machine side is now tested, but the
  phone does not REFUSE such a pairing. Refusing would be worse: the machine has already enrolled
  the device by that point, and `BeginPairing` fail-fasts while a device is registered
  (single-device v1), so a phone that discarded the enrollment would need physical access to the
  machine to recover. A sixth terminal pairing state is a PB-PAIR-5 amendment, not an
  implementer's call.
- **`Core.Reconcile` still does not ADOPT `rec.Machine` on an authenticated record**, only
  validates it. It is no longer load-bearing (the pairing supplies the id before the gateway
  exists), and adopting there would be a second source for one coordinate. Left as it is,
  deliberately.
- **PB-E2E-2 is RED and stays RED**: `android/gate/s19_pbe2e2_test.go` reports four of five in-app
  actions with no subject (no scanner, no SAS display, no confirm control, no keyboard) and no
  instrumented source set. Assigned separately.
