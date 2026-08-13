# Mirror program -- wave M0 evidence

Wave M0 of `docs/specifications/mirror-program.md` ("Ground truth", bead
`agents-tracker-dwwv.1`). Each item below records the question it settled, the test that
settled it, the verbatim result, the outcome taken, and the tracker actions that followed.
Later agents APPEND their section (`## M0.2 ...`, `## M0.3 ...`) below this one; do not
rewrite an earlier section, and keep the run output verbatim.

| # | Item | Bead | Status |
|---|---|---|---|
| M0.1 | Co-presence: owner attach + phone `take_control` at once -- both streams live? | `dwwv.1.1` | settled, outcome A |
| M0.2 | Render the running state on tool cards | `dwwv.1.2` | pending |
| M0.3 | Unknown gateway action yields a sealed refusal, never a hang | `dwwv.1.3` | pending |
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
