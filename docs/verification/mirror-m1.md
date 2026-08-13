# Mirror program -- wave M1 evidence

Wave M1 of `docs/specifications/mirror-program.md` ("The approve moment", bead
`agents-tracker-dwwv.2`). Each item below records the question it settled, the test that
settled it, the verbatim result, the outcome taken, and the tracker actions that followed.
Later agents APPEND their section (`## M1.2 ...`, `## M1.3 ...`) below this one; do not
rewrite an earlier section, and keep the run output verbatim.

| # | Item | Bead | Status |
|---|---|---|---|
| M1.1 | Characterization fixture: the permission dialog's grid signature and accepted keys, version-stamped | `dwwv.2.1` | settled -- 5 recorded grids of claude 2.1.231, key map confirmed by side effects, recognizer shipped |

---

## M1.1 -- The permission dialog: what it looks like, and which keys it obeys

### The question

M1.2 will apply a phone approval by injecting keys into the PTY the daemon owns, gated on the
live VT grid still showing the dialog. Both halves of that gate were assumptions: nobody had
recorded, against a real CLI, (a) what the dialog's grid actually looks like when this repo's
own emulator renders it, or (b) which keystrokes the dialog accepts and what each one does.
The spec is explicit that these are "a recorded characterization fixture per Claude version
(M1.1), not an assumption".

### The instrument

`internal/smoke/permdialog.go` + `internal/smoke/permdialog_test.go`, under the repo's
established never-CI real-CLI pattern (Epic 11): `//go:build realcli` AND `SWARM_REALCLI=1`,
both required, so no CI job and no ordinary `go test ./...` can launch a billable CLI.

    SWARM_REALCLI=1 go test -tags realcli -run TestRealCLIPermissionDialog \
      -timeout 20m -v ./internal/smoke

It differs from the existing `runScenario` in one way the job required: a permission dialog is
a TRANSIENT state, so a harness that only renders the final grid cannot see it. `dialogSession`
keeps the emulator live for the whole run and lets the scenario WAIT on rendered-grid
predicates, inject keystrokes at those moments, and dump a labelled snapshot at each one.

Environment discipline: every run happened in a scratch directory OUTSIDE this repo
(`$TMPDIR/swarm-permdialog/{bash,edit}/scratch`), with `SWARM_DAEMON_SOCK` unset and every
`SWARM_*` variable stripped from the child environment, so no swarm hook could fire. The
owner's global `~/.claude/settings.json` was READ and never modified.

Two findings forced by that inspection, both recorded here because they are the reason the
first two runs produced nothing:

1. The owner's global settings set `"defaultMode": "auto"`. The captures therefore pass
   `--permission-mode manual` explicitly, so the dialog is not suppressed by the operator's
   own configuration.
2. Under manual mode, claude 2.1.231 STILL runs read-only commands (`echo`) in its own sandbox
   with NO approval dialog at all. The dialog is only reached by a command with a side effect
   outside the workspace, so the capture uses `touch /tmp/swarm-m1-<n>.marker`.

A third came from the run itself: when the harness is executed from inside a Claude Code
session, the child inherits the parent's session markers and changes behavior -- an observed
run printed `⚠ Transcript saving is off — inherited CLAUDE_CODE_CHILD_SESSION marker` and
carried the parent's session title. `dialogEnv` now strips `CLAUDE*` as well; the daemon spawns
from no such session, so that is the faithful environment, not a convenience.

### Cost

**Five real `claude` runs**, `sonnet`, one tiny prompt per turn, each killed promptly by the
harness (`--strict-mcp-config`, no MCP servers, no orphan processes left):

| # | Variant | Outcome | API turns |
|---|---|---|---|
| 1 | bash | stalled at the folder-trust gate (wrong wait marker); no session started | 0 |
| 2 | bash | `echo` ran sandboxed, no dialog ever appeared | 1 |
| 3 | bash | **full capture**, 4 dialogs, 4 key experiments | 4 |
| 4 | edit | dialog reached but the wait marker was Bash-only, so the run timed out on it | 1 |
| 5 | edit | **full capture**, 4 dialogs, 4 key experiments | 4 |

### `claude --version`

    2.1.231 (Claude Code)

### The recorded grids

`internal/adapter/claude/testdata/permdialog/` (README.md there names the version, the capture
date, and the harness). Each `*.snap.json` is a verbatim `vt.Snap` -- the exact snapshot bytes
the daemon's output tap carries -- rendered by this repo's own emulator at 100x30; the `*.txt`
beside it is that grid's visible text for human reading.

| File | What it is |
|---|---|
| `bash-approval-2.1.231.snap.json` | Bash tool approval (positive) |
| `edit-approval-2.1.231.snap.json` | Edit tool approval (positive) |
| `neg-composer-idle-2.1.231.snap.json` | idle composer (negative) |
| `neg-working-2.1.231.snap.json` | mid-output, turn in flight (negative) |
| `neg-trust-dialog-2.1.231.snap.json` | the folder-trust dialog (negative, adversarial) |

#### Bash variant, verbatim (rows 18-29 of the recorded grid)

    ────────────────────────────────────────────────────────────────────────────────────────────────────
     Bash command

       touch /tmp/swarm-m1-one.marker
       Create marker file

     Do you want to proceed?
     ❯ 1. Yes
       2. Yes,mandealways allowpaccessttoytmp/ from this project
       3. No

     Esc to cancel · Tab to amend · ctrl+e to explain

#### Edit variant, verbatim (rows 14-29 of the recorded grid)

    ────────────────────────────────────────────────────────────────────────────────────────────────────
     Edit file
     note.txt
    ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
     1 -one
     1 +ONE
     2  two
     3  three
     4  four
    ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
     Do you want to make this edit to note.txt?
     ❯ 1. Yes
       2. Yes, allowiallOedits duringxthis session (shift+tab)
       3. No

     Esc to cancel · Tab to amend

**Option 1 is preselected in both** (the `❯` marker). The question row DIFFERS per variant --
Bash asks `Do you want to proceed?`, Edit asks `Do you want to make this edit to note.txt?`
and carries the file name -- so the recognizer does not anchor on it.

#### The garbled option-2 row is not a transcription error

Both dialogs render option 2 with stray letters (`Yes,mandealways allowpaccessttoytmp/`,
`allowiallOedits duringxthis`). The raw PTY bytes say why:

    \r\x1b[1B  \x1b[4G\x1b[38;2;153;153;153m2. \x1b[39mYes,\x1b[12Gand\x1b[16Galways\x1b[23Gallow
    \x1b[29Gaccess\x1b[36Gto\x1b[39G\x1b[1mtmp\x1b[22m/\x1b[44Gfrom\x1b[49Gthis\x1b[54Gproject

Claude paints the row as words separated by `CSI n G` column jumps and never clears the cells
it jumps over -- its differential renderer believes those cells already hold spaces. It even
ERASES the row the same way later (`  \x1b[5G   \x1b[9G    \x1b[14G  \x1b[17G        `), which
is what proves the belief. Wherever this emulator's cell content differs from that model, the
stale character shows through. The rows written CONTIGUOUSLY and then cleared to end of line
survive intact:

    \x1b[1mBash command\r                            (title, contiguous)
    \x1b[1mEdit file\r                               (title, contiguous)
     ❯ 1. Yes\x1b[K                                  (contiguous + erase to EOL)
       3. No\x1b[K                                   (contiguous + erase to EOL)

That is exactly why the recognizer anchors on those four and on nothing else. **The divergence
itself is a real emulator-fidelity gap and is filed as its own follow-up bead** -- it is
recorded here rather than fixed here, because M1.1's job was to characterize what the daemon
really sees.

### The key map, characterized empirically

Each key was pressed BARE (no Enter after it) on a live dialog with option 1 preselected, and
the answer was taken from the SIDE EFFECT, not from the screen -- the collapsed tool card reads
`Ran 1 shell command` either way, so the screen alone cannot tell allow from deny.

Bash run, harness log verbatim:

    startup screen: "Do you want to proceed?"
    10-bash-dialog options: [❯ 1. Yes   2. Yes,mandealways allowpaccessttoytmp/ from this project   3. No] (preselected="1")
    turn1: sending bare "1" (no CR)
    turn1 after bare "1": dialog present=false
    20-bash-dialog-2 options: [...] (preselected="1")
    turn2: sending ESC (0x1b)
    turn2 after ESC: dialog present=false
    30-bash-dialog-3 options: [...] (preselected="1")
    turn3: sending bare CR
    turn3 after CR: dialog present=false
    40-bash-dialog-4 options: [...] (preselected="1")
    turn4: sending bare deny digit "3"
    turn4 after "3": dialog present=false

Ground truth, the four marker files the four turns asked for:

    $ ls -la /tmp/swarm-m1-*.marker
    -rw-r--r--@ 1 Nathan  wheel  0 Aug 13 16:10 /tmp/swarm-m1-one.marker
    -rw-r--r--@ 1 Nathan  wheel  0 Aug 13 16:10 /tmp/swarm-m1-three.marker

`one` (bare `1`) and `three` (bare `CR`) exist; `two` (ESC) and `four` (bare `3`) do not.

Edit run, same four keys against the Edit dialog, with the seed file as ground truth
(`one\ntwo\nthree\nfour`, one line per turn so a denied turn cannot invalidate the next):

    $ cat note.txt
    ONE
    two
    THREE
    four

Turn 1 (bare `1`) and turn 3 (bare `CR`) applied their edit; turn 2 (ESC) and turn 4 (bare `3`)
did not. **Identical semantics in both variants.**

| Key sent (verbatim bytes) | Bash variant | Edit variant | What the CLI does immediately |
|---|---|---|---|
| `1` | ALLOW | ALLOW | selects option 1 and submits it in one keystroke; the dialog is gone within 2.5 s and the tool proceeds. **No Enter is needed.** |
| `\r` (bare Enter) | ALLOW | ALLOW | confirms the CURRENTLY SELECTED option, which is 1 by default. Allowed both runs -- but the outcome depends on where the selection sits, so it is not what the recognizer returns. |
| `3` | DENY | DENY | selects option 3 and submits it in one keystroke, while option 1 was still highlighted -- proving the digits are ABSOLUTE, not relative to the selection. Dialog dismissed, tool refused, turn ends. |
| `\x1b` (Esc) | DENY | DENY | dismisses the dialog and refuses the tool. Same observable outcome as `3`. |

Option numbering is IDENTICAL across the two variants at 2.1.231 (`1. Yes` / `2. <scoped
always-allow>` / `3. No`), so one key map serves both -- but the recognizer reads the digits off
the screen per dialog rather than hardcoding them, so a variant that renumbers is
characterized, not mis-keyed. **Option 2 is never sent by anything**: in both variants it is a
STANDING grant ("always allow access to /tmp/ from this project", "allow all edits during this
session"), which is not what a single phone tap authorizes.

### The recognizer

`internal/adapter/claude/permdialog.go`:

    func RecognizePermissionDialog(snap *vt.Snap) (PermissionDialog, bool)

    type PermissionDialog struct {
        Variant   string // VariantBash | VariantEdit
        AllowKeys string // keystrokes that approve  ("1")
        DenyKeys  string // keystrokes that refuse   ("3")
    }

Pure, total, no state and no I/O, so it stays inside the adapter's T-5 import boundary
(contract + `vt` only, still enforced by `TestImportBoundary_T5`) while being callable from the
daemon/skeleton layer, which is where the tap's snapshots arrive -- the same package
`internal/skeleton` already imports for the interaction producer.

It positively matches ALL of: the option row whose label is exactly `Yes` and the one whose
label is exactly `No`, both in the bottom region (the engine's own 12-row convention), in that
order; and, walking up from them to the dialog box's own top rule, a title row that is one of
the two RECORDED titles. Anything else is `false`. Anchoring the title to the box rather than
searching the screen keeps an earlier dialog's title, scrolled up in the transcript, from
naming the variant of the dialog now on screen.

It is **the stricter reader of a screen the engine already classifies, not a second opinion
about it.** The engine's claude grid signature (ADR-007, `internal/engine/heuristic.go`) answers
"is this session blocked on a human" for every modal claude dialog and is untouched by this
work; the recognizer answers the narrower question the injection needs. The subset relation is
pinned by a test, not asserted in prose:
`TestRecognizedDialog_IsPermissionToTheStatusEngineToo` runs both positive fixtures through the
REAL engine via the adapter's own `SignalSources()` and requires `interaction=permission`.

### RED, verbatim

    $ go test ./internal/adapter/claude/ -run 'TestRecognize|TestPermissionDialogFixtures|TestRecognizedDialog'
    # github.com/Nathandela/swarm/internal/adapter/claude [github.com/Nathandela/swarm/internal/adapter/claude.test]
    internal/adapter/claude/permdialog_test.go:68:13: undefined: RecognizePermissionDialog
    internal/adapter/claude/permdialog_test.go:72:20: undefined: VariantBash
    internal/adapter/claude/permdialog_test.go:73:50: undefined: VariantBash
    internal/adapter/claude/permdialog_test.go:86:13: undefined: RecognizePermissionDialog
    internal/adapter/claude/permdialog_test.go:90:20: undefined: VariantEdit
    internal/adapter/claude/permdialog_test.go:91:50: undefined: VariantEdit
    internal/adapter/claude/permdialog_test.go:109:18: undefined: RecognizePermissionDialog
    internal/adapter/claude/permdialog_test.go:114:14: undefined: RecognizePermissionDialog
    internal/adapter/claude/permdialog_test.go:117:14: undefined: RecognizePermissionDialog
    internal/adapter/claude/permdialog_test.go:138:18: undefined: RecognizePermissionDialog
    internal/adapter/claude/permdialog_test.go:138:18: too many errors
    FAIL	github.com/Nathandela/swarm/internal/adapter/claude [build failed]

### GREEN, verbatim

    $ go test ./internal/adapter/claude/ -run 'TestRecognize|TestPermissionDialogFixtures|TestRecognizedDialog' -v
    --- PASS: TestRecognizePermissionDialog_BashVariant (0.01s)
    --- PASS: TestRecognizePermissionDialog_EditVariant (0.00s)
    --- PASS: TestRecognizePermissionDialog_RefusesEveryNonDialogGrid (0.00s)
        --- PASS: TestRecognizePermissionDialog_RefusesEveryNonDialogGrid/neg-composer-idle-2.1.231.snap.json (0.00s)
        --- PASS: TestRecognizePermissionDialog_RefusesEveryNonDialogGrid/neg-working-2.1.231.snap.json (0.00s)
        --- PASS: TestRecognizePermissionDialog_RefusesEveryNonDialogGrid/neg-trust-dialog-2.1.231.snap.json (0.00s)
    --- PASS: TestRecognizePermissionDialog_RefusesAnUnknownLayout (0.00s)
        --- PASS: TestRecognizePermissionDialog_RefusesAnUnknownLayout/unknown_tool_title (0.00s)
        --- PASS: TestRecognizePermissionDialog_RefusesAnUnknownLayout/no_allow_row (0.00s)
        --- PASS: TestRecognizePermissionDialog_RefusesAnUnknownLayout/no_deny_row (0.00s)
    --- PASS: TestRecognizedDialog_IsPermissionToTheStatusEngineToo (0.00s)
        --- PASS: TestRecognizedDialog_IsPermissionToTheStatusEngineToo/bash-approval-2.1.231.snap.json (0.00s)
        --- PASS: TestRecognizedDialog_IsPermissionToTheStatusEngineToo/edit-approval-2.1.231.snap.json (0.00s)
    --- PASS: TestPermissionDialogFixtures_AreVersionStamped (0.00s)
    PASS
    ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.732s

The refusal cases are in-memory destructive controls (`rewriteRow` mutates the decoded
snapshot, never a file in the shared tree): an unknown tool title, a missing allow row, and a
missing deny row each leave a grid that is still obviously A dialog but no longer one with a
recorded key map -- and each must be refused rather than answered with the nearest-looking
keys.

### Gates

    $ go build ./...                                   BUILD_OK
    $ go vet ./...                                     VET_OK
    $ go build -tags realcli ./...                     REALCLI_OK   (compiles the harness, launches nothing)
    $ go vet -tags realcli ./internal/smoke            REALCLI_OK
    $ TMPDIR=/tmp go test ./...                        EXIT:0
    $ TMPDIR=/tmp go test -race ./internal/adapter/claude/ ./internal/smoke/ ./internal/engine/
      ok  github.com/Nathandela/swarm/internal/adapter/claude  2.152s
      ok  github.com/Nathandela/swarm/internal/engine          8.160s
    $ PATH="$HOME/go/bin:$PATH" golangci-lint run                              LINT_EXIT:0
    $ PATH="$HOME/go/bin:$PATH" golangci-lint run --build-tags realcli ./internal/smoke/...  EXIT:0

`internal/verify`'s B94 reachability fence caught the new exported symbol on its first run
(`internal/adapter/claude.RecognizePermissionDialog -- 1 unreachable exported symbol`). It is
allowlisted with a STATED, TEMPORARY reason: M1.1 records the reader one wave ahead of M1.2's
caller, and the fence is bidirectional, so leaving that row behind once M1.2 wires the call
fails the test.

### What M1.2 inherits

- `claude.RecognizePermissionDialog(snap)` -- call it on the live tap snapshot before injecting.
  `ok == false` means REFUSE the phone's tap (the terminal may have answered a beat earlier, or
  the dialog is a layout nobody recorded).
- Inject `AllowKeys` / `DenyKeys` VERBATIM, as a single write, with NOTHING after them -- no CR.
  A trailing CR would be a second keystroke arriving at whatever the CLI drew next.
- Do NOT inject Enter as the allow key even though it worked: it confirms whatever option is
  currently selected, and the terminal's own user may have moved that selection.
- Resolution stays observation-driven (mirror-program.md section 3, step 3): every recorded key
  dismissed the dialog immediately, but nothing here licenses emitting `approval_resolved` on
  the tap.

### Follow-ups filed

- `agents-tracker-o8er` (P2, bug, discovered-from `dwwv.2.1`): claude's `CSI n G` differential
  row painting leaves stale cells visible in this emulator's grid (the option-2 artifact above).
  Affects any reader that anchors on a row claude writes with column jumps -- including the
  engine's own `Esc to cancel · Tab to amend` hint, which spans one such gap.
