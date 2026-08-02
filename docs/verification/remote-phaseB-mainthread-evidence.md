# agents-tracker-7j4b: action controls no longer run Go network I/O on the main thread

Evidence for the fix and its fence. Captured 2026-08-02.

## The defect, verified rather than taken from the ticket

`PhoneSurface.invoke` executed the facade verb inside the click listener. Two facts about
`mobile/commands.go` make that an ANR rather than a latency bug:

- `sendContext` (commands.go:513) is `resolveSend(a.awaitConn)`, and `awaitConn` (relay.go:149)
  polls for up to five seconds so a command issued right after `Start` is not refused by a race
  the caller cannot see.
- `liveSendContext` (commands.go:531) is `resolveSend(a.conn)` and deliberately does not wait --
  ADR-007 D7, so a keystroke fails immediately and is recorded undelivered.

`sendContext` has exactly two callers: `sealSignedCommand` (commands.go:657) and
`unsignedCommandAt` (commands.go:780).

One correction to the ticket: the file is
`android/app/src/main/kotlin/dev/swarm/phone/PhoneSurface.kt`, not `.../phone/ui/PhoneSurface.kt`.

## Which verbs moved, and which did not

| Control | Verb | Plane | Why |
| --- | --- | --- | --- |
| Take control | `App.TakeControl` | COMMAND | reaches `sendContext` |
| Kill | `App.Kill` | COMMAND | reaches `sendContext` |
| Launch a session | `App.Launch` | COMMAND | reaches `sendContext` |
| Revoke this device | `App.RevokeThisDevice` | COMMAND | reaches `sendContext`; also drops the push token |
| Stop (lease held) | `App.Interrupt` | LIVE | `Interrupt` -> `SendInput` -> `liveSendContext` |
| Stop (no lease) | `App.TakeControl` | COMMAND | the same control, the other arm |
| Send line | `App.SendInput` | LIVE | `liveSendContext` |

The live verbs moved onto their own lane rather than staying on the looper. `liveSendContext`
never waits for a connection, which is D7's requirement and is untouched here; but the
`MailboxAppend` it then performs is a relay round trip, and a round trip on the drawing thread is
still a freeze. A **separate serial lane** preserves both properties that matter: keystroke order,
and the guarantee that a keystroke never waits behind a command sitting in `awaitConn`. A single
shared background thread would have reintroduced exactly the queue D7 forbids, one layer above
where D7 can see it.

Not affected, checked rather than assumed:

- `App.Start` spawns `a.run(ctx)` on a goroutine and returns (app.go:266-284). It does not block.
- `App.SubscribeJournal` / `UnsubscribeJournal` are flag writes under a mutex (app.go:863-885).
- `App.BeginPairing` does not dial; its own comment records that `join()` is the only thing that
  does.

## Found and NOT fixed here: agents-tracker-jx1x

`PhoneSurface.watch` / `unwatch` call `App.TerminalWatch` / `TerminalUnwatch`, which reach
`awaitConn` through `unsignedCommandAt`. They are called from `renderReady` and from `release`,
on the main thread, with no tap involved -- and `render()` runs on every journal event. Filed as
`agents-tracker-jx1x`, blocking `p12` and `gih`, because `release`'s unwatch has a documented
ordering requirement (it must precede the socket close) and `watch`'s `watching` latch is a state
machine no test in this repository can execute. `android/gate/s25_mainthread_test.go` carries the
two functions as a named, citing ledger that is asserted not to grow.

## RED (GG-5)

Kotlin cannot fail on an absent class without failing to compile, so `VerbDispatch` was first
written in the shape of TODAY's behaviour -- the inline call hoisted verbatim out of
`PhoneSurface.invoke`, running the verb and its settle on the pressing thread, with no lanes, no
in-flight mark and no detach. The suite then fails on absent behaviour, not on absent symbols.

```
$ ./gradlew --no-daemon :app:testDebugUnitTest --tests 'dev.swarm.phone.VerbDispatchTest'
14 tests completed, 8 failed

a_command_verb_does_not_run_on_the_thread_that_pressed_the_control
  the command verb ran on the thread that pressed the control, which on a handset is the main
  thread. awaitConn polls for up to five seconds and then appends to the relay, so that thread
  is the ANR
an_input_verb_does_not_run_on_the_thread_that_pressed_the_control
  the input verb ran on the thread that pressed the control
the_verbs_answer_settles_on_the_main_executor_and_not_on_the_lane
  the verb ran on the pressing thread expected:<0> but was:<1>
the_command_plane_and_the_live_plane_are_not_the_same_lane
  the command did not take the command lane expected:<1> but was:<0>
a_second_press_while_the_first_is_still_crossing_is_refused
  the second tap issued a second verb. The control was responsive and looked pressable, so the
  user pressed it again expected:<1> but was:<2>
a_refused_second_press_settles_nothing
  the refused press produced an answer of its own expected:<1> but was:<2>
the_pressed_control_is_disabled_until_its_answer_lands
  the control still looks pressable while its verb is in flight
an_answer_arriving_after_the_screen_released_settles_nothing
  the answer settled into a surface that had already released its views expected:<0> but was:<1>
```

## GREEN

```
$ ./gradlew --no-daemon test --rerun-tasks --no-build-cache
BUILD SUCCESSFUL in 5m 49s
61 actionable tasks: 61 executed

testDebugUnitTest:   tests=654 failures=0
testReleaseUnitTest: tests=654 failures=0
total across both variants: 1308 tests, 0 failures, 0 errors, 0 skipped
```

640 before this change, 654 after: the 14 added are `VerbDispatchTest`. No existing test was
modified or weakened.

```
$ go build ./...      exit 0
$ go vet ./...        exit 0
$ golangci-lint run   exit 0
$ go test ./android/gate/   ok  4.788s
```

Note: the pinned JDK moved to 21 under this work (the concurrent targetSdk 36 bump; Robolectric
requires it). `. ./toolchain.env` resolves it; a hardcoded `JAVA_HOME` pointing at 17 now fails
every Robolectric class with "Android SDK 36 requires Java 21".

## The fence, proven by mutation

`android/gate/s25_mainthread_test.go`. The waiting/live verb sets are DERIVED from `mobile/*.go`
by a `go/ast` walk that follows method VALUES as well as calls -- `sendContext` writes
`a.resolveSend(a.awaitConn)` and never `a.awaitConn()`, so a walker that recorded only call
expressions would find no edge and report that nothing waits. Each rule was broken in the real
source, the failure captured, and the tree restored byte-for-byte (`git diff` clean afterwards).

| Mutation | Caught by | Message |
| --- | --- | --- |
| `app.kill(session)` added inside a click listener | `TestS25_NoFacadeVerbRunsInsideAClickListener` | `a click listener calls the facade verb "kill" directly` |
| `kill`'s Press flipped to `SendPlane.LIVE` | `TestS25_EveryPressDeclaresThePlaneItsVerbResolvesThrough` | `"kill" resolves through sendContext ... but its Press is declared SendPlane.LIVE` |
| `sendInput`'s Press flipped to `SendPlane.COMMAND` | same | `"sendInput" is a LIVE-ONLY verb (ADR-007 D7) but its Press is declared SendPlane.COMMAND` |
| `app.takeControl` called from `render()` | `TestS25_EveryWaitingVerbIsDispatchedOrLedgered` | `"render" calls [takeControl] outside any Press` |
| the `watch` ledger row deleted | same | `"watch" calls [terminalWatch] outside any Press` |
| a ledger row added for a function that reaches no waiting verb | same | `"drawInbox" is ledgered ... and no longer does. Delete the row` |

Two bugs in the gate were themselves found by running it rather than by asserting it worked:
`Press(` matched inside `confirmThenPress(` and `dispatchPress(` (four phantom violations), and
the first function-body mapper could not see `watch`/`unwatch` at all. Both are recorded in the
file where they were fixed.

## What this does NOT prove

Stated plainly, because the gap is structural and no amount of further unit testing closes it.

`PhoneRuntime.phone()` answers `Unavailable` on every JVM run. `invoke`'s `Ready` branch -- the
only branch on which a verb executes -- is therefore unreachable in the unit suite. **No test in
this repository has ever rendered a screen with data on it, and none can press one of these
controls and have a verb run.**

So:

- `VerbDispatchTest` proves what `VerbDispatch` does, driven directly with real executors and a
  real `View`. It does not prove that `PhoneSurface` calls it.
- `s25_mainthread_test.go` proves, as source text, that every waiting verb on that surface sits
  inside a dispatched `Press` and that no listener reaches a verb. It reads source; it executes
  nothing.
- The JOIN between the two -- that a real tap on a real handset reaches the lane -- is asserted
  by neither, and is owed to `agents-tracker-p12`, the hardware run.
- Nothing here measures an actual ANR, or the absence of one. The claim is that the verb no
  longer runs on the looper, not that the app has been observed staying responsive.
- The in-flight affordance is the honest minimum, not a designed state:
  `docs/design/substrate-components.md` has no pending/in-flight row among its 25, so a pressed
  control is DISABLED (row 24's pair, which the kit already paints) and the outcome line is
  cleared. In-flight and succeeded therefore look the same on the outcome line; only the control's
  own dimming distinguishes them. A real in-flight state is a design gap, not something this
  slice invented copy for.
