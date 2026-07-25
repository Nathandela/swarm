# BRIEF — Phase B slice S18: security hardening (10 requirements)

cwd = `/Users/Nathan/Code/swarm/.claude/worktrees/remote-control-research`. Work only there.

You are the TEST AUTHOR (RED). Write ONLY tests plus the minimum scaffolding to compile and fail
**for the right reason**. A separate agent implements; a third reviews.

Requirements: **PB-SEC-3, 4, 5, 6, 7, 8, 11, 12, 13, 14**. Read every row before writing anything.

## PB-SEC-6 is the one that matters most, and it is adversarial by construction

> The app cannot bypass any server-side control: kill switch, lease, capability, expiry and seq
> gating stay authoritative server-side.
> **Criterion**: adversarial test through the real transport — no typing without a lease or while
> the kill switch is on.

Write this as an attacker, not as a user. The phone is the thing you are trying to make misbehave,
and every client-side check is a courtesy: the test must prove the **server** refuses, with the
client's own guard removed or bypassed. A test that drives the facade and observes a client-side
refusal proves nothing about this requirement.

Use the real transport. `internal/phonesim` never constructs a `phonecore.Core`, so it skips the
lease gate entirely and types with no confirmed lease — which makes it useless here and, if you
route through it, actively misleading.

## Cross-references you should not have to rediscover

- **PB-SEC-3 (no plaintext session content persisted)** overlaps slice S15, in flight now, which
  seals the decrypted caches under the content tier. Before writing a storage assertion, check what
  S15 landed — and note the measurement standard it established: sentinels in **every byte form**
  (raw, base64, hex, decimal, both endiannesses), paired with the positive half that material
  reached the intended sealer. Absence alone is a weak assertion.
  The **log scan** half is not covered by S15 and is yours. The criterion explicitly says an
  evidence artifact is required, **not "reviewed"**.
- **PB-SEC-12's "no sensitive clipboard use"** collides with a real surface: slice S11 added a paste
  verb taking a `string`. A reviewer recorded at the time that a pasted string — a password out of a
  manager — **cannot be zeroized**, and that the byte-slice input path is not zeroized either. That
  is the honest state; decide whether it satisfies the requirement, and if not, say what would.
- **PB-SEC-5 carries its own correction**: v1 wrongly claimed the platform cleartext setting backs
  up the transport requirement. It does not — `networkSecurityConfig` does not govern Go's
  `crypto/tls` inside a native `.so`. Do not restore that claim; the Java/WebView stack is the whole
  scope of this row.
- **PB-SEC-7 (device-loss response)** is re-asserted through the real transport. The revoke path
  already rotates the epoch and removes the device in one transaction — that is verified. What is
  new here is proving the **gateway severs and exits** and the lost device is genuinely dead, end to
  end.
- **PB-SEC-13/14 (release build config, supply chain)** are gate-level. The Android toolchain is
  present but nothing is on `PATH`; see the build-environment section of
  `docs/verification/remote-phaseB-progress.md` for the exports.

## The failure mode this slice is most exposed to

**A green gate that proves less than it appears to.** Two examples already recorded in this project:
the headline latency test skips unless an env var is set, and the bind-produces-an-artifact test
skips when the Android toolchain is absent — both properties are covered elsewhere, but an auditor
reading a green suite would over-read both.

A security assertion that **skips** is worse than one that fails. If any test you write can skip,
make the skip loud and say so in your report, and prefer a formulation that cannot.

## Standing defect classes — construct the failing mutation for every check

(i) a guard that cannot fail; (ii) a plausible-but-wrong value hiding a brick; (iii) a test passing
because its subject became unreachable; (iv) a requirement satisfiable while the defect ships;
(v) a fence guarding a path production does not take.

Class (iv) is this slice's natural hazard: an inventory that reports "no analytics present" or "no
secrets in logs" by reading a declaration rather than the artifact. **PB-SEC-3 and PB-SEC-8 both
demand evidence artifacts precisely because "reviewed" was rejected as unenforceable.**

Run the **vacuous-pass probe**: stub the surface and run every test against it. A prior author found
14 of 65 passing that way, one of which panicked and hid five more. Label any legitimate passers
in-file.

## HARD CONSTRAINT — the physical-handset gate stays deferred

**PB-E2E-5 may NOT be reclassified.** Nothing you write may appear to cover real biometrics, a real
camera, real FCM delivery, real Doze, or hardware Keystore attestation. Robolectric and JVM tests
model **policy**; an emulator is not a handset. Say so in-file.

## Do NOT

- **`internal/remote/crypto` is FROZEN.**
- ADR-007 B8: the key crossing is single and inbound; no bound method may return `[]byte`. A review
  found the existing fences do not cover bound **struct fields**, which gomobile emits as a byte
  getter — that gap is being closed separately; do not widen it.
- Do not edit `docs/specifications/`. Report and I will amend — six requirements in this phase have
  turned out unimplementable or unmet as written, and each was a finding.
- **Do not commit and do not stage anything.** Leave your work unstaged; I stage it explicitly.

## Environment

- Android SDK at `/usr/local/share/android-commandlinetools`; gate is `./gradlew lint test`.
- `golangci-lint` at `/Users/Nathan/go/bin/golangci-lint`.
- Host is an Apple M1 but `/usr/local/bin/go` is x86_64. The reliable check is `go env GOHOSTARCH`
  (= amd64), an **in-process** probe — NOT `uname -m`, NOT `sysctl sysctl.proc_translated` from a
  shell. Timings pessimistic.
- Every latency-budget test here is load-sensitive and passes in isolation; the list is in the
  progress doc. Other agents are running, so the box is loaded — re-run in isolation before
  recording any failure as real.
- Other slices have uncommitted work in the tree. Scope your runs, leave anything dirty outside your
  scope alone, and never `git checkout` what you did not write.

## Deliverable

1. Test files, uncommitted and unstaged.
2. The **verbatim failing-first run**, each failing for the right reason.
3. The vacuous-pass probe result, with legitimate passers labelled in-file.
4. **Every test of yours that can SKIP**, and why you could not avoid it.
5. Your position on PB-SEC-12's clipboard row given the existing paste surface.
6. Anything unimplementable or already unmet as written.

Report via SendMessage to "main" — plain text output is NOT visible.
