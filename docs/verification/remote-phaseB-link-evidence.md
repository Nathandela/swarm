# The link section: green evidence (agents-tracker-ah2)

Pairs with `docs/verification/remote-phaseB-s25-red/link-red.txt`, which is the failing-first run.
Named for its subject rather than for S25 because the slice's other screens (machines, activity)
are a second agent's and `remote-phaseB-s24-evidence.md` already carries theirs; a file claiming
the whole slice would be claiming work this one did not do.

## What was unmet

`ClockBanner` and `StreamView`/`StreamBadge` were fully modelled, unit-tested in
`ConnectionAndErrorTest`, reached by `FacadeBridge` — and drawn by nothing.
`FacadeBridge.clockBanner` and `FacadeBridge.streamView` had no caller outside the adapter and
both were ledgered in `android/unbound-verbs.tsv` as models waiting for a screen.

**One correction to the bead, which named three models.** `FacadeBridge.machineFreshness` was
never orphaned: it has a production caller at `PhoneSurface.kt:790`, on the inbox status line
beside `connectionBanner().text`, and reaches the machines screen a second way through
`MachinePane.presenceExplanation`. It is correspondingly absent from the unbound ledger. The
orphans were two.

## GREEN — the Android suite

    ./gradlew --no-daemon test --rerun-tasks --no-build-cache

    BUILD SUCCESSFUL in 5m 31s
    61 actionable tasks: 61 executed

Executed, not up-to-date. Both unit-test variants ran, read from
`app/build/test-results/*/TEST-*.xml` of that run (timestamp `2026-08-02T10:44:13.592Z`) rather
than retyped from the console:

| variant | classes | tests | failures | errors | skipped |
|---|---|---|---|---|---|
| `testDebugUnitTest` | 79 | 624 | 0 | 0 | 0 |
| `testReleaseUnitTest` | 79 | 624 | 0 | 0 | 0 |

The nineteen assertions this work added, in both variants:

| class | variant | tests | failures | errors |
|---|---|---|---|---|
| `LinkPanelTest` | debug / release | 10 / 10 | 0 / 0 | 0 / 0 |
| `LinkPanelViewTest` | debug / release | 9 / 9 | 0 / 0 | 0 / 0 |

**No Kotlin changed after that run.** Everything committed since is Go gate source and
documentation; `git diff <the green commit> HEAD -- '*.kt'` reports only `push/PushTokens.kt`,
which is another agent's commit.

## GREEN — the gates

    go test ./android/gate/ -count=1
    ok  	github.com/Nathandela/swarm/android/gate	4.905s

    go vet ./...        clean

`golangci-lint` is not installed on this machine and was not run.

## The three checks this added, each shown to be able to fail

A check that has only ever passed is a check nobody has measured. Each was mutated against the
real tree, not only against synthetic input.

### 1. The four repair channels are the four the core repairs

`android/gate/pbapp8_repairchannels_test.go`. The fail-open it closes: **`App.StreamState` does
not validate its argument.** It falls through `streamStale` to a stale map that was never given
the key and answers `"live"` — so a mistyped channel renders healthy forever over a stream nobody
is watching. Its sibling `App.Resync` validates the same four names and fails closed, in its own
words: *"a caller that mistyped one of the four would see exactly what a working resync looks
like."* That argument is stronger for the read than for the write, because the read is what a
screen draws.

Kotlin cannot see `internal/phonecore.StreamJournal` and the Go toolchain cannot see the Kotlin
list, so the two spellings are joined here — the shape `android/gate/pairingstates_test.go` uses
for `PairingStep`, and for the same reason.

Dropping `"grant"` from `FacadeBridge.REPAIR_CHANNELS`:

```
--- FAIL: TestPBAPP8_TheChannelsTheScreenAsksAboutAreTheChannelsTheCoreRepairs (0.02s)
    pbapp8_repairchannels_test.go:155: PB-SYNC-1: the core marks and repairs the "grant" channel and the app never asks about it.
        PB-APP-8's whole discipline is that staleness belongs to ONE stream and never to the handset, so a channel nobody asks about is a hole the user is not told of while the three beside it say they are fine.
        The app's: journal, reply, terminal
```

Adding a name the core does not repair:

```
--- FAIL: TestPBAPP8_TheChannelsTheScreenAsksAboutAreTheChannelsTheCoreRepairs (0.02s)
    pbapp8_repairchannels_test.go:146: PB-APP-8: the app asks `App.StreamState("reconcile")` and the core repairs no such channel.
        StreamState does not validate its argument -- it reads a map key that was never set and answers "live" -- so this channel renders healthy on every draw and can never render anything else.
        The core's four: grant, journal, reply, terminal
```

Both mutations were made in `FacadeBridge.kt` and reverted; the unmutated control is `ok`.

**What was NOT mutated, and why.** Removing a constant from `internal/phonecore/snapshot.go` would
exercise the same comparison from the other end, but that file is in a shared tree with agents
actively committing to `mobile/` and `internal/phonecore/` — two builds failed mid-measurement on
their in-progress edits while this was being recorded. A core-side removal trips the
`repairChannelCount` floor (`Fatalf`) before the set diff runs, which is a different message from
the two above. Mutation 2 covers the same direction of the comparison from the safe side.

### 2. The section is reached from the app

`TestPBDS9_TheLinkSectionIsReachedFromTheApp` in `android/gate/s24_screens_test.go`. A section
built *because* two models were reachable by nothing, then left reachable by nothing, is the same
bug with more files in it — and every check that would notice sits on the wrong side of a seam:
`LinkPanelViewTest` builds the view itself, and the runtime half is out of reach because
`PhoneRuntime.phone()` answers Unavailable on every JVM run, where `drawMachines` gets a null
bridge and draws only the unavailable sentence.

Pointed at a factory name nothing calls:

```
--- FAIL: TestPBDS9_TheLinkSectionIsReachedFromTheApp (0.03s)
    s24_screens_test.go:1316: PB-DS-9: no production Kotlin outside dev/swarm/phone/ui/screens calls `linkPanelViewXX`, so PB-TIME-1's clock verdict and PB-APP-8's four channel verdicts are drawn by nothing again, which is the exact defect this section was written to close.
```

The comparison is shared with `TestPBAPP3_TheSessionDetailIsReachedFromTheApp` rather than copied.

### 3. A ledger row cannot outlive the verb's unboundness

`TestBoundVerbs_TheLedgerCannotExcuseASymbolTheAppNowReaches` in
`android/gate/boundverbledger_test.go`. The existing rot check asks whether a ledgered symbol
still *exists*; nothing asked whether it is still *unbound*. Once a verb acquires a caller its row
goes on excusing it, every other check in that file keeps treating it as exempt, and the symbol
quietly leaves the control that would notice if the caller went away again.

Two things depended on that never happening and on nobody forgetting. PB-DS-9's exit criterion is
literally *"`android/unbound-verbs.tsv` shrinks by the verbs the screens now reach"*. And a row's
REASON is prose that expires: `App.Resync`'s said *"the stale/repairing screen ... does not
exist"* until this section landed.

Restoring the `FacadeBridge.clockBanner` row this work deleted:

```
--- FAIL: TestBoundVerbs_TheLedgerCannotExcuseASymbolTheAppNowReaches (0.10s)
    boundverbledger_test.go:700: android/unbound-verbs.tsv:59 excuses FacadeBridge.clockBanner as deliberately unbound, and production Kotlin now calls it.
```

## What is not covered

- **`StreamBadge.RESYNCING` is unreachable in the shipping app.** Nothing in production sets
  `resyncAsked`, because `App.Resync` is still unbound — so `App.ResyncPending` is always false.
  `LinkPanel` models the state and `LinkPanelTest` asserts it, and no user can currently produce
  one. The `App.Resync` ledger row carries this fact while it stands, and check 3 above is what
  fails the day someone wires the verb and leaves the claim standing.
  Tracked as `agents-tracker-upbo`.
- **PB-TIME-1's clock verdict renders on the Machines destination only.** The Inbox — the
  destination the app opens on, and the one a user sits on while nothing arrives — says nothing
  about the clock. `PhoneSurface` hosts the status line inside `unrecomposedControls`, which is
  passed as `below` only by `drawInbox`, so `ConnectionBanner` and `MachineFreshness` are already
  Inbox-only and the clock verdict is now stranded on the opposite tab from its two siblings.
  The argument for moving it, and the two options, are in `agents-tracker-fch5`.
- **`MachinesPanel` itself is still unrendered** (`agents-tracker-xtj`). This work added a section
  to the destination, not the screen.
- **A call site is not reachability.** Check 2 is satisfied by a `linkPanelView(...)` inside a
  function nothing invokes — the same limit `boundverbledger_test.go` records about its own name
  matching. No runtime test reaches the section, for the JVM reason given above.
- **PB-DS-9's verdict is left NOT MET.** This closes the requirement's orphaned-model clause; its
  exit criteria turn on a per-screen composition claim across eight screens that this work did not
  audit.
