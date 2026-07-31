# BRIEF — Phase B slice S19: the exit demonstration (PB-E2E-1/2/3/4)


> **AMENDED 2026-07-31 (ADR-007 B133) — this brief is a DATED INSTRUCTION and parts of it would now
> instruct wrongly.** The trust boundary is the wire between phone and computer; all phone-side user
> authentication is removed with its code. **PB-SEC-2 is VOID; PB-KEY-2, PB-KEY-7, PB-KEY-8,
> PB-APP-7, PB-PUSH-4 and PB-E2E-5 are NARROWED.** The brief is left unedited as the record of what
> the implementer was told. Do not hand it to anyone as current instructions without reading B133
> and `docs/specifications/remote-phaseB-deauth-plan.md` first.
>
> Specific to this brief: the PB-E2E-5 instruction below is unchanged in spirit — **"may not be
> reclassified" still binds.** B133 is not a reclassification: real biometrics leaves the gate
> because the feature leaves the product, and real camera, real FCM, Doze and hardware Keystore
> attestation all stay deferred and stay in the gate.

cwd = `/Users/Nathan/Code/swarm/.claude/worktrees/remote-control-research`. Work only there.

Four requirements. This is the slice that decides whether Phase B is real, and 27 of the 29 slices
lead here — every other slice's exit criteria are demonstrated through this one.

## PB-E2E-1 is the whole point, and its hard part is a NEGATIVE

> A Go end-to-end test with **no fakes and no `phonesim` seam**: real relay, real client, real
> façade, real gateway, real daemon — pair -> observe -> launch -> take_control -> type -> revoke.
> Passes under `-race`. **Explicitly forbids the injected mailbox seam.**

`internal/phonesim` is the phone stand-in nearly every integration test in this repo drives, and it
**never constructs a `phonecore.Core` at all.** I have the measured inventory of what that skips, and
it is the list of properties with no end-to-end coverage anywhere:

- durable state entirely — no `Store`, no `State`, no `Save`
- the **durable** send-seq reservation (it uses the plain in-memory allocator, not the durable one),
  so burned-block and gap-absorption are never exercised end to end
- the durable receive transaction — no commit, no durable high-water, no relay cursor, no persisted
  stale flags
- **the lease gate** — keystrokes go out with no confirmed lease
- input coalescing
- the reconcile fail-closed gate
- skew, the op queue, and the undelivered ledger
- the machine id, which it takes from its own config rather than from durable state

**Every one of those belongs to a slice that has already SHIPPED.** They have unit coverage and
nothing else. That is what this test exists to cover, and it is why an exit demonstration routed
through `phonesim` would prove almost nothing.

## Two things already learned that will save you a day

**The "no fakes" test can still paper over a missing production path.** This has already happened
here, and it is the single worst defect the phase produced: an integration test built from real
components generated the epoch keys in-test and installed the content key by hand, so nobody noticed
the app had **no way to obtain one in production** — the phone could not receive a key at all. The
test was real; the input was supplied. **For every value your test provides, ask where it comes from
in production.** If the answer is "the test", you have found a hole, not built a fixture.

**There is a latent pairing race that reports itself as the wrong failure.** The relay refuses a
rendezvous id it has never seen and the device side does not retry its claim, so a `BeginPairing`
that beats the machine's `Create` fails **terminally** and surfaces five seconds later as "the phone
never derived a SAS", cause discarded. Two existing tests still have it. Gate on the machine's
create having returned, and fail fast on a terminal pairing state.

## PB-E2E-2 — the emulator smoke, with a real force-stop

APK installs, pairs against a local relay and daemon, SAS matches, observes, takes control, types —
**including one real `adb shell am force-stop` mid-session**, which was an upgrade from "one process
death" because force-stop is strictly stronger. The AVD `swarmtest` exists and the toolchain is
present; see the build-environment section of `docs/verification/remote-phaseB-progress.md` for the
exports, which are NOT on `PATH` by default.

**PB-E2E-5 remains DEFERRED and may not be reclassified**: no real biometrics, no real camera, no
real FCM delivery, no Doze, no hardware Keystore attestation. An emulator is not a handset. Say so
in-file.

## PB-E2E-3 — evidence that states what it PROVES

Per-slice evidence files with a RED-first run each, and **each file states what it proves, not
merely that it exists**. A backfill is already in flight for nine slices that shipped without one
(56 of 98 shipped requirements had no evidence file). Check
`docs/verification/remote-phaseB-traceability.md` for the current state before assuming a gap.

Your job here is the *criterion*, not the backfill: verify each evidence file actually evidences its
slice's requirements, and report any that assert something its tests do not establish.

## PB-E2E-4 — no Phase A regression at any slice boundary

Full suite plus the four gates green. **Every latency-BUDGET test in this repo is load-sensitive**
and passes in isolation; the list is in the progress doc. A full-suite sweep is precisely the load
condition that moves them, so a budget failure in a `go test ./...` run is not automatically a
regression — re-run it in isolation before recording it as one. Equally, do not use that as a
blanket excuse: the list is specific.

## An opportunity recorded for you

S9's harness makes timing the **real facade path** roughly a twenty-line job, and PB-NET-5's existing
latency evidence measures phone-core-to-PTY rather than phone-to-PTY, because its harness drives
`phonesim` and never enters the facade. That gap is recorded with S19 as owner. Two caveats from the
author: measure the first burst only (a later one deliberately eats a coalescing window), and the
machine-side drain polls at 20 ms so the resolution is coarse.

## Standing defect classes — construct the failing mutation for every check

(i) a guard that cannot fail; (ii) a plausible-but-wrong value hiding a brick; (iii) a test passing
because its subject became unreachable; (iv) a requirement satisfiable while the defect ships;
(v) a fence guarding a path production does not take.

Class (v) has bitten this project seven times and this slice is its natural home: an exit
demonstration that quietly routes around the real path is the most expensive possible instance.

## Do NOT

- **`internal/remote/crypto` is FROZEN.**
- Do not weaken or delete `phonesim` — it stays for testability (PB-NET-1 says so explicitly). Just
  do not route the exit demonstration through it.
- Do not edit `docs/specifications/`. Report and I will amend — five requirements in this phase have
  turned out unimplementable as written, and each was a finding.
- **Do not commit and do not stage anything.** Leave your work unstaged; I stage it explicitly.

## Deliverable

1. The end-to-end test and the emulator smoke, uncommitted and unstaged.
2. Its verbatim run, including under `-race`.
3. **The seam inventory**: every component in the chain, real or fake, and for each fake a
   justification. The bar is "no fakes"; anything you could not make real is a finding.
4. **For every value the test supplies, where it comes from in production.** If any answer is "the
   test", say so loudly.
5. Your PB-E2E-3 audit: which evidence files do not evidence what they claim.
6. Anything unimplementable as written.

Report via SendMessage to "main" — plain text output is NOT visible.
