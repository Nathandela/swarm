# R0 — two CI flakes in `internal/skeleton`, reproduced, proven by injection, fixed

Bead: `agents-tracker-hggx.1.11`. Scope: `internal/skeleton` only.

Two tests passed `-race -count=1` locally and failed twice under full-suite CI parallelism:

- `TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped`
- `TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse`

Both are RIG races: the two tests assert on outcomes that only hold when two events land inside
one wall-clock window, and neither rig made that true — it merely happened to be true on an idle
machine. No assertion was weakened, removed, or re-aimed; the assertions are exactly the ones that
failed in CI, and they now hold deterministically.

## Environment

- Repo `/Users/Nathan/Code/swarm`, branch `main`, HEAD `61711b6` ("R6: shim-owned durable hook
  spool with idempotent daemon drain") — the R6 spool landed in `internal/skeleton` at this
  commit, so both shapes were re-confirmed HERE and not only at the commits CI failed on.
- `go version go1.26.1 darwin/arm64`, 8 cores.
- CI failures used as the ground truth for the failure SHAPES:
  - run `31817220138`, job `94821692563` (`test (go test ./...)`), commit of 2026-08-14
  - run `31897572575` **attempt 1**, job `95043157293` (attempt 2 was a green re-run, which is why
    the run reads green today)

The two shapes, verbatim from those logs:

```
--- FAIL: TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped (0.19s)
    interaction_cap_test.go:75: the merged agent_message carries 4096 bytes of text; want 8192 --
      IS-DELTA-2's merge is LOSSLESS text concatenation of two increments each at §5's MaxTextBytes

--- FAIL: TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse (4.31s)
    interaction_chain_live_test.go:228: the file_change item carries turn_id ""; the turn the
      recorded prompt opened is "01M036P16WD98P9K88N2DRBPC8" (IS-ENV-1)
```

## The stress recipe (`/tmp/stress.sh`, reproduced below verbatim)

What CI does that a local `go test ./internal/skeleton/` does not: `go test ./...` runs many
packages CONCURRENTLY on a small runner, so this package's daemons, shims, gateways, relays and
phone cores contend for CPU with every other package's. Both tests are sensitive to wall-clock
spacing, so CPU starvation is the trigger. The recipe reproduces that: a constricted `GOMAXPROCS`
for the target lane, spinning CPU hogs, and one concurrent full-package lane.

```bash
#!/bin/bash
cd /Users/Nathan/Code/swarm/internal/skeleton || exit 1   # fixtures are reached by relative path
BIN=${BIN:-/tmp/skel.test}                                 # go test -race -c -o /tmp/skel.test ./internal/skeleton/
OUT=${OUT:-/tmp/stress}; N=${N:-20}; HOGS=${HOGS:-6}
RUN=${RUN:-'TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped|TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse'}
rm -rf "$OUT"; mkdir -p "$OUT"
for i in $(seq $HOGS); do (while :; do :; done) & done
HOGPIDS=$(jobs -p)
( for i in $(seq $N); do GOMAXPROCS=4 $BIN -test.timeout=20m > $OUT/lane_$i.txt 2>&1; done ) &
LANE=$!
FAIL=0
for i in $(seq $N); do
  if ! GOMAXPROCS=2 $BIN -test.run "$RUN" -test.count=1 -test.timeout=20m > $OUT/target_$i.txt 2>&1; then
    FAIL=$((FAIL+1)); echo "TARGET ITER $i FAILED:"; grep -A2 "^--- FAIL" $OUT/target_$i.txt | head -6
  fi
done
kill $HOGPIDS 2>/dev/null; kill $LANE 2>/dev/null; pkill -P $LANE 2>/dev/null
echo "target failures: $FAIL / $N"
```

Honest note on what the stress DID and DID NOT show. Pure starvation of the target lane (an
earlier profile: `GOMAXPROCS=1` + 16 hogs, 20 iterations) did not reproduce either shape — the
windows are 125ms and 250ms wide, and a scheduler stall that long is rare enough that a
20-iteration local run is not a reliable detector. An over-driven profile (four concurrent
full-package lanes) reproduced only overload artifacts (`owner Launch: protocol: request timed
out`), which are not the CI shapes. **The stress recipe is therefore a regression detector, not
the root-cause instrument. The root cause was established by injection**, below, which reproduces
each CI shape 100% of the time and is what the fix is proven against.

## Root cause 1 — the cap test's merge depended on wall-clock luck

**Where.** `internal/skeleton/interaction.go:215` (`d.items.Offer` inside `captureInteractions`'s
per-item loop) against `internal/remotegw/itemadmission.go:189`
(`if len(a.order) == 0 || now.Sub(a.last) < a.window`), with the rig's false premise stated in
the test's own header, `internal/skeleton/interaction_cap_test.go` ("the two increments that
follow are offered INSIDE one DefaultAppendWindow").

**Mechanism.** `captureInteractions` offers the batch's three items one after another. The first
(`user_message`) finds the slot free and is released immediately, stamping `a.last`. The two
`agent_message` increments then fold into one pending item — *provided* both are offered before
`now - a.last` reaches `DefaultAppendWindow` (125ms). The floor is a SPACING FLOOR, not a batching
delay (IS-DELTA-2): if a scheduler stall (or the daemon's own 125ms release ticker landing in the
gap) opens a full window between the two `Offer` calls, the first increment is released ALONE and
the second starts a fresh pending item. The test then reads the first `agent_message` in the
journal and finds 4096 bytes where the merged union has 8192 — the exact CI line.

Nothing here is a product defect: releasing a lone increment after a full idle window is precisely
what §7 specifies. The defect is that the rig asserted on a merge it never forced.

**Injection proof (before the fix).** Temporary, clearly-marked code in `captureInteractions`,
immediately before `d.items.Offer` — a deterministic stall in the exact gap CPU starvation opens:

```go
// TEMPORARY DEBUG INJECTION (r0-flake-rootcause) -- REMOVE.
if os.Getenv("SWARM_INJECT_OFFER_GAP") != "" {
    if d2, err := time.ParseDuration(os.Getenv("SWARM_INJECT_OFFER_GAP")); err == nil {
        time.Sleep(d2)
    }
}
```

```
$ SWARM_INJECT_OFFER_GAP=150ms go test -count=1 -run TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped ./internal/skeleton/
--- FAIL: TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped (2.75s)
    interaction_cap_test.go:75: the merged agent_message carries 4096 bytes of text; want 8192 ...
FAIL
```

100%, and byte-identical to the CI failure.

**Fix (rig, plus one clock seam).** The window is wall-clock, so no amount of test-side sleeping
can make "these two are inside one window" true under an adversarial scheduler; the floor's own
documented clock seam (`remotegw.ItemAdmissionConfig.Now`) can.

- `internal/skeleton/serve.go:81` — new `Config.ItemClock func() time.Time`, nil in every
  production caller (the floor then defaults to `time.Now`), threaded to the Daemon at
  `serve.go:204` and into the floor at `internal/skeleton/interaction.go:114`. It is a Config
  field rather than a field a test pokes after `Serve`, because `releaseInteractions`' ticker
  reads the floor from its own goroutine — a post-`Serve` write would be a data race, not a seam.
- `internal/skeleton/serve_test.go` — `assemble` takes optional `func(*Config)` opts.
- `internal/skeleton/interaction_cap_test.go:83` — `assembleOnPinnedFloorClock` stands the
  assembly up on a mutex-guarded `pinnedClock`; the test offers all three items at one frozen
  instant and then `clock.advance(remotegw.DefaultAppendWindow)` (line 115) opens the window. The
  release still happens on the DAEMON's production ticker calling the production `Flush`.

**Proof the fix holds with the injection still present:**

```
$ SWARM_INJECT_OFFER_GAP=150ms go test -count=1 -race -run TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped ./internal/skeleton/
ok  	github.com/Nathandela/swarm/internal/skeleton	5.079s
$ SWARM_INJECT_OFFER_GAP=400ms go test -count=1 -race -run TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped ./internal/skeleton/
ok  	github.com/Nathandela/swarm/internal/skeleton	5.638s
```

The injection was then removed.

## Root cause 2 — the live chain test treated a hook process's exit as a sync point

**Where.** `internal/skeleton/interaction_chain_live_test.go`, `replayCorpusThroughHookBinary`
(now line 248) — the rig spawns one `swarm hook` PROCESS per recorded body, serially, and its
header claimed that made the exchange ordered. The three facts that make it not ordered:

- `internal/hookclient/hookclient.go:154-175` — `Post` dials, writes, returns. There is no reply
  to wait for, so the process exits with the callback merely handed over.
- `internal/daemon/daemon.go:115` — "each connection is handed to `ConnHandler` in its own
  goroutine". Two callbacks written in order are applied by two goroutines in no order.
- At this HEAD there is a second, wider reordering seam: `hookclient.PostSmart` prefers the shim's
  hook socket (spool + `HookDrainer`'s 250ms tick, `internal/skeleton/hookdrainloop.go:34`) and
  falls back to a DIRECT daemon post whenever that socket is gone or silent. A direct post is
  applied at once and overtakes everything still sitting in the spool.

**Mechanism.** `internal/skeleton/interaction.go:310-320` (`turnIDLocked`) closes the turn on the
terminal `agent_message` — `delete(d.turnIDs, sessionID)`. The recording's `Stop` is that message.
If `Stop` is applied before the Edit's `PostToolUse`, the turn is already gone when the
`file_change` is shaped, and it reaches the phone with `turn_id ""` — the exact CI line.

**Injection proof (before the fix).** Purely test-side, and a state production really reaches: the
LAST record posts with `SWARM_SHIM_HOOK_SOCK` unset, i.e. through the daemon-socket fallback
`PostSmart` takes for a pre-R6 shim, an ack-less shim, or a mid-upgrade session — while its
predecessors are still in the spool waiting for the drain tick.

```go
// TEMPORARY DEBUG INJECTION (r0-flake-rootcause) -- REMOVE.
if os.Getenv("SWARM_INJECT_LAST_HOOK_DIRECT") != "" && i == len(fx.HookPayloads)-1 {
    e = withEnv(env, hookclient.EnvHookSocket, "")
}
```

```
$ SWARM_INJECT_LAST_HOOK_DIRECT=1 go test -count=1 -run TestClaudeLiveChainE2E ./internal/skeleton/
--- FAIL: TestClaudeLiveChainE2E_... (8.67s)
    interaction_chain_live_test.go:228: the approval_request item carries turn_id ""; the turn the recorded prompt opened is "01M0458RYCF4DMY76HA188AJ6R" (IS-ENV-1)
    interaction_chain_live_test.go:228: the tool_run item carries turn_id ""; ...
    interaction_chain_live_test.go:228: the file_change item carries turn_id ""; ...
FAIL
```

100%, and the third line is the CI failure verbatim. This also answers the bead's question: the
shape still exists at this HEAD. R6 narrowed it — the shim's ack makes a spool-carried post a
durable handover, so the pure-shim path is ordered by the rig's serial spawning — but the daemon
fallback re-opens it, and no rig should depend on which transport happened to serve.

**Fix (rig).** `awaitHookIngested` (`interaction_chain_live_test.go:280`): after each hook process
exits, wait until the DAEMON has ingested that callback before posting the next one. The
observable is the daemon's own ingest bookkeeping — `markHookSeqIngested` records a callback's
sequence only after `serveHookInteractions` has shaped and offered its items
(`internal/skeleton/hookdrain.go`), so a sequence that reads back as ingested means that record's
items already carry the turn they belong to. The sequence just consumed is read from the
daemon-injected per-session counter file (`hookclient.EnvSequenceFile`), which `nextSequence`
writes under its flock before the post. The forged post is deliberately NOT waited on: the engine
refuses it, so it is never marked, and its ordering cannot matter.

An item COUNT would not have worked as the barrier: not every record shapes an item, and the
append floor may still be holding the ones that do.

**Proof the fix holds with the injection still present:**

```
$ SWARM_INJECT_LAST_HOOK_DIRECT=1 go test -count=1 -run TestClaudeLiveChainE2E ./internal/skeleton/
ok  	github.com/Nathandela/swarm/internal/skeleton	12.017s
```

The injection was then removed.

## Gate results (2026-08-16, HEAD `61711b6` + this change, injections removed)

Stress recipe, both tests, 20 iterations under `GOMAXPROCS=2` with 6 CPU hogs and one concurrent
full-package `GOMAXPROCS=4` lane:

```
target failures: 0 / 20
```

The concurrent full-package lane that ran to completion under that same load also passed
(`PASS`, `/tmp/stress/lane_1.txt`).

Repository gates:

| gate | result |
| --- | --- |
| `go build ./...` | OK |
| `go vet ./...` | OK for every package this change touches. The command fails ONLY in `internal/remotegw`, on another lane's untracked TDD-red files (`r3pl_obl9_backoff_test.go`: `undefined: WakeRetryScheduler`). `go vet $(go list ./... \| grep -v 'internal/remotegw$')` is clean. |
| `golangci-lint run` | Same: clean everywhere except that same other-lane red package. `golangci-lint run ./internal/skeleton/...` reports nothing. |
| `go test -race -count=1 ./internal/skeleton/` | `ok  github.com/Nathandela/swarm/internal/skeleton  248.141s` |

## Related finding, NOT fixed here (out of the bead's scope)

`TestI1_TheScreensBytesAreTheFacadesBytes` failed in the SAME CI job (run `31817220138`) with a
third symptom of root cause 1's wall-clock window: its checked-in golden pins the ORDER items were
released in, and that order depends on whether the `approval_request` and the Read `tool_run` were
in the admission queue at the same instant (IS-DELTA-3 puts an approval at the head of the queue,
so it jumps a `tool_run` only if both are pending together). Pinned golden: `user_message`,
`approval_request`, `tool_run`. Recorded under starvation: `user_message`, `tool_run`,
`approval_request`.

The same `Config.ItemClock` seam fixes it, but the fix changes the golden's item order (with the
clock pinned, everything is offered inside one window and the approval leads), and that golden is
staged onto the Android Robolectric classpath by `android/app/build.gradle.kts` — so it is a
cross-surface change that needs its own slice. Filed as `agents-tracker-9ewk`.

## What was NOT changed

- No assertion was weakened, deleted, or re-aimed. Both failing assertions are untouched and now
  hold deterministically.
- No product behaviour changed. The only production edits are the nil-defaulted `Config.ItemClock`
  seam and its two wiring lines; with `ItemClock` nil (every production caller) the floor
  constructs exactly as before.
- The daemon's tolerance of REORDERED hook callbacks is a real product property (a late record
  whose turn already closed carries no `turn_id`). It is documented behaviour of the turn model
  (IS-ENV-1) and changing it is a spec decision needing an ADR, not a flake fix. Recorded here as
  a known consequence of a mixed-transport session, not as something this work altered.

---

# 2026-08-16 — B69 in `internal/remote/pairing`: the rig read a fact that was true but not yet published

CI run `31966331009` failed `TestB69_TheDeadlineCannotCancelTheCommitOnceAcceptanceIsOnTheWire`
on a DOCS-ONLY commit, so nothing in the product could have caused it. The failure was
self-contradictory on its face:

```
b69_accept_commit_test.go: the acceptance frame was never forwarded, so the ordering under
                           test never happened
```

That check sits AFTER the phone-pinned assertion, which passed. The phone can only pin on an
acceptance it received, so the acceptance HAD crossed — and the rig that forwarded it said it
never did.

## Root cause — `internal/remote/pairing/b69_accept_commit_test.go:83` (`stalledAcceptRendezvous.Send`, pre-fix)

Pure rig, no product involvement. The wrapper delivered the frame and only afterwards recorded
that it had:

```go
if err := s.fakeRendezvous.Send(ctx, msg); err != nil { return err }
if n == machineAcceptanceSend {
    s.mu.Lock(); s.forwarded = true; s.mu.Unlock()   // <-- published HERE
    <-ctx.Done()
}
```

`fakeRendezvous.Send` (`harness_test.go:114`) hands the frame to a 16-deep buffered channel. The
instant that channel send returns, the phone goroutine is runnable with the acceptance in hand,
and the whole rest of the phone leg — decrypt, `sendAck`, pin, build `DeviceOutcome` — is
in-memory microseconds. So there is a window in which:

1. the machine goroutine has enqueued the frame but has NOT yet reached `s.forwarded = true`;
2. the phone runs to completion and sends its outcome;
3. the main goroutine receives that outcome, asserts "the phone pinned" (green), and samples
   `forwarded` — still false.

The bool was never wrong, only unpublished. Any preemption of the machine goroutine in those few
instructions produces exactly the CI message, and CI starvation (loaded runner, `-race`, packages
in parallel) supplies preemptions at arbitrary instructions.

## The other hypothesis, checked and EXCLUDED

The R3/ADR-015 waves changed `pairing.go` around msg4 (push-binding conveyance, `RevokePushBinding`),
so `machineAcceptanceSend = 2` could in principle have gone stale, pinning the stall to the wrong
frame. It has not: on the accept path `Machine.Pair` writes exactly two frames — msg2
(`internal/remote/pairing/pairing.go:531`) and the decision (`pairing.go:661` via `sendDecision`,
`pairing.go:867`). msg4 and the ack are RECEIVES on this end (`recvConsent`, `recvAck`); the
push-binding work rides inside msg4 and adds no machine send. The ordinal is still correct.

It is now ASSERTED rather than assumed (see the fix), because a future machine-side frame would
move the stall to the wrong write and this test would silently stop testing what it names.

The starvation hypothesis on `acceptStallWindow` (2 s) was also excluded: if that deadline expired
before the acceptance send, the machine would fail closed with no acceptance and the phone would
park forever on `context.Background()`, failing at `legBudget` with "the phone leg never resolved"
— a different, loud message, not the one CI reported.

## Proof by injection

The window is a handful of instructions, so it was widened deterministically — a `time.Sleep(50ms)`
between the underlying `Send` and the flag write, standing in for the machine goroutine losing its
P at that point. Injection present, pre-fix rig:

```
GOMAXPROCS=1 go test -race -count=10 -run TestB69_... ./internal/remote/pairing/
--- FAIL: ... (0.03s) b69_accept_commit_test.go:170: the acceptance frame was never
    forwarded, so the ordering under test never happened      [10 of 10 runs]
```

10/10 red, with the phone-pinned assertion passing first every time — the CI symptom exactly,
including its self-contradiction.

Baseline for contrast: the same test, same starvation, WITHOUT the injection ran
`-count=200` green (`ok ... 404.815s`). The window is real but narrow, which is why it only ever
appeared on a loaded runner.

## The fix — synchronisation only, no assertion touched

`forwarded` became a signal instead of a sampled flag: a `chan struct{}` closed on the acceptance
send, and the test WAITS for it (bounded by the existing `legBudget`) instead of reading it at
whatever instant the phone happened to finish.

The assertion is unchanged in strength and in message. Nothing closes that channel but a delivered
acceptance, so a frame that genuinely never forwards still fails — it just fails after the bound
rather than before the fact was published.

Added alongside it, a guard on the excluded hypothesis: after `Machine.Pair` returns, the test
asserts the machine wrote exactly `machineAcceptanceSend` frames. If a later wave adds a
machine-side frame, this fails with an instruction to re-derive the ordinal, instead of stalling
some other write and quietly testing nothing.

## Proof the fix closes it

Same injection, same starvation, fixed rig:

```
GOMAXPROCS=1 go test -race -count=10 -run TestB69_... ./internal/remote/pairing/
ok  github.com/Nathandela/swarm/internal/remote/pairing  21.892s      [10 of 10 runs]
```

10/10 red before, 10/10 green after, under the identical injection.

Non-vacuity of the new wait, proven by a second injection (`machineAcceptanceSend` temporarily set
to `3`, so the stall marker never fires and the acceptance is never marked forwarded):

```
--- FAIL: TestB69_... (20.03s) b69_accept_commit_test.go:187: the acceptance frame was never
    forwarded, so the ordering under test never happened
```

The wait still fails on a genuinely unforwarded frame, with the same message. Both injections were
removed; the file contains no `TEMPORARY` markers and no sleeps.

## Gate results (2026-08-16, HEAD `c753bcb` + this change, injections removed)

| gate | result |
| --- | --- |
| `go build ./...` | OK |
| `go vet ./internal/remote/pairing/` | OK |
| `golangci-lint run ./internal/remote/pairing/...` | `0 issues.` |
| `go test -race -count=1 ./internal/remote/pairing/` | `ok  github.com/Nathandela/swarm/internal/remote/pairing  21.195s` |
| `GOMAXPROCS=1 go test -race -count=25 -run TestB69_...` | `ok ... 51.888s` — 25/25 green |
| same, with 8 spinning CPU hogs alongside | `ok ... 52.116s` — 25/25 green |

## Clean stress

Post-fix, injections removed:

- `GOMAXPROCS=1 go test -race -count=25 -run TestB69_...` — 25/25 green (`ok ... 51.888s`).
- The same 25 runs again with eight spinning CPU hogs alongside, to reproduce the runner
  contention that made the window observable in the first place — 25/25 green (`ok ... 52.116s`).
- Pre-fix baseline for the record: `-count=200` under `GOMAXPROCS=1` was green
  (`ok ... 404.815s`), which is why this only ever showed up on CI and never locally.

## What was NOT changed

- No assertion was weakened, deleted, or re-aimed. The forwarded check has the same meaning and
  the same failure text; it is only synchronised with the goroutine that establishes the fact.
- No production code was touched. The whole diff is `b69_accept_commit_test.go`.
- `acceptStallWindow` was left at 2 s. It is the deadline production hands `Machine.Pair`, and the
  test's value comes from handing production its own shape (the point the header makes against
  b52's vacuous 2 s). Its starvation failure mode is loud and distinct, so widening it would buy
  nothing and cost the test's fidelity.
