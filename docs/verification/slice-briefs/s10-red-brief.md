# RED BRIEF — Phase B slice S10: staleness, repair, and epoch-grant recovery (9 requirements)

cwd = `/Users/Nathan/Code/swarm/.claude/worktrees/remote-control-research`. Work only there.

You are the TEST AUTHOR (RED). Write ONLY tests plus the minimum scaffolding to compile and fail
**for the right reason**. A separate agent implements; a third reviews.

Your requirements, verbatim in `docs/specifications/remote-phaseB-requirements.md`:
**PB-SYNC-1, 2, 3, 4, 5, 6, 8** and **PB-KEY-3, PB-KEY-4**. Read every row before writing anything.

## What this slice is for

When the phone misses frames — a gap, a purge, a rotation it slept through — something has to
notice and repair it. This slice is that machinery. It is the difference between a phone that
silently shows stale truth and one that knows it is stale and fixes itself.

## Four traps the requirements already name. Do not rediscover them; test them.

**1. Staleness is per SEQ BUCKET; repair is per CHANNEL (PB-SYNC-1).** The v2 spec required "a gap
in one stream marks only that stream stale", and round 2 proved that **impossible**: journal and
terminal frames share one `(sender, epoch)` sequence space, and the `Gap` bit carries no kind. So a
gap cannot tell you *which* stream lost a frame. The requirement was rewritten around what the wire
can actually support. Your tests must not assume a precision the transport does not have.

**2. A journal reseed must REPLACE the cursor, not merge (PB-SYNC-8).** The daemon emits roster
records with `Cursor` **deliberately unset (0)** — a roster record is a set membership, not a
position. Merge a reseed into an existing cache and the designated repair channel becomes a
**no-op**: the phone stays stale, the resync reports success, and nothing fails. That is standing
class (iv) — a requirement satisfiable while the defect ships — and it is written into the spec
precisely because it already nearly happened.

**3. The authorization claim in v1 was FALSE (PB-SYNC-4).** v1 said the resync rides
`requireRemoteAuthz`. It does not: `handleJournalRead` gates on the negotiated `journal` capability
and the kill switch only (`internal/protocol/server.go:1657-1683`). Test what the code actually
does, and if the requirement asks for something stronger, that is a finding.

**4. `actionClass` is a CLOSED switch that fails closed (PB-SYNC-5).** If the resync is
device-signed, a new `Action*` constant must be added **and mapped** in
`internal/skeleton/deviceauth.go:17-26`. An unmapped action is refused — which is the safe
direction, but it means a half-landed change is a silent brick. Slice S12 has just done this same
dance for a push-preference verb (`ActionPushPrefs`); read what it did before inventing a different
shape. The capability-tier consequence must be **decided**, not defaulted.

## PB-SYNC-3 is the fail-closed heart of the slice

`Stale()` clears **only** after a successful reseed **of that stream**, committed atomically with
the matching transport watermark. A failed resync stays stale. The criterion names two distinct
hazards — **optimistic clearing** (marking fresh before the repair lands) and **watermark/coordinate
confusion** (clearing against the wrong stream's position). Write a test for each; they fail
differently and a single test will not distinguish them.

## PB-KEY-3 and PB-KEY-4 are the "phone was asleep" pair

- **PB-KEY-3, epoch-grant recovery**: a grant can currently be lost with **no recovery** — the relay
  refuses appends past the mailbox depth cap (`relay/server.go:743-747`) and `SweepRetention` purges
  items older than `RetentionCap` (**7 days** by default, `relay/config.go:90`). A phone that
  sleeps through both comes back permanently unable to decrypt. Note this interacts with S14a's
  work: `ErrKeyInvalidated` is the *custody* refusal, this is the *delivery* loss, and they must
  stay distinguishable to the user — one says re-authenticate, the other says re-pair.
- **PB-KEY-4, rotation while backgrounded/offline**: it must update the device record's
  `GrantedEpoch`, because `reconcilePairedDevices` **removes any device whose `GrantedEpoch !=
  curEpoch`** on every daemon reconcile. So a rotation the phone slept through does not merely
  desynchronise it — it **unpairs** it.

## PB-SYNC-6: the relay is the declared adversary

Resync must be bounded and non-amplifying: a hostile relay must not be able to drive unbounded work.
The criterion names non-advancing pages terminating (`errStuckPage` discipline) and a **stated** rate
bound. "Stated" means a number someone chose, in the spec or an ADR, not an emergent one.

## Standing defect classes — construct the failing mutation for every check

(i) a guard that cannot fail; (ii) a plausible-but-wrong value hiding a brick; (iii) a test passing
because its subject became unreachable; (iv) a requirement satisfiable while the defect ships;
(v) a fence guarding a path production does not take.

**Class (v) has bitten this project five times**, most recently and most severely: a whole fixture
family seeded `State.Machine`, so nothing noticed that production never sets it — a fresh install
could not issue a single mutating command and lost its entire durable state on the first process
death. **Before you write a test against a seam, check that production actually takes that seam**,
and check what your fixture is seeding that production would have to earn.

A prior RED author probed all 65 of its tests against a do-nothing stub and found **14 passing**,
one of which panicked and hid five more — including that slice's two headline assertions. **Do the
same probe.** Assume your tests have the same rate until you have measured it.

## Do NOT touch

- **`internal/remote/crypto`** — FROZEN. Changing it requires an ADR.
- **`android/`** and the gomobile facade's exported surface — a golden pins it
  (`mobile/testdata/exported_surface.golden`); if you believe a new facade verb is needed, report it.
- `docs/specifications/` — report, and I will amend. Seven slices so far have found a requirement
  unimplementable as written; that is a finding, not an obstacle.

## Shared tree

Other slices are in flight. **Scope your test runs to your own packages** rather than running the
full suite, leave any dirty file outside your scope completely alone and report it, and never
`git checkout` anything you did not write. **Do not commit and do not stage anything** — leave your
work unstaged and I will stage it explicitly.

## Environment

- `golangci-lint` at `/Users/Nathan/go/bin/golangci-lint`.
- Host is an Apple M1 but `/usr/local/bin/go` is x86_64. The reliable check is `go env GOHOSTARCH`
  (= amd64), an **in-process** probe — NOT `uname -m`, NOT `sysctl sysctl.proc_translated` from a
  shell. Both have misled agents here, in opposite directions. Timings pessimistic.
- Relay quotas: `MailboxAppendPerMin: 600`; `mailbox_read`/`mailbox_ack` DO meter against
  `OpsPerMin: 600` while appends do not. A resync that pages aggressively can hit that — if it does,
  that is a finding about the resync, not about the relay.
- A load-sensitive test family fails only under concurrent agent load and passes in isolation; the
  list is in `docs/verification/remote-phaseB-progress.md`. **Re-run in isolation before concluding
  anything is a regression.**

## Deliverable

1. Test files, uncommitted and unstaged.
2. The **verbatim failing-first run**, each failing for the right reason.
3. The **vacuous-pass probe result**: how many of your tests pass against a do-nothing stub, which
   ones legitimately do, and label those in-file so no evidence line can count them as earned.
4. A traceability note: all nine requirements, and which test covers each criterion.
5. The decisions the requirements leave open — the capability tier for any new action, the stated
   rate bound for PB-SYNC-6, and how PB-KEY-3's delivery loss stays distinguishable from S14a's
   custody refusal — with a recommendation and the evidence behind it.
6. Anything unimplementable as written.

Report via SendMessage to "main" — plain text output is NOT visible.
