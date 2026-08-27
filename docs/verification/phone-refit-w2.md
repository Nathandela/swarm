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
