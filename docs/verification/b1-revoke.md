# b1 — `swarm remote revoke` must verify the relay-side outcome (ADR-007 B120 F3)

> AMENDED BY SH5 (2026-08-22, bead agents-tracker-dtc5): this file's standing claims that no
> pending-purge state exists and that nothing retries the pending arm — including the reference to
> the `ponytail:` honest-ceiling comment at the pending branch — describe the tree BEFORE SH5. The
> deferral is now built: `internal/remote/relaypurge` records the obligation before the local
> revoke, and `swarm remote pair` (refusing to proceed while one is owed) and a later reachable
> `swarm remote revoke` drive it. The transcript below is preserved as the dated evidence it is.

**Status: RED.** The failing tests exist and were run at HEAD `b077104`; no production code has
been changed. This file is the failing-first record required by implementation-goals.md GG-5.

**Owned defect**: ADR-007 B120, finding **F3 (HIGH)** — *"the stolen-handset revoke fails at the
relay, silently, and the relay decides whether it does"* (`docs/adr/ADR-007-remote-access.md`,
line 6699).
**Governing requirement**: ADR-007 **D9** (line 74) — *"device de-authorization + mailbox purge
on revocation (a revoked device keeps neither connectivity nor a drainable pre-rotation
mailbox; an offline-at-revoke machine defers the purge to reconnect)"*.

---

## 1. The defect still exists at HEAD, checked against the source

`cmd/swarm/remote.go`:

| line | what is there |
|---|---|
| `560` | `func runRemoteRevoke(args []string, stdout, stderr io.Writer) int` |
| `582` | `purgeRelayState(stateDir, routingID, stderr)` — **the return value is not the issue: the function has none** |
| `723` | `func purgeRelayState(stateDir, routingID string, stderr io.Writer)` — no error out-parameter, every relay failure ends at `fmt.Fprintf(stderr, ...)` (line 735) |
| `585` | `fmt.Fprintf(stdout, "revoked device %s\n", deviceID)` — unconditional |
| `590` | `return 0` — unconditional |

The doc comment at lines 555-559 states the rule outright: *"EVERY PURGE FAILURE IS A WARNING,
never a nonzero exit."* So the three relay-side outcomes are reported identically at the exit
code and at the one sentence an operator reads:

```
the relay acked the purge     ->  "revoked device X", exit 0   (true)
the relay refused the purge   ->  "revoked device X", exit 0   (false)
the relay was never reached   ->  "revoked device X", exit 0   (false)
```

No pending-purge state exists anywhere in the tree, so D9's *"an offline-at-revoke machine
defers the purge to reconnect"* is unimplemented — the third row above is not a deferral, it is
a drop.

**Why this is HIGH and not cosmetic.** B120 measured what survives a "successful" revoke whose
relay half did not land: the revoked handset **retained** mailbox drain, append into the owner's
machine mailbox, push wake delivery, and a relay re-auth whose `Peer` query said it had **not**
been revoked. Post-B133 the phone is trusted and the wire is the trust boundary, which makes
this verb the product's only safety net for a lost handset.

**Not the same defect as j45x.** `93cc2ec` and the `j45x` bead concern the ANDROID side —
`PhoneSurface.revokeNotice`'s answered arm never firing because the press's `finally` purge nils
`State.OpOutcomes` and destroys the content key. That is the phone-initiated revoke's reply
race. This file is about the machine-side CLI verb, a different process and a different failure.
Neither commit touched `cmd/swarm/remote.go`. `git blame -L 577,591 -- cmd/swarm/remote.go`
dates the whole success path to `870bfc5a` (2026-07-23) and `3b6694f8` (2026-07-25), both
**before** B120 (2026-07-31): the defect is untouched since it was recorded.

**What must NOT change.** The local half of the revocation is durable before the relay is ever
dialled — the device is de-registered and the epoch rotated (2026-07-24 amendment). Both failing
arms below therefore assert the device is **gone** from the registry: a nonzero exit must say
"the relay half is not finished", never "nothing happened".

---

## 2. The failing tests

**File**: `cmd/swarm/b1_revoke_relayverify_test.go` (new; nothing existing was modified).

| test | arm | asserts |
|---|---|---|
| `TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess` | relay **acks** | exit 0, mailbox depth 0, output says `purged` and never `pending` |
| `TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge` | relay **refuses** | exit **nonzero**, mailbox still holds its 3 items, output carries the relay's own reason (`quota`), device gone locally |
| `TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable` | relay **unreachable** | exit **nonzero**, output says `pending`, device gone locally |

**The fixture is real on both sides.** One machine provisioned by the actual verb
(`swarm remote init --relay-url`), a live `skeleton` daemon over it, a real `relay.Server`
holding a genuinely authorized mailbox, and the real `runRemote([]string{"revoke", id})`. Only
the gateway supervisor is faked, through the pre-existing `installFakeSupervisor` seam, so no
test touches launchd or systemd.

The paired device is **synthetic rather than a real `swarmmobile` handset**: what the revoke's
relay half acts on is a routing id, a relay-auth key and a route consent, and those three are
producible directly. The relay verifies the consent for real (`handleAuthorizeDevice` checks the
device's own signature over `relay.ConsentMessage`), so the mailbox the fixture fills is a
genuinely authorized route. The fixture also writes a sealed grant sidecar and stamps the
machine's current epoch, because `skeleton.Serve`'s `reconcilePairedDevices` removes a device
missing either — without that the whole file would have been measuring the "no such device"
refusal instead (it was, on the first run; see §4).

**The refused arm uses a proxy, and the choice is deliberate.** `relay.ErrNotAuthorized` is the
one refusal `purgeRelayState` treats as benign ("no mailbox of ours to empty", `remote.go:720`),
and that ruling is not this slice's to reopen. So `b1RefusingRelay` interposes a websocket proxy
that answers **only** `device_revoke` with a clean `quota_exceeded` — exactly what a real relay
answers past its per-key `OpsPerMin` budget (`internal/remote/relay/harden_test.go` enumerates
`device_revoke` among the rate-limited ops). Auth, `authorize_device` and every append are
served by the real relay behind it, and the refused op never reaches the relay, so the mailbox
it would have emptied provably stays full.

---

## 3. The failing runs, verbatim

At HEAD `b077104`, production untouched.

```
$ go test -run 'TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess' -race -count=1 ./cmd/swarm/
--- FAIL: TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess (0.50s)
    b1_revoke_relayverify_test.go:287: ADR-007 B120 F3: the revoke confirmed nothing about the RELAY side -- the operator reads the same sentence here as when the purge never happened, and only here does it mean the handset is locked out now. output:
        revoked device 06b1e287bf5ffa5bb55a821ad959cd4939368ddeaab53e350fa1cb12e5023e27
        run `swarm remote pair` to pair a device again
FAIL
FAIL	github.com/Nathandela/swarm/cmd/swarm	2.539s
FAIL
```

```
$ go test -run 'TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge' -race -count=1 ./cmd/swarm/
--- FAIL: TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge (0.48s)
    b1_revoke_relayverify_test.go:313: ADR-007 B120 F3: `swarm remote revoke` exit = 0 while the relay REFUSED the purge and the revoked handset's mailbox still holds 3 item(s) it can drain. The exit code is the only thing a script reads, and this verb is the product's whole safety net for a lost handset. output:
        revoked device 38016b1736fb8e7d0b94da741a9e9148ec1282ba53621ce4d904e586a412c640
        run `swarm remote pair` to pair a device again
        remote revoke: the device is revoked, but its relay-side mailbox and push token were not purged: relay: quota exceeded
FAIL
FAIL	github.com/Nathandela/swarm/cmd/swarm	2.161s
FAIL
```

```
$ go test -run 'TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable' -race -count=1 ./cmd/swarm/
--- FAIL: TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable (0.46s)
    b1_revoke_relayverify_test.go:346: ADR-007 B120 F3: `swarm remote revoke` exit = 0 with the relay UNREACHABLE. Nothing was de-authorized and nothing was purged -- the handset keeps mailbox drain, push wake and relay re-auth -- and the command reported the same success it reports when the purge landed. output:
        revoked device 875834f9c98b3b39e12c81fa1ec97b3fa630c40eb21bf552eba165c34b345eca
        run `swarm remote pair` to pair a device again
        remote revoke: the device is revoked, but its relay-side mailbox and push token were not purged: failed to websocket dial: failed to send handshake request: get "http://127.0.0.1:58525": dial tcp 127.0.0.1:58525: connect: connection refused
    b1_revoke_relayverify_test.go:352: ADR-007 D9: the purge is DEFERRED to reconnect here, and the output does not say so, so the owner cannot distinguish it from the purge that actually happened. output:
        revoked device 875834f9c98b3b39e12c81fa1ec97b3fa630c40eb21bf552eba165c34b345eca
        run `swarm remote pair` to pair a device again
        remote revoke: the device is revoked, but its relay-side mailbox and push token were not purged: failed to websocket dial: failed to send handshake request: get "http://127.0.0.1:58525": dial tcp 127.0.0.1:58525: connect: connection refused
FAIL
FAIL	github.com/Nathandela/swarm/cmd/swarm	2.102s
FAIL
```

**Every failure is the missing behaviour, not a compile error and not a fixture bug.** Every
symbol the tests touch already exists, so the package compiles; `go vet ./cmd/swarm/` is clean
and `go build ./...` is green. Each test's *preconditions* — the mailbox filled to 3, the acked
arm's mailbox emptied, the refused arm's 3 items surviving — are `t.Fatalf` guards and **none of
them fired**, so the fixture did its job in all three arms and what is left is the verdict.

The verbatim outputs are also the defect itself: all three arms print the same two lines and
exit 0, and the two failing arms already know what went wrong — the reason is on stderr, where
nothing acts on it.

**No regression.** `go test -race -count=1 ./cmd/swarm/` (whole package, 45.3 s) fails on exactly
these three tests and nothing else:

```
--- FAIL: TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess (0.54s)
--- FAIL: TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge (0.22s)
--- FAIL: TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable (0.23s)
FAIL	github.com/Nathandela/swarm/cmd/swarm	45.313s
```

---

## 4. Probes, so the RED is measured rather than asserted

**Vacuous-pass probe 1 — COSMETIC ONLY.** `remote.go:585` changed to print
`revoked device %s; relay mailbox purged`, no behaviour touched:

```
--- FAIL: TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge (0.26s)
--- FAIL: TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable (0.26s)
```

**1 of 3 passes.** The passer is the acked arm, which is correctly a wording requirement — the
operator has to be able to read that the relay half landed. Both behavioural arms stay red, so
no rewording closes this slice.

**Vacuous-pass probe 2 — BLUNT NONZERO.** Every non-benign purge error exits 1 under one
undifferentiated message:

```
--- FAIL: TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable (0.26s)
    b1_revoke_relayverify_test.go:352: ADR-007 D9: the purge is DEFERRED to reconnect here, and the output does not say so ...
```

**2 of 3 pass**, and the survivor is exactly D9's deferred-purge parenthesis: an unreachable
relay is a legitimate state that a finished purge is not, and collapsing the two is the second
half of the defect. The three arms therefore fence each other — a fix that always exits 0 fails
two, a fix that always exits nonzero fails one, and a fix that only rewords fails two.

Both probes were reverted; `git status` shows `cmd/swarm/remote.go` unmodified.

---

## 5. What GREEN owes (not implemented here)

1. `purgeRelayState` reports its outcome instead of swallowing it, and `runRemoteRevoke` decides
   the exit code on that outcome rather than unconditionally.
2. Three honest verdicts: **purged** (the relay acknowledged the de-authorization and the
   mailbox purge — exit 0), **purge failed** (the relay answered a refusal, whose reason reaches
   the operator — nonzero), **purge pending** (the relay was unreachable, the purge is deferred
   to reconnect — nonzero).
3. The local half stays durable and untouched in all three: de-registration, epoch rotation
   (2026-07-24 amendment), gateway stop, outbound-custody purge.
4. `errRelayNotProvisioned` stays benign — a machine with no relay URL holds no relay-side state
   and must not be told its relay work failed (`TestRemoteRevoke_Removes` and
   `TestRemoteRevoke_StopsTheGateway` are its fences, both green today and both must stay so).
5. The **pending** verdict must not become a new lie: whatever finishes a deferred purge has to
   exist, or the sentence has to say what the owner does instead. B120 records that no
   pending-purge state exists in the tree today.

---

## 6. GREEN — the fix, and the runs that show it

**Status: GREEN.** Production changed in `cmd/swarm/remote.go` only (plus the operator runbook
paragraph that documented the old rule). `cmd/swarm/b1_revoke_relayverify_test.go` was **not
touched**: no expectation was edited, no assertion relaxed, no test bug found.

### 6.1 What changed

| | before | after |
|---|---|---|
| `purgeRelayState(stateDir, routingID string, stderr io.Writer)` | returns nothing; every relay failure ends at `fmt.Fprintf(stderr, ...)` | `purgeRelayState(stateDir, routingID string) (relayPurgeVerdict, error)` |
| `runRemoteRevoke` | `return 0`, unconditional | returns on the verdict; prints one sentence per verdict |

`relayPurgeVerdict` has four values, and every one of them is a distinct thing that happened:

- **`relayPurgeNone`** — no relay state exists for that device (no routing id, no relay
  provisioned → `errRelayNotProvisioned`, or `relay.ErrNotAuthorized`, which says the relay holds
  no pairing of ours to purge). Silent, exit **0**. This is contract item 4's benign path and the
  reason `TestRemoteRevoke_Removes` and `TestRemoteRevoke_StopsTheGateway` are untouched.
- **`relayPurgeDone`** — the relay acknowledged. Exit **0**, and stdout gains one line saying so.
- **`relayPurgeRefused`** — the relay ANSWERED and the answer was no. Exit **1**, the relay's own
  error on stderr.
- **`relayPurgePending`** — the relay was never reached, or reached and answered nothing. Exit
  **1**, named as pending.

The refused/pending split needs to know whether the relay spoke at all, and the dial failure and
the op's refusal return through the same value — so the closure sets `reached` when it runs.
`relay.ErrTimeout` and `relay.ErrConnClosed` are excluded from *refused* deliberately:
`internal/remote/relay/errors.go` defines both as the relay answering **nothing** ("IT IS NOT A
REFUSAL AND MUST NOT BE TREATED AS ONE"), which is the pending state.

### 6.2 The three arms, verbatim, as the operator sees them

Captured through the same rig with a scratch test that printed both streams (deleted after the
run; the pinned tests assert, they do not print).

```
acked        exit=0
  STDOUT:
    revoked device b4b0e21e7f4021964674f919ab8c2ac2fd7614ccf7fbbc208b4892e89ecd55aa
    relay state purged: its mailbox, its push token and its route are gone from the relay
    run `swarm remote pair` to pair a device again
  STDERR: (empty)

refused      exit=1
  STDOUT:
    revoked device a3694de627c6ede92a2d18a9254b88209146d0e54ac5f52f335f98f4b20ae7cd
    run `swarm remote pair` to pair a device again
  STDERR:
    remote revoke: the relay REFUSED to purge this device's relay-side state: relay: quota exceeded
    remote revoke: until that purge lands the handset keeps its relay mailbox, its push wake and
    its route (routing id 88aafb779ce9e785f4f1a228479a5315). Nothing retries it, and this verb
    cannot re-address the device: the local record naming that routing id is already gone.

unreachable  exit=1
  STDOUT:
    revoked device b63bde72d10226cb7db4d7e6ef15c4f2d4567c0fade5a8ccc43f12759eddb715
    run `swarm remote pair` to pair a device again
  STDERR:
    remote revoke: the relay was not reached, so its half of this revocation is PENDING: failed to
    WebSocket dial: failed to send handshake request: Get "http://127.0.0.1:60460": dial tcp
    127.0.0.1:60460: connect: connection refused
    remote revoke: until that purge lands the handset keeps its relay mailbox, its push wake and
    its route (routing id ca1b7dace8fe5b855c9961957b85135c). Nothing retries it, and this verb
    cannot re-address the device: the local record naming that routing id is already gone.
```

### 6.3 §5.5 answered honestly: "pending" does not promise a retry that does not exist

§5 required that the new *pending* verdict not replace one false claim with another. It was
checked rather than assumed, and the check changed the wording:

**"Re-run `swarm remote revoke <id>` once the relay is reachable" is NOT a valid step and is not
printed.** By the time the purge is attempted the daemon has already removed the device, and an
owner-tier `device_revoke` that removes nothing is a refusal —
`internal/protocol/server.go:1358`, `device_revoke: no such device %q; nothing to revoke`, fenced
by `TestRemoteRevoke_UnpairedIDIsRefused`. A second run therefore exits 1 with `no such device`
and never reaches the relay. The routing id it would need is gone with the record.

So the pending sentence states only what is true: what the handset still holds, that nothing
retries it, and that this verb cannot re-address the device — plus the routing id, which is the
only surviving handle on the leftover relay state. The code carries a `ponytail:` comment naming
the ceiling and D9's own upgrade path (persist the routing id, drain it on the machine's next
relay connection — the gateway's connect and this CLI's own dial).

### 6.4 Runs

```
$ go test -run 'TestB120F3_' -race -count=1 -v ./cmd/swarm/
=== RUN   TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess
--- PASS: TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess (0.60s)
=== RUN   TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge
--- PASS: TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge (0.38s)
=== RUN   TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable
--- PASS: TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable (0.44s)
PASS
ok  	github.com/Nathandela/swarm/cmd/swarm	3.988s
```

The exit-0 fences of contract item 4, and the PB-STATE-10 chain that drives the same verb against
a real relay:

```
$ go test -run 'TestRemoteRevoke_' -race -count=1 -v ./cmd/swarm/
--- PASS: TestRemoteRevoke_RequiresOneArg (0.01s)
--- PASS: TestRemoteRevoke_Removes (0.17s)
--- PASS: TestRemoteRevoke_UnpairedIDIsRefused (0.05s)
--- PASS: TestRemoteRevoke_UnpairedRefusalMatchesRegrant (0.04s)
--- PASS: TestRemoteRevoke_StopsTheGateway (0.06s)
ok  	github.com/Nathandela/swarm/cmd/swarm	3.612s

$ go test -run 'TestPBSTATE10' -race -count=1 -v ./cmd/swarm/
--- PASS: TestPBSTATE10_CorruptStateFailsClosedAndNamesTheOwnerSideRecovery (0.59s)
--- PASS: TestPBSTATE10_ThePairRefusalNamesHowToFindAndRevokeTheStrandedDevice (0.35s)
--- PASS: TestPBSTATE10_RevokeNamesTheRePairThatFinishesTheRecovery (0.49s)
--- PASS: TestPBSTATE10_RevokePurgesTheStrandedDeviceRelayState (0.54s)
--- PASS: TestPBSTATE10_RevokePurgesTheMachineSideOutboundCustody (0.51s)
--- PASS: TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset (1.02s)
--- PASS: TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatTheOperatorWasTold (0.71s)
ok  	github.com/Nathandela/swarm/cmd/swarm	6.693s
```

Whole package, and the gates:

```
$ go test -race -count=1 ./cmd/swarm/
ok  	github.com/Nathandela/swarm/cmd/swarm	45.958s

$ go build ./...        -> clean
$ go vet ./...          -> clean
$ golangci-lint run ./cmd/swarm/...
27 issues (errcheck, all pre-existing) -- identical count before and after the change; none in
cmd/swarm/remote.go's new lines.
```

### 6.5 What GREEN did not do

The deferral itself. D9's *"an offline-at-revoke machine defers the purge to reconnect"* is still
unimplemented: no pending-purge state is written and no later relay connection completes one. This
slice makes the command stop claiming otherwise; it does not build the retry. That is the one open
item, recorded here and in the `ponytail:` comment at the pending branch.
