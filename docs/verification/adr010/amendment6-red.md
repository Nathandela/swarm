# ADR-010 Amendment 6 — sectioned hands-off prompt with delegated reading: failing-first evidence

**Slice**: the daemon-embedded hands-off prompt (`internal/skeleton/templates/hands-off.md.tmpl`)
becomes one `<swarm_handoff>` element with six flat sections, its `<reading>` section tells a
harness that can delegate to have a subagent read the transcript, and the forgery guard refuses
tag brackets in pointer values. Tracks `swarm-o5x`. Normative:
[ADR-010](../../adr/ADR-010-inter-session-orchestration.md) Amendment 6 (G1–G3), committed before
the code.

Branch `feat/handoff-prompt-xml`, linked worktree, every command with `GOFLAGS=-buildvcs=false`.

## 1. RED (tests written, template and guard untouched)

```
--- FAIL: TestHandsOffPromptIsStructuredWithXMLSections (0.00s)
    handsoff_prompt_xml_test.go:33: prompt is not wrapped in one <swarm_handoff> element
    handsoff_prompt_xml_test.go:39: section <situation> opens 0 times and closes 0 times, want exactly once each
    handsoff_prompt_xml_test.go:39: section <pointers> opens 0 times and closes 0 times, want exactly once each
    handsoff_prompt_xml_test.go:39: section <reading> opens 0 times and closes 0 times, want exactly once each
    handsoff_prompt_xml_test.go:39: section <weighing> opens 0 times and closes 0 times, want exactly once each
    handsoff_prompt_xml_test.go:39: section <before_writing> opens 0 times and closes 0 times, want exactly once each
    handsoff_prompt_xml_test.go:39: section <then> opens 0 times and closes 0 times, want exactly once each
    handsoff_prompt_xml_test.go:48: section <pointers> is missing or closed before it opens
--- FAIL: TestHandsOffPromptDelegatesTheTranscriptReading (0.00s)
    handsoff_prompt_xml_test.go:69: section <reading> is missing or closed before it opens
--- FAIL: TestHandsOffPromptRefusesATagBracket (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	0.007s
FAIL
```

`TestHandsOffPromptRefusesATagBracket` failed on all five fields: each value holding a bracket
rendered instead of being refused (subtest lines elided above).

## 2. GREEN

```
ok  	github.com/Nathandela/swarm/internal/skeleton	0.424s
```

The run above is `-run 'HandsOffPrompt|HandsOff'`, so it includes the unchanged guards from
Amendment 4: the five pointers present, no shell recipe (no text-processing tool name, no pipe,
no backtick, no substitution, no redirect), the honesty guard (says "may still be running" and
"git status", never claims the source ended), awkward-but-legal paths intact (space, apostrophe,
percent), every empty field refused, control characters and U+2028/U+2029 refused, and the whole
hands-off composition suite. No existing test was modified.

## 3. What changed, and what did not

- `<pointers>` still holds the five bare `label: value` lines; the test asserts they are bare and
  that no tag nests inside the block.
- The E5 sentence ("Swarm injects no digest, no summary and no extract of it, because any
  condensation would be Swarm deciding on your behalf what mattered") is present word for word;
  the test asserts it inside `<reading>`.
- The delegation guidance is agent-agnostic and names the five handover headings the supervised
  method already uses.
- `forgesPromptText` refuses `<` and `>` in addition to control, line-separator and format
  characters; the refusal message now names all three classes.

## 4. Gates

Run on the branch tip in the linked worktree, 2026-09-02:

| Gate | Result |
|---|---|
| `go vet ./internal/skeleton/` | clean |
| `golangci-lint run ./internal/skeleton/...` | 0 issues |
| `go test -race ./internal/skeleton/` (the six `SWARM_*` session variables unset) | ok |
| `go test -race ./internal/tui/` | 1 failure, not this slice's: `TestLaunchAndHandoffRenderCursorAtFastNavigationBoundary/launch_prompt` renders the launch form's directory field with the test process's own cwd, and the worktree path `…/swarm-handoff-prompt/internal/tui` is one character longer than the canonical clone's, which pushes the cursor past the clamped line. The same test passes in the canonical clone and fails in the worktree with or without `-race`; the TUI is untouched here. Filed as a path-length dependence of the test. |

CI runs the full matrix on the pull request from a fixed runner path.
