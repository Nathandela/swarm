# Phone refit W2: Make sending work (verification evidence)

Bead `agents-tracker-d45a.2`. Contract: `docs/specifications/phone-refit-playbook.md` §3
(W2.1-W2.4). Worktree `refit-w2`, branch `refit/w2`. Each item below records the RED run
(tests written first, exact failure text), the GREEN run, and one negative control per
behavioural change (the fix perturbed back, the test shown failing, the file restored).

Environment: go1.26.5 darwin/amd64, golangci-lint 2.12.2 (matches `.github/workflows/ci.yml`).
The machine was shared with two other fleets and a Gradle lane during this wave; one early
skeleton run failed with `shim for session ... did not confirm serving` (launchConfirmTimeout,
load average 11) on the new test AND on the untouched control
`TestR6Interrupt_AnInterruptOnAClaudeSessionTypesTheDeclaredSequence`, and both passed their
launch on the next run. That failure is environmental and is not counted as RED evidence.

## W2.1 Daemon-authored control keys carry their provenance

Tests written first: `internal/shimwire/shimwire_test.go` (`:45`, `:64`, `:132` changed as the
contract lists, each with a one-line comment citing W2.1), `internal/shim/controlinput_test.go`
(new: `TestHello_AdvertisesControlInput`, `TestControlInput_ReachesThePTYByteExact`,
`TestSubmitMessage_AfterInterruptKeys_IsAccepted`, `TestSubmitMessage_AfterApprovalKeys_IsAccepted`,
`TestSubmitMessage_AfterTypedText_IsRefused`), `internal/skeleton/s0_controlkeys_test.go` (new:
`TestPhoneStopThenSend_IsDelivered`).

### RED

```
## shimwire
# github.com/Nathandela/swarm/internal/shimwire [github.com/Nathandela/swarm/internal/shimwire.test]
internal/shimwire/shimwire_test.go:55:3: undefined: TypeControlInput
internal/shimwire/shimwire_test.go:80:78: unknown field ControlInput in struct literal of type Control
internal/shimwire/shimwire_test.go:81:45: undefined: TypeControlInput
internal/shimwire/shimwire_test.go:81:63: unknown field Keys in struct literal of type Control
internal/shimwire/shimwire_test.go:82:49: undefined: TypeControlInput
internal/shimwire/shimwire_test.go:82:67: unknown field Keys in struct literal of type Control
FAIL	github.com/Nathandela/swarm/internal/shimwire [build failed]
FAIL
exit=1
## shim
# github.com/Nathandela/swarm/internal/shim [github.com/Nathandela/swarm/internal/shim.test]
internal/shim/controlinput_test.go:89:42: undefined: shimwire.TypeControlInput
internal/shim/controlinput_test.go:89:60: unknown field Keys in struct literal of type shimwire.Control
internal/shim/controlinput_test.go:134:14: r.hello.ControlInput undefined (type shimwire.Control has no field or method ControlInput)
internal/shim/controlinput_test.go:137:21: r.hello.Caps().ControlInput undefined (type shimwire.Caps has no field or method ControlInput)
FAIL	github.com/Nathandela/swarm/internal/shim [build failed]
FAIL
exit=1
## skeleton (after fixing the test's own expected_turn; the first run refused stale_turn, a test bug)
=== RUN   TestPhoneStopThenSend_IsDelivered
    s0_controlkeys_test.go:47: phone send after a phone Stop = code "input_busy" err IS-LIFE-5: session "ep-3461099a/v5zvhzj7wqes62aj" had input on its line, so this message was not written; nothing was typed, want delivered.
        The daemon's own ESC travelled as typed input, so the shim counts the line as dirty and refuses every send until someone presses Enter at the machine (phone-refit-playbook W2.1).
--- FAIL: TestPhoneStopThenSend_IsDelivered (6.16s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	8.085s
FAIL
```

The shimwire and shim suites fail to compile on the undefined type, fields and capability;
the skeleton test reaches the shipped defect exactly: a phone Stop followed by a phone send is
refused `input_busy`.

`TestPhoneApproveThenSend_IsDelivered` (same file) was added after the fix landed, because the
contract's "Done when" names Approve-then-send and the rig made it cheap. Its RED was taken
honestly against the pre-fix call site: `inject.go`'s `applyDecision` perturbed back to
`sub.Input([]byte(keys))`, then restored:

```
## RED: inject.go call site perturbed back to sub.Input
=== RUN   TestPhoneApproveThenSend_IsDelivered
    s0_controlkeys_test.go:84: phone send after a phone approval = code "input_busy" err IS-LIFE-5: session "ep-14bea29d/thvgcyl6pabawysx" had input on its line, so this message was not written; nothing was typed, want delivered (phone-refit-playbook W2.1)
--- FAIL: TestPhoneApproveThenSend_IsDelivered (4.79s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	6.407s
FAIL
## GREEN: call site restored
=== RUN   TestPhoneApproveThenSend_IsDelivered
--- PASS: TestPhoneApproveThenSend_IsDelivered (4.22s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	7.083s
```

### GREEN

```
## shimwire
--- PASS: TestR7BackendAttach_TheVerbExistsAndRoundTripsItsAgentArgs (0.01s)
--- PASS: TestR7BackendAttach_AnEmptyAgentArgsIsTheOrdinaryGoAhead (0.00s)
--- PASS: TestR7BackendAttach_AnOldShimToleratesTheVerbRatherThanErroring (0.00s)
--- PASS: TestVersionIsOne (0.00s)
--- PASS: TestTypeConstants (0.00s)
--- PASS: TestRoundTrip_EveryMessageType (0.00s)
--- PASS: TestExitCode_NilVersusZeroPreserved (0.00s)
--- PASS: TestEncode_OmitsZeroValuedOptionalFields (0.00s)
--- PASS: TestDecode_UnknownTypePreserved (0.00s)
--- PASS: TestDecode_UnknownFieldsTolerated (0.00s)
--- PASS: TestDecode_RejectsMalformedJSON (0.00s)
--- PASS: TestEncode_IsValidJSON (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/shimwire	1.807s
exit=0
## shim
--- PASS: TestHello_AdvertisesControlInput (0.01s)
--- PASS: TestControlInput_ReachesThePTYByteExact (0.21s)
--- PASS: TestSubmitMessage_AfterInterruptKeys_IsAccepted (0.36s)
--- PASS: TestSubmitMessage_AfterApprovalKeys_IsAccepted (0.36s)
--- PASS: TestSubmitMessage_AfterTypedText_IsRefused (0.21s)
ok  	github.com/Nathandela/swarm/internal/shim	3.729s
exit=0
## skeleton
=== RUN   TestPhoneStopThenSend_IsDelivered
--- PASS: TestPhoneStopThenSend_IsDelivered (5.35s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	7.974s
exit=0
```

### Gates (W2.1 with W2.4 also in the tree)

`golangci-lint version` = 2.12.2 (matches `.github/workflows/ci.yml`). `go build ./...` exit 0,
`go vet ./...` exit 0, `golangci-lint run ./...` 0 issues, exit 0.

`go test -race -count=1 ./...` first run: 57 packages ok, 6 FAIL. Every failure was outside W2's
code paths and had one of two causes, both environmental:

1. This session's shell inherits `SWARM_HOOK_CAPTURE`, `SWARM_SESSION_ID`, `SWARM_HOOK_SEQ_FILE`,
   `SWARM_HOOK_TOKEN`, `SWARM_DAEMON_SOCK` and `SWARM_SHIM_HOOK_SOCK` (it runs inside a
   swarm-supervised shell). `internal/hookclient` says so itself: "SWARM_SHIM_HOOK_SOCK is set in
   this test's environment; the test needs it genuinely unset". `cmd/swarm` TestRunHook_* ("no
   hook callback reached the socket") and `internal/daemon` ("daemon: another instance is already
   running", via SWARM_DAEMON_SOCK) are the same cause.
2. The machine was shared with two other fleets' `go test -race ./...` runs and a Gradle JVM
   (load average 119 at one point): `internal/adapter/opencode` was killed at the 10m default
   timeout after 660s, `internal/e2e`'s replay saw idle 2.2-2.8s after activity (wants >= 3s),
   `internal/skeleton` TestS18's gateway did not stop within its 2s window.

Rerun of exactly those six packages with the variables unset:

```
env -u SWARM_HOOK_CAPTURE -u SWARM_SESSION_ID -u SWARM_HOOK_SEQ_FILE -u SWARM_HOOK_TOKEN \
    -u SWARM_DAEMON_SOCK -u SWARM_SHIM_HOOK_SOCK \
    go test -race -count=1 -timeout 40m ./cmd/swarm ./internal/adapter/opencode ./internal/daemon \
    ./internal/e2e ./internal/hookclient ./internal/skeleton
ok  	github.com/Nathandela/swarm/cmd/swarm	235.670s
ok  	github.com/Nathandela/swarm/internal/adapter/opencode	3.777s
--- FAIL: TestLaunch_InjectsHookEnvToAgent (1.10s)      launch_inject_test.go:73: SWARM_SESSION_ID="", want "32nvvkucg4d2axov"
--- FAIL: TestSurvival_RealKillNineReconnectsAll (20.09s) realkill_test.go:161: host launched 0/3 agents before kill
FAIL	github.com/Nathandela/swarm/internal/daemon	271.148s
--- FAIL: TestE2E_ReplayProductionPath_AgyOpencode (22.80s) idle observed only 2.210389709s after the first active sample (want >= 3s)
FAIL	github.com/Nathandela/swarm/internal/e2e	72.671s
ok  	github.com/Nathandela/swarm/internal/hookclient	7.552s
ok  	github.com/Nathandela/swarm/internal/skeleton	750.035s
```

`internal/skeleton` (the package that launches shims hundreds of times, including the new
control-keys tests) is green under `-race`. The two packages still red fail on wall-clock windows
(an env dump read while the agent was still writing it; 3 of 3 launches not confirmed within the
launch timeout; a 3s idle window) at load average 119. Their third run, queued to start when the
load average drops below 25, is recorded below.

Third and fourth runs (same `env -u ...` prefix, `-race -count=1 -timeout 40m`), started once the
load average had fallen below 25:

```
load at start: { 18.97 67.16 70.98 }   go test ./internal/daemon ./internal/e2e
--- FAIL: TestLaunch_FiltersClientEnv (0.26s)   launch_test.go:203: allowlisted var did not reach the agent; env=
FAIL	github.com/Nathandela/swarm/internal/daemon	64.100s
ok  	github.com/Nathandela/swarm/internal/e2e	31.687s

load at start: { 17.02 22.99 44.53 }   go test ./internal/daemon
ok  	github.com/Nathandela/swarm/internal/daemon	66.937s
```

`internal/daemon` failed on a DIFFERENT env-dump test each time (`TestLaunch_InjectsHookEnvToAgent`,
then `TestLaunch_FiltersClientEnv`, both reading the agent's env dump as an empty file) and passed
whole on the next run; the env-dump reader races the writer under load and is unrelated to W2.
Every package in the module has therefore passed at least once with W2.1 in the tree.

### Negative controls (clean tree at 1e9657c3; each file restored with `git checkout --`)

```
## Negative control 1: shim writes control_input through the COUNTING path (WriteInput)
 internal/shim/server.go | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
--- FAIL: TestSubmitMessage_AfterInterruptKeys_IsAccepted (0.01s)
    controlinput_test.go:158: submit after a daemon-authored interrupt was refused "input_busy"; want delivered. The phone's Stop counted as typing, and every later send is input_busy until someone presses Enter at the m
--- FAIL: TestSubmitMessage_AfterApprovalKeys_IsAccepted (0.00s)
    controlinput_test.go:170: submit after a daemon-authored dialog answer was refused "input_busy"; want delivered
FAIL
FAIL	github.com/Nathandela/swarm/internal/shim	2.388s
FAIL
server.go restored

## Negative control 2: interruptTurn back to sub.Input (typed provenance)
 internal/skeleton/chat.go | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
--- FAIL: TestPhoneStopThenSend_IsDelivered (6.82s)
    s0_controlkeys_test.go:47: phone send after a phone Stop = code "input_busy" err IS-LIFE-5: session "ep-9981481b/egyt4alllc4bprz7" had input on its line, so this message was not written; nothing was typed, want delivered.
        The daemon's own ESC travelled as typed input, so the shim counts the line as dirty and refuses every send until someone presses Enter at the machine (phone-refit-playbook W2.1).
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	8.478s
FAIL
chat.go restored
```

In control 1 `TestControlInput_ReachesThePTYByteExact` and `TestSubmitMessage_AfterTypedText_IsRefused`
still pass (the bytes still arrive; a typed draft still refuses), so the failing pair isolates the
counting decision. Residual risk as implemented, stated: a daemon-authored key that lands on an
EMPTY dialog dirties one character the shim does not count; bounded, rare, and preferable to a
session poisoned until someone presses Enter at the machine. The counter does not reset at turn
end, so an owner's half-typed draft outlives the turn and still refuses.

Deviation from the contract's file list: `internal/protocol/fromdaemon.go` gained
`shimStream.ControlInput` and `ErrControlInputUnsupported`. The list names only `types.go` for
`internal/protocol`, but the daemon's sole shim-wire implementation lives in `fromdaemon.go`
(`Submit` is there too), and without it `tapSub.ControlKeys`'s type assertion could never succeed
in production. `tapSub.ControlKeys` also treats `ErrControlInputUnsupported` as the old-shim
degrade (the contract's snippet only shows the missing-interface case), because the production
upstream always has the method once it exists.

## W2.4 Claude's synthetic prompts are not messages

Fixture check first, as the contract asks: no `prompt` in `internal/adapter/claude/testdata/interaction/*.json`
opens with `<` (the six fixtures carry PreToolUse/PostToolUse/PermissionRequest/Stop bodies), so
the golden corpus tests at `interaction_test.go:232` and `:259` are unchanged and `:54` is untouched.

Tests written first, appended to `internal/adapter/claude/interaction_test.go`:
`TestUserPromptSubmit_SyntheticEnvelopesShapeNothing` (table over the fourteen recorded tags, plus
an attributed `<teammate-message ...>`), `TestUserPromptSubmit_ARealPromptContainingAngleBracketsIsKept`
(the negative control: `fix the <div> wrapper`, `<title>Foo</title> what does this render?`),
`TestIsSyntheticPrompt_GoldenTagListMatchesTheRecordedCorpus`, `TestIsSyntheticPrompt_AnUnclosedEnvelopeIsKept`.

### RED

The two behavioural tests were run first so the RED is the defect and not a compile error:

```
--- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing (0.02s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/system-reminder (0.01s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/task-notification (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/teammate-message (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/agent-message (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/tool_use_error (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/persisted-output (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/command-name (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/command-message (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/local-command-caveat (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/local-command-stdout (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/local-command-stderr (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/bash-input (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/bash-stdout (0.00s)
    --- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing/bash-stderr (0.00s)
--- PASS: TestUserPromptSubmit_ARealPromptContainingAngleBracketsIsKept (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	1.094s
FAIL

(one failure's text) interaction_test.go:401: a <system-reminder> envelope shaped 1 item(s), want 0: the CLI's own prompt was drawn as a message the owner typed: [{Kind:user_message ... Text:<system-reminder>
```

Then the two predicate tests, which fail to compile until the predicate exists:

```
# github.com/Nathandela/swarm/internal/adapter/claude [github.com/Nathandela/swarm/internal/adapter/claude.test]
internal/adapter/claude/interaction_test.go:437:24: undefined: syntheticPromptTags
internal/adapter/claude/interaction_test.go:438:75: undefined: syntheticPromptTags
internal/adapter/claude/interaction_test.go:441:6: undefined: syntheticPromptTags
internal/adapter/claude/interaction_test.go:455:6: undefined: isSyntheticPrompt
internal/adapter/claude/interaction_test.go:459:6: undefined: isSyntheticPrompt
FAIL	github.com/Nathandela/swarm/internal/adapter/claude [build failed]
FAIL
```

### GREEN

`go test -race -count=1 ./internal/adapter/claude/` (whole package, 55 tests):

```
--- PASS: TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems (0.02s)
--- PASS: TestGoldenCorpus_PassesCheckInteractionFixture (0.03s)
--- PASS: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing (0.01s)
--- PASS: TestUserPromptSubmit_ARealPromptContainingAngleBracketsIsKept (0.00s)
--- PASS: TestIsSyntheticPrompt_GoldenTagListMatchesTheRecordedCorpus (0.00s)
--- PASS: TestIsSyntheticPrompt_AnUnclosedEnvelopeIsKept (0.00s)
ok  	github.com/Nathandela/swarm/internal/adapter/claude	3.620s
```

### Gates

`go build ./...` exit 0, `go vet ./...` exit 0, `golangci-lint run ./...` 0 issues (2.12.2).
The module-wide `go test -race ./...` recorded under W2.1 already had this change in the tree for
every package that ran after `internal/adapter/claude` (the adapter package itself was tested
with the change in the targeted GREEN above). For this commit the packages that import
`internal/adapter/claude` (`go list -deps`) were run again under `-race` with the SWARM_* variables
unset:

```
ok  	github.com/Nathandela/swarm/internal/adapter/claude	4.466s
ok  	github.com/Nathandela/swarm/internal/adapter/registry	3.714s
FAIL	github.com/Nathandela/swarm/internal/skeleton	1221.946s   (549 pass, 54 fail: every failure "shim ... did not confirm serving",
                                                                    during the other lane's Gradle compile burst; no test failed on an assertion)
ok  	github.com/Nathandela/swarm/cmd/swarm	76.581s
ok  	github.com/Nathandela/swarm/cmd/swarm-char	14.352s

rerun at load average 3.4:
ok  	github.com/Nathandela/swarm/internal/skeleton	425.273s   (497 pass, 0 fail; includes TestSlice0_* and the two W2.1 control-key tests)
```

W2.1's untouched tests, run by name under `-race` (`s0_realclipty_test.go` is behind
`//go:build realcli` and needs a real `claude`; it is untouched and was not run):

```
--- PASS: TestG4_ARefusedSendLeavesTheSessionsTerminalUntouched (4.75s)
--- PASS: TestSlice0_OwnerEnterAndTwoPhoneSendsEachLandAsOneWholeMessage (1.28s)
--- PASS: TestSlice0_AnOwnerDraftAndItsEnterNeverSplitAPhoneSend (1.07s)
--- PASS: TestSlice0_TwoPhoneSendsAreNotMergedIntoOneSubmit (0.54s)
--- PASS: TestSlice0_AnOwnerDraftIsNeverMergedWithAPhoneSend (0.22s)
ok  	github.com/Nathandela/swarm/internal/skeleton	10.598s
```

### Negative controls (clean tree at a1189887; the file restored with `git checkout --`)

```
## Negative control 1: the closed-tag half of the rule removed (any prompt opening with a listed tag is dropped)
 internal/adapter/claude/interaction.go | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
--- FAIL: TestIsSyntheticPrompt_AnUnclosedEnvelopeIsKept (0.00s)
    interaction_test.go:456: isSyntheticPrompt("<system-reminder> keeps showing up in my transcripts, what is it?") = true, want false: the envelope is never closed
    interaction_test.go:456: isSyntheticPrompt("<system-reminder>") = true, want false: the envelope is never closed
    interaction_test.go:456: isSyntheticPrompt("<command-name>/clear") = true, want false: the envelope is never closed
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	0.944s
FAIL
interaction.go restored

## Negative control 2: the filter bypassed at the UserPromptSubmit case
 internal/adapter/claude/interaction.go | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
--- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeNothing (0.02s)
FAIL
FAIL
interaction.go restored
```

Control 1 leaves the three other tests green (the envelopes are still dropped, the bracketed
prompts still kept), so the failure isolates the closed-tag half of the rule; control 2 fails only
the envelope table, so the bracketed-prompt control is not the filter's accident.

## W2.2 Every machine reason gets plain words

Tests written first: `MachineCodeRoutingTest.kt` gained `every daemon code this build ships has a
sentence` (key set equals the eighteen literals; `sentenceFor` returns each row; an unseen code
falls to the router's UNKNOWN sentence; `toToken` still has three rows); `:63` and `:69` untouched.
`ErrorRoutingRefusalCopyTest.kt:165` (`the deliberate absences stay absent`) now also asserts
`unavailable`/`invalid_field` are absent from `toToken` and present in `sentence`, with a one-line
comment citing W2.2. `mobile/ksvb5_refusalcopy_test.go` gained `TestRefusalSentences_CoverEverySchemaCode`,
which reads the eighteen `schema` constants by value and checks each has a row in the Kotlin
table; `:62` untouched.

### RED

Go (`go test -count=1 ./mobile/ -run TestRefusalSentences_CoverEverySchemaCode`):

```
    ksvb5_refusalcopy_test.go:284: ErrorRouting.kt declares no `val sentence: Map<String, String> = mapOf(`
--- FAIL: TestRefusalSentences_CoverEverySchemaCode (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/mobile	1.237s
FAIL
```

Kotlin, on the serialised Gradle lane (`w2-gradle.sh --tests 'dev.swarm.phone.ui.*Routing*'`;
the RED is the test source set failing to compile, so the filter only trims execution):

```
START=1787863105 (Thu Aug 27 22:38:25 CEST 2026)
e: android/app/src/test/kotlin/dev/swarm/phone/ui/ErrorRoutingRefusalCopyTest.kt:176:77 Unresolved reference 'sentence'.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/MachineCodeRoutingTest.kt:95:51 Unresolved reference 'sentence'.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/MachineCodeRoutingTest.kt:98:48 Unresolved reference 'sentenceFor'.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/MachineCodeRoutingTest.kt:99:46 Unresolved reference 'sentence'.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/MachineCodeRoutingTest.kt:99:55 Cannot infer type for type parameter 'V'. Specify it explicitly.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/MachineCodeRoutingTest.kt:103:51 Unresolved reference 'sentenceFor'.
BUILD FAILED in 2m 45s
GRADLE_EXIT=1
SUMMARY: xml files=0 stale(older than START)=0 tests=0 failures=0 errors=0
AAR unchanged (mtime 1787859790)
script exit=1
```

### GREEN

Go (`go test -race -count=1 ./mobile/ -run 'TestRefusalSentences_CoverEverySchemaCode|TestOutcomeOf_TheMachinesOwnWordsAreNotReplaced'`):

```
--- PASS: TestOutcomeOf_TheMachinesOwnWordsAreNotReplaced (0.00s)
--- PASS: TestRefusalSentences_CoverEverySchemaCode (0.00s)
ok  	github.com/Nathandela/swarm/mobile	2.428s
```

Kotlin, the whole suite on the serialised lane (`w2-gradle.sh`: waits for `pgrep -f gradle-wrapper.jar`
to be empty, records START, runs `./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache`,
then checks every result XML is newer than START and the AAR did not move):

```
START=1787863777 (Thu Aug 27 22:49:37 CEST 2026)
BUILD SUCCESSFUL in 39m 1s
GRADLE_EXIT=0
SUMMARY: xml files=202 stale(older than START)=0 tests=1590 failures=0 errors=0
AAR unchanged (mtime 1787859790)
script exit=0
```

`go build ./...`, `go vet ./...` and `golangci-lint run ./...` (0 issues) were run with the
`mobile/` test in the tree (recorded under W2.4's gates). `toToken` still has three rows.

No production caller of `sentenceFor` lands in this wave: the contract names the table as a
"copy-only sibling" and no caller, and the only `routeMachineCode` caller in the app
(`SessionDetailPanel.composerVerdictFor`) keys `ComposerModel.noticeFor` on the routed state's
name. "No refusal from a shipped daemon renders the UNKNOWN sentence" therefore still needs a
caller (W5's words pass is the natural home); the table is ready for it.

### Negative control (clean tree at 9bc0f7f2; the file restored with `git checkout --`)

One perturbation, checked from both sides: the `stale_instance` row removed from
`MachineRefusalCodes.sentence`.

```
## Negative control: one row (stale_instance) removed from MachineRefusalCodes.sentence
 android/app/src/main/kotlin/dev/swarm/phone/ui/ErrorRouting.kt | 1 -
 1 file changed, 1 deletion(-)
--- Go ---
--- FAIL: TestRefusalSentences_CoverEverySchemaCode (0.00s)
    ksvb5_refusalcopy_test.go:299: schema code "stale_instance" has no row in MachineRefusalCodes.sentence; a reader gets the UNKNOWN sentence for it
FAIL
FAIL	github.com/Nathandela/swarm/mobile	1.137s
FAIL
--- Kotlin (targeted, serialised lane) ---
START=1787866677 (Thu Aug 27 23:37:57 CEST 2026)
MachineCodeRoutingTest > every daemon code this build ships has a sentence FAILED
    java.lang.AssertionError at MachineCodeRoutingTest.kt:95
> Task :app:testDebugUnitTest FAILED
BUILD FAILED in 1m 59s
GRADLE_EXIT=1
SUMMARY: xml files=4 stale(older than START)=0 tests=24 failures=1 errors=0
AAR unchanged (mtime 1787859790)
ErrorRouting.kt restored
```

## W2.3 One refusal, said once -- NOT STARTED (contract premise wrong; ruling requested)

Verified against the tree before any edit: `PhoneSurface.renderComposerVerdict` (`:4478-4497`) sets
`composerRefusal` and calls `say(PressFeedback.ofRefusal(verdict.notice, verdict.detail))`; `say()`
(`:5098`) writes `outcome.text` (drawn as `DetailTag.OUTCOME`) and a toast whose mono suffix is
`verdict.detail`, the machine's words. The composer-notice path
(`SessionDetail.composerRefusal` -> `SessionDetailPanel.composerNotice` (`:1097`) ->
`SessionDetailView` `DetailTag.COMPOSER_NOTICE` (`:529`)) carries only
`ComposerModel.noticeFor(state).copy`; no `noticeDetail` cell exists on that path (grep
`composerDetail|noticeDetail` over the three files: nothing). So the contract's sentence "`verdict.detail`
still reaches the reader through the notice's detail cell" is not true of the shipped code: deleting the
`say()` block alone drops the machine's words for every composer refusal, the F4 defect class
`android/gate/r6_chat_ui_test.go:334` fences. The two Kotlin tests the contract names build the view
with `outcome = ""` and so already pass; they cannot be RED against a PhoneSurface-only change.

Per fleet protocol item 3 and the wave's brief ("if the contract turns out to be wrong ... stop and say
so; do not widen the wave"), W2.3 was not started. The orchestrator was sent the options: (A) delete
`say()` as written and accept the loss of the words; (B) also add a `composerRefusalDetail` cell
through `SessionScreens.kt`, `SessionDetailPanel.kt`, `SessionDetailView.kt` and `PhoneSurface.kt`
(about fifteen lines, four files outside the list), which makes the contract's sentence true and the
view test genuinely RED; (C) defer. Recommendation: B. The RED-able test that does exist for the
PhoneSurface half is a Go gate in `android/gate/` reading `renderComposerVerdict`'s body
(`r6SurfaceFunc`): it must not contain `say(`, while `renderInterruptVerdict` still does (a refused
Stop keeps its toast).

### The caller (orchestrator ruling, playbook as recorded on main at 5ee276f: "The caller")

Tests written first: `MachineCodeRoutingTest.kt` `an unmapped code with a sentence keeps UNKNOWN's
routing and says its sentence` (:63 and :69 untouched); `SessionDetailVerdictTest.kt` `an unmapped
code with a sentence says that sentence and keeps the draft`; `SessionDetailComposerTest.kt` `an
unmapped code with a sentence shows that sentence under the composer`.

RED (`w2-gradle.sh --tests` over the three classes; 32 tests, the three new ones fail):

```
START=1787867164 (Thu Aug 27 23:46:04 CEST 2026)
MachineCodeRoutingTest > an unmapped code with a sentence keeps UNKNOWN's routing and says its sentence FAILED
SessionDetailComposerTest > an unmapped code with a sentence shows that sentence under the composer FAILED
SessionDetailVerdictTest > an unmapped code with a sentence says that sentence and keeps the draft FAILED
> Task :app:testDebugUnitTest FAILED
BUILD FAILED in 2m 12s
GRADLE_EXIT=1
SUMMARY: xml files=3 stale(older than START)=0 tests=32 failures=3 errors=0
AAR unchanged (mtime 1787859790)
script exit=1

org.junit.ComparisonFailure: expected:<[Chat is off for this session].> but was:<[Something failed in a way the app does not recognise. Try again, and report it if it keeps happening].>
org.junit.ComparisonFailure: expected:<[Chat is off for this session].> but was:<[Your message was refused and not delivered].>
org.junit.ComparisonFailure: expected:<[Chat is off for this session].> but was:<[Your message was refused and not delivered].>
```

Implementation: `ErrorRouter.routeMachineCode` returns `unknown.copy(message = sentence)` for a code
with a sentence and no `toToken` row (state and remedy stay UNKNOWN; the `sentence` map is read
directly because `sentenceFor` falls back to `routeMachineCode` itself); a code with no sentence
returns `unknown` unchanged. `SessionDetailPanel.composerVerdictFor` prefers the routed message for
such a code and carries the CODE as the refusal token, and the panel builder says
`MachineRefusalCodes.sentence[composerRefusal]` before `ComposerModel.noticeFor(...)`, so
`structured_unsupported` reads "Chat is off for this session." under the composer.

FOUND BY THE GO GATE, AND A DEVIATION FROM THE TABLE: `go test ./android/gate` failed
`TestR1_NoProductionKotlinOffersTakeControl` (`r1_takecontrolgone_test.go:65` bans the literal
"Take control" on the chat path, owner ruling R1 of 2026-08-26) on the contract's
`stale_generation` row "Your turn at this terminal ended. Take control again." -- a remedy naming
a verb the product no longer offers. The row now reads "Your turn at this terminal ended."; W5
owns the final words. This gate had not been run after the table landed in 3ae018fc (only
`mobile/`, build/vet/lint and the Kotlin suite were), so that commit trips it on its own; the
caller commit corrects it.

GREEN: `go test -count=1 ./android/gate` ok (9.3s, the R1 gate green again); `go test -race ./mobile`
ok (43.9s); Kotlin, the whole suite on the lane:

```
START=1787867580 (Thu Aug 27 23:53:00 CEST 2026)
BUILD SUCCESSFUL in 4m 26s
GRADLE_EXIT=0
SUMMARY: xml files=202 stale(older than START)=0 tests=1593 failures=0 errors=0
AAR unchanged (mtime 1787859790)
script exit=0
```

Negative control (clean tree at 90999445; the file restored with `git checkout --`): `routeMachineCode`
perturbed back to plain `unknown` for an unmapped code. The routing and verdict tests fail; the panel
test stays green because the builder reads the map itself, so the failure isolates the router.

```
## Negative control: routeMachineCode back to plain UNKNOWN for an unmapped code
 android/app/src/main/kotlin/dev/swarm/phone/ui/ErrorRouting.kt | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
START=1787867891 (Thu Aug 27 23:58:11 CEST 2026)
MachineCodeRoutingTest > an unmapped code with a sentence keeps UNKNOWN's routing and says its sentence FAILED
SessionDetailVerdictTest > an unmapped code with a sentence says that sentence and keeps the draft FAILED
> Task :app:testDebugUnitTest FAILED
BUILD FAILED in 2m 20s
GRADLE_EXIT=1
SUMMARY: xml files=3 stale(older than START)=0 tests=32 failures=2 errors=0
AAR unchanged (mtime 1787859790)
ErrorRouting.kt restored
```

## W2.3 One refusal, said once -- option B (orchestrator ruling; playbook as recorded on main at 5ee276f)

WHY THE CONTRACT'S SENTENCE WAS FALSE, AND WHAT MADE IT TRUE. The composer-notice path carried
only `ComposerModel.noticeFor(state).copy` (`SessionDetail.composerRefusal` ->
`SessionDetailPanel.composerNotice` -> `DetailTag.COMPOSER_NOTICE`); `verdict.detail`, the
machine's words, reached the reader only through `say()`'s toast suffix, which is what W2.3
deletes. So a detail cell is built: `SessionDetail.composerRefusalDetail` ->
`SessionDetailPanel.composerNoticeDetail` -> `DetailTag.COMPOSER_NOTICE_DETAIL`, the kit's
`noticeDetail` (mono, tertiary ink, `.sheet2 .ctx`) drawn directly under the notice and absent
when the machine sent no words; `PhoneSurface` keeps `verdict.detail` as `composerRefusalDetail`
where it keeps `composerRefusal`, clears it in the same two places, and no longer calls `say()`
for a composer refusal. `renderInterruptVerdict` keeps its `say()`: a refused Stop is not a
composer refusal.

Files (the widened list, exactly): `ui/SessionScreens.kt`, `ui/screens/SessionDetailPanel.kt`,
`ui/screens/SessionDetailView.kt`, `PhoneSurface.kt` (the function, the var, its two clears and the
pass into `SessionDetail`).

Tests written first: `android/gate/w23_refusalonce_test.go`
`TestW23_ARefusedSendIsSaidOnceAndNeverToasted` (`renderComposerVerdict`'s body contains no
`say(` and keeps `composerRefusalDetail = verdict.detail`; `renderInterruptVerdict` still contains
`say(`, the control); `SessionDetailViewTest.kt` `the machine's words are drawn under the composer
notice and absent when empty`. Fences kept: `SessionDetailViewTest.kt` `a refused send says its
sentence exactly once across the view tree`; `SessionDetailComposerTest.kt:315` broadened
(`DetailTag.OUTCOME` absent when `DetailTag.COMPOSER_NOTICE` present). `SessionDetailVerdictTest.kt:117`
untouched.

### RED

Go (`go test -count=1 ./android/gate -run TestW23 -v`):

```
    w23_refusalonce_test.go:25: renderComposerVerdict still calls say(...): the refusal goes to the outcome line and a toast on top of the composer notice, so one refused send says its sentence three times (phone refit W2.3). Body:
    w23_refusalonce_test.go:30: renderComposerVerdict never keeps verdict.detail as composerRefusalDetail, so once the toast is gone the machine's own words reach nobody (W2.3's detail cell; the F4 class TestR6R3_TheTwoM3ReadsSayWhatTheMachineSaid fences). Bod
--- FAIL: TestW23_ARefusedSendIsSaidOnceAndNeverToasted (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	0.927s
FAIL
```

Kotlin (`w2-gradle.sh --tests` over SessionDetailViewTest and SessionDetailComposerTest; the test
source set fails to compile on the field and the tag that do not exist yet):

```
START=1787868085 (Fri Aug 28 00:01:25 CEST 2026)
e: android/app/src/test/kotlin/dev/swarm/phone/ui/screens/SessionDetailViewTest.kt:546:13 No parameter with name 'composerRefusalDetail' found.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/screens/SessionDetailViewTest.kt:578:46 Unresolved reference 'COMPOSER_NOTICE_DETAIL'.
e: android/app/src/test/kotlin/dev/swarm/phone/ui/screens/SessionDetailViewTest.kt:594:63 Unresolved reference 'COMPOSER_NOTICE_DETAIL'.
BUILD FAILED in 1m 46s
GRADLE_EXIT=1
SUMMARY: xml files=3 stale(older than START)=3 tests=32 failures=2 errors=0
AAR unchanged (mtime 1787859790)
script exit=1
```

### GREEN

`go vet ./android/gate/` exit 0, `golangci-lint run ./android/gate/...` 0 issues,
`go test -count=1 ./android/gate` ok (TestW23 passes; the R1, R6 and every other gate stays green).
Kotlin, the whole suite on the lane:

```
START=1787868253 (Fri Aug 28 00:04:13 CEST 2026)
BUILD SUCCESSFUL in 4m 10s
GRADLE_EXIT=0
SUMMARY: xml files=202 stale(older than START)=0 tests=1595 failures=0 errors=0
AAR unchanged (mtime 1787859790)
script exit=0
```

### Negative controls (clean tree at 62bf30d9; each file restored with `git checkout --`)

```
## Negative control 1 (Go gate): the say() call put back into renderComposerVerdict
 android/app/src/main/kotlin/dev/swarm/phone/PhoneSurface.kt | 1 +
 1 file changed, 1 insertion(+)
--- FAIL: TestW23_ARefusedSendIsSaidOnceAndNeverToasted (0.01s)
    w23_refusalonce_test.go:25: renderComposerVerdict still calls say(...): the refusal goes to the outcome line and a toast on top of the composer notice, so one refused send says its sentence three 
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	0.939s
FAIL
PhoneSurface.kt restored

## Negative control 2 (Kotlin view): the detail cell no longer drawn under the notice
 .../app/src/main/kotlin/dev/swarm/phone/ui/screens/SessionDetailView.kt | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
START=1787868551 (Fri Aug 28 00:09:11 CEST 2026)
SessionDetailViewTest > the machine's words are drawn under the composer notice and absent when empty FAILED
> Task :app:testDebugUnitTest FAILED
BUILD FAILED in 2m 23s
GRADLE_EXIT=1
SUMMARY: xml files=2 stale(older than START)=0 tests=31 failures=1 errors=0
AAR unchanged (mtime 1787859790)
SessionDetailView.kt restored
```

In control 2 the fence `a refused send says its sentence exactly once across the view tree` and
the broadened `SessionDetailComposerTest:315` stay green, so the failure isolates the detail cell.

## Round-1 review fixes

### W2.4 amended: keep the turn, drop the bubble

WHY THE CONTRACT'S "shapes zero items" WAS AMENDED, AND BY WHOM. Round-1 review (both reviewers,
orchestrator ruling of 2026-08-28): `internal/skeleton/interaction.go` `turnIDLocked` opens a turn
on every `KindUserMessage` and the Claude adapter sets `TurnRef` nowhere, so a user_message is the
ONLY turn-opening signal Claude gives. W2.4 as first shipped (33ca6d6d) shaped the CLI's envelopes
as nothing, so a session whose work starts from a teammate-message / task-notification /
command-name envelope opened NO turn: its tool items carried `turn_id ""`, the phone drew it idle,
and Stop was refused (`chat.go` stale_turn / empty expected_turn). Ruling: the adapter returns the
envelope as a `KindUserMessage` with the new `adapter.SourceSynthetic` (admitted by `Validate`);
the daemon runs the turn-open logic on it (`openSyntheticTurn` -> `turnIDLocked`) and neither
persists nor publishes it. `SourceSynthetic` never reaches the wire, so interaction-schema.md
§3.1's `phone|owner|derived` stays the wire's whole vocabulary.

Tests written first: `internal/adapter/interaction_test.go` (constant table gains
`{SourceSynthetic, "synthetic"}`; `TestValidate_AdmitsTheSyntheticSource`);
`internal/adapter/claude/interaction_test.go` (`TestUserPromptSubmit_SyntheticEnvelopesShapeNothing`
becomes `TestUserPromptSubmit_SyntheticEnvelopesShapeASyntheticUserMessage`: each envelope shapes
exactly one item with `Source == SourceSynthetic`; the two negative controls -- a real prompt
containing `<`, an unclosed envelope -- shape `SourceOwner`);
`internal/skeleton/synthetic_turn_test.go` `TestSyntheticPrompt_OpensATurnAndPublishesNoMessage`
(through the SHIPPED adapter: the next tool item carries the fresh non-empty turn id; the
published stream holds no user_message, re-read after three append windows).

RED (compile, then behavioural once only the vocabulary existed, then the daemon):

```
## adapter
# github.com/Nathandela/swarm/internal/adapter [github.com/Nathandela/swarm/internal/adapter.test]
internal/adapter/interaction_test.go:88:4: undefined: SourceSynthetic
internal/adapter/interaction_test.go:338:97: undefined: SourceSynthetic
FAIL	github.com/Nathandela/swarm/internal/adapter [build failed]
FAIL
## adapter/claude
# github.com/Nathandela/swarm/internal/adapter/claude [github.com/Nathandela/swarm/internal/adapter/claude.test]
internal/adapter/claude/interaction_test.go:407:95: undefined: adapter.SourceSynthetic
FAIL	github.com/Nathandela/swarm/internal/adapter/claude [build failed]
FAIL
## skeleton
=== RUN   TestSyntheticPrompt_OpensATurnAndPublishesNoMessage
    synthetic_turn_test.go:43: a synthetic prompt opened no turn: every tool item that follows carries turn_id "", the phone draws the session idle, and Stop is refused for want of a turn to name
--- FAIL: TestSyntheticPrompt_OpensATurnAndPublishesNoMessage (4.08s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	6.196s
FAIL

## adapter (vocabulary only)
ok  	github.com/Nathandela/swarm/internal/adapter	0.777s
## adapter/claude: behavioural RED with the vocabulary present
    interaction_test.go:415: an envelope shaped [], want exactly one synthetic user_message [{Kind:user_message Status: Ref: ClientRef: TurnRef: Text:<system-reminder>
    interaction_test.go:415: an envelope shaped [], want exactly one synthetic user_message [{Kind:user_message Status: Ref: ClientRef: TurnRef: Text:<task-notification>
    interaction_test.go:415: an envelope shaped [], want exactly one synthetic user_message [{Kind:user_message Status: Ref: ClientRef: TurnRef: Text:<teammate-message>
    (... one line per recorded tag, 14 in all)
```

GREEN (`go test -race`; build/vet/lint with a per-worktree `GOLANGCI_LINT_CACHE`, review item 4):

```
## GREEN
ok  	github.com/Nathandela/swarm/internal/adapter	2.086s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	2.601s
--- PASS: TestSyntheticPrompt_OpensATurnAndPublishesNoMessage (4.62s)
ok  	github.com/Nathandela/swarm/internal/skeleton	7.339s
## build/vet/lint (GOLANGCI_LINT_CACHE=/private/tmp/claude-501/-Users-Nathan-Code-swarm/fff31caf-df8b-416f-8990-ecc58eb0dcaf/scratchpad/golangci-cache)
build exit=0
vet exit=0
0 issues.
lint exit=0
```

Gate (`go test -race -count=1 -timeout 40m`, env-unset prefix, the touched packages and their
importers):

```
load at start: { 18.43 10.83 8.07 } (Fri Aug 28 00:26:45 CEST 2026)
ok  	github.com/Nathandela/swarm/internal/adapter	2.726s
ok  	github.com/Nathandela/swarm/internal/adapter/agy	2.627s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	3.078s
ok  	github.com/Nathandela/swarm/internal/adapter/codex	2.651s
ok  	github.com/Nathandela/swarm/internal/adapter/detect	10.688s
ok  	github.com/Nathandela/swarm/internal/adapter/fixtureio	2.584s
ok  	github.com/Nathandela/swarm/internal/adapter/opencode	2.775s
ok  	github.com/Nathandela/swarm/internal/adapter/refadapter	2.339s
ok  	github.com/Nathandela/swarm/internal/adapter/registry	1.880s
ok  	github.com/Nathandela/swarm/internal/skeleton	421.562s
ok  	github.com/Nathandela/swarm/cmd/swarm	66.559s
ok  	github.com/Nathandela/swarm/cmd/swarm-char	10.039s
ok  	github.com/Nathandela/swarm/android/gate	108.333s
ok  	github.com/Nathandela/swarm/mobile	56.540s
test exit=0
```

Negative controls (clean tree at f8808cd3; each file restored with `git checkout --`):

```
## Negative control 1: the daemon drops the synthetic message WITHOUT opening the turn
 internal/skeleton/interaction.go | 1 -
 1 file changed, 1 deletion(-)
    synthetic_turn_test.go:43: a synthetic prompt opened no turn: every tool item that follows carries turn_id "", the phone draws the session idle, and Stop is refused for want of a turn to name
--- FAIL: TestSyntheticPrompt_OpensATurnAndPublishesNoMessage (3.25s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	4.965s
FAIL
interaction.go restored

## Negative control 2: the adapter shapes the envelope as the OWNER's message
 internal/adapter/claude/interaction.go | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)
--- FAIL: TestUserPromptSubmit_SyntheticEnvelopesShapeASyntheticUserMessage (0.01s)
FAIL
FAIL
    synthetic_turn_test.go:54: the first published item is a "user_message"; the envelope reached the wire as a message
--- FAIL: TestSyntheticPrompt_OpensATurnAndPublishesNoMessage (2.32s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	3.970s
FAIL
 M internal/skeleton/chat.go
 M internal/skeleton/sessiontap.go
claude/interaction.go restored
```

### Docs (round-1 item 2)

`internal/skeleton/chat.go` ("THE QUESTION THAT IS ANSWERABLE") now says that `control_input`
frames are written but not counted, and names the residual (an approval key on an already-closed
dialog, one uncounted character); `internal/skeleton/sessiontap.go`'s mode note says readWrite
also forwards Submit and ControlKeys. Round-1 item 3 (control_input is fire-and-forget, the PTY
write error discarded exactly as for TDataIn) is recorded on the bead. Round-1 item 4: every lint
run from here on uses a per-worktree `GOLANGCI_LINT_CACHE` under the scratchpad.
