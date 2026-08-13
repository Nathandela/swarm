# Mirror program -- wave M0 evidence

Wave M0 of `docs/specifications/mirror-program.md` ("Ground truth", bead
`agents-tracker-dwwv.1`). Each item below records the question it settled, the test that
settled it, the verbatim result, the outcome taken, and the tracker actions that followed.
Later agents APPEND their section (`## M0.2 ...`, `## M0.3 ...`) below this one; do not
rewrite an earlier section, and keep the run output verbatim.

| # | Item | Bead | Status |
|---|---|---|---|
| M0.1 | Co-presence: owner attach + phone `take_control` at once -- both streams live? | `dwwv.1.1` | settled, outcome A |
| M0.2 | Render the running state on tool cards | `dwwv.1.2` | settled -- running now renders (tag + mono-line word), static; the card itself (glyph, colour, expand) stays M2.2's |
| M0.3 | Unknown gateway action yields a sealed refusal, never a hang | `dwwv.1.3` | settled, guarantee held (pin); on the phone the approve press latches no operation id so the refusal is never claimed -- mechanism ships, rendering open, deferred to M1 |
| M0.4 | Hygiene (done at filing) | -- | done |

---

## M0.1 -- Co-presence: does an owner attach evict the phone?

### The question

Two sources in the repo asserted opposite things about one session held by an owner-tier
TUI attach and a remote-tier phone `take_control` at the same time:

- `internal/skeleton/sessiontap_test.go:4-8` -- the shared per-session tap fans live frames
  to two consumers and "neither supersedes the other".
- `internal/tui/attach.go:65-70` (and commit `6ac05db`'s message) -- a TUI attach
  "unconditionally evicts the phone", because the attach and a phone `take_control` "contend
  for the SAME single shim subscriber slot".

The tap unit test could not settle it: it drives the tap through its injectable dial seam,
BELOW the two protocol Servers, and the eviction claim is about what those two Servers do to
each other. So the answer had to be produced at the assembled wiring, not read off either
comment.

### The test

`TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive` --
`internal/skeleton/copresence_test.go` (the program spec calls it
`...OwnerAttachAndRemoteLease...`; the bead's name is used, the subject is identical).

It runs at the highest production-faithful layer reachable without a handset:

- the REAL assembly (`assembleWithRemote`), i.e. `skeleton.Serve` standing up ONE `coreAPI`
  behind TWO protocol Servers exactly as `internal/skeleton/serve.go:283-312` does -- the
  owner-tier `d.srv` on the main UDS and the remote-tier `d.remoteSrv` on `remote.sock`;
- a REAL shim session (`launchFake`) running the fakeagent script
  `print READY / ask Q1? / ask Q2? / idle 60s`, so the test -- not a clock -- decides when
  new output is produced and from which surface the keystroke that produced it came. Each
  answered `ask` prints `got: <line>`, a string the session cannot emit on its own;
- a REAL paired device (`registerPhone(t, sk, device.CapFull)`) and a REAL
  `phonecore.SignCommand` take_control with the one-shot gate token bound into the Ed25519
  signature via `content_hash = SHA256(GateToken)` -- `signedTakeControl` / `sendTakeControl`
  reused verbatim from `internal/skeleton/takecontrol_gatetoken_test.go`. No crypto shortcut,
  no stubbed authorizer: the daemon verifies the signature before any lease opens;
- a REAL `protocol.Client.Attach` on the owner socket -- the same call
  `cmd`'s attachDialer makes.

Three surfaces are established per subtest, because the phone holds TWO things a single
subscriber slot would have to fight over: its `terminal_subscribe` peek (its ONLY live
terminal view -- the remote-tier pump suppresses raw output by design,
`internal/protocol/server.go:723`) and its `take_control` control lease. So the test asserts
both survivals, plus the owner's own:

1. `RemoteFirstThenOwnerAttach` -- phone peek, phone lease, then the owner attach. Output is
   then produced by the OWNER (`att.Input("alpha\n")`) and the PHONE's peek must show
   `got: alpha`; then output is produced by the PHONE (`wire.TDataIn "beta\n"` on the
   take_control connection) and the OWNER's attachment must show `got: beta` -- which also
   proves the phone's lease still reaches the PTY after the owner attached.
2. `OwnerFirstThenRemoteTakeControl` -- the mirror image: owner attach, then phone peek and
   phone lease; the phone types `gamma`, the OWNER's live stream must carry `got: gamma`, and
   the owner can still type (`delta`) with the phone's lease live.

Two controls ship with it, so a green run is a finding and not a blind spot:

- **Vacuity control** -- `awaitPeek` is asked for `got: never-typed`, a string the session
  never emits, and must NOT find it. This pins that the helper can return false while the
  stream is alive.
- **Negative control** -- a SECOND owner attach is opened on the same session, and the FIRST
  owner attachment's frame channel must CLOSE within 10s. Eviction IS real within one tier
  (`internal/protocol/server.go` `attach`, phase 2 tears down the prior controller on that
  Server's own lease map), so this proves the harness observes an eviction when one happens
  -- it is exactly the signal the cross-tier assertions looked for and did not find.

### The result

FIRST RUN WAS GREEN. Under this project's TDD rule that is itself the finding: no production
change was needed, the test ships as a PIN on behavior that already held, and the run below
is quoted in place of a RED run.

```
$ go test ./internal/skeleton/ -run 'TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive' -v -count=1 -timeout 300s
=== RUN   TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive
=== RUN   TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive/RemoteFirstThenOwnerAttach
2026/08/13 10:34:58 skeleton: grid-tap snapshot failed for session 6r7sriz44uz6me6z (1 total): dial unix /tmp/swsk-rgw2902863623/6r7sriz44uz6me6z/shim.sock: connect: no such file or directory
=== RUN   TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive/OwnerFirstThenRemoteTakeControl
--- PASS: TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive (5.49s)
    --- PASS: TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive/RemoteFirstThenOwnerAttach (5.28s)
    --- PASS: TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive/OwnerFirstThenRemoteTakeControl (0.21s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	6.519s
```

(The `grid-tap snapshot failed` line is the roster grid poller racing the shim socket's
teardown at cleanup; it is pre-existing noise on this harness, not an assertion.)

Stability, since a co-presence claim resting on one green run would be worth little:

```
$ go test ./internal/skeleton/ -race -run 'TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive' -count=3 -timeout 600s
ok  	github.com/Nathandela/swarm/internal/skeleton	12.514s
```

### The answer, and why

**OUTCOME A: both live streams survive, in both orders. There is no cross-tier eviction.**
The `sessiontap_test.go` claim was correct; the `internal/tui/attach.go` comment was stale.

The mechanism, confirmed by the code the test exercises:

- production runs TWO protocol Servers over ONE `coreAPI`
  (`internal/skeleton/serve.go:283-312`: owner `d.srv`, remote `d.remoteSrv`). Each Server
  keeps its OWN `s.leases` map, so `Server.attach`'s supersede
  (`internal/protocol/server.go`, phase 2: tear down the prior controller, close its stream)
  can only ever reach a controller on the SAME Server;
- `coreAPI.Attach` (`internal/skeleton/api.go`) does not dial the shim -- it calls
  `a.tap.subscribe(id, readWrite)`. `coreAPI.TerminalTap` calls `a.tap.subscribe(id, readOnly)`
  for the peek. The tap dials the shim on its FIRST subscriber and refcounts thereafter;
- so `hub.attach` (`internal/shim/server.go`), the true single-subscriber constraint, is
  reached ONCE per session -- by the tap -- and every later attach (owner attach, remote
  take_control, remote peek) fans off that one upstream. There is no slot to contend for.

Eviction is real only WITHIN a tier: a second owner attach supersedes the first, which is
what the negative control demonstrates.

### Outcome taken

1. **Comment corrected** -- `internal/tui/attach.go` (the `tookOver` sampling site): now
   states that the dial does NOT evict the phone and why (two Servers, separate lease maps,
   one shared tap, `hub.attach` reached once), names the proving test, and records that
   eviction is real only within a tier.
2. **Two matching claims corrected in `internal/attach/attach.go`** -- the `Config.TookOverRemote`
   doc ("marks an attach that EVICTED ... hub.attach evicts unconditionally") and the
   `takeoverNote` doc. Both repeated the same disproved mechanism.
3. **The takeover note's WORDING now lies, and was NOT fixed here.** `takeoverNote =
   "took over from phone"` is painted whenever a phone held control at the keypress; with
   co-presence proven, the attach took nothing over. It is not a one-line fix:
   `internal/attach/hintrow_takeover_test.go` pins the strings `"took over"` and `"phone"`
   as that note's contract (four tests), and the honest replacement is a LIVE co-presence
   indicator, not a one-shot sampled note -- a design change, with a test rewrite that must
   quote the assertions it replaces. Deferred to M2 as `agents-tracker-dwwv.3.1`, which also
   records the liveness source already in place (`serve.go` wires
   `d.srv.SetRemoteControlledFunc(rs.IsControlled)`, and `rostercontrol_test.go` pins that a
   control flip alone fires a roster event). The stale premise in that test file's own header
   comment was left untouched on purpose -- rewriting an existing test file, even its prose,
   belongs with the rewrite that changes its assertions.
4. **No production behavior changed.** Only the test and three comment blocks.

### Tracker actions

- `agents-tracker-nx44.8` ("Live co-presence: a non-evicting observer role at the shim") --
  CLOSED. Its premise was that the shim allows exactly one live subscriber and the last
  attach evicts, so a live "phone has control" display needs a NEW non-evicting subscriber
  role built across shim + daemon + attach + TUI. That premise is disproved: the non-evicting
  role already exists and already ships (the shared per-session tap plus `TerminalTapper`),
  and the co-presence it was meant to enable is what the test observes. The remaining work is
  the display wording, which is `agents-tracker-dwwv.3.1`, not an architecture change.
- `agents-tracker-dwwv.3.1` -- CREATED under M2: reword the attach-chrome takeover note and
  make it live, rewriting `hintrow_takeover_test.go`'s assertions with the new contract quoted.
- `agents-tracker-dwwv.1.1` -- CLOSED (this section is its evidence).

---

## M0.3 -- Unknown gateway action: sealed refusal, never a hang

### The question

`agents-tracker-joyi`'s notes carry a live fact from the 2026-08-09 outage diagnosis: a
phone-sealed `approve` against the released 0.8.0 gateway fell into
`internal/remotegw/command_loop.go`'s default arm, which errored BEFORE `b.consume` --
no reply was ever sealed, so the phone's pending operation never resolved and the approval
card hung forever. The nx44 wave (released 0.9.0) is recorded as having changed refusal
consumption. The question this settles: does the gateway, TODAY, guarantee that an envelope
carrying an action string it does not know is (a) consumed -- never re-served in a loop -- and
(b) answered with a sealed refusal reply the phone can render, so a future version-skewed pair
(newer phone, older machine) degrades to a visible failure instead of a silent hang? And,
read-only: does that guarantee actually reach the phone's pending-approval sheet as a visible
failure?

### The code path read (`internal/remotegw/command_loop.go`)

- `handle` (:496-526) opens a mailbox frame and, for a command, calls `routeCommand` BEFORE
  `consume`. If `routeCommand` returns an error that is a `refusedCommand` (`errors.As`,
  :516-519), `handle` STILL calls `b.consume` (:520) before returning the error -- so a refusal
  is consumed (cursor + replay high-water advance durably) but still surfaces in the poll
  error. Any OTHER error (forward failure, seal failure) is left unconsumed on purpose, so a
  transient fact (daemon down) can still be retried.
- `routeCommand`'s default arm (:646-648, for any `rc.Action` not matched by the named cases)
  calls `b.forward`.
- `forward` (:653-665) calls `opForAction`, whose OWN default arm (:828-830) returns
  `fmt.Errorf("remotegw: unsupported command action %q", rc.Action)` for any action it does not
  recognize. `forward` hands that reason to `b.refuseCommand` (:656).
- `refuseCommand` (:688-698) seals a `protocol.Control{Op: protocol.OpError, SessionID:
  rc.Session, OperationID: rc.OperationID, Error: reason.Error()}` to the phone's mailbox
  FIRST, and only then returns `refusedCommand{reason}` -- so the seal happens before the
  caller ever sees the refusal-shaped error, and a seal failure (not a refusal) is what stays
  unconsumed and retryable.

This is exactly the guarantee M0.3 asks for, and the code's own comments (:508-515, :667-698,
:783-803) date it to `agents-tracker-nx44.4` (the missing-arm class S18 and IS-LIFE-4 each hit
once, generalized) and `agents-tracker-2pnu` F3 (the consume half, added after a refusal that
was sealed but left the item unconsumed wedged the mailbox on every later drain). Both predate
this bead. Two tests already existed pinning one half each:
`nx444_unknownaction_test.go:TestNx444_AnUnknownActionIsRefusedWithASealedReply` (the seal) and
`refusal_consume_test.go:TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains` (the consume, via
two `PollOnce` calls against one poison item).

### The test

`TestM03_UnknownActionIsConsumedAndSealedNeverLeftPending` --
`internal/remotegw/m03_unknown_action_test.go`. It is the M0.3 pin: a single test naming
joyi's exact scenario (an action string this build has no arm for, fed through `Run` -- the
production drive, not `PollOnce` directly) and asserting BOTH halves of the guarantee
together, so a regression in either shows up here even if one of the two upstream tests above
is ever narrowed. It asserts: the durable cursor advances past the unknown-action item (never
re-served); nothing reaches the fake daemon forwarder; exactly one reply is sealed to the
phone's mailbox; that reply is `Op: OpError` carrying the command's own `OperationID`; and its
`Error` text names the unknown action string.

### The result

FIRST RUN WAS GREEN. The guarantee was ALREADY BUILT (nx44.4 + 2pnu F3, both released in
0.9.0) -- this bead did not need to change `command_loop.go`. Under this project's TDD rule
that is the finding: the test ships as a PIN on behavior that already held, and the run below
is quoted in place of a RED run.

```
$ go test ./internal/remotegw/... -run 'TestNx444_AnUnknownActionIsRefusedWithASealedReply|TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains|TestM03_UnknownActionIsConsumedAndSealedNeverLeftPending' -v -count=1
=== RUN   TestM03_UnknownActionIsConsumedAndSealedNeverLeftPending
--- PASS: TestM03_UnknownActionIsConsumedAndSealedNeverLeftPending (0.21s)
=== RUN   TestNx444_AnUnknownActionIsRefusedWithASealedReply
--- PASS: TestNx444_AnUnknownActionIsRefusedWithASealedReply (0.00s)
=== RUN   TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains
--- PASS: TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/remotegw	1.051s
```

### The phone-side read-only check: does a refusal reach the pending-approval sheet?

Traced `App.Approve` (`mobile/commands.go:91-116`) through the reply path to the Android
approval sheet:

- `App.Approve` reads the binding tuple off the card the phone already holds (`PendingApproval`,
  `internal/phonecore/interaction.go:415-434`) and, on success, seals a signed `approve` command
  and returns an `*Op` -- this is the LOCAL send succeeding, before any reply crosses back.
- The Android tap (`PhoneSurface.approvalAction`, :2204-2210) wires `app.approve(...)` with no
  `settle`; `Press.settle` "runs only if the verb returned; a refusal goes to the outcome line
  and the toast instead" (:2610-2611) -- and that refusal/success split is decided entirely by
  whether the SYNCHRONOUS Go call threw, in `dispatchPress` (:2688-2719). A refusal arriving
  LATER, asynchronously, from the machine is not this path at all.
- The async reply lands in `mobile/relay.go:onReply` (:820-835): `a.resolve(ctrl.OperationID)`
  clears the operation from `a.inflight` (the `PendingOps` counter, `app.go:674,1356-1362`) and
  an `Event{Kind:"outcome", Stream:"reply", State: ctrl.Op, Message: ctrl.OperationID}` is
  emitted. Note what is NOT on `Event` (`mobile/types.go:50-58`): there is no `Error` field, so
  `ctrl.Error` -- the refusal text `refuseCommand` sealed -- is dropped ON THE EVENT PATH.
  It is NOT dropped from the facade, and an earlier draft of this section wrongly said it never
  crossed the boundary at all (see the correction below). The reply itself is cached
  VERB-AGNOSTICALLY: `MailboxRouter.apply` appends every `kindCommandReply` frame to the
  `ReplyCache` (`internal/phonecore/snapshot.go:667-673`), an approve's gateway refusal included.
  `App.Outcome(operationID)` (`mobile/app.go:1298-1315`) claims it BY KEY through
  `Replies().TakeFor` (`snapshot.go:182-198`) and returns
  `Outcome{Code: ErrorCode|Op, Message: ctrl.Error, Resolved: true}` (`outcomeOf`, :1342-1352).
  So the refusal text does cross the gomobile boundary -- on the OUTCOME path. The event is only
  the nudge that says "ask again".
- On the Android side, `PhoneEvents.onEvent` (`PhoneEvents.kt:35-37,62-68`) receives that event
  and, BY DESIGN ("THE EVENT IS NOT READ... there is nothing to branch on"), does exactly one
  thing for ANY event: `main.post { sink?.invoke() }` -- a full redraw, with the event's Kind,
  State and Message all discarded before the redraw runs.
- The redraw recomputes the approval panel via `approvalPanel` (`PhoneSurface.kt:1390-1394`) ->
  `FacadeBridge.pendingApproval` -> `ItemStore.PendingApproval`
  (`internal/phonecore/interaction.go:425-434`), which is keyed SOLELY on `it.Resolved` -- set
  only by an `approval_resolved` JOURNAL record the DAEMON emits. A gateway-level refusal (the
  approve never reaching the daemon) can never produce that record.

**Answer: neither.** The card does NOT silently vanish -- `PendingApproval` is correctly still
true, since the daemon genuinely never resolved the question, so no false "accepted" is shown.
But the refusal does NOT reach the sheet as a visible failure either -- and the reason is much
NARROWER than the event plane. `approvalAction` (`PhoneSurface.kt:2204-2210`) builds its `Press`
with no `settle`, so the `*Op` that `App.Approve` returns (`mobile/commands.go:91,108`) is
dropped on the floor and NO OPERATION ID IS EVER LATCHED for the press. With no id in hand
nothing can ask `bridge.launchOutcome(...)` about it, so the sealed refusal sits in the reply
cache unclaimed. The user sees a positive "sent" outcome line at press time (the local send DID
succeed), then nothing: the card simply keeps showing as pending, indefinitely, with no
indication their tap was ever refused.

THE CORRELATION MECHANISM ALREADY SHIPS, and an earlier draft of this section wrongly called
it `take_control`-specific. Three verbs already resolve an async reply by operation id through
the same facade method:

- `renderLaunch` (`PhoneSurface.kt:2441-2455`) -- launch, polled per draw off `launchScreen.inFlight`.
- `renderKillVerdict` (:2392-2413) -- kill.
- `leaseVerdictFor` (:2363-2372) -- take_control, via `leaseOp`/`rememberLease` (:2276-2303).

All three call `FacadeBridge.launchOutcome(operationId)` (`ui/FacadeBridge.kt:464-472`), whose
doc line is "PB-SYNC-2: outcomes are claimed BY OPERATION ID, never by proximity", and
`renderVerdicts`' own KDoc (:2374-2390) describes itself as "renderLaunch's PROGRAM ON THE OTHER
TWO VERBS". `CommandVerdict.of` (`ui/CommandVerdict.kt:152-174`) is verb-agnostic BY
CONSTRUCTION -- the accepted code is a PARAMETER exactly so no table has to know the verb -- and
its `else` arm already handles a codeless gateway refusal: `refuseCommand` seals no `ErrorCode`
(`internal/remotegw/command_loop.go:688-698`), so `outcomeOf` falls back to `ctrl.Op` =
`protocol.OpError` = `"error"`, which maps to `REFUSED` carrying `outcome.message` -- and that
message IS `ctrl.Error`.

So the size of the gap is: the same three lines `takeControlOf` (:2276-2283) already writes -- a
`settle` that latches `issued.operationID` -- plus a per-draw poll beside the other two, plus
ONE genuine design question: how the approval sheet renders a failed-not-vanished state. The
other two verbs answer on the outcome line and a toast; the sheet is D4.4's heaviest surface and
its card is still correctly pending, so "toast it like a kill" is a decision to make rather than
a pattern to copy. Per this bead's instructions nothing new is filed for it: the task names
`handleApprove` as due for an M1 rework already, and this finding feeds directly into that
rework. M1 does NOT have to re-derive the mechanism -- it exists, it is named above, and only
the sheet's rendering is open.

### Outcome taken

1. **`internal/remotegw/m03_unknown_action_test.go` added.** One new test, no production code
   changed: the guarantee `command_loop.go` needs was already built by `agents-tracker-nx44.4`
   and `agents-tracker-2pnu` F3 (both shipped in 0.9.0), and this test pins it under M0.3's own
   name so a regression in either upstream test does not go unnoticed here.
2. **No production behavior changed** in `internal/remotegw`.
3. **Phone-side gap documented, not fixed.** The approve press latches no operation id, so the
   sealed refusal is never claimed and the sheet shows no failure. The correlation mechanism it
   needs already ships for launch, kill and take_control; the only open design question is how
   the approval sheet renders a failed-not-vanished state. Left for M1's `handleApprove` rework
   rather than patched here or filed as a new bead.

### Tracker actions

- `agents-tracker-joyi` -- CLOSED. What it asked for has two parts, and both are now
  accounted for: (1) the approve verb itself has shipped end to end -- `protocol.ActionApprove`
  / `protocol.OpApprove` wire format, `opForAction`'s arm (`command_loop.go:817-825`), the
  daemon's `handleApprove` (IS-LIFE-4), the `approval_resolved` journal record, and the facade
  surface (`App.Approve`, `approvalAction` wired to `ApprovalSheetPanel`) -- so the "needs a
  core protocol ADR... then O6 closes and the sheet becomes real" ask is moot: the sheet has a
  caller today. (2) The version-skew fact in joyi's notes asked for "versioned action
  negotiation OR a guaranteed refusal-reply for unknown actions" -- the latter is what shipped
  (nx44.4/2pnu F3) and is what this bead's test pins; no action negotiation is needed on top of
  it. What joyi's notes did NOT anticipate and this bead's read-only check found: the
  guaranteed refusal-reply is a MACHINE-side guarantee only -- on the phone the approve press
  latches no operation id, so nothing ever claims the sealed refusal and the sheet shows no
  failure (see above). That residual is NOT joyi's ADR ask; it feeds M1's `handleApprove` rework
  instead, which inherits a shipped correlation mechanism and one open rendering decision.
- `agents-tracker-dwwv.1.3` -- CLOSED (this section is its evidence).

### Correction (adversarial review of wave M0, `agents-tracker-dwwv.7`)

The Go-side half of this section survived tracing unchanged. The PHONE-side half above shipped
with two falsifiable claims, both traced false by the wave's reviewer and both corrected in
place; the sentences they replace are recorded here so the record is not silently rewritten.

1. WAS: "there is no `Error` field [on `swarmmobile.Event`], so `ctrl.Error` ... never crosses
   the gomobile boundary at all." The `Event` half is true (`mobile/types.go:50-58`); the
   absolute is false. `outcomeOf` (`mobile/app.go:1342-1352`) carries `ctrl.Error` into
   `Outcome.Message`, `App.Outcome` (:1298-1315) serves it after `Replies().TakeFor`, and
   `FacadeBridge.launchOutcome` (`ui/FacadeBridge.kt:464-472`) reads it on the Kotlin side. The
   refusal text crosses on the OUTCOME path; only the EVENT path drops it.
2. WAS: "there is no per-operation-id correlation on the Android side for an async reply (the
   one thing that exists, `leaseOp`/`rememberLease` at :2276-2303, is `take_control`-specific
   and does not generalize)." False for three verbs -- `renderLaunch`, `renderKillVerdict` and
   `leaseVerdictFor` all correlate by operation id through `bridge.launchOutcome`, and
   `CommandVerdict.of` takes the accepted code as a parameter so it never knows the verb.
   `ReplyCache` underneath caches every reply regardless of verb.

Consequence for the CONCLUSION, which rested on those two claims: this is not "three design
decisions, structural". Two of the three were already-shipped mechanisms. The gap is the
unlatched operation id (a `settle` on `approvalAction`) plus one real design question (the
sheet's failed-not-vanished rendering). The "no bead filed, hand to M1's `handleApprove`
rework" call STANDS -- the rendering decision belongs with that rework and not beside it -- but
it stands on a smaller item than the original text implied, and M1 inherits the mechanism
rather than re-deriving it.

Everything else in the M0.3 phone trace was re-traced and held: `Event` carries no `Error`
field, `PhoneEvents.onEvent` discards the event by design, and `PendingApproval` is keyed on
`Item.Resolved` whose only writer is `resolveLocked` on an `approval_resolved` record
(`internal/phonecore/interaction.go:304-311`) -- so "no false accept, the card sits pending" is
correct as written.

---

## M0.2 -- Render the running state on tool cards

### The gap

`InteractionItem.status` (§4 of `docs/specifications/interaction-schema.md`, `in_progress` |
`completed` | `failed` | `declined`) crosses the wire and is populated at
`FacadeBridge.kt:120` -- and was read by nothing. `TranscriptPanel.blockFor`
(`ui/screens/TranscriptPanel.kt:164-240` at the time this bead was filed) built a `tool_run`
block from `tool`, `action`, `output_excerpt` and `truncation_marker` alone; a tool still running
and a tool long finished produced the byte-identical `TranscriptBlock`. The acknowledged-gap
comment at `SessionDetailPanel.kt:44-47` named the CARD as absent ("a bordered block with a tool
glyph, a status and an expandable body") without naming that the underlying FACT never reached the
model at all.

### Design decisions taken, and why

This screen package is fenced against choosing a colour, a dimension or a typeface at all
(`s24_screens_test.go` / PB-DS-6), and every kit component with a coloured mark --
`statusDot`'s four `status.Group`s, `presenceDot`'s three relay states, `syncPill`'s three link
tones -- is bound to a CLOSED, documented vocabulary that is not this one; `PresenceDot.kt`'s own
KDoc argues at length why borrowing one of them for an unrelated state ("the cheap implementation
is the defect") is wrong twice over: semantically dishonest, and it drifts the moment the borrowed
enum changes for its own reason. Adding a genuinely NEW coloured kit factory needs a `derived:`
row in `docs/design/substrate-components.md` (`s23_kit_test.go`'s provenance fence) that does not
exist for a running mark -- `docs/specifications/mirror-program.md` M2.2 is where one gets written,
against `ui/kit/ToolCard.kt`. So the amber dot and the pulse are BOTH left to M2, not just the
pulse: this bead ships the FACT, static and textual, through the two factories `tool_run` already
draws through --

- **Panel** (`TranscriptBlock.running: Boolean`): `TOOL_RUN`'s arm sets it from
  `item.status == "in_progress"` and leads the mono well with the word `"running"` when it is
  true -- copy the screen supplies (PB-DS-9), on the same footing as `"You"`, because a lone
  `tool_run` has no sibling value beside it to read the wire's own `in_progress` token against the
  way a `plan_update`'s steps do.
- **View** (`TranscriptTag.RUNNING`): the row is tagged distinctly from `TranscriptTag.BLOCK`,
  `TranscriptTag.APPROVAL`'s own precedent restated -- so a running block is findable without
  re-parsing the sentence the panel wrote.

`agent_message` was checked and deliberately left alone: `internal/adapter/claude/interaction.go`
always closes an `agent_message` `StatusCompleted` in the same record that carries its text, so no
adapter emits an `in_progress` one today and there is no wire value to test against. Marking one if
it arrived would also reopen `AGENT_MESSAGE`'s own stated rule -- "no attribution and no marked
span" -- on a SENTENCE rather than a tool's one line, which is a design question this bead does not
answer.

### RED (panel level -- `TranscriptPanelTest`, `TranscriptViewTest`)

Compiling the two new tests against the pre-change model/view fails for the missing symbols, not a
syntax error -- the RED this codebase's TDD convention treats as pinned:

```
> Task :app:compileDebugUnitTestKotlin
e: file:///.../android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptPanelTest.kt:203:21 Unresolved reference 'running'.
e: file:///.../android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptPanelTest.kt:209:77 Unresolved reference 'running'.
e: file:///.../android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptViewTest.kt:58:9 No parameter with name 'running' found.
e: file:///.../android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptViewTest.kt:217:40 Unresolved reference 'RUNNING'.
e: file:///.../android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptViewTest.kt:234:40 Unresolved reference 'RUNNING'.

> Task :app:compileDebugUnitTestKotlin FAILED
BUILD FAILED in 54s
gradle exit status: 1
```

`TranscriptBlock.running` and `TranscriptTag.RUNNING` did not exist yet -- the right reason.

### GREEN

`scripts/o2-gradle-run.sh testDebugUnitTest --tests TranscriptPanelTest --tests TranscriptViewTest
--tests SessionDetailPanelTest --tests TranscriptScreenGoldenTest`, `BUILD SUCCESSFUL`, four fresh
result files:

| Suite | tests | failures | errors |
|---|---|---|---|
| `TranscriptPanelTest` | 22 | 0 | 0 |
| `TranscriptViewTest` | 9 | 0 | 0 |
| `SessionDetailPanelTest` | 14 | 0 | 0 |
| `TranscriptScreenGoldenTest` | 6 | 0 | 0 |

New tests: `a tool still in_progress is named as running, and a completed one is not`
(`TranscriptPanelTest`), `a running tool's row is tagged distinctly and its mono line carries the
word` and `a completed tool's row is an ordinary block, not tagged running`
(`TranscriptViewTest`).

Then the full serialized lane, `scripts/o2-gradle-run.sh test` (both variants, no filter),
`BUILD SUCCESSFUL in 4m 44s`, `61 actionable tasks`, mtimes newer than the run's own start
(11:12:38 CEST):

```
testDebugUnitTest: 142 result files, 142 written in the last hour
testReleaseUnitTest: 142 result files, 142 written in the last hour
```

Aggregated across every `TEST-*.xml` in each variant: `tests=1155 skipped=0 failures=0 errors=0`
-- both `testDebugUnitTest` and `testReleaseUnitTest`. `android/app/libs/swarm.aar` was present
before the run (`Aug 9 21:01`) and its mtime is unchanged after (test-only lane, no AAR rebuild
triggered).

### Go side

- `go test ./android/gate/...` -- `ok` (6.585s). The source-scanning gates (`s24_screens_test.go`
  PB-DS-6/PB-DS-11, `s23_kit_test.go` PB-DS-7) read these exact files and stayed green: the
  screens package took no `R.color`, `R.dimen`, `setTextColor` or `setPadding` (verified by
  inspection against `s24ScreenForbidden`'s list -- the diff adds only a `Boolean` field, two
  `String` constants and one tag string).
- `go build ./...`, `go vet ./...` -- clean.
- `go test ./...` -- one pre-existing, unrelated failure:
  `internal/hookclient.TestPost_RawBytesCrossTheWireVerbatim`, `bind: invalid argument` on a
  `$TMPDIR`-rooted unix socket path -- an environmental macOS socket-path issue in a package this
  bead's diff never touches (`git status` for this session is five Android files only). Every
  other package is `ok`.
- `PATH="$HOME/go/bin:$PATH" golangci-lint run` -- clean, no output.

### Outcome taken

1. **`TranscriptBlock.running: Boolean`** added, set in `TOOL_RUN`'s arm of `blockFor` from
   `item.status == "in_progress"`; the mono well leads with the screen-authored word `"running"`
   while it is true.
2. **`TranscriptTag.RUNNING`** added; `rowFor` tags a running block with it instead of
   `TranscriptTag.BLOCK`. `TranscriptTag.APPROVAL` and its click-wiring are untouched.
3. **`SessionDetailPanel.kt:44-47`** rewritten to describe what ships (the fact renders, static,
   tagged, textual) and to name what is still absent and where it is owned (`ui/kit/ToolCard.kt`,
   M2.2 -- glyph, colour, expand/collapse, and the pulse this bead deliberately leaves out).
4. **Deferred to M2, explicitly**: the amber/coloured dot itself (needs a new `derived:` kit
   provenance row this bead has no design authority to write), the pulse animation (named
   out-of-scope by this bead's own instructions), and any marker on `agent_message` (no producer
   emits `in_progress` on one today, and marking prose reopens a stated design rule this bead does
   not have the authority to revise).

### Tracker actions

- `agents-tracker-dwwv.1.2` -- CLOSED (this section is its evidence).
