# S10 evidence — staleness, per-channel repair, and epoch-grant delivery (PB-SYNC-1..6, 8; PB-KEY-3, 4, 10)

**Commits**: `fcd7d7a` (slice, 42 files, +3587/-82), `c7acd7b` (cross-slice build fix — see below).
**Spec**: `61e40f3` added PB-KEY-10, raising the requirement count to 143.
**Requirements**: 10.

> **RECONSTRUCTED**, 2026-07-25, from the commits, their diffs and their tests. All 44 S10-owned
> tests were re-run at HEAD.

## PB-KEY-10 is the one that mattered

**The phone could never obtain an epoch key**, so every other requirement about sending, typing or
opening a frame was unreachable in production. This is the most severe finding of Phase B, and it
was a hole in the **requirements set**, not merely in the code: 142 requirements, validated by a
full audit committee, and none of them owned the step without which nothing else can work.

The chain, each link verified independently before the requirement was written:

- `State.Keys` is written **only** by `InstallWakeKey`/`InstallContentKey` — inbound verbs called
  from Kotlin. **Nothing supplied those bytes.**
- The machine delivers the epoch key as a sealed `crypto.EpochGrant` in a **tagged bootstrap
  frame** appended to the phone's mailbox (`cmd/swarm-remote/deliver.go`).
- `phonecore.AcceptGrant` had exactly **one** production caller: `internal/phonesim`, the test
  simulator. `mobile/` never called it.
- `MailboxRouter.TakeGrant` had **zero** production callers; its own comment said "route+expose
  only" and deferred consumption to a work item never done. `Core.Grants()` had zero callers
  anywhere.
- Kotlin could not supply them either: the custody surface is inbound-only by design (ADR-007 B8)
  and the golden-pinned facade had **no verb that ingests a grant**.

On a real handset: `resolveSend` returns `errNoContentKey` for every send, every inbound frame fails
to open, the relay cursor never advances, and the drain polls the same page forever. **The product
does not function at all.**

**How it shipped**: the bootstrap frame is *tagged plaintext*, not a ContentKey-sealed envelope —
deliberately, because it is what **delivers** the ContentKey — so `MailboxRouter.AcceptCommit`'s
`ParseEnvelope` refused it, committed nothing and acked nothing.

**The fix needed no facade verb.** Consumption now rides `AcceptCommit` **ahead of** the envelope
parse, so the bound surface and the golden are untouched.

### Why the whole suite was blind, and this is the part worth internalising

- No test in `mobile/` called `App.BeginPairing`.
- The conformance harness says out loud that it seeds durable state "rather than running a pairing
  handshake".
- **Even the PB-NET-1 real-wire test did not catch it.** It generates the epoch keys in-test and
  hands the content key to `InstallContentKey`. The "no fakes" test performs **by hand** the exact
  step the facade was missing — and it was written by the agent that had just found the machine-id
  defect, in the slice created to close this very class.

**A test can be built entirely from real components and still paper over a missing production path,
because supplying the missing input is the most natural thing in the world when you are setting up
a test.** Standing class (v) survives even a no-fakes integration test unless someone asks *where
each input comes from in production*.

So `mobile/conformance/s10_bootstrap_test.go` deliberately **does not use `newHarness`**. It runs
the whole first-run path with real parts — real relay, real Noise XXpsk0 pairing, real
`enroll.Enroll`, the gateway's own `grant.MarshalBootstrap + MailboxAppend` byte-for-byte as
`cmd/swarm-remote/deliver.go` does it, and the facade's own drain — and then asks the only two
questions that matter: can the phone **send** a signed command the machine opens, and can it
**open** an inbound frame the machine sealed. Both under the epoch key, **neither with any
test-supplied key**. It opens with an explicit **non-vacuity** check: before the grant the phone
must be *unable* to send, or the later assertions would hold for reasons unrelated to the bootstrap.

## Per requirement: what proves it

| Requirement | What proves it |
|---|---|
| PB-SYNC-1 (staleness per **seq bucket**, repair per **channel**) | `TestS10_ASharedBucketGapStalesJournalAndTerminal` (one gap on the shared bucket stales *both*, because `MailboxResult` carries only `{Plaintext, Gap bool}` and the bit has no frame kind), `_ACommandReplyGapStalesNeitherJournalNorTerminal` (the third bucket is separate), `TestS10_TheFacadeStalesBothChannelsOfAGappedBucket` |
| PB-SYNC-2 (repair per stream; a journal reseed cannot repair a missed grid) | `TestS10_AJournalReseedRepairsTheJournalChannel`, `_AJournalReseedDoesNotClearTerminalStaleness`, `_AFreshSnapshotRepairsTheTerminalChannelOnly`, `_AReplyGapLeavesTheOpUnresolved` |
| PB-SYNC-3 (clears only after a successful reseed of *that* stream, committed atomically with the watermark; failed resync stays stale) | `TestS10_AFailedReseedStaysStale`, `_AReseedClearsOnlyItsOwnBucketsCoordinate`, `_StalenessSurvivesTheProcessDeath`, `TestS10_ResyncDoesNotClearStalenessBeforeTheRepairLands` |
| PB-SYNC-4 (the correct authorization gate is named and implemented) | `TestS10_ResyncIsRefusedWithoutTheJournalCapability`, `_ResyncDoesNotRideTheDeviceSignatureGate` (`internal/protocol`) |
| PB-SYNC-5 (if device-signed, a new `Action*` must be **mapped** in the closed `actionClass` switch) | Resolved by **not** signing it: `TestS10_TheResyncActionIsDeliberatelyUnsigned`, backed by `TestS10_EveryForwardedActionIsMappedInActionClass` — the closed-switch guard itself |
| PB-SYNC-6 (bounded, non-amplifying; a hostile relay cannot drive unbounded work) | `TestS10_ResyncIsRateBounded` (§6.0: <= 1 per stream per 5 s, <= 12 per 5 min, and **per stream** so the two channels cannot starve each other), `TestS10_ANonAdvancingPageTerminates` |
| PB-SYNC-8 (a journal reseed must **REPLACE** the cache cursor, not merge) | `TestS10_AReseedReplacesTheCacheCursorRatherThanMergingIntoIt`, `_AReseedReplacesTheSessionSetNotJustTheCursor`, `_JournalReseedWireShape`, `TestS10_TheReseedFrameMatchesThePhonesWire`, `_TheSinkSealsTheReseedOnTheSharedStream`, and the **fixture rule** `TestS10_NoTestFixtureStampsANonzeroRosterCursor` |
| PB-KEY-3 (epoch-grant recovery) | `TestS10_GrantLossIsNotACustodyRefusal`, `_ARegrantRecoversAGrantLossDevice`, `_ARegrantOfAnUnknownDeviceIsRefused`, `_ARegrantAdvancesTheGrantSeq`, plus the owner-tier op tests `TestS10_TheOwnerCanRegrantADevice`, `_ARegrantIsRefusedOnTheRemoteTier`, `_ARegrantWithNoTargetIsRefused` |
| PB-KEY-4 (rotation while backgrounded/offline; **must update `GrantedEpoch`**) | `TestS10_ARotationTheDeviceSleptThroughUnpairsItOnTheNextRestart` (the failure it prevents, asserted directly), `_ARegrantConvergesTheDeviceOntoTheCurrentEpoch` |
| PB-KEY-10 (the phone obtains the epoch key at all) | `TestPBKEY10_AFreshPairingObtainsTheEpochKeyWithoutAnyInstallCall`, `_TheBootstrapFrameIsCompactedRatherThanRePolledForever`, `_TheGrantSurvivesTheFirstProcessDeath`; unit half `TestS10_ABootstrapGrantOnTheMailboxDeliversTheEpochKey`, `_ABootstrapFrameIsAckedSoTheMailboxCompacts`, `_ABootstrapFrameDoesNotBlockTheFramesBehindIt`, `_AReplayedBootstrapGrantIsRefused`, `_AnUnopenableBootstrapGrantIsAcked`, `_AGrantWhoseCommitFailedIsNotAcked` |

## The design judgements, recorded so they are not re-litigated

**Staleness is per bucket because that is what the wire supports.** PB-SYNC-1 v2 required "a gap in
one stream marks only that stream stale", and round 2 proved it **impossible**: journal and terminal
frames share one `(sender, epoch)` sequence space and the gap bit carries no kind. There are three
buckets — shared journal+terminal, command-reply (via the deliberate sender-zero split), and grant.
The requirement was corrected to match the wire rather than the code being bent to match the
requirement.

**Marking and clearing both happen inside the single `Save` that moves the watermark**, so a repair
can never be claimed before it is durable. That is PB-SYNC-3's "committed atomically with the
matching transport watermark", implemented as one write rather than as an ordering convention.

**The journal reseed REPLACES the cache cursor.** The daemon emits roster records with `Cursor`
**deliberately unset** — "a roster record is a set member keyed by `SessionID`, **not** a point in
the cursor-ordered event stream" — while `SessionCache.Apply` drops any record below the highest
applied cursor. So a merge makes **the designated repair channel a silent no-op**. The state schema
is bumped for this reason: an older build decoding the new blob would drop the field and **report a
channel it knows has a hole as live**.

**The resync is deliberately NOT device-signed**, which is what dissolves PB-SYNC-5. The risk
analysis is concrete rather than general: a forged resync can at worst make the machine republish a
roster **to a mailbox the forger already had to be able to write to** — unlike a push preference,
where a locally-decided value would let anyone who can inject a plaintext-shaped frame **silence the
owner**. No new `Action*` constant, no `actionClass` mapping, and no capability tier had to be
invented (the only fitting existing class was `ActionControl`, which would have made a read-repair
require the control tier — and `rec.Capability` is pinned at enrollment and never read from the
wire, so an observe-tier device could never resync).

**A lost grant is a distinct identity from a custody refusal.** The reason is concrete: a custody
refusal means *re-pair*, and re-pairing is **refused while a device is registered** — so routing a
grant-loss user there is a brick exitable only by physical access to the machine. Three failure
modes, three remedies. `TestS10_GrantLossIsNotACustodyRefusal` is that assertion.

## Two findings beyond the RED set

1. **The re-grant verb existed only as a method on an unexported type**, so the documented
   machine-side unblock "would have shipped as a Go function nothing could invoke" — *the same class
   of defect this slice exists to close*, reproduced inside its own remedy. It is now an owner-tier
   op with a CLI (`cmd/swarm/remote.go`), **deliberately not behind the remote authorisation path**,
   because a device whose grant was lost holds no content key and cannot seal a command: making the
   remedy require the broken thing is the brick again. `TestS10_ARegrantIsRefusedOnTheRemoteTier`
   pins the tier from the other side.
2. **The first bootstrap implementation acked every refused grant**, which would have let the relay
   compact **the only copy of the frame carrying the epoch key**. The rule that shipped: a
   *definitive* refusal is acked, a *transient* persist failure never is. Both directions fenced —
   `TestS10_AnUnopenableBootstrapGrantIsAcked` and `_AGrantWhoseCommitFailedIsNotAcked`.

## Failing-first evidence (GG-5)

No preserved RED transcript; tests and implementation are in one commit. What is durable and
checkable:

- **The PB-KEY-10 chain is mechanically reproducible at `fcd7d7a^`**: `AcceptGrant`'s only
  non-`phonesim` caller, `TakeGrant`'s and `Core.Grants()`'s zero callers, and the absence of any
  grant-ingesting verb in `mobile/testdata/exported_surface.golden`. The requirement itself
  (`61e40f3`) was written from that audit, before the code.
- **Two existing test fixtures were corrected to what the wire actually emits, and BOTH BROKE** —
  each a real finding, and the commit states "no assertion was removed or weakened". Verified
  against the diff: one asserted a property at a cursor value **production cannot produce**
  (`internal/phonecore/journal_replay_test.go`'s `TestSessionCache_OutOfOrderCursorNotApplied`,
  rewritten to assert the equal-cursor sibling **before** the cursor advances, "the only order the
  roster can arrive in"), and the other required a roster record to appear in the paged journal,
  **which only its invented cursor made possible** (`mobile/conformance`). This is the strongest
  failing-first evidence the slice has, because the fixtures were pre-existing and the breakage was
  produced by making them honest.
- **The fixture rule was then made mechanical.**
  `TestS10_NoTestFixtureStampsANonzeroRosterCursor` walks **every `_test.go` in the module** — "a
  rule enforced only where it is already obeyed is not a rule" — and fails on any composite literal
  both tagged as a roster record and given a nonzero `Cursor`. It is a **source** guard by
  necessity: nothing at runtime can tell a roster record whose cursor was invented from one whose
  cursor was read off the wire.
- **`TestS10_ANonAdvancingPageTerminates` is recorded as LEGITIMATELY PASSING TODAY** rather than
  counted as earned — its own comment says so. `mobile/relay.go` already processes every item in a
  page even when an earlier one cannot be opened, so one planted undecodable item costs one re-read,
  not a permanent wedge. It is a **regression fence**, and it explicitly disclaims being evidence
  for the bootstrap stall, which is a different failure (unopenable item at the **head**, nothing
  behind it commits, so no frame ever advances the cursor — fenced separately).

## Gates, re-run at HEAD 2026-07-25

`go test -count=1 -run 'TestS10_|TestPBKEY10_' ./internal/phonecore/ ./internal/remotegw/
./internal/skeleton/ ./internal/protocol/ ./mobile/conformance/` — **44 tests, all PASS, five
packages ok, zero failures.**

| Package | Tests | Result |
|---|---|---|
| `internal/phonecore` | 21 (grant 8, sync 12, fixture rule 1) | ok 1.20 s |
| `internal/remotegw` | 4 (resync routing + reseed wire shape) | ok 1.13 s |
| `internal/skeleton` | 6 (re-grant, epoch convergence, action-class closure) | ok 2.08 s |
| `internal/protocol` | 5 (owner-tier re-grant op, resync gate) | ok 1.29 s |
| `mobile/conformance` | 8 (3 PB-KEY-10 end-to-end, 5 resync) | ok 8.08 s |

## The cross-slice lesson this slice produced

S10 and S14 were implemented in parallel and cherry-picked with **zero textual conflicts**, because
they touched different files. **The build then failed**: S14 changed `swarmmobile.NewApp` to take a
second argument and S10 had added a test calling the old signature. Git cannot see that.

In the S14 implementer's own words, it reported the conflict it could **see** (the one file both
slices edited) rather than the class of conflict it had **created**. **An arity or signature change
makes every caller a merge hazard regardless of which file it lives in**, including callers added
after the surveying grep was run. Cost: one commit (`c7acd7b`). `go build ./...` after every
cherry-pick is mandatory, not advisory.

## Accepted residuals

- **The grant package was split so the phone imports only the wire half** (`internal/remote/grantwire`).
  The machine's registry-sidecar file I/O is **deliberately excluded** from the bound dependency
  closure, per `mobile/deps_allowlist.txt`'s own stated policy. `TestPBBIND0_AllowlistMovedToTheFacade`
  (S8) is what keeps that true.
- **PB-SYNC-7 is not this slice's** — it is S1b's, and its production wiring is a recorded
  cross-slice brick risk. See `remote-phaseB-s1b-evidence.md` and the progress doc.
- **The resync rate bound lives on the phone**, in `mobile/app.go`'s per-stream `resyncAt`. A
  machine-side bound is not part of this slice: the argument is that the resync is unsigned and its
  worst case is a roster republish to a mailbox the caller can already write to. If a later slice
  ever makes the resync do more work, that argument has to be re-made.
- **PB-SYNC-1's three buckets are a property of the current wire.** If a future frame kind gets its
  own sequence space, or if `MailboxResult` ever carries a frame kind, the per-bucket model becomes
  more conservative than necessary — it will over-stale rather than under-stale, which is the safe
  direction, but the tests assert the current shape and would need revisiting.
- **PB-STATE-9's push replay coordinate is still unfilled** (`e649b4b`), which is S12's residual, not
  this slice's — but it lives in the same `State` blob whose schema S10 bumped. See
  `remote-phaseB-s12-evidence.md`.

## Re-audit round 2 — a locked handset destroyed its own epoch key, and PB-KEY-3 got a real entry

An independent review of this slice returned REVISE with two blockers. Both are fixed at `03f87f0`.

### B1 — the ack rule's "transient" class was too small, and S14 widened the hole

`acceptBootstrap` used `opened` as the ack discriminator, and `opened` is false for **any**
`installGrant` failure — including `crypto.ErrKeyAuthRequired` raised because the content-tier KEK
refused. That refusal is **transient** and is the *designed* locked-handset steady state: the loader
deliberately tolerates a locked content tier so the wake tier keeps the relay dialled with nobody
present.

The relay DELETES acked items, and the bootstrap frame is appended once per gateway session. So:
pair, phone wakes with the screen locked, grant acked, relay deletes it, and the user unlocks to
every send failing forever. Reproduced: `acked=true relayAcks=[900]`.

**This is a cross-slice interaction neither slice could see alone.** S10 wrote the ack rule when the
open could not fail that way; S14 made it failable. Both cherry-picked cleanly, so nothing caught it
— the same shape as the constructor-arity break, but semantic, so the build could not see it either.

Fixed by refusing to ack on either custody sentinel. Worth keeping: **the non-ack costs nothing while
locked**, because a keyless phone commits no sealed frame and the relay cursor does not advance past
the bootstrap anyway — it is simply re-offered each poll until the tier opens.

*A conditional worth re-checking if the design changes*: "costs nothing" holds **because** a phone
locked at Resume binds a zero content key, so no sealed frame commits and the relay cursor cannot
advance past the bootstrap. If anything ever makes the content tier re-openable in-process without a
restart, that argument needs revisiting. The correctness of not acking does not change — only the
claim that it is free.

### B2 — PB-KEY-3's terminal state, closed with no clock, no threshold and no wire change

`MarkGrantLost` had zero production callers, so the state was unreachable and its test supplied the
entry by hand. Two obvious paths were **rejected with evidence**, and the reasoning is the valuable
part:

- **"Drained to head, paired, no content key" is insufficient.** The gateway appends once per
  session, so a phone that drains before the daemon has ever reconnected sees exactly this. The
  remedy there is "wait", not "regrant".
- **A durable pairing timestamp does not make it decidable.** What must be exceeded is the RELAY's
  retention cap — configuration asserted by the party this design treats as **hostile**, and unknown
  to the phone. Elapsed time also says nothing about whether the gateway reconnected: delivery comes
  from a *persistent* sidecar, so a machine that was off for eight days delivers a good grant on day
  nine. A timestamp produces false terminal states on exactly the slow-machine case.

The signal that works needs no content key and is already on the only inbound path: **a bootstrap
refused as `ErrGrantReplay` while the phone holds no content key is proof, not inference.** The frame
is tagged plaintext signed by the machine key pinned at pairing, so its arrival proves the gateway is
connected and delivering; the coordinates are ones this phone already consumed; and the phone can
open nothing with what it has. Re-delivery can never help — only a seq-advancing re-grant can.

**It is reached in ordinary use.

> **CORRECTION 2026-07-26 (round-2 audit) — the paragraph above describes PRE-B44 behaviour and is now FALSE.** ADR-007 B44 changed exactly this: the lock no longer destroys the sealed content key, and `grantLossDetected`'s keyless test now also requires `!contentSealed`, which is **true** after an ordinary screen lock. So a screen lock is now **deliberately excluded** from the terminal grant-loss detector — the opposite of what this paragraph claims, and the change was made precisely because presenting a fine phone as permanently lost was the brick B44 closed on its way.
>
> **PB-KEY-3 itself remains MET**: the genuine cross-epoch-rotation grant loss the requirement cares about still reaches the terminal state, because `contentSealed` is false for an epoch the phone never received a key for. What is stale is this file's *narrative*, not the requirement.
>
> **Fourth instance of the class**: a later fix to a *different* requirement (PB-KEY-7/PB-SEC-2, B44) invalidated a claim in an earlier requirement's evidence, and nothing re-derived it. The first three invalidated the requirements themselves; this one invalidated only the evidence — which is worse in one way, because the traceability index goes on citing the file and no check reads its prose.** The lock purge deliberately preserves the grant watermark (the
watermark IS the replay defence) while destroying the content key, so after any screen lock the phone
is keyless at exactly the coordinates the gateway is about to resend.

Fenced both ways, including a false-positive guard — a relay replaying a retired grant to a phone
with a **working** key must not be marked — which fails if the keyless condition is dropped.

*Residual, stated plainly*: this closes the causes that are **terminal**. It does not flag "the relay
purged the frame and the gateway has not reconnected", correctly, because the machine still holds the
sidecar and a reconnect delivers it.

### Two guards that could not fail, both mutation-proven

- The re-grant's strict-coordinate advance was asserted only on a fixture where the **epoch had
  already moved**, so it passed regardless of the sequence number — a neutered allocator left the
  whole repository green. A same-epoch fixture now covers the primary case, which is also the one
  PB-KEY-7 depends on: the purge preserves the coordinates, so ordinary re-delivery of the same
  sidecar is refused as a replay and only a seq-advancing re-grant recovers.
- The schema version could be reverted with everything green. It is now tied to the durable field set
  by a pinned byte-literal per version. That fence fired on its very next integration, catching a
  version bump with no pinned literal.
