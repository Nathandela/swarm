# Phase 0a RED evidence — launch-form worktree regression (bead agents-tracker-v4ir)

Date: 2026-08-07

## Bug

`submitLaunch()` in `internal/tui/launch.go` (~line 393) composes `protocol.LaunchReq`
but never copies `launchModel.worktree` into it — neither `req.Worktree` (schema
field: `internal/protocol/schema/schema.go:194`) nor `Options`. The TUI worktree
checkbox is silently dropped, so the daemon's `launchOptions` (`internal/protocol/
server.go:96`, which reads `req.Worktree` to inject `OptionWorktree`) never sees the
toggle regardless of what the user selected.

## Tests added

`internal/tui/launch_worktree_test.go`:

- `TestLaunch_SubmitCarriesWorktreeToggle` — tabs to the worktree field, toggles it
  on with Space, submits, and asserts the fired `LaunchReq.Worktree == true`.
- `TestLaunch_SubmitWorktreeOffByDefault` — submits with the checkbox untouched
  (off) and asserts the fired `LaunchReq.Worktree == false`, guarding against a fix
  that hardcodes `true` instead of wiring the actual form field.

## Command and failing output

```
$ go test ./internal/tui/ -run 'TestLaunch_SubmitCarriesWorktreeToggle|TestLaunch_SubmitWorktreeOffByDefault' -v
=== RUN   TestLaunch_SubmitCarriesWorktreeToggle
    launch_worktree_test.go:41: LaunchReq.Worktree = false, want true: the toggle was on but submitLaunch dropped it
--- FAIL: TestLaunch_SubmitCarriesWorktreeToggle (0.02s)
=== RUN   TestLaunch_SubmitWorktreeOffByDefault
--- PASS: TestLaunch_SubmitWorktreeOffByDefault (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	1.199s
FAIL
```

`TestLaunch_SubmitCarriesWorktreeToggle` fails for the right reason: the form
correctly toggles `launchModel.worktree` to `true` (asserted inline before submit),
but the fired `LaunchReq.Worktree` stays `false` because `submitLaunch` never copies
it — pinning the exact bug. `TestLaunch_SubmitWorktreeOffByDefault` passes today
(the zero-value `false` coincidentally matches an untouched toggle) and is expected
to keep passing after the fix; it exists to catch a regression where a fix
hardcodes `Worktree: true` instead of wiring `lm.worktree`.

`go build ./...` and `go vet ./internal/tui/...` are both clean — the test file
compiles against the existing `protocol.LaunchReq.Worktree` field and
`launchModel.worktree`/`isWorktree()`; the failure is a runtime assertion, not a
compile error, because the target field already exists and only the copy in
`submitLaunch` is missing.
