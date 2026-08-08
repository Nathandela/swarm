# A1c — the approve round trip: evidence

**Workpackage W-APPROVE.** Complete the path a tap on an approval card travels, so a phone
can answer a real approval.

The two ends already existed and nothing between them did:

- **Machine end** — `internal/skeleton/approval.go:approveInteraction` validates the
  ADR-007 D7 binding tuple (agent instance, `content_hash`, daemon-authoritative
  `expires_at`), refuses a decision the request never offered, classifies allow/deny from
  the adapter's verdict (IS-RES-1), and resolves the approval on all five paths. It had
  **zero production callers** — its own `ponytail:` comment said so: *"opForAction refuses
  an approve one hop short of the daemon."*
- **Phone end** — `mobile.PendingApprovals` / `mobile.ReadTranscript` expose the card, and
  `phonecore.ItemStore` folds `approval_resolved` so an answered card leaves the pending
  set. `PendingApprovals` was documented **READ ONLY by construction**, and
  `mobile/interaction_screencoverage_test.go` adversarially pinned the absence of any
  deciding verb.

Between them were four independent breaks:

| # | Break | Layer |
|---|---|---|
| 1 | no `OpApprove`, no `handleControl` arm, no backend seam to `approveInteraction` | `internal/protocol` |
| 2 | `opForAction` refused `approve` one hop short of the daemon, sealing no reply | `internal/remotegw` |
| 3 | no signer for the approve tuple, no envelope carrying an `ApproveReq` | `internal/phonecore` |
| 4 | no facade verb; the surface was pinned answer-less | `mobile` |
| 5 | `coreAPI` was not an approver, so even a routed approve found nobody | `internal/skeleton` |

**Normative:** `docs/specifications/interaction-schema.md` — IS-LIFE-2 (exactly one
resolution), IS-LIFE-3 (retention exemption), IS-LIFE-4 (a phone approval is the EXISTING
signed `ActionApprove`; the decision itself is unsigned), IS-APR-1 (`item_id` IS
`interaction_id`; `operation_id` is never equal to it), IS-APR-2 (`content_hash` and
`expires_at` echoed **verbatim**, never computed), IS-APR-3, IS-RES-1. ADR-007 **D4** (no
remote-class mutating op executes without a valid device signature) and **D7** (the signed
tuple's one content slot is spent on the interaction content; an approve is *"never
translated into a blind keystroke"*). B43 (live-only, never queued).

---

## What was built

**Wire carriage.** `protocol.OpApprove` + `clientConn.handleApprove`, dispatched from
`handleControl`. `schema.RemoteCommand.Approve *ApproveReq` — the ONE wire field IS-LIFE-4
adds; it adds no signed action, because `schema.ActionApprove` already existed and
`skeleton/deviceauth.go` already classed it `device.ActionApprove`.

**The signature discipline is its siblings'.** `handleApprove` goes through
`requireRemoteAuthz(c, ActionApprove, c.SessionID, hash)` exactly like kill / launch /
device_revoke / take_control — kill switch first, then `operation_id`, then
`device_id`/`device_sig`/`expires_at` over the canonical tuple. `hash` is the **decoded
wire `content_hash`**, so ADR-007 D7's content slot carries the interaction content and a
gateway that swaps the hash to redirect an approval breaks the signature instead. Three
structural refusals precede authorization: a missing body, a body naming a session the
command does not, and a `content_hash` that is not 32 bytes of hex (`SHA256("")` is a valid
digest — handleTakeControl's empty-gate-token trap, for the other content slot).

**Gateway route.** `opForAction` gained its `ActionApprove` arm, under launch's rule: a
command whose body was stripped is refused loudly rather than forwarded as a zero
`ApproveReq` (which the daemon would refuse `stale_approval`, telling a user their card is
out of date about a frame that merely lost its payload). `CommandForwarder.ForwardCommand`
collapsed from `(op, sessionID, cmd, launch)` to `(op, rc protocol.RemoteCommand)` — the
shape `LeaseRouter.Begin` already had — rather than growing a fourth nil-by-default
parameter.

**Phone core.** `SignApprove` (decodes the echoed `content_hash` into the signed tuple;
refuses a value it would have to invent) and `SealApproveEnvelope`. The derivation lives in
phonecore for the reason `mobile/commands.go` already records for the gate token:
re-deriving it at the call site produces a signature the daemon rejects with no compile
error and no message naming the cause. `ItemStore.PendingApproval(session, itemID)` reads
§3.5's three daemon-authoritative fields off the stored card — the ONE place phonecore
decodes per-kind item fields, because the binding tuple is not rendering, it is wire
content the phone must reproduce byte for byte.

**Facade.** `App.Approve(session, itemID, decisionID string) (*Op, error)` — three flat
strings, gomobile-safe. **The binding tuple is deliberately not a parameter**: a signature
that accepted `content_hash`/`expires_at` from a caller is a signature that invites
computing them, which IS-APR-2 forbids. Live-only and never queued (B43).

**Assembly.** `coreAPI.ApproveInteraction` satisfies `protocol.InteractionApprover`,
delegating to `Daemon.approveInteraction` through a func field wired in `Serve` (the
`sampleFn`/`captureFn` shape, not a back-pointer to the whole assembly). An unwired
approver **refuses**: replying OK dismisses the card on every surface (IS-LIFE-2) while the
CLI stays blocked.

---

## RED / GREEN, per layer

TDD order was strict: each layer's test was written and run to a verbatim failure before
any of its implementation existed.

### Layer 1 — `internal/protocol`

`internal/protocol/approve_test.go` (8 tests: authorized path + tuple contents, authenticator
rejection, kill switch first, bodyless body, cross-session body, unhashable `content_hash`,
the daemon's `stale_approval` reaching the phone with its code, and a backend that cannot
answer).

RED — `go test ./internal/protocol/ -run 'TestIsLife4'`:

```
# github.com/Nathandela/swarm/internal/protocol [github.com/Nathandela/swarm/internal/protocol.test]
internal/protocol/approve_test.go:78:7: undefined: OpApprove
FAIL	github.com/Nathandela/swarm/internal/protocol [build failed]
FAIL
```

GREEN — same command after `OpApprove`, `InteractionApprover` and `handleApprove`:

```
ok  	github.com/Nathandela/swarm/internal/protocol	0.755s
```

### Layer 2 — `internal/remotegw`

`internal/remotegw/approve_route_test.go` (forwarded as `OpApprove` with the signature
untouched and the body intact, reply sealed back; a bodyless approve refused loudly and
never forwarded).

RED — `go test ./internal/remotegw/ -run 'TestIsLife4'`:

```
# github.com/Nathandela/swarm/internal/remotegw [github.com/Nathandela/swarm/internal/remotegw.test]
internal/remotegw/approve_route_test.go:52:3: unknown field Approve in struct literal of type protocol.RemoteCommand
internal/remotegw/approve_route_test.go:116:13: fwd.approves undefined (type *fakeForwarder has no field or method approves)
internal/remotegw/approve_route_test.go:117:110: fwd.approves undefined (type *fakeForwarder has no field or method approves)
internal/remotegw/approve_route_test.go:119:14: fwd.approves undefined (type *fakeForwarder has no field or method approves)
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
FAIL
```

GREEN:

```
ok  	github.com/Nathandela/swarm/internal/remotegw	0.950s
```

### Layer 3 — `internal/phonecore`

`internal/phonecore/command_approve_test.go` (the signed tuple binds the item's own hash;
a hash the phone would have to invent is refused; the sealed envelope carries the body the
gateway rebuilds `Control.Approve` from; the binding tuple is read off the held card, and
is refused for an absent, a resolved, and a malformed card).

RED — `go test ./internal/phonecore/ -run 'TestIsLife4'`:

```
# github.com/Nathandela/swarm/internal/phonecore [github.com/Nathandela/swarm/internal/phonecore.test]
internal/phonecore/command_approve_test.go:51:14: undefined: SignApprove
internal/phonecore/command_approve_test.go:51:30: undefined: ApproveInput
internal/phonecore/command_approve_test.go:86:16: undefined: SignApprove
internal/phonecore/command_approve_test.go:86:32: undefined: ApproveInput
internal/phonecore/command_approve_test.go:106:14: undefined: SignApprove
internal/phonecore/command_approve_test.go:106:30: undefined: ApproveInput
internal/phonecore/command_approve_test.go:121:14: undefined: SealApproveEnvelope
internal/phonecore/command_approve_test.go:179:13: s.PendingApproval undefined (type *ItemStore has no field or method PendingApproval)
internal/phonecore/command_approve_test.go:194:16: s.PendingApproval undefined (type *ItemStore has no field or method PendingApproval)
internal/phonecore/command_approve_test.go:197:16: s.PendingApproval undefined (type *ItemStore has no field or method PendingApproval)
internal/phonecore/command_approve_test.go:197:16: too many errors
FAIL	github.com/Nathandela/swarm/internal/phonecore [build failed]
FAIL
```

GREEN for the new tests, then a SECOND, unplanned RED from an existing fence — PB-GW-6's
producer sweep, doing exactly the job it was written for:

```
--- FAIL: TestPhoneSeals_TheSweepCoversEveryProducerInThePackage (0.01s)
    issuedat_test.go:130: command.go declares SealApproveEnvelope, which the IssuedAt sweep does not cover: PB-GW-6 says EVERY phone -> machine seal stamps IssuedAt, and an uncovered producer that forgets the stamp is refused by PB-GW-2's 10-minute bound forever, silently
FAIL	github.com/Nathandela/swarm/internal/phonecore	7.786s
```

`SealApproveEnvelope` was added to the sweep's covered set (it seals through the shared
`sealPhoneFrame` funnel, so it already stamped). Full package:

```
ok  	github.com/Nathandela/swarm/internal/phonecore	8.616s
```

### Layer 4 — `mobile`

`mobile/approve_test.go` (live-only and never queued; a card the handset cannot answer is
refused locally; an empty decision is refused; an answerable card reaches the transport).

RED — `go test ./mobile/ -run 'TestIsLife4'`:

```
# github.com/Nathandela/swarm/mobile [github.com/Nathandela/swarm/mobile.test]
mobile/approve_test.go:67:15: a.Approve undefined (type *App has no field or method Approve)
mobile/approve_test.go:75:10: assignment mismatch: 1 variable but a.PendingOpCount returns 2 values
mobile/approve_test.go:89:17: a.Approve undefined (type *App has no field or method Approve)
mobile/approve_test.go:99:17: a.Approve undefined (type *App has no field or method Approve)
mobile/approve_test.go:114:17: a.Approve undefined (type *App has no field or method Approve)
mobile/approve_test.go:144:15: a.Approve undefined (type *App has no field or method Approve)
FAIL	github.com/Nathandela/swarm/mobile [build failed]
FAIL
```

Then the three checked-in ledgers fired, which is their whole purpose:

```
--- FAIL: TestPBBIND3_NoUntracedEntryPoint (0.22s)
    coverage_test.go:208: PB-BIND-3: 1 exported entry point(s) appear in no screen_coverage.tsv row:
        	App.Approve
--- FAIL: TestPBBIND7_ExportedSurfaceMatchesTheGolden (0.22s)
    golden_test.go:54: PB-BIND-7: the exported surface drifted from the pinned contract.
        REMOVED (breaks the Android app):
        	(none)
        ADDED (new API, must be traced in screen_coverage.tsv):
        	method App.Approve(string, string, string) (*Op, error)
        If the change is intended and reviewed, re-run with -update-surface and justify the diff in the slice evidence.
--- FAIL: TestInteraction_TheTranscriptVerbsAreTracedToFacadeMethods (0.23s)
    interaction_screencoverage_test.go:66: PB-BIND-3 COVERAGE FAILURE: screen element "transcript.approve" (ADR-009,PB-APP-2,PB-APP-3) has no row in screen_coverage.tsv. ADR-009 moves the product's main screen onto this model, so a transcript verb no row traces is the whole feature untraceable to a requirement
FAIL	github.com/Nathandela/swarm/mobile	17.364s
```

Answered by: a `transcript.approve` row in `mobile/screen_coverage.tsv`, the element added
to `requiredScreenElements` (`mobile/coverage_test.go`), an `App.Approve` row in
`android/unbound-verbs.tsv`, and the golden re-pinned with `-update-surface`. The **only**
line the golden gained is the intended one:

```
+method App.Approve(string, string, string) (*Op, error)
```

GREEN:

```
ok  	github.com/Nathandela/swarm/mobile	14.849s
ok  	github.com/Nathandela/swarm/android/gate	5.236s
```

### Layer 5 — the round trip, `internal/skeleton`

`internal/skeleton/approve_roundtrip_e2e_test.go` enters where a **user** does —
`swarmmobile.App.Approve` — over the real rig: real relay server, real gateway in a
**separate process**, real daemon, real device signature, real Claude Code corpus replayed
through the production capture path.

RED, with layers 1–4 already green and only the assembly seam missing — the approve was
authored, sealed, relayed, opened and forwarded, and the daemon refused it because
`coreAPI` was not an `InteractionApprover`, so no resolution ever came back:

```
--- FAIL: TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval (51.67s)
    approve_roundtrip_e2e_test.go:96: timed out after 45s: the approval_resolved for 01KZGDYC8ZEARA2Y461T1R5MCC reached the phone
        machine sessions:
          ep-83e463db/m7b2u6nkzlnbkbfz group=working
FAIL	github.com/Nathandela/swarm/internal/skeleton	52.719s
```

GREEN after `coreAPI.ApproveInteraction` + `d.api.approve = d.approveInteraction`:

```
=== RUN   TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval
--- PASS: TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval (6.81s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	7.707s
```

What it asserts, in order: the phone's `Approve` returns an `Op` whose `Action` is
`approve` and whose `OperationID` is not the interaction id (IS-APR-1); the
`approval_resolved` reaches the phone with `decision: allowed` (from the adapter's verdict,
IS-RES-1), `by: phone`, and the phone's own `operation_id` echoed (§3.6); the card leaves
`PendingApprovals`, which is IS-LIFE-3's retention exemption lifting, while the request
item stays in the transcript marked `Resolved`; a second tap on the same card is refused
(IS-LIFE-2 spends exactly one resolution); and a decision id the card never offered is
**not** refused by the phone — it reaches the daemon, which refuses it and leaves the card
pending, because §3.5 keeps the offered set the machine's.

---

## The test this slice deliberately changed, and why

`mobile/interaction_screencoverage_test.go`'s
`TestInteraction_TheFacadeCannotAnswerAnApproval` banned the facade from exporting any verb
named `approve` / `answerapproval` / `decide` / … Its stated reason was that *"answering an
approval is IS-LIFE-4's signed ActionApprove with an ApproveReq wire body no slice has
built; a verb here today can only send a refusal"*, and its final sentence named its own
expiry: *"When the ApproveReq slice lands it will add the verb, its row and its own guard;
until then the absence is the contract."*

This is that slice. The premise the ban rested on is now false, so keeping it would enforce
a condition that no longer exists against the requirement it was protecting.

It is **replaced, not deleted**, by `TestInteraction_TheFacadeAnswersAnApprovalAndNothingElse`,
which is not weaker:

- it now **requires** `App.Approve` to exist (the old test could only forbid);
- it keeps the danger the old test was really defending — ADR-007 D7 and IS-LIFE-6, *the
  phone must never author the approving keystroke* — as a ban on the verb names an
  implementer reaching for that shortcut would actually type
  (`approvekeystroke`, `answerprompt`, `sendapproval`, `approvewithkey`, …). That danger
  did not expire with the wire body; it got sharper, because a screen now has a button.

Two other existing tests were touched **mechanically only**, with no assertion changed:

- `internal/phonecore/issuedat_test.go` — the new seal producer added to PB-GW-6's covered
  set, which is the fence's designed response to a new producer (see the RED above).
- eight `ForwardCommand` call sites in `internal/skeleton/*_test.go`, one fake in
  `internal/remotegw/cursoradopt_test.go`, `push_prefs_test.go`, `command_loop_test.go`
  and `mobile/conformance/s16_pushprefs_test.go` — the seam signature collapse. In every
  case the old `sessionID` argument was already `cmd.Session`, so the rewrite is exact.

---

## Quality gates

```
go build ./...        clean
go vet ./...          clean
gofmt -l              no file this slice touched is listed (25 pre-existing, all untouched)
golangci-lint         58 issues, all pre-existing (was 59; one SA4001 introduced here was fixed).
                      No finding names any symbol this slice added.
go test -race ./internal/protocol ./internal/remotegw ./internal/phonecore
                      ok / ok / ok  (29.8s / 36.0s / 28.5s)
```

`go test ./... -count=1`: every package green except one **pre-existing load flake**,
which this change cannot reach:

```
--- FAIL: TestRunShim_LaunchesAgentPersistsAndLeadsSession (12.37s)
    role_test.go:112: shim pid 63166 never became its own session leader (getsid != pid) — setsid was not guaranteed; stderr:
FAIL	github.com/Nathandela/swarm/cmd/swarm
```

Why it is not this change's: it is a 3-second wall-clock poll
(`becomesSessionLeader(pid, 3*time.Second)`, `cmd/swarm/role_test.go:110`) waiting for a
freshly spawned `swarm shim` to call `setsid`, and under a fully parallel `go test ./...`
— several e2e rigs, a relay server, a gateway subprocess — 3 seconds is not enough to get
scheduled. **No file under `cmd/` or on the shim process-role path is touched by this
slice.** `go test ./cmd/swarm/ -run TestRunShim_LaunchesAgentPersistsAndLeadsSession
-count=5` is green (27.4s).

Packages this change touches, all green: `internal/protocol`, `internal/protocol/schema`,
`internal/remotegw`, `internal/phonecore`, `internal/skeleton`, `mobile`,
`mobile/conformance`, `android/gate`.

---

## What this slice does NOT do, stated rather than left to be inferred

- **It does not apply the decision to the CLI.** Writing the adapter's `DecisionAction`
  back on the pending hook (ADR-010 §4) is the producer's, and `approveInteraction`'s own
  comment has always said so. What this slice removes is the *route*, which is why that
  function had no production caller.
- **It does not touch `android/` Kotlin.** A parallel package owns the approval card.
  `App.Approve` is therefore bound-and-uncalled, ledgered in `android/unbound-verbs.tsv`
  with that reason. The AAR is a gitignored local artifact (`android/build-aar.sh`), so the
  Kotlin binding does not exist until someone reruns that script — which is exactly the
  "Go half lands first so the screen is written against a facade that already answers"
  pattern `App.MachinePresence`'s row records.
- **It adds no offline queue.** B43 stands: with no live link `Approve` refuses with the
  offline class and seals nothing.
- **It adds no replay-dedup store for approve.** A re-delivered approve finds the approval
  already resolved and is refused `stale_approval` by `approveInteraction`'s first case —
  the correct answer to a replay, failing closed by construction rather than by a second
  durable store.
