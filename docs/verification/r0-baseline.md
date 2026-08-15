# Wave R0 — trustworthy baseline: evidence

**Bead:** `agents-tracker-hggx.1` | **Playbook:** docs/specifications/remote-control-product-playbook.md, Wave R0
**Process:** every fix ran a TDD chain (RED agent → implementer → independent adversarial reviewer,
separate model instances); RED evidence under `docs/verification/r0-red/`. Orchestrated 2026-08-14/15.

## The six named red items

| Item | Root cause | Fix | Evidence |
|---|---|---|---|
| Shim PTY close/resize race | `pty.Setsize` used `File.Fd()`, bypassing poll.FD's close guard; teardown closes the PTY before handler join | ioctl via `SyscallConn().Control` (serializes with Close, refuses a closed fd) | r0-red/shim-race-red.txt; `TestCloseVsResizeRace`; reviewer reproduced the race standalone pre-fix |
| Gateway shutdown stall | `ForwardCommand`'s 10s daemon reply read outlived ctx cancel; two cleanup bounds had been widened to 5s without diagnosis | `forwardCtx`/`beginLeaseCtx` race the call against ctx; abandoned call leaves the frame unconsumed (PB-GW-3 shape); bounds restored to 2s | r0-red/gw-cancel-red.txt with goroutine dumps |
| PB-BIND-6 overflow test miswired | Flooded the journal family while subscribing only terminal; passed via a default; count bound unreachable | Subscribes the flooded family, asserts newest-survive/oldest-drop, surfaced overflow signal; drop-newest mutation recorded failing | r0-red/overflow-test-red.txt (incl. MUTATION CONTROL) |
| Generic lane missing bind tools | `mobile/bind_test.go` hard-fails without gomobile/gobind; test job never installed them | Pinned install from android/toolchain.env in the test job | ci.yml; r0-red/ci-workflows-red.txt |
| Lint gate ran zero linters | golangci-lint v1.64 (go1.24 binary) exits 3 against this go 1.25 module | v2.12.2 via action v7; the surfaced debt (~250 findings) fixed behavior-preservingly in two reviewed rounds plus stragglers; 4 justified nolint (QF1011 signature pins) | commits 2bed856, d38a927 |
| Gradle verification stale | linux-only and buildscript-classpath artifacts missing (macOS regeneration cannot resolve the linux aapt2) | minimal entries: 3 BOM/POMs + aapt2-linux (sha256 from dl.google.com, recorded in the entry origin) | r0-red/gradle-meta-red.txt |

## Release gating (the wave's purpose)

v0.10.2 and v0.10.3 had been published while their tag CI was red; no release workflow or branch
protection existed. Now: `release.yml` reuses ci.yml's full gate set via workflow_call and runs
goreleaser only after all pass (`needs: gates` — structural, independent of branch settings);
branch ruleset `main-required-ci` (id 20874942) requires all 13 jobs with a repo-admin bypass for
the direct-push flow; the Homebrew cask publishes through the fine-grained tap PAT
(`HOMEBREW_TAP_GITHUB_TOKEN`, Contents-RW on the tap repo only).

## Beyond the named list (found because the gates started working)

- vt `FuzzFeedSplitConsistency` crasher + a second CI-found hang: CSI param clamp
  (`csiParamDigitCap`), then the C0-in-CSI marker-strip/digit-concat bypass and the
  SetCallbacks finalizer-cycle leak (r0-red/vt-fuzz-red.txt, vt-c0-param-red.txt,
  vt-emulator-leak-red.txt) — both proven by isolation, corpus seeds committed.
- hookclient verbatim-bytes test: rig-only (macOS 104-byte sun_path; sibling pattern applied).
- Android: `VIBRATE` permission genuinely missing (masked by the Gradle failure);
  one lint suppression for an untraceable IntDef, reasoning attached.
- Four never-watched Kotlin test suites: the androidgate discovery keyed by filename; now
  keyed by fully-qualified class name (strictly stronger; teeth verified by hiding a report).
- Three CI-only flakes root-caused, all rig races, none reproducible on darwin:
  relay S6B wait metering (release-after-respond ordering — production fix, proven by
  injection), presence sweep (client Close is not server observation), take-control expiry
  clamp (the OpLease grant rides the pump goroutine and was never a valid sync point).

## Exit status

Three consecutive fully green required runs: 31865631617 and 31871192243 achieved, then the
streak reset twice on orchestrator lane-split staging errors (31877066151, 31879368459 — both
caught by the fences doing their job) and once on the obligations/webpki closure split. Current
streak restarts at 31880-series green on the whole tree. The bead closes when the counter reads
three; every named red item is individually fixed and reviewed.
