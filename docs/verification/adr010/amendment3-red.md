# RED evidence: ADR-010 Amendment 3 — supervision modes (passive | manual | none)

date: 2026-08-18
branch: feat/supervision-modes (from main 78aa8c5d, v0.11.1)
bead: swarm-wmy (epic), swarm-mjo / swarm-q9p / swarm-ax8 / swarm-6hs (slices)

Scope: the plan's four slices, each with failing-first tests committed BEFORE the
implementation (GG-5). Excerpts below are the first lines of each slice's RED run;
the complete captures were taken from the job scratch directory at the time.

## Slice 1 — wire + persistence contract (tests: fec62582, green: 9042c185)

Test files: internal/protocol/supervision_test.go, internal/persist/supervision_test.go,
internal/daemon/supervision_test.go, internal/skeleton/rostersupervision_test.go

```
$ go vet ./internal/protocol/ ./internal/persist/ ./internal/daemon/ ./internal/skeleton/
vet: internal/persist/supervision_test.go:33:6: m.Supervision undefined (type Meta has no field or method Supervision)
vet: internal/daemon/supervision_test.go:33:9: spec.Supervision undefined (type LaunchSpec has no field or method Supervision)
vet: internal/protocol/supervision_test.go:37:28: undefined: SupervisionPassive
vet: internal/skeleton/rostersupervision_test.go:67:6: api.SetSupervisionPendingFunc undefined (type *coreAPI has no field or method SetSupervisionPendingFunc)
$ go test ./internal/protocol/ ./internal/persist/ ./internal/daemon/ ./internal/skeleton/
# github.com/Nathandela/swarm/internal/protocol [github.com/Nathandela/swarm/internal/protocol.test]
internal/protocol/supervision_test.go:55:51: unknown field Supervision in struct literal of type LaunchReq
internal/protocol/supervision_test.go:59:90: unknown field SupervisionPending in struct literal of type SessionView
...
FAIL (build failed) for all four packages — every error is an undefined frozen identifier
```

## Slice 2 — the passive supervisor (tests: 597bda33 on the slice worktree)

Test files: internal/skeleton/supervision_test.go (16 unit tests over injected fakes: arm
idempotency and mode gate, once-per-transition delivery with increasing seq, first
ready_for_review waits for working, notification text contract, unsafe source keeps the
event pending, prompt-state source is safe, retry after controller release, send error
keeps pending, newer state replaces the undelivered one, completed/child-gone retire the
record, source-gone keeps it pending, restart replays exactly once, concurrent signals
deliver once), internal/skeleton/supervision_e2e_test.go (assembly: fake source and passive
fake child, notification typed into the source, pending while the source is busy)

```
$ go vet ./internal/skeleton/
vet: internal/skeleton/supervision_test.go:194:53: undefined: supervisor
$ go test ./internal/skeleton/ -run 'Supervis'
internal/skeleton/supervision_test.go:197:12: undefined: newSupervisor
internal/skeleton/supervision_test.go:253:50: undefined: supervisionRecord
internal/skeleton/supervision_test.go:379:11: undefined: supervisionNotification
internal/skeleton/supervision_test.go:379:51: undefined: supervisionEvent
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
(-gcflags=-e confirms the compile errors are exclusively those five frozen identifiers)
```

## Slice 3 — CLI flag, TUI field, prompt, markers (tests: 5bdb35ad on the slice worktree)

Test files: cmd/swarm/handoff_test.go (TestRunHandoff_SupervisionModeTravelsInLaunchReq,
_RefusesUnknownSupervisionMode, _UsageNamesSupervisionModes), internal/tui/handoff_test.go
(TestHandoff_FormHasSupervisionAsThirdField, _SubmitPassesSupervisionModeIntoPrompt),
internal/tui/handoff_prompt_test.go (TestRenderHandoffPrompt_ModeSpecificTails,
_UnknownSupervisionRefused, plus the existing tests carried onto the 3-argument renderer),
internal/tui/supervision_marker_test.go

```
$ go vet ./cmd/swarm/ ./internal/tui/
vet: internal/tui/handoff_prompt_test.go:15:52: too many arguments in call to renderHandoffPrompt
	have (string, string, string)
	want (string, string)
$ go test ./cmd/swarm/ ./internal/tui/ -run 'Handoff|Usage|Supervision'
internal/tui/handoff_test.go:307:16: rm.handoff.supervision undefined (type handoffModel has no field or method supervision)
--- FAIL: TestRunHandoff_SupervisionModeTravelsInLaunchReq/default_is_passive
    handoff_test.go:292: LaunchReq.Supervision = "", want "passive"
--- FAIL: TestRunHandoff_SupervisionModeTravelsInLaunchReq/passive
    handoff_test.go:288: exit = 2, want 0; stderr="flag provided but not defined: -supervision ..."
--- FAIL: TestRunHandoff_RefusesUnknownSupervisionMode/eager
    handoff_test.go:315: stderr = "flag provided but not defined: -supervision ...", want it to name --supervision
FAIL	github.com/Nathandela/swarm/cmd/swarm
FAIL	github.com/Nathandela/swarm/internal/tui [build failed]
```

## Review-driven additions (each RED before its fix)

- Slice 1 (f73199c8): `TestSupervision_RemoteTierLaunchRefusesAMode` and
  `TestSupervision_ServerSendInputIsOwnerTierOnly` failed (`op "launch" code ""`; "remote-tier
  Server.SendInput succeeded") before the remote-tier refusals.
- Slice 2 (ebe3962f, orchestrator): C3 was tightened so a source waiting on its own question
  (interaction `prompt`) is not safe either — `TestSupervisor_SourceAtAPromptIsSafe` was
  replaced by the "source waiting on a question" row of the unsafe-source table; this is a
  deliberate specification change recorded in the ADR, not a test weakened to pass.
- Slice 2 (41aceb4f): `TestSupervisor_RestartEvaluatesAChildThatEndedWhileDown` ("accepted
  sends = 0, want exactly 1") and `TestSupervisor_ChildBackToWorkingClearsAStaleEvent`
  ("pending(kid) = true after the child resumed working") failed before the fix;
  `TestSupervisor_LaunchBaselineNeverCountsAsWorking` pins the raw-Turn rule.
- Slice 3 (1f8ec40f): refusal tables gained `""`, the up-key direction was pinned, the `none`
  tail test discriminates on "the human supervises" and forbids `swarm peek` / `swarm send`.

## Gates on the merged branch (2026-08-18)

`go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test ./...`
(60 packages ok), `go test -race` on internal/protocol, internal/tui, cmd/swarm,
internal/persist and the Supervis/RosterPoll tests of internal/skeleton and internal/daemon:
all green.
