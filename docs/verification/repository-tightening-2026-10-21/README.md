# Repository tightening verification, 2026-10-21

The owner requested a Luna audit swarm, a detailed attack plan, and implementation. Ten Luna agents covered bounded audit, implementation, debt inventory, and independent review lanes. The starting checkout was clean at `accef4c2`; the plan and remaining work live in Beads epic `agents-tracker-rgk9`.

Read the [ranked audit and attack plan](audit.md), [complete shortcut ledger](debt.txt), and [Android test disposition](android-disposition.txt). The audit is selective and does not certify every source line or all existing backlog items.

## Changes and reviews

- `e570d6af` binds replayed operation IDs to action/session and refuses closed or canceled queued app-server calls before their execution callback. [Independent regression review](review-regressions.txt), [idempotency red](idempotency-red.txt), [idempotency green](idempotency-green.txt), [app-server red](appserver-red.txt), [app-server race](appserver-race.txt), and [caller race checks](appserver-callers-race.txt) record the evidence.
- `a2f1aa3f` removes the unused 5,124,096-byte `e2e2session` executable, ignores its exact path, and aligns README toolchain/release guidance with the project.
- `9a8cfe34` orders asynchronous board-layout writes. [Independent review](layout-review.txt), [overlap red](layout-red.txt), [overlap green](layout-green.txt), [reverse-order red](layout-reverse-red.txt), and [reverse-order green](layout-reverse-green.txt) cover both relevant schedules.
- `20787614` removes the retired Android terminal drawer, watcher, bridge, component options, and deleted-subject tests. It removes 1,298 net production Kotlin lines and preserves Go/wire compatibility. [Independent review](android-review.txt) accepted the final change after a generic wrapper-dispatch guard was restored.

The Android author's initial failing-first gate output was observed in tool results but not saved as a raw log. The integration lane independently compiled the final gate, ran it against an archive of the original production Kotlin from `accef4c2`, and observed [the expected failure](android-original-red.txt); the same binary [passes against current source](android-current-green.txt). The shared checkout was never restored or perturbed for this control.

## Passed checks

| Check | Evidence |
|---|---|
| `go build ./...`, `go vet ./...` | Exit 0 with native Go 1.26.0: [build](go-build.txt), [vet](go-vet.txt); initial Go 1.26.9 build/vet also passed |
| Linux-target `golangci-lint run` | Pinned v2.12.2, Go 1.26.0, `GOOS=linux CGO_ENABLED=0`: [0 issues](lint-linux.txt) |
| Focused new runtime/layout regressions under `-race` | Linked red/green and review records above |
| Full Android source gates | [Gate output](android-gate.txt); also passes in the final full Go race run |
| Current gomobile AAR | Built through `android/build-aar.sh`; exact facade-surface gate passes; both arm64-v8a and x86_64 libraries present |
| Debug and release Kotlin/JVM tests | 1,727 tests per variant, 216 suites each, zero failures/errors/skips: [result manifest](android-results.json) |
| Android lint | `:app:lint` passed in the [successful Gradle run](android-gradle.txt) |
| Actual local relay runtime | `RELAY_TEST_BUDGET_SECONDS=600 npm test` in `services/relay` exited 0: [full output](relay.txt), including real workerd and the required native/operator/mobile/recovery/security cases |
| Diff hygiene | `git diff --check` passed |

## Platform and toolchain limits

The installed Homebrew Go, linter, Java, gomobile, and gobind executables were Intel binaries and could not run on this ARM host. Validation used native pinned tools in scratch directories, writable Go/Gradle/Maven caches, the existing scratch Android SDK/NDK, and the current rebuilt AAR. Gradle inherited explicit Java temporary/user-home paths to keep caches writable. Initial failures caused by protected cache locations were corrected by this environment setup; no project gate or test expectation was weakened.

The repository test sweeps removed inherited Swarm hook/session variables. The final race sweep used canonical `/private/tmp`, a writable module cache, `-p 2`, and a ten-minute per-package timeout. The earlier full normal baseline used the original temporary-directory environment and is therefore preserved separately from final results.

The final full race sweep exited 1: **68 packages passed, 10 failed, and 8 had no tests**, with zero reported data races or package timeouts. Every failing top-level test name also failed in the earlier normal baseline. [Final results](race-results.json) preserve the package/test inventory and raw-log hash; [baseline results](baseline-results.json) preserve the earlier 64-pass/14-fail inventory. Different environments and the rebuilt AAR account for part of that difference; it is not a code-only improvement measurement.

Two package failures involve sandbox process discovery. The existing unattended-restart test and all three failing end-to-end process fixtures passed under `-race` when rerun outside the sandbox against their own temporary daemons: [restart](unattended-process-check.txt), [end-to-end](e2e-process-checks.txt). The other account, process-fixture, platform-inventory, and reachability failures remain recorded, including the Darwin reachability assertion over Linux-only `processcontain` callers.

The initial complete relay runner exited 124 at its 180-second outer deadline after several successful cases. The final scrubbed rerun used the runner's existing 600-second budget option and exited 0; per-test assertions and deadlines were unchanged. Its opt-in `TestS19Workerd_InputLatencyPhoneToPTY` benchmark was skipped because `SWARM_S6B_LATENCY` was not enabled. This is runtime/interop verification, not latency qualification.

Native macOS lint still reports [11 existing unused declarations](lint-macos.txt) in account code with Linux-only callers, tracked by `agents-tracker-nsxw`. Linux-target lint is clean. Existing macOS account/platform/process fixture failures are also tracked in `agents-tracker-vyhy.10` and `agents-tracker-4p4q`; they must not be reported as a green whole-repository gate.

The audit also confirmed existing mailbox recovery (`agents-tracker-y86g`) and audit-retention (`agents-tracker-85yb`) work. New follow-up `agents-tracker-rgk9.6` reconciles the seven shortcut comments without a revisit trigger. The parent epic remains open while these platform/policy/debt requirements remain unresolved.

No physical-handset, cloud deployment, live provider compatibility, Linux runtime, vulnerability, or full fuzz/soak acceptance was performed. Existing user stashes were preserved; this campaign created none.
