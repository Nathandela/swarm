# BRIEF — Phase B slice S17: the phone's push client (PB-PUSH-4, PB-PUSH-9)

cwd = `/Users/Nathan/Code/swarm/.claude/worktrees/remote-control-research`. Work only there.

You are the TEST AUTHOR (RED). Write ONLY tests plus the minimum scaffolding to compile and fail
**for the right reason**. A separate agent implements; a third reviews.

Two requirements, both on the **phone** side. The machine side already shipped (slice S12): the
gateway-side trigger, the FCM sender, the durable preference, and the signed preference verb all
exist and are wired.

## PB-PUSH-9 carries its own warning, and you should treat it as the point of the slice

The requirement says, verbatim: *"A façade method can exist while no Android code ever calls it."*

That is this project's standing defect class (v) written into the requirement text, and it has
already happened twice in Phase B — once where a fully-tested FCM sender had **zero production
callers** so push would not have existed on a real machine, and once, far worse, where the phone
could not obtain an epoch key at all because the only code that opened the delivering frame was the
test simulator.

So: **for every value and every call in your tests, ask where it comes from in production.** A test
that drives a facade method proves the method works. It does not prove Kotlin calls it. Fence the
call, not just the callee.

The lifecycle to cover: initial `getToken`, `onNewToken` rotation, **re-registration on every
authenticated reconnect**, deletion on revoke/disable, and correct behaviour across process death
and app upgrade. The stated acceptance is an end-to-end test with a **fake FCM and a real relay**:
rotate the token and assert delivery still works; restart the relay and assert re-registration
restores it.

Useful context from S12: relay push tokens **are** persisted now, with deletion and revocation
persisting too — so a relay restart no longer forgets a token. PB-PUSH-9's reconnect re-registration
is still required, and note the asymmetry recorded during S12: reconnect re-registration only covers
a phone that is **already awake**, which is the case that did not need a push.

## PB-PUSH-4 is a security requirement wearing UI clothing

A locked device must render a **content-free** notification and must **never decrypt session content**
(PB-KEY-2). Lock-screen redaction and notification-channel privacy must be set.

Two things make this sharper than it reads:

- The wake payload is deliberately content-free and a constant 78 bytes with zeroed key ids
  (ADR-007 B20), so there is nothing in it to render even if the app wanted to. Your test should
  prove the app does not go **fetch** content to fill the notification while locked — that is the
  reachable defect, not the payload.
- The custody seam now refuses: the content tier is biometric-gated and a locked read returns
  `ErrKeyAuthRequired`. So "never decrypts with a locked device" has a real mechanism behind it. A
  test that only asserts a generic string was rendered is weaker than one that asserts **no content
  unseal was attempted**, and weaker still than one that would fail if the app started attempting it.

## HARD CONSTRAINT — the physical-handset gate stays deferred

**PB-E2E-5 may NOT be reclassified.** There is no Apple or Google account here. Write nothing — no
test, no comment, no evidence claim — that appears to cover **real FCM delivery, real Doze, a real
handset, or a real biometric prompt**. Robolectric models **policy**; a fake FCM endpoint models the
**protocol**. Every file you write should say so in its own words, as S12's push files do. If a
requirement seems to demand real delivery, STOP and report.

## Standing defect classes — construct the failing mutation for every check

(i) a guard that cannot fail; (ii) a plausible-but-wrong value hiding a brick; (iii) a test passing
because its subject became unreachable; (iv) a requirement satisfiable while the defect ships;
(v) a fence guarding a path production does not take.

Run the **vacuous-pass probe**: stub the surface under test and run every test against it. A prior
author found 14 of 65 passing that way, one of which panicked and hid five more — including that
slice's two headline assertions. Label any that legitimately pass, in-file, so no evidence line
counts them as earned.

## Do NOT

- **`internal/remote/crypto` is FROZEN.**
- Do not change the machine-side trigger, sender, or preference storage — that is S12's, shipped.
  If you find a defect in it, report it; do not fix it here.
- `TestS14A_TheCleartextSealerHasNoCallSitesLeft` pins the insecure sealer at **zero** call sites.
- ADR-007 B8: the key crossing is single and inbound; no bound method may return `[]byte`.
- Do not edit `docs/specifications/`. Report and I will amend.
- **Do not commit and do not stage anything.** Leave your work unstaged; I stage it explicitly.

## Environment

- Android SDK at `/usr/local/share/android-commandlinetools`; an AVD named `swarmtest` exists. Gate
  is `./gradlew lint test`.
- `golangci-lint` at `/Users/Nathan/go/bin/golangci-lint`.
- Host is an Apple M1 but `/usr/local/bin/go` is x86_64. The reliable check is `go env GOHOSTARCH`
  (= amd64), an **in-process** probe — NOT `uname -m`, NOT `sysctl sysctl.proc_translated` from a
  shell. Timings pessimistic.
- Every latency-budget test here is load-sensitive and passes in isolation; the list is in
  `docs/verification/remote-phaseB-progress.md`. Re-run in isolation before calling anything a
  regression.
- Other agents may be working in the shared tree. Scope your runs to your own packages, leave
  anything dirty outside your scope alone, and never `git checkout` what you did not write.

## Deliverable

1. Test files, uncommitted and unstaged.
2. The **verbatim failing-first run**, each failing for the right reason.
3. The vacuous-pass probe result, with legitimate passers labelled in-file.
4. **How you fence the Kotlin call**, not just the facade method — the specific answer to
   PB-PUSH-9's own warning.
5. Anything unimplementable as written.

Report via SendMessage to "main" — plain text output is NOT visible.
