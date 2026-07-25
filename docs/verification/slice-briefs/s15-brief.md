# BRIEF — Phase B slice S15: which tier seals which state (PB-STATE-9, PB-STATE-6, PB-SEC-10)

cwd = `/Users/Nathan/Code/swarm/.claude/worktrees/remote-control-research`. Work only there.

You are the TEST AUTHOR (RED). Write ONLY tests plus the minimum scaffolding to compile and fail
**for the right reason**. A separate agent implements; a third reviews.

Three requirements, and they are smaller in text than in consequence.

## What I verified before writing this — read it first

`persistState` (`internal/phonecore/state.go:655`) writes **everything in the clear** except the two
epoch keys. The sealed blobs are `WakeKey` and `ContentKey`; every other field goes to disk as plain
JSON — including **`Sessions`, `Snapshots` and `OpOutcomes`, which are decrypted session content**.

So on a locked handset today, the decrypted journal, terminal snapshots and command outcomes sit in
plaintext at rest. PB-KEY-7 purges them from **memory** on lock; nothing purges or seals them **at
rest**. The `android/gate` PB-SEC-1 pair does not catch this: it searches for the epoch keys and the
device private scalars, not for session content.

That is the gap PB-STATE-9 exists to close, and it is why this slice was deliberately ordered after
Android key custody rather than sitting in the state slice (a v2 dependency cycle, recorded in the
requirement itself).

## PB-STATE-9 — the tier split, stated so an implementer cannot pick whichever passes

- **Wake tier**: state the wake path must read **while locked** — the push token and the dedup
  coordinate. Nothing else. The wake path runs with no user present, so anything under this tier is
  reachable without the biometric.
- **Content tier**: send-seq, receive high-waters, and the decrypted caches.

The requirement says in its own words that one undifferentiated "sealed" would let the implementer
pick whichever tier passes. **Your tests are what make that choice unavailable.** The stated
acceptance is: *a locked-device process can read only the wake-tier state*.

Think carefully about what "can read" means as a test. A test asserting a Go accessor returns empty
is weaker than one asserting the bytes on disk do not yield the content — this project has been
bitten repeatedly by acceptance that reads declarations rather than bytes, and the existing
`android/gate` tests exist precisely because a Kotlin test could have been made green by writing
`sealedByKeystore = true` next to a file in the clear.

**A trap worth naming**: the send-seq ceiling is content-tier by this requirement, but the phone
reserves send-seqs on paths that may run while locked. If sealing it under the content tier bricks
the wake path, that is a finding about the requirement, not something to quietly resolve by moving
the field to the wake tier. Report it.

## PB-STATE-6 and PB-SEC-10 — the Android half

`allowBackup=false` plus backup rules, asserted on the manifest, and state must not be extractable
via ADB backup. PB-STATE-6 is the joint assertion: sealed per the tier split above **and** excluded
from backup.

**Do not write a test that appears to prove a real ADB backup was attempted and failed.** PB-E2E-5,
the physical-handset gate, is DEFERRED and may NOT be reclassified. Assert the manifest and the
rules; say plainly in-file that this models configuration, not a device.

## Standing defect classes — construct the failing mutation for every check

(i) a guard that cannot fail; (ii) a plausible-but-wrong value hiding a brick; (iii) a test passing
because its subject became unreachable; (iv) a requirement satisfiable while the defect ships;
(v) a fence guarding a path production does not take.

Class (iv) is the one aimed straight at you: an at-rest inventory that reports "sealed" while a field
is in the clear. Read the bytes.

Also run the **vacuous-pass probe**: stub the surface and run every test against it. A prior author
found 14 of 65 tests passing that way. Label any that legitimately pass, in-file.

## Do NOT

- **`internal/remote/crypto` is FROZEN.**
- The custody seam already exists (slice S14): `swarmmobile.NewApp` takes a `KeyCustody`, and
  `phonecore.Resume` takes a per-tier `Sealer`. **You need no new crossing** — ADR-007 B8 says the key
  crossing is single and inbound and may only ever narrow.
- `TestS14A_TheCleartextSealerHasNoCallSitesLeft` pins the insecure sealer at **zero** call sites. Do
  not reintroduce one.
- Do not edit `docs/specifications/`. Report and I will amend.
- **Do not commit and do not stage anything.** Leave your work unstaged; I stage it explicitly.

## Environment

- Android SDK at `/usr/local/share/android-commandlinetools`; gate is `./gradlew lint test`.
- `golangci-lint` at `/Users/Nathan/go/bin/golangci-lint`.
- Host is an Apple M1 but `/usr/local/bin/go` is x86_64. The reliable check is `go env GOHOSTARCH`
  (= amd64), an **in-process** probe — NOT `uname -m`, NOT `sysctl sysctl.proc_translated` from a
  shell. Timings pessimistic.
- Every latency-budget test in this repo is load-sensitive and passes in isolation; the full list is
  in `docs/verification/remote-phaseB-progress.md`. Re-run in isolation before calling anything a
  regression.
- Another agent is working in the shared tree. Scope your runs to your own packages, leave anything
  dirty outside your scope alone, and never `git checkout` what you did not write.

## Deliverable

1. Test files, uncommitted and unstaged.
2. The **verbatim failing-first run**, each failing for the right reason.
3. The vacuous-pass probe result, with legitimate passers labelled in-file.
4. **The at-rest inventory you measured**: for each `State` field, which tier it lands under after
   your tests pass, and how you assert it from the bytes rather than from an accessor.
5. Whether the send-seq trap above is real, and anything else unimplementable as written.

Report via SendMessage to "main" — plain text output is NOT visible.
