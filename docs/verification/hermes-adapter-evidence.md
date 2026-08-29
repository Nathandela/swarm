# Hermes CLI adapter evidence

- **Target:** NousResearch Hermes Agent `0.20.6` at `aff5125f`
- **Support boundary:** macOS arm64; Linux amd64 and arm64; macOS amd64 unsupported
- **Design contract:** [Hermes CLI adapter contract](../design/hermes-adapter.md)
- **Characterization:** [sanitized pre-implementation record](hermes-adapter/characterization.md)

This evidence file separates facts already established by characterization from
acceptance that must be completed by the implementation lanes. It must not be
marked complete merely because production code compiles.

## Pre-implementation evidence (T-6)

Before the production adapter was written, the real `swarm-char` harness drove
Hermes's classic CLI on a real PTY against a local non-billable mock provider.
It recorded a normal turn, dangerous-command approval, clarification request,
session identity, graceful exit, and the screen markers used by the proposed
classifier. A final native production-argv run verified fresh launch, clean
exit, identity capture, and resume with conversation retention. It also disproved
the intended `--no-restore-cwd` behavior: Hermes `0.20.6` later restores its
recorded cwd despite that flag. The exact fixture hashes, excerpts, correction,
and architecture finding are recorded in the characterization document.

This establishes the capability matrix without inventing a transport:

| Capability | v1 decision | Evidence level |
|---|---|---|
| Detection | `hermes --version`; minimum `0.20.6` | live |
| Fresh interactive launch | classic `hermes chat --cli` | live |
| Initial prompt | `-q` while retaining the interactive PTY | live |
| Active/idle | dedicated bounded grid signature | live markers plus production 16-byte replay |
| Permission | approval navigation chrome | live marker plus production 16-byte replay |
| Clarification | question navigation chrome | live marker plus production 16-byte replay |
| Native ID | branded, bordered startup `Session:` and corroborated graceful-exit block | live |
| Resume | history restored with `--resume ID --no-restore-cwd`; 0.20.6 still restores recorded cwd | live, including known upstream cwd bug |
| Named profile | adapter carries explicitly supplied `--profile` on launch/resume | source/argv verified; automatic TUI carry unsupported |
| Typed events | none in terminal v1 | design decision under T-2 |
| Structured backend | unsupported in v1 | explicitly deferred |

The final native default-profile fresh and resumed `swarm-char` runs
independently derived the
same capability entry: Hermes `0.20.6`, hooks false, resume and conversation ID
true, seven options, and one heuristic signal. Resume restored the visible
`Reply exactly pong` / `pong` history, accepted a second prompt, and answered
`pong` again; the cwd limitation was the only failed intended behavior in that
smoke.

## TDD evidence (GG-5)

GG-5 is satisfied only when the implementation records genuine failing-first
output for each behavior slice. The final evidence must name the retained RED
artifact or ordered test-before-production commits for:

1. adapter contract: version, argv/options, resume, identity, no-I/O;
2. grid classification: active, idle, approval, clarification, adversarial
   prose, partial redraw, and fixture timeline;
3. registry/picker/capability/generic launch composition;
4. end-to-end fresh launch and resume.

The disposable feasibility prototype passing its own tests is exploration, not
GG-5 evidence for production. RED evidence must target the production package.

### Recorded RED — adapter slice

The production tests were created before `hermes.go`. The first run was:

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/adapter/hermes
internal/adapter/hermes/hermes_test.go:15:44: undefined: New
FAIL github.com/Nathandela/swarm/internal/adapter/hermes [build failed]
```

An adversarial review then added an exit-block spoof test before hardening the
extractor:

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/adapter/hermes \
    -run TestExtractConversationIDRejectsQuotedExitBlockWithoutSummary
--- FAIL: TestExtractConversationIDRejectsQuotedExitBlockWithoutSummary
extract_test.go:81: ExtractConversationID(quoted block) =
    ("20260829_103232_1a7c23",true); want no identity without
    corroborating final summary
```

The green extractor requires the exact exit heading, immediately following
strict resume command, and a matching source-shaped unbordered summary within a
bounded eight-line window. That summary contains the same `Session:` ID,
optional `Title:`, and contiguous grammar-valid `Duration:` and `Messages:`
fields. This closes a realistic write-once identity-poisoning case; it is not
merely compile-first RED.

Requiring only the original three visible lines was itself shown to remain
spoofable. The source-shaped summary requirement was added failing-first:

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test \
    ./internal/adapter/hermes \
    -run TestExtractConversationIDRejectsCompleteThreeLineModelSpoof \
    -count=1
--- FAIL: TestExtractConversationIDRejectsCompleteThreeLineModelSpoof
extract_test.go:86: ExtractConversationID(complete three-line spoof) =
    ("20260829_103232_1a7c23",true); want no identity without Duration and
    Messages summary fields
```

The same focused command passed after the extractor began validating the exact
classic-CLI `Duration` forms and `Messages: N (U user, T tool calls)` shape.

A second adversarial test established that lexical shape alone was too weak:

```text
--- FAIL: TestExtractConversationIDRejectsTruncationMalformedIDsAndSpoofs/impossible_timestamp
extract_test.go:104: ExtractConversationID(
    "│ Session: 20261340_296199_1a7c23\n") =
    ("20261340_296199_1a7c23",true); want no identity
```

The green parser additionally validates Hermes's naive local wall-clock
`YYYYMMDD_HHMMSS` as a real calendar time. It does not incorrectly claim that
the upstream timestamp carries UTC or any timezone.

A final failing-first spoof case showed that a border alone did not prove the
startup marker came from Hermes's welcome panel:

```text
$ go test ./internal/adapter/hermes \
    -run 'TestExtractConversationIDRejectsTruncationMalformedIDsAndSpoofs/bordered_line_without_banner_context'
--- FAIL: TestExtractConversationIDRejectsTruncationMalformedIDsAndSpoofs/bordered_line_without_banner_context
extract_test.go: ExtractConversationID(
    "│ Session: 20260829_103232_1a7c23 │\n") =
    ("20260829_103232_1a7c23",true); want no identity
```

The green extractor now requires a short, contiguous run of bordered outer-panel
rows: within the four rows preceding `Session:`, one must contain configured
`Nous Research` branding or Hermes's upstream first-run `no model configured`
text. A non-bordered row breaks that local context. The lone bordered line is
rejected, while all three retained live fixtures still pass.

The optional native-ID validator was then exercised at every generic trust
boundary. Before the validator was wired, corrupt saved metadata reached resume
argv composition:

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/skeleton \
    -run 'TestComposeLaunchSpec_HermesSavedResumeRejectsCorruptConversationIDBeforeArgv|TestValidateStoredResumeConversationIDUsesHermesAdapterValidator' \
    -count=1
--- FAIL: TestComposeLaunchSpec_HermesSavedResumeRejectsCorruptConversationIDBeforeArgv
corrupt ID composed /abs/hermes chat --cli --resume
    20261340_296199_1a7c23 --no-restore-cwd with nil error
--- FAIL: TestValidateStoredResumeConversationIDUsesHermesAdapterValidator
corrupt saved Hermes identity accepted
```

A separate focused compose RED showed that a canonical UUID passed Swarm's
generic external-resume gate and reached a Hermes resume with nil error. After
native validation was added, the same UUID fails before argv because it is not a
classic Hermes ID. This makes the current direct-external limitation explicit:
classic Hermes IDs fail the UUID gate, while UUIDs fail the Hermes-native gate.

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/skeleton \
    -run TestComposeLaunchSpec_ExternalHermesResumeRejectsUUIDThatNativeValidatorCannotAccept \
    -count=1
--- FAIL: TestComposeLaunchSpec_ExternalHermesResumeRejectsUUIDThatNativeValidatorCannotAccept
external Hermes UUID composed a resume launch with nil error; want
    native-identity rejection before argv
```

Capture needed the same protection before write-once persistence. The RED was
run in an environment permitted to bind the skeleton's test socket:

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/skeleton \
    -run TestCaptureConversationID_RejectsExtractorOutputBeforeWriteOncePersistence \
    -count=1
--- FAIL: TestCaptureConversationID_RejectsExtractorOutputBeforeWriteOncePersistence
invalid extractor output reached write-once persistence: "corrupt-native-id"
```

The green generic capture path now applies an adapter's optional native-ID
validator before storage. Adapters without the extension retain their historical
opaque-ID behavior; the frozen base adapter interface was not widened.

Final repeated trust-boundary greens were:

```text
$ GOCACHE=/tmp/swarm-hermes-go-cache go test -count=10 \
    ./internal/skeleton \
    -run 'TestComposeLaunchSpec_(Hermes|ExternalHermes)|TestValidateStoredResumeConversationIDUsesHermes'
ok github.com/Nathandela/swarm/internal/skeleton 2.093s

$ GOCACHE=/tmp/swarm-hermes-go-cache go test -race -count=3 \
    ./internal/skeleton \
    -run 'TestComposeLaunchSpec_(Hermes|ExternalHermes)|TestValidateStoredResumeConversationIDUsesHermes'
ok github.com/Nathandela/swarm/internal/skeleton 4.176s

$ GOCACHE=/tmp/swarm-hermes-go-cache go test -race -count=3 \
    ./internal/skeleton \
    -run TestCaptureConversationID_RejectsExtractorOutputBeforeWriteOncePersistence
ok github.com/Nathandela/swarm/internal/skeleton 5.219s

$ GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/skeleton \
    -run 'TestCaptureConversationID|TestEndSession_CapturesConversationID' \
    -count=1
ok github.com/Nathandela/swarm/internal/skeleton 12.847s

$ GOCACHE=/tmp/swarm-hermes-go-cache go vet \
    ./internal/adapter ./internal/adapter/hermes ./internal/skeleton
# exit 0; no output
```

The two capture commands used an environment permitted to create their temporary
Unix sockets; they are not sandbox-waived results.

Focused adapter greens, all exit 0:

```text
GOCACHE=/tmp/swarm-hermes-go-cache go test ./internal/adapter/hermes
GOCACHE=/tmp/swarm-hermes-go-cache go test -race ./internal/adapter/hermes
GOCACHE=/tmp/swarm-hermes-go-cache go vet ./internal/adapter/hermes
GOCACHE=/tmp/swarm-hermes-go-cache go test -count=10 ./internal/adapter/hermes
GOCACHE=/tmp/swarm-hermes-go-cache go test -race -count=3 ./internal/adapter/hermes
GOCACHE=/tmp/swarm-hermes-go-cache go test -count=10 \
  ./internal/adapter ./internal/adapter/hermes
GOCACHE=/tmp/swarm-hermes-go-cache go test -race -count=3 \
  ./internal/adapter ./internal/adapter/hermes
```

The latest comparable ordinary/race runs completed in 0.773 s and 2.202 s;
the earlier Hermes-only stress runs completed in 1.776 s and 2.544 s. The final
combined adapter/extension repetitions also exited 0. These focused greens do
not replace GG-4's repository-wide close gate.

### Recorded RED — grid/classifier slice

The first production fixture-timeline run found a real bug that the synthetic
happy paths missed:

```text
$ GOCACHE=/tmp/swarm-hermes-status-gocache go test ./internal/engine \
    -run TestHermesGridSignatureLiveFixtureTimelines -v
--- FAIL: TestHermesGridSignatureLiveFixtureTimelines/approval
hermes_heuristic_test.go:260: live capture falsely classified idle inside
    active envelope at byte offset 19312: interaction=none
```

At that offset, prompt_toolkit's partial redraw had produced:

```text
──❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel
────────────────────────────────────────────────────────────────
```

The generic one-token composer-prefix rule accepted `──` as a profile and
therefore reported false idle. The green classifier mirrors Hermes's actual
profile grammar, `[a-z0-9][a-z0-9_-]{0,63}`, and treats this redraw as
inconclusive. This is the central false-ready safety property, demonstrated by
the real fixture rather than only a constructed unit case.

A later failing-first expansion covered upstream classic-CLI variants that the
three retained scenarios do not all exercise:

```text
$ GOCACHE=/tmp/swarm-hermes-status-gocache go test ./internal/engine \
    -run 'TestHermesGridSignature(ClassifiesUpstreamStateVariants|RequiresTerminalComposerChrome|WidthBoundary)' \
    -count=1
--- FAIL: TestHermesGridSignatureClassifiesUpstreamStateVariants/...
custom prompt ">" = (unknown, unknown, conclusive=false),
    want (idle, none, true)
--- FAIL: TestHermesGridSignatureRequiresTerminalComposerChrome/...
quoted complete busy chrome ... = (active, none, conclusive=true),
    want (idle, none, true)
quoted complete approval chrome ... = (idle, permission, conclusive=true),
    want (idle, none, true)
```

The failures spanned non-`❯` prompt suffixes, compact generation/approval/
clarification chrome, free-text and selection clarification, slash confirmation,
synchronous-command spinners, the 63/64-column breakpoint, and fully quoted
state chrome above the actual terminal composer. The green classifier now
requires source-shaped terminal composer geometry at the responsive width
boundary, so complete-looking transcript output cannot outrank the true bottom
surface.

Focused engine greens, all exit 0:

```text
GOCACHE=/tmp/swarm-hermes-status-gocache go test ./internal/engine \
  -run TestHermes -count=1
GOCACHE=/tmp/swarm-hermes-status-gocache go test ./internal/engine \
  -run TestHermes -count=10
GOCACHE=/tmp/swarm-hermes-status-gocache go test ./internal/engine
GOCACHE=/tmp/swarm-hermes-status-gocache go test -race ./internal/engine
GOCACHE=/tmp/swarm-hermes-status-gocache go vet ./internal/engine
git diff --check -- internal/engine/heuristic.go \
  internal/engine/hermes_heuristic_test.go
```

They completed in 2.433 s, 20.707 s, 5.797 s, and 27.402 s for the single
Hermes-focused run, ten-run repetition, package, and race runs respectively.
The 16-byte timeline replay feeds every byte of all three captures through the
production VT emulator and classifier; it requires active and inconclusive
working frames, forbids idle between the first and last active reading, and
verifies the last conclusive normal, permission, and clarification states.

### Recorded RED — registry, picker, and generic composition

Registry tests were written before Hermes was imported or placed in the
production table:

```text
$ go test ./internal/adapter/registry -count=1
--- FAIL: New("hermes") failed
--- FAIL: IsProduction("hermes") = false
--- FAIL: TestRegistryDrivesHermesDetection: New("hermes") failed
```

After the generic registry wiring, that command passed. The production picker
test likewise failed first because the advertised production set omitted
Hermes, then passed through the same registry-driven path:

```text
$ go test ./cmd/swarm \
    -run TestDetectAgentsWith_SurfacesProductionAdaptersWithOptionSchemas \
    -count=1
--- FAIL: production adapter set lacked hermes
```

Hermes capability and launch/resume-composition tests under
`internal/skeleton` were also authored before the adapter package existed; the
first run failed to compile because `internal/adapter/hermes` was absent. The
focused capability/composition cases passed after the adapter and generic
registry wiring landed. This records the ordering without inventing a more
specific diagnostic or timing than was retained.

## Acceptance matrix

| Requirement | Required production proof | Current evidence status |
|---|---|---|
| T-1 adapter contract | `adapter.Conformance`; deterministic and defensive unit tests | established by focused adapter tests |
| T-2 signal choice | documented reason hooks/Gateway/ACP are not v1 terminal sources | established |
| T-3 grid fallback | structural classifier tests and real capture replay | established by production 16-byte replay |
| T-4 humility | partial redraw preserves committed state; no false ready | established by engine state test and replay RED/green |
| T-5 isolation | coupling sweep proves no daemon-core/protocol/TUI Hermes branch | established by clean production-source sweep |
| T-6 characterization | real pre-implementation PTY/version/capability evidence | established |
| T-7 shipped lineup | system spec explicitly names Hermes | established by T-2/T-7 and architecture-diagram update |
| L-2 detection | production registry and picker accept characterized banner | established by registry/picker RED-green tests |
| R-2 resume | saved `resume_from` ID composes a new linked session; 0.20.6 returns to recorded Hermes cwd | saved-source composition/linkage and live default-profile restore established |
| Platform policy | native macOS arm64 smoke; Linux amd64/arm64 build/test and smoke | established: LinuxKit-native arm64 and Docker-emulated x86_64 real-Hermes smokes plus all target cross-builds |
| GG-4 close gates | build, vet, lint, tests, and proportionate race evidence | established by canonical CI `33250012015` and relay `33250012007` for race-fix commit `b1951444ca80f8269af37c068b68fcfaad31dc0b` |

### T-5 coupling sweep

The final production-source sweep found no Hermes reference in
`internal/daemon` or `internal/protocol`, and no provider-specific production
branch under `internal/tui`. Hermes remains confined to the adapter, registry,
and engine integration points, with generic skeleton/picker tests and these
documents as consumers.

```text
$ rg -n -i hermes internal/daemon internal/protocol
# no matches
$ rg -n -i hermes internal/tui -g '*.go' -g '!**/*_test.go'
# no matches
```

At review time, the shared worktree also contained an unrelated, untracked
concurrent line-editor refactor whose integration test uses
`Name: "hermes"` at `internal/tui/line_editor_integration_test.go:354`. That is
test data for independent generic TUI work, is not a Hermes-specific production
branch, and is explicitly excluded from the Hermes change set. The clean sweep
therefore does not count or claim that concurrent test as Hermes implementation
evidence.

## Required adversarial review

Before declaring the adapter production-ready, reviewers must verify all of the
following rather than infer them from happy-path fixtures:

- marker phrases in model output cannot trigger active, permission, or prompt,
  including exact bare navigation/status lines rather than only prose-prefixed
  quotations;
- modal chrome outranks stale busy/composer rows;
- partial and narrow redraws are inconclusive, never falsely idle;
- a truncated, uppercase, overlong, or unterminated session ID is rejected;
- a lone bordered `Session:` row is rejected unless nearby contiguous startup
  panel rows contain Hermes branding or the upstream unconfigured-model marker;
- a later transcript mention cannot replace the startup identity;
- corrupt saved Hermes identity metadata is rejected before binary resolution
  or resume argv composition;
- prompts containing newlines or flag-looking text remain one argv element;
- `--cli` and resume's `--no-restore-cwd` cannot be omitted by user options;
- tests and documentation do not mistake the emitted `--no-restore-cwd` flag
  for effective cwd control on Hermes `0.20.6`;
- blank/unknown options cannot smuggle arbitrary flags;
- returned option/signal collections do not mutate adapter behavior;
- no adapter source reads files, environment, network, or starts a process;
- no implementation reads or mutates Hermes profile/auth/hook configuration;
- `--worktree` is never emitted;
- `yolo` is false by default and visibly warns that approvals are bypassed;
- a native Apple Silicon binary launches Hermes without Rosetta; and
- both Linux target architectures have recorded real-Hermes smoke evidence,
  with arm64 native to the LinuxKit VM and x86_64 executed under Docker
  emulation on Apple Silicon; cross-builds alone would not satisfy this gate.

Review must also preserve the documented identity-rotation limitation: startup
capture provides a resumable ID, but no v1 test currently proves that Swarm's
write-once identity follows a later Hermes `/new`, branch, or compression-driven
continuation.

## Quality gates (GG-4)

Final measurement used:

```text
go version go1.26.5 darwin/amd64
golangci-lint 2.12.2
```

The toolchain's reported default tuple was `darwin/amd64`; the acceptance
`swarm-char` binary was separately built for `darwin/arm64`, inspected as Mach-O
arm64, and used for the native Apple Silicon smoke recorded above.

These repository and target-build gates passed:

| Command/gate | Measured result |
|---|---|
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `golangci-lint run` | pass, zero issues |
| `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` | pass |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` | pass |
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` | pass |
| native `swarm-char` artifact inspection | Mach-O 64-bit arm64 |
| `internal/adapter/hermes` under `go test -race` | pass, 2.456 s |
| `internal/adapter` under `go test -race` | pass, 2.338 s |
| `internal/engine` under `go test -race` | pass, 35.516 s |
| Hermes compose/validator/capture skeleton cases, `-race -count=3` | pass |
| production picker case, `-race -count=3` | pass |
| race-exposing skeleton E2E group, `-race -count=3` | pass after fix, 43.641 s |
| full `internal/skeleton` under `go test -race` | pass after fix, 454.737 s |

CI run `33248579855` at historical commit `8b151` then exposed a real data race
that the earlier focused and full-suite race runs at `b233cc7b` had missed.
Capability authoring in `sessionCapabilityInputs` read `d.adapterFor` while an
E2E test replaced that resolver concurrently. The production read now goes
through `d.resolveAdapter`,
and tests replace the resolver through `setAdapterForTest`, which writes under
the same `itemMu`; test setups that publish the daemon first were also moved to
set the resolver before launch where required. The race-exposing E2E repetition
and full skeleton race run above are green on fix commit
`b1951444ca80f8269af37c068b68fcfaad31dc0b`.

The local macOS full-repository race run was **not green**. Its remaining
host-baseline failures were:

- Android gate tests read a stale `android/app/libs/swarm.aar` whose exported
  surface lacks the current timestamp accessors;
- the AGY production replay timing case missed its bound under the local race
  load; and
- two `internal/upgrade` tests retained their host-state-dependent failures.

All Hermes adapter, engine, picker, validator/capture, race-exposing E2E, and
full skeleton race runs passed after the fix. The local repository-wide baseline
does not close GG-4; canonical CI remains the authority. Socket-dependent
capture tests ran in an environment that permitted their temporary Unix sockets.

Historical GitHub Actions CI run `33248192599` and relay-container run
`33248192619` completed successfully for integration commit `b233cc7b`. That CI
included:

- Linux `go test -race ./...`;
- active fuzzing, lint, and documentation checks;
- Android fresh-AAR, Gradle, and artifact gates;
- Darwin engine and integration tests;
- static `linux/amd64` and `linux/arm64` builds, plus Darwin builds; and
- the release dry-run.

Those runs remain valid historical evidence for `b233cc7b`, but they are
insufficient as final attestation because run `33248579855` later exposed the
adapter-resolver seam race. Fix commit
`b1951444ca80f8269af37c068b68fcfaad31dc0b` has green local build, vet, lint
(zero issues), all three target cross-builds, and the focused/full race results
above. Its relay-container run `33250012007` is green. Canonical CI run
`33250012015` also completed successfully. Those two runs are the sole formal
GG-4 close gate and release attestation for the race-fix commit; neither the
older green `b233cc7b` runs nor the failed `8b151` run is treated as current
attestation. CI does not install or launch Hermes; the separate real-binary
smokes below provide platform evidence and are not attributed to CI.

## Linux real-binary acceptance smokes

Both target architectures ran the real classic CLI from the pinned upstream
checkout `aff5125f8edf5095aef5d3d79bbbb101c95b9413`, reporting Hermes `0.20.6`.
The environment was the pinned minimal
`ghcr.io/astral-sh/uv:0.11.6-python3.13-trixie` image on Docker LinuxKit kernel
`6.10.14`. The Swarm integration exercised by these platform smokes is commit
`b233cc7b`. The later `b1951444ca80f8269af37c068b68fcfaad31dc0b` fix changes
the synchronized daemon adapter-resolution seam and tests, not Hermes argv or
platform behavior; the smoke provenance remains `b233cc7b` rather than being
silently relabeled as a run of the later commit.

| Target | Execution qualification | Fresh session | Resume result |
|---|---|---|---|
| `linux/arm64` | container `uname -m`: `aarch64`; native LinuxKit VM architecture | returned `pong`; clean exit matched `20260829_105452_32ede9` and reported 2 messages | restored the prior 2-message conversation, returned `pong` to a second prompt, and clean-exited with the same ID and 4 messages |
| `linux/amd64` | container `uname -m`: `x86_64`; Docker emulation on Apple Silicon, not native x86_64 hardware | returned `pong`; clean exit matched `20260829_105814_e05e3f` and reported 2 messages | restored the prior 2-message conversation, returned `pong` to a second prompt, and clean-exited with the same ID and 4 messages |

Each fresh session used the adapter's exact argv:

```text
hermes chat --cli --provider swarm-mock --model swarm-test \
  -q "Reply exactly pong"
```

Each resume used the corresponding captured ID in the adapter's exact form:

```text
hermes chat --cli --resume ID --no-restore-cwd
```

Resume was invoked from `/resume-origin` while the session's recorded cwd was
`/workspace`. On both architectures Hermes first displayed `/resume-origin`,
then changed to `/workspace` despite `--no-restore-cwd`. This independently
confirms the documented Hermes `0.20.6` cwd bug on both Linux targets.

These smokes establish detection, fresh classic-CLI execution, initial prompt,
clean-exit identity consistency, Hermes conversation restoration, continued
interaction, and the cwd limitation on both target architectures. They did not
exercise approval or clarification on Linux; those states remain supported by
the retained macOS PTY captures, adversarial classifier tests, and production
byte-stream replay rather than Linux-live modal evidence.

## Explicit limitations

- Swarm does not install or upgrade Hermes and does not validate provider
  credentials beyond observing the interactive CLI.
- Terminal status is coupled to characterized classic-CLI chrome. An upstream
  redesign can require fixture and classifier updates.
- The v1 adapter has no structured event stream, remote Gateway attachment, or
  rich structured-chat history.
- Hermes hooks are not installed or consumed.
- Swarm owns worktrees; Hermes's nested worktree mode is unsupported.
- macOS amd64/Rosetta is unsupported for Hermes. This does not remove Swarm's
  macOS amd64 release for other adapters.
- Linux live evidence covers Hermes `0.20.6` in one pinned Debian Trixie-based
  container environment: arm64 was native to the LinuxKit VM, while x86_64 ran
  under Docker emulation on Apple Silicon. It does not prove every Linux distro,
  packaging method, kernel, or native physical x86_64 host.
- Mid-process Hermes ID rotation is not represented by Swarm's write-once native
  conversation identity in v1.
- Conversation-ID evidence is structurally corroborated terminal output, not an
  authenticated event. Early startup capture plus write-once persistence
  prevents later replacement, but a deliberately exact forged structure emitted
  before any valid capture remains a residual heuristic-transport risk.
- Hermes `0.20.6` ignores `--no-restore-cwd` during its later resumed-agent
  setup, changes the process cwd, and retargets `TERMINAL_CWD` to the recorded
  session directory. Swarm emits the flag for forward compatibility but cannot
  enforce the launch-form cwd for resumed Hermes sessions.
- Hermes classic IDs are not UUIDs. The generic external
  `resume_conversation_id` path requires a lowercase UUID and then applies the
  Hermes-native validator, so neither a classic ID nor a UUID is accepted for
  direct Hermes adoption. Hands-off-handoff identity/history paths are also
  UUID/provider-layout constrained. V1 Hermes resume must use `resume_from` with
  a Swarm-saved source whose captured native conversation ID validates.
- The adapter emits `--profile` on resume when a caller explicitly supplies it,
  but the current one-key TUI `resume_from` flow does not replay persisted source
  launch options. Default-profile resume is live-proven; automatic resume of a
  named-profile Hermes session is unsupported and may look in the wrong
  profile-scoped session store.

## Closure rule

Every acceptance-matrix row now has concrete test or artifact evidence. The
clean coupling sweep and reviewer passes covered false-ready risk, identity
correctness, unsafe argv, user-config mutation, concurrency, and platform
overclaiming. Canonical CI run `33250012015` and relay run `33250012007` are
green for commit `b1951444ca80f8269af37c068b68fcfaad31dc0b`; together they
are its sole GG-4 close gate and release attestation. The Linux real-binary
smokes retain their truthful `b233cc7b` provenance because the later fix did not
change Hermes argv or platform behavior. All explicit limitations above remain
part of the attestation.
