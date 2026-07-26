# Phase B slice S7 — durable phone state (PB-STATE-1..5, 7, 8; PB-GW-6)

**The most severe gap the audit committee found, unanimous across all three reviewers.**

## The defect (§4.3)

`phonecore.Sequencer` was a bare in-memory counter returning 1 on first call, and
`internal/phonecore` performed **no persistence at all**. Android kills backgrounded processes
as routine behaviour, so after **one** process death the phone restarted at seq 1 under the same
epoch and the gateway rejected every keystroke, `take_control`, launch and kill as stale —
permanently, until an epoch rotation or re-pair. **The exit criterion failed on the second app
launch.** The mirror direction was a security regression: the receive high-water also reset, so
a retaining relay could redeliver.

The tree already proved the problem class was understood for the *other* direction:
`remotegw/seqstore.go` exists so a restarted gateway never re-emits a seq "the phone's
**durable** per-(sender,epoch) high-water would stale-drop" — a comment presuming a durable
phone high-water that did not exist.

## RED

27 tests, verified against an inert seam: **27 FAIL, zero passing against unfixed code.**
Headline:

```
--- FAIL: TestProcessDeath_TypingLaunchAndKillSurviveAKillWhileAReplayDoesNot
    post-restart frame 1 refused by the gateway: crypto: stale or reordered sequence number
    -- this is §4.3: the phone restarted its send-seq under the same epoch and every
    keystroke, take_control, launch and kill is stale-dropped, permanently
```

That test now passes, and it is the guard in **both** directions at once: typing, launch and
kill survive a real SIGKILL of a real second process, while a frame captured before the kill is
still refused as a replay.

## Both standing questions, answered up front

**"What if there is more than one?"** Two live cases, both now tested. (1) v1 has **two receive
buckets per epoch** — the machine-sender journal/terminal bucket and the sender-zero
command-reply bucket, with independent seq spaces; a scalar high-water lets one stale-drop the
other. (2) **Send-seq is keyed per epoch**, because revoke rotates the epoch and the stream
legitimately restarts at 1. This also closed a residual S1b had recorded (the reconcile arm
never validated `Machine`/`EpochID`); since `SeedFrom` is monotonic, adopting a foreign
authority would have been *unrewindable*.

**"Does durability make a self-healing failure permanent?"** Yes — in exactly the shape S2
shipped and had to fix. A state blob naming **another machine** (after `swarm remote init`)
loads **empty, not as an error**, so a re-pair works and a retained epoch-1 high-water cannot
stale-drop the fresh phone. Only genuinely unreadable or unversioned blobs fail closed.

## The load-bearing judgement: the contiguity rule

Two pinned tests look contradictory over identical persisted state — a reply must survive a
restart; a retained frame must not — differing only in the frame's seq relative to the
**durable** high-water. Durable content therefore requires `seq == durableHW+1`; a gapped frame
is guard-committed, acked, applied in memory and marked stale, but not folded into durable
state. `crypto`'s `Gap` cannot answer this: a fresh receiver reports no gap on a stream's first
frame.

The reviewer verified the rule sits exactly on the boundary by flipping it **both ways**:
`contiguous=true` breaks one test, `contiguous=false` breaks the other.

## The blocking defect: content committed after the ack

`AcceptCommit` acked the relay and only then committed the decoded content, violating
PB-STATE-7's text and acceptance criterion. Concrete loss: a `command_reply` at `durableHW+1`
persists its guard, the ack succeeds — so the relay may compact the only copy and the cursor has
moved past it — then a SIGKILL lands before the content commit. On restart the outcome is gone
and redelivery is `ErrStaleSeq`. The same window loses a journal record outright, leaving a
session that emitted its `exited` record showing as running forever.

**The ordering was forced by a mis-specified assertion, not chosen.** Moving the commit earlier
broke exactly one test, whose "1 reply" was not a second application but the durable outcome
restored by `rebind` — the very mechanism its sibling test requires. The reviewer proved it and
correctly refused to edit a pinned test; the **test author** returned, reproduced it, and ran a
control that settled it: a router resumed from that state **with the frame never offered at
all** also holds 1.

Its own verdict: *"My assertion was measuring an absolute count where only the delta is
meaningful — it made 'the outcome survived the crash' indistinguishable from 'the frame was
applied twice', and so forbade the correct implementation."*

The corrected test is **strictly more adversarial**: it now also pins that the durable outcome
survives the crash (which fails against the old implementation), asserts the redelivery's
*delta* is zero, and checks the persisted `OpOutcomes` holds exactly one entry. Commit and ack
are now one transaction before the ack, and **fsyncs per contiguous frame dropped from 2 to 1**,
measured with the suite's own counting store.

One exception is now stated precisely rather than left absolute: a **gapped** frame is
deliberately acked with only its guard committed, so an op it would have resolved stays
unresolved until re-driven — the PB-STATE-8 trade.

## A race this slice created, and fixed

Adding the reconcile record turned `Snapshot` into a relay round-trip **inside** the documented
read->subscribe window, reproducibly breaking `TestPhonesim_PairObserveKillE2E` under `-race`: a
session launched right after gateway start had its record delivered to neither. Fixed by
subscribing before forwarding the snapshot. The reviewer confirmed this **narrows** the
pre-existing hole rather than widening it — same connection, so the daemon serialises
read-then-subscribe, double delivery is impossible, cursor dedup intact, and the **relay** wire
order is unchanged.

## The cross-slice brick, closed twice

PB-SYNC-7 shipped the reconcile record; this slice wires the phone-side gate, so **both halves
had to land together** or the phone would refuse every mutating op forever with nothing failing.
The gateway side is wired here.

Then it reappeared one level down: `ServiceConfig.Machine` had no obtainable production value,
so the record would have been published unattributable and the phone would have stayed
fail-closed. **The orchestrator's prescribed source was wrong** — `machineid.Identity` exposes
only a hostname and routing id, and `Core.Reconcile` refuses both identically to `""`, so a
plausible-but-wrong value would have *hidden* the brick. The reviewer took it from the only
authority that has it: the endpoint id the **daemon** assigns at hello, which is what is stamped
on every session id the phone sees.

## Recorded residuals

- **Reconcile adoption is not persisted**, so every phone process death re-arms the fail-closed
  refusal of mutating ops, clearable only by a gateway reconnect the phone cannot trigger.
  PB-STATE-10 territory; flagged for the later slices.
- **`Core.Save` rebinds the live sequencer**, so a mid-session save jumps to the persisted block
  ceiling and sets the gap flag until a command frame is sent.
- **Subscriber-eviction exposure moved** by the reordering: the subscription is now live across
  the whole roster forwarding.
- **`Sequencer.Next()` stays exported** on the gomobile-bound package and issues seqs with no
  durable reservation on a Core-bound sequencer. No production caller today, but the façade
  slice binds this surface.
- **`ReplyCache` is rebuilt from an unpruned `OpOutcomes`**; the unkeyed `Take()` can hand the
  app stale outcomes (`TakeFor` is safe).
- **`OpQueue`'s durability claim is false** — its comment says the Core persists every mutation;
  it does not. Zero non-test callers today.
- **Ephemeral keystore writes private keys to `$TMPDIR`** when `Dir == ""` (frozen crypto has no
  in-memory KeyStore). Test-only path; production always passes a directory.

## Gates

```
go test ./internal/phonecore/ ./internal/remotegw/ ./cmd/swarm-remote/ -count=1   ok
go test -race ./internal/phonecore/ ./internal/remotegw/ ./internal/skeleton/     ok
go test ./internal/phonecore/ -run TestBoundClosure                               ok (PB-BIND-0 holds)
go build ./... && go vet                                                          clean
```
59 tests green in `phonecore`. Persistence is one versioned JSON blob written
temp+fsync+rename+dir-fsync, maps travelling as sorted arrays for byte-stability, replay guards
merged monotonically on save.

## Per-requirement evidence (PB-E2E-3)

Added in S19. The traceability table cites this file for **PB-STATE-1, PB-STATE-2, PB-STATE-3,
PB-STATE-4, PB-STATE-5, PB-STATE-7, PB-STATE-8 and PB-GW-6**, and until now it named only the
first, last and the range form `PB-STATE-1..5` in its title — so four shipped rows cited a
document that never mentioned them and no auditor could get from the row to the proof. What
follows is reconstructed from the tests, not from recollection: every test named below is in the
tree and can be run.

### PB-STATE-2 — process-death acceptance

`internal/phonecore/processdeath_test.go`:
`TestProcessDeath_TypingLaunchAndKillSurviveAKillWhileAReplayDoesNot`. It SIGKILLs a real second
process mid-session (`TestHelperPhoneCoreSession` is that process) and restarts it over the same
state directory, then asserts BOTH directions on one run: an input frame, a `take_control` and a
`kill` are all accepted by the gateway's real inbound guard after the restart, while a frame
captured before the kill is still refused as a replay. The RED form is quoted verbatim above —
`post-restart frame 1 refused by the gateway: crypto: stale or reordered sequence number`.

The requirement's own clarification is honoured rather than assumed: what survives is the durable
send-seq, keys and coordinates, **not the lease**. The lease is a live gateway->daemon socket and
cannot survive a phone restart by construction, so the post-restart sequence is re-`TakeControl` ->
await the confirmed generation -> type. S19's `TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke`
is the first test that runs that sequence through the real facade rather than through `phonesim`.

### PB-STATE-3 — reserve-a-ceiling-and-burn-the-gap

`internal/phonecore/sendseq_test.go`, seven tests, each pinning one clause:
`TestSendSeq_ReservesABlockRatherThanFsyncingPerKeystroke` (the cost claim, measured on the
suite's counting store), `TestSendSeq_NeverReusesASeqAcrossACrashAtAnyPointInTheWindow` (the
acceptance criterion verbatim, including a crash between reservation and use),
`TestSendSeq_ResumesAtTheReservedCeilingNotTheLastIssuedSeq` (the burn),
`TestSendSeq_ReservesOnFirstUseNotOnOpen`, `TestSendSeq_ReservationFailureIssuesNoSeq` (a failed
reservation issues nothing rather than a seq nothing recorded), `TestSendSeq_IsKeyedPerEpoch` (a
revoke rotates the epoch and the stream legitimately restarts at 1). The block size is §6.0's 256.

The gap consequence PB-STATE-3 defers to PB-STATE-8 is pinned by
`TestGapIsAbsorbedByTheRelease_NotByAKeystroke` and `TestOperationGapForcesOutcomeReconciliation`:
the burned block is absorbed by the next COMMAND frame, never by an input frame, because the
gateway drops a gapped input frame silently.

### PB-STATE-4 — crash-atomic writes, fail-closed corruption, a named rollback anchor

Two halves, and the second is the one the requirement was rewritten to force.

*Atomicity and fail-closed*: the blob is written temp+fsync+rename+dir-fsync and the in-memory
copy advances only after the write lands. `internal/phonecore/state_test.go`:
`TestStateStore_CorruptFailsClosedButAForeignMachineIsMerelyEmpty` (unreadable or unversioned is
`ErrCorruptState`; a blob belonging to another machine is not corrupt and loads empty, so a
re-pair works) and `TestStateStore_UnknownFutureSchemaFailsClosed`.

*The rollback anchor, per coordinate*: `internal/phonecore/rollback_test.go` tests one authority
each, which is precisely what the requirement's "not send-seq alone" demands —
`TestRollback_SendSeqResumesAboveTheGatewayInboundHighWater` (a),
`TestRollback_ReceiveHighWatersRefuseRetainedFramesPerBucket` (b, and *per bucket*: the shared
journal/terminal bucket and the sender-zero reply bucket have independent seq spaces),
`TestRollback_GrantWatermarkRefusesAnOlderSignedGrant` (c, a correctly-signed older grant is
refused), and `TestRollback_FailsClosedForMutatingOpsAndMarksChannelsStale` for the unreachable
case. `TestReconcile_RefusesAnAuthorityForAnotherMachineOrEpoch` closes the S1b residual named
above: `SeedFrom` is monotonic, so adopting a foreign authority would be unrewindable.

*The 2026-07-26 revoke exemption is NOT S7's* and is not claimed here. It landed with S18b and its
two-directional test is `mobile/conformance/pbstate4_revokeexempt_test.go`
(`TestPBSTATE4_AnUnreconciledPhoneCompletesItsRevokeEndToEnd` and
`TestPBSTATE4_TheRevokeExemptionDoesNotWidenToTheStateSelectedVerbs`).

### PB-STATE-5 — a schema version with a forward-migration path

`internal/phonecore/state_test.go`: `TestStateStore_PinnedV1FixtureStillLoads` and
`TestStateStore_PinnedSealedFixturesStillLoad` load byte-literal fixtures of EVERY shipped schema
version and assert every coordinate of that version survives — which is the migration test, and
the only mechanical form of one: a fixture regenerated by the current writer would prove nothing.
`TestStateSchemaVersion_IsPinnedToTheDurableFieldSet` fences the version against the field set, so
a new durable field cannot land without a bump, and `TestStateStore_UnknownFutureSchemaFailsClosed`
is the "unknown future version fails closed" half.

### What this file does NOT establish

The four rows above are all `internal/phonecore` properties, exercised at the core and (for
PB-STATE-2) against a real second process. None of them was exercised through the **bound facade**
until S19: `internal/phonesim`, which drives nearly every integration test in this repo, never
constructs a `phonecore.Core` at all. See `docs/verification/remote-phaseB-s19-evidence.md`.
