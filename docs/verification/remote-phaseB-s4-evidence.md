# Phase B slice S4 — gateway supervision (PB-LIFE-1..6, PB-OPS-4)

**Requirements**: PB-LIFE-1 (one unit definition per host supervisor), PB-LIFE-2 (a successful
pair leaves the gateway running, with no manual restart), PB-LIFE-3 (quiescent / active /
failed, and quiescence is NOT a failure), PB-LIFE-4 (owner-only unit permissions, paths only),
PB-LIFE-5 (a throttled restart policy), PB-LIFE-6 + PB-OPS-4 (`swarm-remote` and `swarm-relay`
are buildable RELEASE artifacts).

ADR-007 D5 is the constraint the whole slice is shaped by: the gateway runs under an external
supervisor and is **never** spawned by the daemon. The owner-invoked CLI is therefore the only
thing that installs a unit (`swarm remote init`) or activates one (`swarm remote pair`).

## What the slice delivers

- `internal/remote/supervise` — the ONE source both unit types are generated from (launchd
  LaunchAgent plist, systemd user unit), plus the `Supervisor` seam (`Install`/`Ensure`/`Stop`)
  the CLI drives. Units land in `<stateDir>/remote/units`, inside the 0700 tree that already
  guards the machine identity, and carry **paths only** — never key material.
- `supervise.Desired` — the single definition of quiescence, shared by the CLI, the units and
  the gateway binary. `ExitQuiescent` is a **success** status on purpose: launchd's `KeepAlive`
  has no per-exit-code list, so only a zero status makes one restart policy (`on failure only`)
  correct on both platforms.
- `.goreleaser.yaml` — `swarm-remote` and `swarm-relay` join the release matrix, `swarm-remote`
  in the **same archive** as `swarm` so an installed `swarm` always has a gateway next to it.

## Failing-first (GG-5)

The slice is uncommitted, so the pre-slice state is `HEAD`. Reconstructed by extracting `HEAD`
into a scratch tree, dropping in the slice's test files and running them there:

```
=== cmd/swarm (S4 tests vs the pre-slice tree) ===
github.com/Nathandela/swarm/internal/remote/supervise: no non-test Go files in .../internal/remote/supervise
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]

=== internal/remote/supervise (the package did not exist) ===
internal/remote/supervise/state_test.go:86:11:  undefined: State
internal/remote/supervise/state_test.go:88:7:   undefined: StateQuiescent
internal/remote/supervise/state_test.go:89:7:   undefined: StateActive
internal/remote/supervise/state_test.go:153:34: undefined: Spec
internal/remote/supervise/state_test.go:195:24: undefined: Supervisor
internal/remote/supervise/unit_test.go:73:29:   undefined: Spec
internal/remote/supervise/unit_test.go:561:39:  undefined: Platform
internal/remote/supervise/release_test.go:150:15: undefined: Render
```

A compile-fail RED, unambiguous by name: nothing installed or activated anything, and the
release matrix built one binary out of three.

---

# Remediation round (independent review returned REVISE)

Three blocking findings, all real, all fixed. Each fix was driven by a test written and run
**before** the change.

## B1 — the Homebrew cask shipped `swarm-remote` and never linked it

`.goreleaser.yaml` put `swarm-remote` in the `swarm` archive, but the `homebrew_casks` entry was
left at its default, so the generated cask emitted `binary "swarm"` and nothing else. **A cask
links only what it declares**: `swarm-remote` was downloaded, staged under the Caskroom, and
never put on PATH. `exec.LookPath("swarm-remote")` then failed for every Homebrew user,
`installGatewayUnit` warned and returned, and `swarm remote pair` advised `swarm remote init` —
advice that could never succeed. PB-LIFE-1/-2/-6 were silently undelivered on the flagship macOS
install path, and the manifest's own header asserted the opposite.

The coverage gap that let it through: `release_test.go` parses the `homebrew_casks` block and
asserts **nothing** about it.

**RED** (`internal/remote/supervise/release_cask_test.go`, new):

```
=== RUN   TestGoreleaser_CaskLinksEveryBinaryItsArchiveShips
    release_cask_test.go:168: cask "swarm" declares binaries [], but the archives it is built from ship [swarm swarm-remote].
        A cask links ONLY the binaries it declares -- every other one is downloaded into the Caskroom and never put on PATH,
        so `swarm remote init` cannot resolve it and PB-LIFE-1/-2 are undelivered on every Homebrew install.
--- FAIL: TestGoreleaser_CaskLinksEveryBinaryItsArchiveShips (0.00s)
=== RUN   TestGoreleaser_CommentMatchesHowTheGatewayIsResolved
    release_cask_test.go:189: .goreleaser.yaml still says `swarm remote init` resolves the gateway from PATH alone;
        it looks BESIDE its own executable first, which is what makes a cask install work
--- FAIL: TestGoreleaser_CommentMatchesHowTheGatewayIsResolved (0.00s)
=== RUN   TestHomebrewCaskReference_IsNotStale
    release_cask_test.go:222: packaging/homebrew/swarm.rb still strips quarantine from one named binary; the config strips staged_path
--- FAIL: TestHomebrewCaskReference_IsNotStale (0.00s)
FAIL
```

The expectation is **derived, not enumerated**: the cask must declare every binary the archives
it is built from collect, so a fourth binary keeps the test honest. Both YAML list forms (block
and inline `[a, b]`) are understood, so the assertion does not depend on formatting. No YAML
dependency was added, for the reason `release_test.go` already gives.

**GREEN**:

- `.goreleaser.yaml` — the cask now declares `binaries: [swarm, swarm-remote]`, with a comment
  saying why a cask does not derive that list on its own. The header comment's false claim
  ("init resolves the gateway binary from PATH") is replaced by what the CLI actually does.
- `cmd/swarm/remote.go` — `resolveGatewayBinary()` looks for the gateway **beside**
  `filepath.EvalSymlinks(os.Executable())` first, and falls back to PATH. Adjacency is the
  relationship the archive guarantees; PATH is not. Symlinks are resolved first because
  Homebrew's `bin` entry is a link into the Caskroom, where both binaries live together.
- `packaging/homebrew/swarm.rb` — **regenerated, not hand-edited**, from a real
  `goreleaser release --snapshot --clean --skip=publish,before,validate,announce` run, which is
  what its own header says it is. It was stale twice over: the old `postflight` named
  `"#{staged_path}/swarm"` against the config's whole-path form, and it carried one `binary`
  line. `--skip=before` matters — `before.hooks` runs `go mod tidy`, and `go.mod` belongs to
  another slice in this worktree.

The generated cask, verified against the real pipeline rather than inferred:

```
  binary "swarm"
  binary "swarm-remote"

  postflight do
    system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", staged_path]
  end
```

and the archive it links from:

```
$ tar tzf dist/swarm_0.2.0-SNAPSHOT-6de055e_darwin_arm64.tar.gz
README.md
swarm-remote
swarm
```

`goreleaser check` passes. `dist/` was removed afterwards.

The unit test asserts on the parsed YAML, not on `dist/`: a snapshot builds 12 binaries in ~17s
plus archiving, which is too slow for `go test`. `TestHomebrewCaskReference_IsNotStale` bridges
the gap by requiring the committed reference cask to carry a `binary` line for every binary the
config declares — the file exists so a reader can see what the pipeline emits without running
it, and a copy showing one linked binary would teach the reader this exact defect.

## B2 — `Ensure`'s already-loaded classifier was message-fragile, and re-pair is the path that hits it

`Ensure` treated a `launchctl bootstrap` failure as benign only if its output contained
"already" or "file exists"; anything else returned early and **never reached `kickstart`**.
Nothing calls `Stop` in production, so after `init -> pair -> revoke -> re-pair` the job is
still bootstrapped and every re-pair takes that branch — and macOS commonly reports an
already-bootstrapped label as `Bootstrap failed: 5: Input/output error`, which carries neither
substring. Every re-pair would report "the gateway was not started" and genuinely not start it,
breaking PB-LIFE-3(c).

**RED** (`internal/remote/supervise/supervisor_test.go`, new). The seam came first (compile-fail
`unknown field run in struct literal of type hostSupervisor`), then the behavioral RED against
the unchanged logic:

```
=== RUN   TestHostEnsure_AlreadyBootstrappedStillKickstarts
    supervisor_test.go:100: Ensure() with an already-bootstrapped job = supervise: launchctl bootstrap: exit status 5:
        Bootstrap failed: 5: Input/output error, want nil; every re-pair after a revoke takes this path (PB-LIFE-3(c))
--- FAIL: TestHostEnsure_AlreadyBootstrappedStillKickstarts (0.00s)
=== RUN   TestHostEnsure_ClassifiesNothingByMessage
    --- FAIL: TestHostEnsure_ClassifiesNothingByMessage/Bootstrap_failed:_5:_Input/output_error (0.00s)
    --- PASS: TestHostEnsure_ClassifiesNothingByMessage/Load_failed:_37:_Operation_already_in_progress (0.00s)
    --- PASS: TestHostEnsure_ClassifiesNothingByMessage/Bootstrap_failed:_17:_File_exists (0.00s)
    --- FAIL: TestHostEnsure_ClassifiesNothingByMessage/Boostrap_failed:_125:_Domain_does_not_support_specified_action (0.00s)
    --- FAIL: TestHostEnsure_ClassifiesNothingByMessage/#00 (0.00s)
=== RUN   TestHostEnsure_KickstartFailureIsTheRealFailure
    supervisor_test.go:158: Ensure() error = "supervise: launchctl bootstrap: exit status 5: Bootstrap failed: 5: Input/output error",
        want it to name kickstart as the failure
    supervisor_test.go:161: Ensure() error = ... drops kickstart's own output
--- FAIL: TestHostEnsure_KickstartFailureIsTheRealFailure (0.00s)
=== RUN   TestRunUnit_RefusesUnderTest
    supervisor_test.go:195: runUnit error = "exit status 5", want it to name the test binary as the reason
--- FAIL: TestRunUnit_RefusesUnderTest (0.27s)
```

The two PASSing sub-cases are the point: the old classifier is a coin flip on wording, and the
assertions are deliberately message-**independent** — they script launchctl's exit status, not
its prose.

**GREEN**: `Ensure` runs `bootstrap`, ignores its error entirely, and lets `kickstart` decide —
`kickstart` fails if and only if the label is not loaded in the domain. `alreadyLoaded` is
deleted. When both fail, bootstrap's output is appended to the error, because that is the only
place launchd says why the load did not happen. The systemd arm is untouched: `systemctl --user
enable --now` is one call whose failure is a real failure.

`hostSupervisor` gained a `run` field (defaulting to `runUnit`) rather than a package-level var,
so tests inject per-instance and stay race-free.

### The existing test at `remote_supervise_test.go:269` still means something

It uses `launchctl: Bootstrap failed: 5: Input/output error` as its exemplar of a real failure —
the same string this fix proves is ambiguous. Its premise is **not** void: it drives the CLI's
`ensureGatewayRunning` through a fake supervisor and asserts that any non-`ErrNotInstalled`
error is surfaced, not swallowed. The error's *text* is arbitrary to that assertion. It was left
unmodified. Residual, for the record: after this fix the host supervisor can no longer produce
that particular string, so the exemplar is now unrealistic even though the assertion is intact.

## B3 — `ensureGatewayRunning`'s stated rationale was false, and it exposed a real operability hole

The comment justified exiting 0 with "a nonzero exit ... would invite the operator to re-run
`swarm remote pair` — which enrolls a SECOND device". **A second pairing is refused**:
`internal/skeleton/pairing.go` ("a device is already paired; revoke it first (single-device
v1)") and again in the registry's `AddSole`. `Desired(2)` is unreachable via re-pairing. The
decision (exit 0) is right — the enrollment is durable and a nonzero exit would misreport it —
so the behaviour stands and the comment now says the true reason.

The hole it exposed: `sup.Ensure()` had exactly **one** call site, inside the one command that
cannot be re-run while a device is paired. An operator whose `Ensure` failed had no supported
way to start the gateway; `swarm remote init` only installed files. PB-LIFE-2's "no manual
restart" was not met on that path.

**RED** (`cmd/swarm/remote_supervise_test.go`):

```
=== RUN   TestRemoteInit_PrefersTheGatewayBesideItsOwnExecutable
    remote_supervise_test.go:214: Spec.Exec = ".../003/swarm-remote", the PATH copy; the gateway shipped BESIDE
        this binary (".../001/swarm-remote") is the one this install guarantees
--- FAIL: TestRemoteInit_PrefersTheGatewayBesideItsOwnExecutable (0.11s)
=== RUN   TestRemoteInit_FallsBackToPATHWithNoSibling
--- PASS: TestRemoteInit_FallsBackToPATHWithNoSibling (0.12s)
=== RUN   TestRemoteInit_EnsuresGatewayWhenADeviceIsAlreadyPaired
    remote_supervise_test.go:262: Ensure called 0 times by `swarm remote init` with one paired device, want 1
        (supervise.Desired(1) is StateActive); calls=[install]
--- FAIL: TestRemoteInit_EnsuresGatewayWhenADeviceIsAlreadyPaired (0.09s)
=== RUN   TestRemoteRevoke_StopsTheGateway
    remote_supervise_test.go:284: Stop called 0 times after revoking the only paired device, want 1; calls=[]
--- FAIL: TestRemoteRevoke_StopsTheGateway (0.23s)
```

**GREEN**: `runRemoteInit` installs the unit and then converges on the state the device count
already implies — `Ensure` when `supervise.Desired(pairedDeviceCount(stateDir)) == StateActive`,
nothing when it is quiescent. No new verb: `swarm remote init` is idempotent and always
available, and fewer surfaces is better. The existing "init must not activate anything"
assertion still holds, and for the right reason — a machine with no devices reads a count of 0.

`pairedDeviceCount` opens `<stateDir>/devices` directly (`device.Open` + `Count`, the same
registry `cmd/swarm-remote` opens). Deliberately **not** through `dialClient`: that goes through
`daemon.EnsureDaemon`, which would auto-**spawn** a daemon as a side effect of provisioning a
machine — and, inside `cmd/swarm`'s own tests, would spawn the test binary. An unreadable
registry reports 0, which is quiescent, i.e. exactly the old behaviour. One benign side effect:
`device.Open` creates `<stateDir>/devices` if it is absent, so `init` now does too.

## N3 — `TestDaemonNeverSpawnsTheGateway` would have failed the moment it merged

Its walk skipped `.git`, `dist`, `docs`. Sibling worktrees live at `<repo>/.claude/worktrees/*`
and are visible from the main checkout, so from `main` the walk records
`pkg = ".claude/worktrees/.../cmd/swarm"`, which is not in `allowed`, and the test errors.
`.claude` added to the skip list.

## N4 — test isolation is now structural, not conventional

`runUnit` **refuses** when `testing.Testing()` reports it is inside a test binary. The guard sits
in the runner rather than in `Ensure`/`Stop` so it covers every path to a real init system at
one point, and so `ErrNotInstalled` still wins where the existing tests expect it. Tests that
need `Ensure`/`Stop` substitute `hostSupervisor.run`, which is the only way past it.

This was not hypothetical: the RED run of `TestRunUnit_RefusesUnderTest` shows `exit status 5`,
i.e. it really did invoke `launchctl` on the developer's machine (a bootstrap of a nonexistent
plist — a no-op, but the reach was real).

The package doc at `unit.go` claimed "no test in this tree can write into the real system's
supervision directories". True of what the package writes, false of what `Ensure` causes
systemd to do (`systemctl --user enable --now` links the unit into `~/.config/systemd/user`).
Softened to say exactly that, and to point at the `runUnit` guard.

## N6 — `Supervisor.Stop` is no longer dead production code

`runRemoteRevoke` now calls it, via `stopGatewayIfQuiescent`, when the roster the revoke just
wrote no longer justifies a gateway. The change was small and safe, so it was made rather than
recorded. It closes the residual it was filed under: the revoked device's gateway is supposed to
self-exit, but `deviceRevoked()` returns false when it cannot read the registry, and a surviving
pre-revoke process makes the next pairing's `Ensure` a documented no-op against a running job —
the new phone would be served by a process still holding the revoked device's epoch. This also
makes the `ensure ... stop ... ensure` ordering that `state_test.go` asserts a real production
sequence rather than a model-only one.

`ErrNotInstalled` is silent here: a machine with no unit has nothing to stop.

## Accepted residuals

Carried forward from the review, unfixed and deliberately recorded:

- **N1**: re-`Install` never refreshes a loaded definition — launchd ignores `bootstrap` on a
  loaded label and `Ensure` never runs `systemctl --user daemon-reload`, so after an upgrade
  `swarm remote init` silently leaves the old definition running until logout/reboot.
- **N2**: the unit's `SWARM_DAEMON_REMOTE_SOCK` may point at a socket nothing serves —
  `gatewaySocket` defaults to ADR-007 D4's canonical `<stateDir>/remote.sock`, but the daemon
  opens a remote socket only when that env var is set in the DAEMON's environment
  (`cmd/swarm/main.go`); it does not default to that path. Symptom: "phone pairs, then
  silence". Spec/ADR follow-up.
- **N4 (partial)**: the Linux path remains unexercised on this darwin host.
- **N5**: `StateFailed` is unreachable on macOS (launchd has no start-limit), so `state.go`'s
  "often enough that the supervisor gave up restarting it" overstates it for launchd.
  `unit.go` already acknowledges the asymmetry.
- **N6 (partial)**: freshness still relies primarily on the gateway self-exiting
  (`service.go`); `stopGatewayIfQuiescent` is a second line of defence at the moment the owner
  is present, not a replacement for it.
- **B2 (partial)**: `remote_supervise_test.go`'s `Bootstrap failed: 5: Input/output error`
  exemplar is no longer a message the host supervisor can produce, though the assertion it
  drives is unaffected.

## Gates

```
$ go build ./...
exit=0

$ go vet ./internal/remote/supervise/ ./cmd/swarm/
exit=0

$ go test ./internal/remote/supervise/ ./cmd/swarm/ ./cmd/swarm-remote/ -count=1
ok  	github.com/Nathandela/swarm/internal/remote/supervise	5.614s
ok  	github.com/Nathandela/swarm/cmd/swarm	18.692s
ok  	github.com/Nathandela/swarm/cmd/swarm-remote	1.610s

$ go test -race ./internal/remote/supervise/ -count=1
ok  	github.com/Nathandela/swarm/internal/remote/supervise	8.002s

$ gofmt -l cmd/swarm/remote.go cmd/swarm/remote_supervise_test.go \
    internal/remote/supervise/supervisor.go internal/remote/supervise/supervisor_test.go \
    internal/remote/supervise/release_cask_test.go internal/remote/supervise/unit.go
(no output)

$ golangci-lint run ./internal/remote/supervise/ ./cmd/swarm/
exit=0

$ goreleaser check
  • 1 configuration file(s) validated
```

`go test ./...` is **not** clean in this worktree, for reasons outside this slice: two other
uncommitted slices are in flight in the same checkout. `internal/remotegw` and
`internal/remote/relay` fail here and **pass** against a pristine `HEAD` extraction; neither
package has any dependency on anything S4 touches (`go list -deps` confirms). `cmd/swarm`'s
`TestTUI_OpensAndRestoresOverPTY` and `TestRunShim_LaunchesAgentPersistsAndLeadsSession` fail
only under whole-repo parallel load and pass when the package is run on its own.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** to emit the traceability
table's DERIVATION column. One row per requirement, the verdict token `DERIVED` or `NOT DERIVED`,
and -- for `DERIVED` -- **the mutation that was made to fail, in the same row**; an empty mutation
cell is refused and counted NOT DERIVED.

Every mutation below moved a PRODUCTION connection (ADR-007 B113) -- a template directive, a
branch, a file mode, a call site -- and was applied, run, and reverted. `git status` is clean.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-LIFE-1 | DERIVED | (a) the launchd template's `KeepAlive/SuccessfulExit` flipped `<false/>` -> `<true/>`, i.e. restart on CLEAN exit only -> `TestRenderLaunchd_RestartPolicyOwnerAndNoSecrets` fails. (b) the systemd template hardcodes `ExecStart=/usr/local/bin/swarm-remote` and `RestartSec=30` instead of `{{.Exec}}`/`{{.BackoffSeconds}}`, breaking the ONE-SOURCE clause -> `TestRender_OneSourceDrivesBothUnits` fails on both fields, in both directions (*"still carries the OLD Exec after the Spec changed"*) |
| PB-LIFE-2 | DERIVED | the `ensureGatewayRunning("pair", stderr)` call removed from `runRemotePair` after `res.Paired` (`cmd/swarm/remote.go:979`) -> `TestRemotePair_SuccessEnsuresGatewayRunning`, `TestRemotePair_UnitNotInstalledIsAHintNotAFailure`, `TestRemotePair_SupervisorFailureIsReportedNotSwallowed` and `TestRemoteInitThenPair_DoesNotLeaveTheGatewayDialingNothing` all fail |
| PB-LIFE-3 | DERIVED | (a) `ExitCodeFor` stops treating `ErrQuiescent` as success -> `TestExitCodeFor_QuiescenceIsNotAFailure`, `TestSupervisionSequence_RevokeQuiescenceRepair_NoCrashLoop` (*"gateway started 1 times across 100 zero-device ticks"*) and `TestGateway_ExitsQuiescentWithNoPairedDevice` fail, i.e. the revoke crash loop returns. (b) `Desired` maps zero devices to `StateFailed` and everything else to `StateActive` -> `TestDesired_ThreeStates` fails on 0, 2 and 7 |
| PB-LIFE-4 | DERIVED | (a) `hostSupervisor` writes and chmods the installed unit `0o644` instead of `0o600` -> `TestInstall_WritesOwnerOnlyUnderTheStateDir` fails. (b) `Environment=SWARM_RELAY_TOKEN=...` added to the systemd template -> `TestUnits_CarryNoCredentials` fails, naming the line |
| PB-LIFE-5 | DERIVED | `StartLimitIntervalSec`/`StartLimitBurst` removed from the systemd `[Unit]` section and `ThrottleInterval` removed from the plist -> `TestRenderSystemd_UserUnitRestartsOnFailureOnly` (*"without it a failing gateway restarts unthrottled"*), `TestRenderLaunchd_RestartPolicyOwnerAndNoSecrets` and `TestRender_ZeroBackoffStillThrottles` all fail. **A second mutation SURVIVED -- see finding (1)** |
| PB-LIFE-6 | DERIVED | (a) the `swarm-remote` build's `binary:` renamed to `swarm-remote-preview` in `.goreleaser.yaml` -> `TestGoreleaser_ShipsGatewayAndRelay` and `TestGoreleaser_CaskLinksEveryBinaryItsArchiveShips` fail. (b) the whole `id: swarm-remote` build block deleted -> the same two fail, naming *"declares no build with id \"swarm-remote\"; got ids [swarm swarm-relay]"* |

### Finding from this derivation

1. **PB-LIFE-5's rounding rule is unfenced, and production names the hazard itself.**
   `Spec.resolve` computes `BackoffSeconds: int((backoff + time.Second - 1) / time.Second)` under a
   comment that reads *"Rounded UP: both unit types take whole seconds, and rounding a sub-second
   backoff down to zero would leave the gateway unthrottled (PB-LIFE-5)."* Changing it to
   `int(backoff / time.Second)` leaves `./internal/remote/supervise` **entirely green**. The
   surviving coverage is `Backoff == 0` (which takes the `DefaultBackoff` branch, 10s, unaffected
   by rounding) and `Backoff < 0` (refused). Nothing exercises `0 < Backoff < 1s`, which is the
   only input the rule exists for and which renders `RestartSec=0` / `ThrottleInterval=0` -- an
   unthrottled crash loop, this row's exact prohibition. One table case at 500ms closes it.
