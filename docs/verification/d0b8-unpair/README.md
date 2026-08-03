# agents-tracker-d0b8 / agents-tracker-2lz5 -- verification (GG-5)

`go-red.txt` and `kotlin-red.txt` are the RED runs captured before any implementation existed, at
`main` HEAD `00b2c82`. They are here because GG-5 requires the failing run to be EVIDENCED rather
than asserted, and because this defect is one two agents and a full green suite already walked
past: the code carried a comment stating the behaviour that was missing.

`go-red-round2.txt` is the second RED, captured after review widened the scope: the first version
of the fix covered one of the three ways a registration ends. `green.txt` is every subject after
the change, plus `go build`, `go vet`, `golangci-lint` and the whole Go and Kotlin suites.

## The three ways a registration ends, and what covers each

The acceptance criterion is not "Replace works". It is that **no path that ends a registration
leaves the phone unable to reach the pairing screen.**

| # | How it ends | What the phone runs | Writes `Disowned` | Test |
|---|---|---|---|---|
| 1 | `Replace this computer` on the phone | `App.PurgeKeys`, in a `finally`, whether or not the command reached the machine | `disown()`, inside the purge | `mobile/conformance` x2, `internal/phonecore` x4 |
| 2 | `swarm remote revoke <device-id>` on the machine | **nothing until its next dial** | `recordUnpaired()`, at the refused handshake | `…AnOwnerSideRevokeAlsoUnpairsThePhone`, `…SurvivesTheProcessDeathThatFollowsIt` (real `DeviceRevoke`, real relay) |
| 3 | this handset's relay-auth key destroyed (PB-KEY-6) | **nothing until its next dial** | `recordUnpaired()` | `…ADestroyedRelayAuthKeyAlsoUnpairsThePhone` |
| — | pairing again, after any of the above | `pin` | clears it, unconditionally | `…PairingAgainClearsATransportSideUnpair` (real ceremony), `…PairingAgainClearsTheUnpair` |

Path 2 is the one a real owner takes — it is what the machine-side runbook names and the only
mitigation ADR-007 B133 leaves for a lost handset — and nothing on the phone runs for it until the
relay refuses its next handshake. Paths 2 and 3 already carried `Remedy.RE_PAIR` in the shipped
error table, so before this change the app was instructing a recovery its own gate refused to allow.

## One durable fact, reached two ways, read with a live fallback

`Paired = Machine != "" && !Disowned && !transportEndsPairing(...)`.

Every path ends at the same durable coordinate. The live transport reading is kept beside it
because it cannot fail: it answers in the window before the write lands, and it answers if the
write was refused by a full disk or a read-only data directory. The durable write is what survives
the SIGKILL — a handset that observes a revoke and then comes back somewhere with no signal has
nothing to re-derive the verdict from, which is what `…SurvivesTheProcessDeathThatFollowsIt`
asserts by restarting the app **without dialling**.

Two guards keep the durable write from becoming a brick of its own:

- **PB-STATE-10's window.** Inside `rearmAfterPairing`'s grace a `revoked` verdict is explicitly
  allowed to be stale — the machine's authorize races the phone's first dial after a re-pair — so
  neither the read nor the write fires there. `TestPBSTATE10_AStaleRevokedVerdictDoesNotUnpairAFreshlyPairedPhone`
  pins the decision; `TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace` (which
  still passes) drives the race end to end.
- **Pairing clears it unconditionally.** `pin` does not ask what set the flag, so a transport-side
  unpair is ended by the same ceremony as a press-side one. Without that, a phone could complete
  the pairing and still be shown the pairing screen — a worse brick than the one being fixed.

## What is deliberately NOT done on the transport path

`PurgeKeys` is not called. It destroys both key tiers irreversibly and its trigger is the OWNER
acting on this handset (ADR-007 B133). Running it on `relay.ErrRevoked` would let the relay — a
party this design trusts with no plaintext, no ordering and no authority — destroy a user's cached
content by answering one handshake with `revoked`; on the `connRepairRequired` arm it would destroy
content over a platform fault that is not a revocation at all. The consequence is stated rather
than hidden: after an owner-side revoke the phone records the unpair and keeps its key material
until the next pairing. That is no worse than before this change, when nothing happened at all, and
closing it is a security decision of its own — filed separately, not taken here.

## What was red, and why each failure is the right one

`go-red.txt`, three suites:

- **`internal/phonecore`** -- a build failure. `State` and `stateFile` have no `Disowned`, which is
  the correct RED for a coordinate that does not exist yet.
- **`mobile/conformance`** -- a build failure. `StateSummary` has no `Paired`.
- **`android/gate`** -- three real assertion failures, each printing the defective source it read:
  the presentation gate passing `{ startup.app.stateSummary().machine }`, `PairingSurface.isPinned`
  reading the same coordinate, and the replace settle ending in a bare `render()` with nothing in
  `PhoneSurface` wired to it.

`kotlin-red.txt` is a `:app:compileDebugUnitTestKotlin` failure: `PairOnlyScreenTest` passes
`() -> Boolean` readers to a `presentationOf` that still takes `() -> String`. The fact the gate
turns on changed from a machine NAME to "is this phone usably paired", so the test's subject
changed type with it. Every assertion, its direction and its failure message survive that edit --
nothing was weakened to reach green, and the new red above is what drove the change.

## The defect, in one paragraph

`Replace this computer` deregisters the device, rotates the epoch, severs the gateway and destroys
both key tiers. It left no durable trace that the registration was over: `State.Machine` is written
once at pairing success and cleared by nothing, `persistState` writes it back, and the presentation
gate read it. So a revoked phone came back up in the four-tab scaffold with the pairing entry point
on the settings screen inside -- unpairable short of clearing the app's data. The second half
(2lz5) is that the unqualified `render()` in the replace settle binds to `SettingsSurface.render`,
so the gate was not re-asked by the press that changes its answer.

## Why the fix is a new coordinate

`State.Machine` cannot be emptied. `OpenStore` FILTERS the durable blob on it and initialises it
from the same value precisely so a store cannot write a blob it will itself discard -- that discard
is S9, and it takes the pairing, the epoch, the sealed content key, the relay cursor and the
send-seq ceilings with it on the first Android process death. It is also what every mutating verb
signs over. Deriving the gate from key material fails the other way: a phone holding no content key
is the ordinary condition of a push-woken process and of a paired phone between an epoch rotation
and the grant that fills it, so that gate would show the pairing screen to a phone that has just
paired. The fact is therefore explicit and durable: `phonecore.State.Disowned`, set by the purge
whose only trigger is revoke/unpair, re-applied to any writer that has not noticed the purge, and
cleared by the one act that makes a disowned registration current again -- pairing.
