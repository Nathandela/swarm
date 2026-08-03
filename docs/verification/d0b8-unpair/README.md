# agents-tracker-d0b8 / agents-tracker-2lz5 -- verification (GG-5)

`go-red.txt` and `kotlin-red.txt` are the RED runs captured before any implementation existed, at
`main` HEAD `00b2c82`. They are here because GG-5 requires the failing run to be EVIDENCED rather
than asserted, and because this defect is one two agents and a full green suite already walked
past: the code carried a comment stating the behaviour that was missing.

`green.txt` is the same subjects after the change, plus `go build`, `go vet`, `golangci-lint` and
the whole Go and Kotlin suites.

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
