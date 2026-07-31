# S18b evidence — fail-closed must not mean bricked (PB-STATE-10)

**RECONSTRUCTED, not written by the implementer.** S18b shipped without an evidence file; its
only durable record was a commit message. This file was written on 2026-07-26 at HEAD
`21307d5`, from the commits and from running the tests. Every claim below was executed in this
worktree, and every claim about *a guard's ability to fail* was mutation-checked — mutation
applied, result recorded, mutation reverted. `git status` is clean.

**The commit ids, corrected.** The reconstruction brief named `59fcbd5`; that is *"docs: correct
my Kotlin test count"*. S18b shipped in **`3b6694f`** and was substantially remediated in
**`a1110dd`**. Its central decision was then reworked twice more, in `c955dd9`/`4a91dc0` (B25,
B26) and `833534f`/`c326b44` (B27). **This file describes the code as it is now.**

## The requirement, verbatim

> **PB-STATE-10 — Fail-closed must not mean bricked.** PB-STATE-4 fails closed and prompts
> re-pair, but PB-KEY-3 establishes that re-pairing is *refused* while a device is registered
> (`BeginPairing` fail-fasts on a non-empty registry), so the phone could brick into a state
> whose only exit is physical access to the machine. **The recovery flow must be unconditional**,
> not inherited from PB-KEY-3's optional branch: PB-KEY-3 permits an implementation to choose
> re-grant instead of an unblock, and re-grant cannot recover a phone whose local state is
> corrupt and fail-closed. Required as its own owner-side flow: list/identify the stranded
> device, revoke/unregister it, purge machine and relay state, re-pair.
>
> *Criterion:* Test drives the exact CLI-visible path: corruption -> fail-closed -> owner-side
> recovery -> working re-pair, with no step requiring undocumented knowledge.

**The last clause is the slice.** A recovery reachable only by an operator who already knows to
run `swarm remote revoke` satisfies "reachable" and fails "discoverable".

## What is asserted

| | |
| --- | --- |
| Go tests owned by S18b | **10 functions**, all green |
| Kotlin tests added by S18b | `PhoneStartupRoutingTest`, **7 `@Test` methods** — **NOT RUN**, no JDK |
| Mutations applied and reverted | 5, each named at the claim it supports |

```
go test ./cmd/swarm/          -run TestPBSTATE10   ok  4.329s   (7/7 PASS)
go test ./mobile/conformance/ -run TestPBSTATE10   ok  1.341s   (1/1 PASS)
go test ./internal/remote/relay/ -run 'TestRelay_ABanIsLifted|TestRelay_TheBanningMachine'
                                                   ok  2.723s   (2/2 PASS)
```

The `cmd/swarm` file drives a **real** relay, a real skeleton daemon over a real machine
identity, the real unexported `runRemote`/`runRemotePair` verbs, and a real `swarmmobile.App` as
the phone, in one process. Only the gateway supervisor is faked, through the pre-existing
`installFakeSupervisor` seam, so no test touches launchd.

## The chain, link by link, each mutation-checked where the link is behavioural

| link | what performs it | test |
| --- | --- | --- |
| corruption -> fail closed, with an actionable class | `ErrClassStateCorrupt` / `STATE_CORRUPT` / remedy `clear_data_and_re_pair` | `TestPBSTATE10_CorruptStateFailsClosedAndNamesTheOwnerSideRecovery` |
| the pair refusal names how to find and revoke | `internal/skeleton/pairing.go` | `TestPBSTATE10_ThePairRefusalNamesHowToFindAndRevokeTheStrandedDevice` |
| the listing names the revoke | `cmd/swarm/remote.go:458` | `TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatTheOperatorWasTold` |
| the revoke names the re-pair, and actually unregisters | `cmd/swarm/remote.go:546` | `TestPBSTATE10_RevokeNamesTheRePairThatFinishesTheRecovery` |
| purge **relay** state | `purgeRelayState` -> `relay.Client.DeviceRevoke` | `TestPBSTATE10_RevokePurgesTheStrandedDeviceRelayState` |
| purge **machine** state | `purgeOutboundCustody` -> `Outbox.Purge` | `TestPBSTATE10_RevokePurgesTheMachineSideOutboundCustody` |
| the purge is not a permanent ban | `store.authorizePair` + `authorizeAtRelay` + `rearmAfterPairing` | `TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset` |
| the whole chain, closed | all of the above | `TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatTheOperatorWasTold` |

**The behavioural links, shown failing:**

```
MUTATION: purgeRelayState(...) removed from runRemoteRevoke
  -> TestPBSTATE10_RevokePurgesTheStrandedDeviceRelayState FAIL
     "the stranded device's relay mailbox still holds 3 item(s)"

MUTATION: purgeOutboundCustody(...) removed from runRemoteRevoke
  -> TestPBSTATE10_RevokePurgesTheMachineSideOutboundCustody FAIL
     "the machine still holds 1 undelivered outbound entr(ies) sealed under the epoch
      the revoke rotated away"
```

Both purge assertions carry a **fixture-non-vacuity fatal** first (an empty mailbox / an empty
outbox fails the test rather than passing it), so neither can pass by measuring nothing. The
relay half is asserted at the relay's own store (`Server.MailboxDepth`), the machine half at the
outbox **re-opened from disk**, so the subject is what the next gateway process reads.

`relay.Client.DeviceRevoke` previously had **no production caller anywhere in the tree** — a
capability shipped as a function nobody invokes, this project's standing "requirement satisfiable
while the defect ships" class. `purgeRelayState` is now that caller. The order in
`runRemoteRevoke` is load-bearing and documented at the function: the routing id is read
**before** the daemon revoke (which deletes the record carrying it), the gateway is stopped
**before** the relay is dialled (a second connection for the same routing id supersedes the
first), and the outbox is purged **after** the gateway is stopped.

## ADR-007 B22, and why this evidence must not repeat its argument

The `3b6694f` commit message states:

> ADR-007 B22 decides that authorizing a device clears its ban, **safe because only an
> authenticated machine can issue it and a revoked device cannot authenticate.**

**That safety argument is false, and the ADR itself now says so.** B24 records the falsification:
relay auth is **open registration** (`handleAuthInit` accepts any self-minted keypair) and
`handleAuthorizeDevice` checked only `requireAuth()` with no ownership or role check. The second
half of the argument is true and the conclusion does not follow — a revoked device could mint a
throwaway identity, authenticate as it, and authorize its **own** revoked routing id back in.
Authentication proves identity, not authority.

**The decision as it stands today, after B24 and B27:**

- `bucketRevoked` stores the **banning** routing id as its value; `authorizePair` clears a ban
  only when the pairer matches the banner (`internal/remote/relay/store.go:319-331`). Fenced in
  both directions by `TestRelay_ABanIsLiftedOnlyByTheIdentityThatPlacedIt` and
  `TestRelay_TheBanningMachineCanLiftItsOwnBan` — both green.
- The relay's authority rule is no longer `isPaired`. **B27**: *"You may act on a target's route
  if the target has authorized you — or if the target has authorized nobody at all."*
  Implemented as `store.mayActOn` (`store.go:396`), gating `mailbox_append`, `push_trigger` and
  `device_revoke` uniformly. `authorize_device` records a **directed** intent; `isPaired` became
  mutual and is no longer the gate for acting on a route.
- The bootstrap clause is load-bearing rather than a convenience: `deliverEpochGrant` authorizes
  the phone and immediately appends the sealed epoch grant, and its failure is fatal, so a mutual
  gate would refuse the append that makes a pairing usable. That circularity is what falsified
  B25's direction, measured at `TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap`.
- **B27's accepted residual, restated because it bounds this slice's claim**: the first-use
  clause is trust-on-first-use. A party that knows a never-yet-paired identity's relay-auth
  pubkey can act on it before it authorizes anyone. That pubkey is disclosed at the relay
  handshake and over the SAS-authenticated pairing channel, so the window is reachable in
  practice by the **relay operator**, to whom the threat model already concedes availability. It
  closes permanently once an identity has granted or banned anyone —
  `hasActedAsAuthority` counts bans as well as grants, deliberately, because `revokeAndPurge`
  *deletes* the authorization it severs and counting live grants alone would re-open a machine's
  bootstrap window one revoke later.

**The clearing behaviour is load-bearing for PB-STATE-10, and it was shown failing:**

```
MUTATION: authorizePair no longer clears the ban (pre-B22 behaviour)
  -> TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset FAIL after 45.70s
     "the recovered handset has reported REVOKED continuously for 45s -- past the
      post-pairing grace window, so this is the relay's settled answer"
```

## B23's two further bricks, both shown failing

B22 alone did not make the recovery complete. Two more production changes were needed, and
neither is a test artifact.

**(a) `swarm remote pair` authorizes the new device at the relay itself.** The gateway is the
only production caller of `authorize_device`, and `swarm remote revoke` **stops the gateway**. So
after a revoke nothing clears the ban until the supervisor restarts the sidecar — and
`relay.ErrRevoked` is **terminal** on the phone, so the recovered handset latches its revoked
state before the gateway boots.

```
MUTATION: authorizeAtRelay(...) removed from runRemotePair
  -> TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset FAIL after 45.76s
     (same settled-REVOKED verdict)
```

**(b) The phone re-arms a transport a revocation killed, when a pairing pins a destination.**
`connRevoked` returns from the dial loop, so the generation is over; a completed pairing is the
one event that can make that verdict stale. `rearmAfterPairing` (`mobile/app.go:299`) opens a
bounded 30-second grace window covering the genuinely unorderable race between the phone's first
post-pairing dial and the machine's authorize.

```
MUTATION: a.rearmAfterPairing() removed from the pairing completion path
  -> TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace FAIL
     "the pairing did not re-arm the phone's transport ...
      connection states seen, in order: [connecting revoked]"
```

**B23's own claim about the state sequence was corrected against measurement**, and the
correction is in the ADR (`bcd540c`, B23). The original wording said the state "stays revoked
throughout the grace window"; the re-armed generation opens with `first=true`, so the measured
sequence is `connecting -> revoked -> online`. The property that actually matters is that
**`reconnecting` never appears**, because that — not `connecting` — is the failure loop PB-APP-10
forbids. Nothing hides behind a spinner.

## The acceptance fence was racy in both directions, and only one direction is fixable there

Recorded because it changes how the green run above should be read.

`s18bAwaitOnline` originally fail-fasted the instant it observed `"revoked"`. That reads as
fail-fast discipline and is actually a race: B23(b) *deliberately holds* that state between
retries. An independent reviewer measured the old helper failing **2 runs in 10 against correct
code**, with a message that then misdiagnosed it. It now treats `revoked` as terminal only once
it has persisted past the grace window plus a margin (`s18bRevokedIsTerminalAfter = 45s`).

**The other direction is not fixable in that test and has not been fixed.** Deleting the grace
window escapes the end-to-end test **3 runs in 5**: when the machine's authorize happens to land
before the phone's first post-pairing dial, the phone never needs the grace and the recovery
genuinely succeeds without it. An end-to-end test driving both ends through their real verbs
cannot order that race — it can only sample it. The deterministic half is
`mobile/conformance/s18b_gracewindow_test.go`, which **holds** the authorize until the phone has
already been refused; it catches the deletion **5 runs in 5**, and that is the mutation recorded
above.

**So `TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset` is a sampling test for the grace
window and a deterministic test for the ban.** Both mutations that target the ban (the two above)
failed it deterministically at ~45s, which is the settled-verdict path rather than the sampled
one.

## The error class, because the brick was also expressible as a screen

`ErrClassStateCorrupt` (`swarm/state-corrupt`, sentinel `phonecore.ErrCorruptState`, state
`STATE_CORRUPT`, remedy `clear_data_and_re_pair`) was added because a corrupt durable blob was
classed `ErrClassInternal`, whose own row reads *"Never the user's fault and never has a user
action"* with remedy `report_bug`. A recoverable state routed to report-a-bug is the brick
expressed as a screen. Its remedy is its own because it is **two acts and neither alone works**:
the user clears the app's data, *and* the owner unregisters the device at the machine, or the
re-pair the advice ends in is refused by PB-KEY-3.

`TestPBSTATE10_CorruptStateFailsClosedAndNamesTheOwnerSideRecovery` asserts the class **first**,
before the message, because the class decides which screen the user ever sees; it fails on
`ErrClassInternal` and on `ErrClassUnknown` by name.

`ErrClassDeviceUnsupported` (`swarm/device-unsupported`) was accepted in the same entry, for
PB-KEY-8's two refusals. **Its wart is recorded in the ADR and holds in the code**: the Go facade
exports a class it never stamps — the condition is produced only on the Android side. That is
structural, since the taxonomy is a closed set checked for equality across the golden, the table
and the Kotlin enum.

## PB-STATE-4's amendment, which lands on this requirement's own remedy

PB-STATE-4 was **amended 2026-07-26** to exempt `RevokeThisDevice` from the reconcile
fail-closed gate. Current text, verbatim:

> **AMENDED 2026-07-26 — `RevokeThisDevice` is EXEMPT, and the exemption is stated here rather
> than left to the implementer.** S18 found that revoke now runs the reconcile gate, so **the
> phone's panic button refuses on an unreconciled phone** — which is close to the definition of a
> lost or long-disconnected handset, i.e. the exact state the button exists for. The boundary is
> not "revoke is special": this gate protects ops whose **target is selected from synchronized
> state** (`kill`, `launch`, `take_control` — the three this requirement itself enumerates),
> because stale state makes them act on the wrong object. `RevokeThisDevice` **selects no target**
> (it names its own signer, which needs no synchronized state to identify) and **only removes
> capability, never grants it** […] **Test both directions**: an unreconciled phone completes a
> revoke end to end, **and** `kill`, `launch` and `take_control` still refuse with
> `swarm/unreconciled` on that same phone — an exemption that widens to the other three is the
> failure this amendment risks.

Implemented at `mobile/commands.go:557` (`if action != schema.ActionDeviceRevoke { … }`) and
both directions are tested and green:

```
TestPBSTATE4_AnUnreconciledPhoneCompletesItsRevokeEndToEnd            PASS
TestPBSTATE4_TheRevokeExemptionDoesNotWidenToTheStateSelectedVerbs    PASS
  /Kill  /Launch  /TakeControl
```

## Gate results

```
go build ./...        clean
go vet ./...          clean
go test ./...         two known flakes, neither S18b's:
                        TestS6B_GatewayInputLatencyIsNotPollGated   (internal/remotegw)
                        TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT (mobile/conformance)
                      every other package green
golangci-lint run ./...   25 findings, none introduced by this slice
./gradlew test        COULD NOT RUN — no JDK in this environment
```

Two of the lint findings are in `internal/remote/relay/store.go` (`purgeMailbox` and `removePair`
unused). Both were orphaned at **`26a47a4`, Phase A**, well before this slice —
`git log -S` confirms it. They are pre-existing, not B27 fallout.

## Claims I could NOT substantiate

| claim | source | status |
| --- | --- | --- |
| "ADR-007 B22 decides that authorizing a device clears its ban, **safe because only an authenticated machine can issue it and a revoked device cannot authenticate**" | `3b6694f` commit message | **THE SAFETY ARGUMENT IS FALSE.** Falsified by B24 and superseded by B27. The *decision* survives; the *reason given for it in the commit message does not*, and the commit message is the only place a reader would meet it without the ADR beside it. |
| "the ban is scoped to the pair" (B26) | ADR-007 B26 | **FALSIFIED by B27** and no longer the code's rule. `revokeAndPurge` also deletes the target's mailbox and push token, both keyed per *target*; scoping the ban never scoped the purge. |
| "mutual pairing removes the whole class" (B25) | ADR-007 B25 | **FALSIFIED by B26**, measured against `TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap`. |
| `PhoneStartupRoutingTest` (7 tests, S18b's Kotlin fence over `routeStartupFailure`) | `3b6694f` | **NOT RUN.** No JDK. Read from source; asserts that no startup failure is given a remedy the user can perform that cannot help. |
| a failing-first (RED) run for S18b | GG-5 | **NOT IN THE HISTORY.** `cmd/swarm/s18b_unbrick_test.go` first appears in `3b6694f`, the same commit as the implementation. The three vacuous-pass probes documented in the file header (no-op: 6/7 fail; cosmetic-only: 3/7 pass; the fence probe) are the only failing-first record, and they are the author's own report. |
| "the recovery completes with no step requiring undocumented knowledge" | PB-STATE-10 criterion | **SUBSTANTIATED for the CLI text**, and bounded: the closure test asserts each command appears in the previous step's *output*. It does not model a user who cannot read, nor a phone screen — the phone half is `ErrCorruptState`'s message text, because the app never constructs and there is no screen. |

## Residuals

1. **PB-STATE-4's exemption question is now answered.** The S18 evidence recorded, as its
   residual 3, that whether revoke should be exempt from the reconcile gate "is not S18's to
   answer". It has been answered: PB-STATE-4 is amended, the exemption is implemented, and both
   directions are tested. That residual is corrected in place in
   `docs/verification/remote-phaseB-s18-evidence.md`.
2. **The mirror hole B24 recorded is closed by B27, not by B24.** `handleDeviceRevoke` used to
   let any authenticated routing id ban any routing id, including the machine's, and B24's
   narrowing made an attacker-placed ban *permanent* rather than self-healing. `mayActOn` now
   gates `device_revoke` too, so a ban of that shape can no longer be placed. This is stated
   because B24's own text still says the escalation is unfixed, and a reader stopping at B24
   would conclude wrongly.
3. **The grace-window half of the end-to-end recovery test is a sample, not a proof.** Deleting
   `rearmAfterPairing` escapes `TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset` 2 runs
   in 5. The deterministic guard is the conformance test, which must stay green for that claim to
   hold. A reader treating the `cmd/swarm` test alone as the fence for B23(b) would be wrong.
4. **The 45-second terminal threshold makes the S18b suite slow to fail.** Every mutation that
   breaks the ban costs ~46s per run. That is the correct trade against a helper that was wrong
   20% of the time, but it is worth knowing before someone "optimises" it back.
5. **Nothing here claims a physical-handset property.** PB-E2E-5 stays deferred: no real
   biometrics, camera, FCM delivery, Doze or hardware attestation. The Kotlin routing fence is a
   plain-JVM test over a routing table and touches no Keystore, Context or hardware — and it was
   not run here in any case.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** (ADR-007 B129). One row per
requirement, verdict `DERIVED` or `NOT DERIVED`, and for `DERIVED` the mutation that was made to fail,
in the same row.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-STATE-10 | DERIVED | The already-paired refusal stripped of its recovery guidance — `cmd/swarm/remote.go`'s `"to unregister one: swarm remote revoke <device-id>"` replaced with a bare `"pairing refused"` -> `TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatTheOperatorWasTold` fails. Reverted; `remote.go` sha256 `69ad91bca874...` identical. **The fence's subject is the right one:** it does not check that the refusal *has* text, it checks that the chain an operator can follow is **closed under what they were actually told** — so removing the one sentence that names the next command breaks it, which is exactly the bricked-owner scenario this row exists for. An earlier pass recorded this row READ-not-mutated and counted it unexamined; it is now measured. |
