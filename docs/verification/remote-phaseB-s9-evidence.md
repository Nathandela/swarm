# S9 evidence — the facade drives the real client (PB-NET-1)

**Commit**: `b31c56b` — five files, +1098/-2. **Requirement**: 1 (PB-NET-1).
**Spec context**: `2971632` recorded the defect before the fix landed; `262b198` recorded the latent
pairing race S9 found while debugging its own scaffolding.

> **RECONSTRUCTED**, 2026-07-25, from the commit, the diff and the tests, re-run at HEAD.

## The requirement, and why a whole slice for one row

> **PB-NET-1**: The real `relay.Client` drives the core through the façade; the `phonesim` mailbox
> seam stays for testability. **Criterion**: integration test — façade + real client + real
> in-process relay: **pair -> read -> ack -> append.**

The four steps had each been driven somewhere, and **the sequence never had**. The composition is
the requirement, and it is the whole of what S9 adds — stated in the test's own header:

- **`internal/phonesim` is not the shipped path.** `Phone.Type` seals with
  `phonecore.SealInputData` and calls `relay.MailboxAppend` **directly**, never entering
  `mobile/commands.go`. So the coalescer, the lease gate, the durable input-seq reservation and
  `sendInputFrame`'s ordering lock — everything a real handset's keystrokes go through — had unit
  tests and **nothing end to end**. Standing class (v), and it had already cost the project twice:
  PB-NET-5's latency harness measures phonesim -> PTY and cannot see a facade regression at all, and
  S11's input-inversion defect shipped partly because its fence guarded a seam production does not
  take.
- **The S8 conformance harness drives the facade over a real relay but SEEDS the paired state**, by
  design — so its `MachineRelayAuthPub` is a fixture rather than something a handshake produced.
- **The two tests that DO run a real handshake** (`TestPBSAS2_...`, `TestPBPAIR5_...`) publish
  `MachineRelayAuthPub: make([]byte, 32)` — a **zero key** — and send nothing afterwards, because
  what they assert is the SAS and the terminal states.

So nothing anywhere proved that **what the pairing handshake teaches the phone is what the phone
then reads, acks and appends over**.

## What the first run found — the most severe defect in Phase B

**A fresh install never learned its machine id.** Found on the *first run* of the first test that
pairs a fresh install and then uses it.

The chain, every link verified independently before a fix was dispatched:

- `mobile/app.go` passes `cfg.MachineID` into `phonecore.Config.Machine`.
- `phonecore/core.go` forwards it to `OpenStore`, where it is **only a load-time filter** —
  `if machineID != "" && f.Machine != machineID { return nil }`. **Never an initialiser.**
- On a fresh directory the file does not exist, so `load()` returns early with the state zero.
- `mobile/pairing.go`'s `pin()` sets `MachineStatic`, `MachineSignPub` and `MachineRelayAuthPub` —
  **never `st.Machine`**.

So `State.Machine` stayed `""` for the life of an install.

**Consequence A**: `crypto.Command.Canonical()` refuses an empty `Machine`, so `TakeControl`,
`Kill`, `Launch` and `ReleaseControl` — **every mutating verb** — fail on a first-launch
pair-then-use.

**Consequence B, and this is the worse one**: `persistState` writes `Machine: ""`, so the **next**
process start compares it against the configured id and **discards the entire durable blob** —
pairing, epoch, content key, relay cursor, durable send-seq ceilings. On Android a process death is
routine. A product that fails every command on first launch is noticed in five minutes; a product
that then silently forgets a working pairing on the first restart presents as *"it randomly loses my
phone"*.

### The fix, and the three places it deliberately does not live

Stamped in **`OpenStore`'s struct literal**, not in `load()`, `Resume` or `pin()`:

- `pin()` has nothing to set it from — **the pairing payload carries no endpoint id**.
- `Resume` would give two answers to one question for any caller that reads custody directly.
- `OpenStore` also covers the **purely in-memory** store, which returns before `load()` is ever
  called.

**Both early returns are covered, including the "blob belongs to a different machine" path** —
without that one, the re-pair that was supposed to self-heal "healed into an identical brick". This
is the edge the obvious fix misses, and it was called out in the progress doc before the fix landed.

### A second defect the naive fix would have shipped

`StateSummary.Restored` began **misreporting** once the id was stamped at load time: it was derived
from the machine id being non-empty, so a phone that had **never paired** would claim to be restored.
It now derives from the machine **relay-auth key**, which only pairing pins and which is the same
predicate the destination lookup uses. The fresh-install probe asserts `Restored` is **false before
pairing**, so the naive derivation cannot come back.

## Per requirement: what proves it

| | Test | What it establishes |
|---|---|---|
| PB-NET-1, the criterion verbatim | `TestPBNET1_TheFacadeDrivesTheRealClientFromPairingThroughAppend` (`mobile/conformance/s9_pbnet1_test.go`) | **pair** — a real `pairing.Machine` responder over the real relay rendezvous, phone running `BeginPairing`/`SAS`/`Confirm`, machine publishing its **real** relay-auth pub so `pin() -> setDestination` targets a mailbox that actually exists and the machine authorises the phone using the routing key **the handshake carried**. **read** — `App.drain`'s real `relay.Client.MailboxRead` into the core's real `AcceptCommit`, surfacing on the facade's own read models. **ack** — the relay *deletes* acked items, so the phone's mailbox depth measured on the real server **is** the ack. **append** — `App.TakeControl` and `App.SendInput`: signed command, lease gate, coalescer, durable `NextInput` reservation, seal-and-append under the bucket lock, opened machine-side by the real `remotegw` openers |
| the regression it found | `TestPBNET1_AFreshInstallsPairingSurvivesTheNextProcessStart` | the durable blob survives the first process restart after a fresh pairing |
| unit half | `TestS9_AFreshInstallsFirstSaveSurvivesTheNextProcessStart`, `TestS9_OpenStoreStampsTheMachineItWasOpenedFor` | the stamp happens in `OpenStore`, on both early-return paths |

## Failing-first evidence (GG-5)

Unusually strong for a slice with no RED commit, because **the RED was a real failure of a real
test on its first execution**, and it was written down *before* the fix
(`2971632 docs: record the fresh-install machine-id defect before its fix lands`). The chain above is
reproducible at `b31c56b^` by reading four named source locations.

**Both new fences deliberately avoid the fixture family that hid the defect** — this is the part
worth keeping:

- the conformance test **does not use the shared harness**, because that harness seeds the very
  field in question;
- the `phonecore` helper **never touches** `State.Machine`, so no fixture can seed past it.

That is the correct response to a class-(v) finding: not "add a test", but "add a test that cannot
be rescued by the fixture that hid it".

## Gates, re-run at HEAD 2026-07-25

`go test -count=1 -run 'TestS9_|TestPBNET1_' ./internal/phonecore/ ./mobile/conformance/` —
**4 tests, all PASS**, `ok internal/phonecore 0.88 s`, `ok mobile/conformance 2.56 s`.

## Accepted residuals

- **A LATENT PAIRING RACE that reports itself as the wrong failure**, found by this slice's
  implementer while debugging its own scaffolding. It is **not** a load flake — it is a real ordering
  bug that discards its own cause. `relay.handleRendezvousClaim` refuses a rendezvous id it has never
  seen, and `pairing.RunDevice` does **not retry** its claim. So a `BeginPairing` that beats the
  machine goroutine's `Create` fails the handshake **terminally**, and the waiting test reports it
  five seconds later as *"the phone never derived a SAS"*, with the actual cause thrown away.
  Reproduced 2 runs in 5 under concurrent agent load.
  **S9 fixed only its own new test** — gating on the machine's `Create` having returned, and making
  the SAS wait fail fast on a terminal pairing state. `mobile/conformance/s9_pbnet1_test.go` is
  clean; **`conformance_test.go`'s `runMachinePairing` still has it**, deliberately left alone.
  Anyone who sees "never derived a SAS" should suspect this before anything else.
- **PB-KEY-10 was still open when this slice shipped, and this test could not have caught it.** The
  PB-NET-1 test generates the epoch keys in-test and hands the content key to `InstallContentKey` —
  *the no-fakes test performs by hand the exact step the facade was missing*. That is the sharpest
  instance of standing class (v) the project has produced, and it was produced by the slice created
  to close that class. S10 closed PB-KEY-10; see `remote-phaseB-s10-evidence.md`.
- **The `phonesim` seam stays**, as the requirement says it should. Everything it skips is inventoried
  in the progress doc under "What phonesim skips, precisely", and every item belongs to a slice that
  has already shipped with unit coverage and no end-to-end coverage. **That list is what S19 must
  actually demonstrate**, and it is why an exit demonstration driven through phonesim would be worth
  very little.
