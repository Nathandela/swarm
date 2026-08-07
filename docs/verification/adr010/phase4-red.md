# RED evidence: ADR-010 Phase 4 — agents install + TUI trigger (agents-tracker-prq0)

date: 2026-08-07
HEAD: 98bbd5e
worktree: .claude/worktrees/inter-session-orchestration

Scope: ADR-010 A5 Phase 4 (D3) — PIECE 1 `swarm agents install`, a one-template-source
generator writing `/swarm-handoff` and `/swarm-delegate` command files into each CLI's
convention; PIECE 2 the TUI trigger, a keybinding on the selected roster row that injects
the same slash-command text via the owner-tier `send_input` op (A2), gated to a session at a
prompt.

Test files (all new):

| File | Piece |
| --- | --- |
| `cmd/swarm/agentsinstall_test.go` | 1 — content contract, targets, dry-run, skip, overwrite, non-interference, dispatch wiring |
| `internal/tui/handoff_test.go` | 2 — the key, the gate, the busy message, selection targeting, the help surface |
| `internal/tui/fakes_test.go` | shared fake extension only — `fakeClient.SendInput` + `sendInputCalls()` (edit, not new file) |

## Frozen API the tests are written against

```go
// cmd/swarm — mirrors the spawnStateDir injectable-home precedent (spawn.go)
var agentsInstallHome = func() (string, error) { return os.UserHomeDir() }
func runAgents(args []string, stdout, stderr io.Writer) int        // dispatches "install"; else usage, exit 2
func runAgentsInstall(args []string, stdout, stderr io.Writer) int // "swarm agents install [--dry-run]"

// main.go dispatch gains, alongside the existing verb cases:
//   case "agents": return runAgents(args[1:], stdout, stderr)
// (direct dispatch, NOT dispatchAgentVerb — this verb touches no daemon socket)

// internal/tui/tui.go:31 — Client widened by exactly protocol.Client's own signature
type Client interface {
    /* existing methods unchanged */
    SendInput(id string, req protocol.SendInputReq) error
}
```

Install targets (checked against `docs/research/inter-session-orchestration-landscape.md`
and `internal/adapter/agy`, `internal/adapter/opencode`, neither of which documents a
command/prompt-file convention):

| CLI | Target | Convention |
| --- | --- | --- |
| claude | `<home>/.claude/commands/{swarm-handoff,swarm-delegate}.md` | documented in the research doc |
| codex | `<home>/.codex/prompts/{swarm-handoff,swarm-delegate}.md` | documented in the research doc |
| agy | — | no known convention: skipped, reported |
| opencode | — | no known convention: skipped, reported |

## Content contract pinned (golden-ish key lines, not full-file goldens)

Both generated files must carry, verbatim: the shared cheat-sheet lines (`swarm watch <id>
--until needs_input|ready_for_review`, `swarm peek <id>`, `swarm send <id> --text "..."`,
`swarm send <id> --key enter`, `swarm ls --json`, `swarm kill <id>`) and the POINTERS a
handoff/delegation document must record (`$SWARM_SESSION_ID`, "transcript", "git branch",
"issue"). They differ ONLY in: the spawn line they name (`swarm spawn --cli <target-cli>
--handoff <handoff-file>` vs. `swarm spawn --cli <target-cli> --delegate <handoff-file>
--worktree`, the latter carrying D2's "--worktree recommended" default) and the intent
phrase ("handoff document" vs. "delegation document"), each absent from the other variant.

## Behavior pinned beyond content

- **Always regenerate, never touch a neighbor.** A pre-existing `swarm-handoff.md` with
  stale content is fully overwritten; an unrelated file already in the same directory
  (`my-other-command.md`) survives byte-for-byte and the directory ends with exactly the
  pre-existing file plus the two generated ones.
- **`--dry-run` writes nothing** and prints every path it WOULD write, so an agent can
  inspect before touching `$HOME`.
- **A real run prints one line per file actually written**, each containing that file's path.
- **agy/opencode are skipped, not guessed**: stdout names each CLI with "skipped: no known
  command convention", and the home directory carries only the `.claude`/`.codex` entries
  this run created — no stray per-CLI path.
- **`swarm agents install` needs no daemon**: proven by driving it through `dispatch`
  directly with no daemon socket configured or reachable.

## TUI trigger pinned

- Key **`h`** on the general view (unbound today — checked against every `k.Text ==`/`k.Code
  ==` case in `general.go`/`launch.go`/`attach.go` and `keymap_test.go`), acting on the
  SELECTED row (moved off the first row and re-checked, mirroring the rename/kill-confirm
  identity discipline already in `general.go`).
- On a `needs_input` or `ready_for_review` row: exactly one `SendInput(id,
  {Text:"/swarm-handoff ", Submit:false})` — the literal trailing space, unsubmitted.
- On any other group (`working`, `completed`): zero `SendInput` calls and the view carries
  the exact refusal `"session is busy — try when it is at a prompt"` via the existing
  transient-banner surface (`generalModel.setBanner`, the same mechanism the ended-row Enter
  refusal and a failed launch/resume already use).
- The bottom status bar (`generalStatus`) lists the key alongside its neighbors (`n new`, `e
  rename`, `ctrl+x kill`).

## Commands

```
$ go build ./...
BUILD OK

$ go vet ./cmd/swarm/...
$ go test ./cmd/swarm/... -run 'TestAgentsInstall|TestDispatch_Agents' -v

$ go test ./internal/tui/... -v
```

## Failing output (trimmed)

Piece 1 — the whole `cmd/swarm` package fails to COMPILE on undefined production symbols
(the repo's standard compile-fail red; `go build ./...` above stays green — production code
is untouched):

```
# github.com/Nathandela/swarm/cmd/swarm [github.com/Nathandela/swarm/cmd/swarm.test]
cmd/swarm/agentsinstall_test.go:122:10: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:123:2: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:124:21: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:136:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:190:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:211:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:263:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:310:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:332:13: undefined: runAgentsInstall
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]
```

Piece 2 — `internal/tui` COMPILES (the fake gained `SendInput` without widening the `Client`
interface itself, so `New(f, ...)` still typechecks) and fails BEHAVIORALLY, each for the
missing key binding / help text:

```
--- FAIL: TestKeymap_HandoffInjectsOnNeedsInputSession (0.00s)
    handoff_test.go:84: SendInput called 0 times, want exactly 1: []
--- FAIL: TestKeymap_HandoffInjectsOnReadyForReviewSession (0.00s)
    handoff_test.go:100: SendInput called 0 times, want exactly 1: []
--- FAIL: TestKeymap_HandoffRefusedOnWorkingSession (0.01s)
    handoff_test.go:119: a busy session must surface "session is busy — try when it is at a prompt", got:
        ... (unchanged general board, no banner line)
--- FAIL: TestKeymap_HandoffRefusedOnCompletedSession (0.00s)
    handoff_test.go:136: a non-prompt session must surface "session is busy — try when it is at a prompt", got:
        ... (unchanged general board, no banner line)
--- FAIL: TestKeymap_HandoffTargetsTheSelectedRow (0.00s)
    handoff_test.go:157: SendInput called 0 times, want exactly 1: []
--- FAIL: TestStatusBar_HelpListsHandoffKey (0.00s)
    handoff_test.go:169: bottom status bar must list the handoff key, got last line
        "  ↑↓ navigate   ⏎ attach (ctrl+q returns)   e rename   n new   ctrl+x kill   esc quit"
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	25.911s
```

`go test ./internal/tui/...` otherwise reports 162 pre-existing passes unchanged — nothing
else regressed; `go vet ./internal/tui/...` is clean.

## RED interpretation

Piece 1's red is a full package compile failure on undefined production symbols only
(`agentsInstallHome`, `runAgentsInstall`) — the strongest form of "missing implementation."
`runAgents` and the `dispatch` "agents" case are exercised only through `dispatch(...)` in
the new file (no direct symbol reference), so they do not appear in the undefined-symbol
list; `TestDispatch_AgentsInstallNeedsNoDaemon` is the test that will fail once the two
symbols above exist but `case "agents":` is not yet wired into `dispatch`'s switch (an
unrecognized top-level verb falls through to the generic top-level usage today, which
already satisfies the weaker `TestDispatch_Agents{No,Unknown}SubcommandPrintsUsage` pair —
those two remain valid regression tests post-green, they are just not independently
discriminating pre-green while the package cannot build at all).

Piece 2's red is behavioral, not a compile failure: extending `fakeClient` with a `SendInput`
method (recorded in `internal/tui/fakes_test.go`, per the task's explicit allowance —
"adding a method to a shared fake is not weakening a test") does not require widening the
`Client` interface for the package to compile, since Go interface satisfaction only requires
the methods actually in scope at each call site. The six new tests instead fail on their
actual assertions: zero `SendInput` calls where one was expected, and no busy-message/help
text present — exactly the shape "the key does not exist yet" produces. No existing test was
modified; `fakes_test.go`'s diff is additive only (new struct fields, one new method, one new
accessor).

## What each test pins

| Test | Behavior |
| --- | --- |
| `TestAgentsInstall_WritesClaudeAndCodexCommandFiles` | All 4 files exist under the injected home with the shared cheat-sheet + pointer substrings, and each variant's own spawn line + intent phrase (never the other variant's). |
| `TestAgentsInstall_DryRunWritesNothingButPrintsPaths` | `--dry-run`: no file created, every path named on stdout. |
| `TestAgentsInstall_UnknownConventionCLIsSkipped` | agy/opencode reported "skipped: no known command convention"; home ends with exactly `.claude`/`.codex`. |
| `TestAgentsInstall_NeverTouchesOtherFilesInTargetDir` | A pre-existing unrelated file in both target dirs survives byte-identical; the dir ends with exactly it plus the two generated files. |
| `TestAgentsInstall_OverwritesExistingGeneratedFile` | A stale `swarm-handoff.md` is fully replaced with fresh content. |
| `TestAgentsInstall_UnknownFlagIsMisuse` | An unrecognized flag is exit 2. |
| `TestDispatch_AgentsNoSubcommandPrintsUsage` / `_UnknownSubcommandPrintsUsage` | `swarm agents` / `swarm agents bogus` print a usage message and exit 2. |
| `TestDispatch_AgentsInstallNeedsNoDaemon` | `dispatch(["agents","install","--dry-run"])` succeeds with no daemon reachable — proves direct dispatch wiring, not `dispatchAgentVerb`. |
| `TestKeymap_HandoffInjectsOnNeedsInputSession` / `_OnReadyForReviewSession` | The gate's two positive cases: exactly one `SendInput{Text:"/swarm-handoff ", Submit:false}` against the selected session. |
| `TestKeymap_HandoffRefusedOnWorkingSession` / `_OnCompletedSession` | The gate's negative cases: zero `SendInput` calls, the exact busy message shown instead. |
| `TestKeymap_HandoffTargetsTheSelectedRow` | With two eligible rows, the SELECTED one (not the first) is the target. |
| `TestStatusBar_HelpListsHandoffKey` | The bottom bar lists the new key alongside its neighbors. |

## Amendment 1 (2026-08-07): contract fixes from the adversarial RED review

An adversarial review of this RED slice found eight defects IN THE TEST CONTRACT
itself (not the not-yet-written implementation). Fixed here, while still RED, in
the same two test files — no production code, no test weakened.

1. **Unrunnable pinned watch line.** `parseWatchUntil` (agentverbs.go) takes ONE
   `--until` value; the previously-pinned `swarm watch <id> --until
   needs_input|ready_for_review` has an unquoted `|`, a shell pipe, not a
   runnable command. `cheatSheetLines` now pins the single runnable
   `swarm watch <id> --until ready_for_review`; a new `unrunnableWatchLine`
   constant is asserted ABSENT from both generated docs, and a new
   `watchUntilProseValues` (`needs_input`, `completed`, `change`) requires the
   other three values to be named in prose nearby.
2. **Wrong exit-code claim.** `watchTimeoutExit = 2` is watch's TIMEOUT exit
   specifically; several other verbs (`sendpeek.go`'s `misuseExit`) also exit 2
   for MISUSE, so a blanket "exit 2 = misuse" line would misdescribe watch. The
   doc must now carry `watchTimeoutExitPhrase` ("watch exits 2 on timeout") and
   must NOT carry `blanketExitTwoMisuseClaim` ("exit 2 = misuse").
3. **Missing `--no-submit`.** `sendNoSubmitMention` pins that the cheat sheet
   teaches send's draft mode (`sendpeek.go`'s `--no-submit`).
4. **Weak dry-run assertion.** `TestAgentsInstall_DryRunWritesNothingButPrintsPaths`
   now also asserts the four target directories (`.claude`, `.claude/commands`,
   `.codex`, `.codex/prompts`) do not exist after `--dry-run` — an
   MkdirAll-before-decide implementation now fails the test, not just a
   WriteFile-before-decide one.
5. **Missing error paths.** Two new tests:
   `TestAgentsInstall_HomeResolutionErrorExitsOneWritesNothing` (an injected
   `agentsInstallHome` error exits 1, names the failure on stderr, writes
   nothing) and
   `TestAgentsInstall_WriteFailureMidwayExitsOneNamesFailureOnStderr` (the
   second target, `swarm-delegate.md`, is made unwritable by chmod'ing its
   directory read-only AFTER the first target is pre-created — so the first
   write only needs to truncate an existing file while the second, a new
   dentry, cannot be created — proving a MIDWAY failure exits 1 and names the
   failing path on stderr, while the first target's write still lands).
6. **Weak help-text assertion.** `TestStatusBar_HelpListsHandoffKey` checked
   only the word "handoff" appeared anywhere on the footer, which a stray
   mention (not tied to the `h` key) would satisfy. It now requires the exact
   `"h handoff"` pairing, matching how neighbors render (`n new`, `e rename`,
   `ctrl+x kill`).
7. **Dead `sendInputErr` fake field.** Two new tests:
   `TestKeymap_HandoffSendInputErrorBanners` (a daemon-refused `SendInput` must
   banner "handoff failed: ..." and not crash the model — mirroring
   `TestKill_ErrorSurfacesToBanner` / `TestRename_SkewErrorBanners`) and
   `TestKeymap_HandoffOnEmptyRosterIsNoOp` (pressing `h` against an empty
   roster sends nothing — this one already passes today, since `h` is
   currently unbound; it is a valid regression pin going forward, not a
   currently-red assertion).
8. **Unpinned agents usage.** `TestDispatch_AgentsNoSubcommandPrintsUsage` and
   `TestDispatch_AgentsUnknownSubcommandPrintsUsage` now also require "install"
   in the stderr output (an agents case that falls through to the generic
   top-level usage would satisfy only the old, weaker "usage" substring check).
   A new `TestUsage_ListsAgentsVerb` pins that the top-level `usage` const
   documents `swarm agents`, alongside the existing `TestUsage_ListsSpawnVerb` /
   `TestUsage_ListsAgentVerbs` / `TestUsage_ListsSteeringVerbs` precedents.

### Fresh failing run (post-fix, still RED)

Piece 1 — `cmd/swarm` still fails to COMPILE on the same two undefined
production symbols, now referenced from more places:

```
$ go vet ./cmd/swarm/...
# github.com/Nathandela/swarm/cmd/swarm
vet: cmd/swarm/agentsinstall_test.go:156:10: undefined: agentsInstallHome

$ go test ./cmd/swarm/...
# github.com/Nathandela/swarm/cmd/swarm [github.com/Nathandela/swarm/cmd/swarm.test]
cmd/swarm/agentsinstall_test.go:156:10: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:157:2: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:158:21: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:170:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:242:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:276:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:328:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:375:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:397:13: undefined: runAgentsInstall
cmd/swarm/agentsinstall_test.go:407:10: undefined: agentsInstallHome
cmd/swarm/agentsinstall_test.go:407:10: too many errors
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]
```

`go build ./...` stays green — production code is untouched.

Piece 2 — `internal/tui` still COMPILES and fails BEHAVIORALLY. 163 pre-existing
plus new passes (up from 162: the new empty-roster no-op test passes today, as
expected — see item 7 above); seven fail, the six original plus the new
error-banner test:

```
$ go test ./internal/tui/... -v
--- FAIL: TestKeymap_HandoffInjectsOnNeedsInputSession
--- FAIL: TestKeymap_HandoffInjectsOnReadyForReviewSession
--- FAIL: TestKeymap_HandoffRefusedOnWorkingSession
--- FAIL: TestKeymap_HandoffRefusedOnCompletedSession
--- FAIL: TestKeymap_HandoffTargetsTheSelectedRow
--- FAIL: TestStatusBar_HelpListsHandoffKey
--- FAIL: TestKeymap_HandoffSendInputErrorBanners
    handoff_test.go:192: Update(h) on a needs_input row returned a nil command;
    the handoff trigger is not wired yet
--- PASS: TestKeymap_HandoffOnEmptyRosterIsNoOp
FAIL	github.com/Nathandela/swarm/internal/tui
```

`go vet ./internal/tui/...` is clean; `gofmt -l` on both amended files reports
nothing.

`git diff --stat` for this amendment: `cmd/swarm/agentsinstall_test.go` and
`internal/tui/handoff_test.go` only — no production file touched.

## Post-GREEN disclosures (2026-08-07)

Three notes the review required on the record:

1. During the completion round's GREEN, three test artifacts were edited additively to
   absorb the new `h handoff` help entry and the widened TUI Client interface:
   `internal/tui/epic8_test.go` (blockingClient stub gains SendInput),
   `internal/tui/style_hoist_test.go` (two pins), and
   `internal/tui/testdata/TestGoldenGeneralView.golden`. Verified by word-diff: the only
   behavioral delta is the inserted help entry; no assertion weakened.

2. Review fix round (RED first, then template): two assertions added to
   TestAgentsInstall_WritesClaudeAndCodexCommandFiles — the mktemp line must use
   trailing X's with no suffix (BSD mktemp takes a suffixed template literally, so every
   invocation on darwin collided on one fixed path), and each CLI's copy must open with
   its own invocation form (`/swarm-<slug>` for claude command files,
   `/prompts:swarm-<slug>` for codex prompt files). Both failed against the shipped
   template ("got \# /swarm-handoff" on the codex copies; "XXXXXX.md" present), then the
   template gained `{{.Invocation}}` with per-target rendering and the suffix was
   dropped.

3. A GREEN-phase agent ran the real installer against the actual $HOME mid-round,
   leaving four draft command files; all four verified stale (they carried the
   XXXXXX.md defect) and deleted. Tests exercise installs only under injected temp
   homes.
