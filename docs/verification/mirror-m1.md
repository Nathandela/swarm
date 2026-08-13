# Mirror program -- wave M1 evidence

Wave M1 of `docs/specifications/mirror-program.md` ("The approve moment", bead
`agents-tracker-dwwv.2`). Each item below records the question it settled, the test that
settled it, the verbatim result, the outcome taken, and the tracker actions that followed.
Later agents APPEND their section (`## M1.2 ...`, `## M1.3 ...`) below this one; do not
rewrite an earlier section, and keep the run output verbatim.

| # | Item | Bead | Status |
|---|---|---|---|
| M1.1 | Characterization fixture: the permission dialog's grid signature and accepted keys, version-stamped | `dwwv.2.1` | settled -- 5 recorded grids of claude 2.1.231, key map confirmed by side effects, recognizer shipped |
| M1.2 | Apply-by-injection: the phone's answer is typed into the CLI's own dialog, gated on the live grid; the tap stops resolving | `dwwv.2.2` | settled -- injection primitive shipped, 5 refusal reasons, resolution moved to observation, 4 tests rewritten |

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

---

## M1.2 -- Apply by injection: the phone's answer reaches the CLI's own dialog

### The question

M1.1 recorded WHAT the dialog looks like and WHICH keys it obeys. M1.2 is the wave item that
uses it: make a phone tap actually release the agent. The audit of `cd648a7` recorded the gap
precisely -- `App.Approve -> OpApprove -> handleApprove` shipped, and `handleApprove` only
resolved the card. `approveInteraction`'s own header said so:

    What is still the PRODUCER's and not this function's is APPLYING the decision

So a tap dismissed the card on every surface while the CLI stayed blocked on a dialog nobody
had answered. **The card lied**, and IS-LIFE-2's "exactly one resolution" was spent on an
outcome no party had observed.

### The design, and the alternative it rejects

`mirror-program.md` section 3, unchanged by this item and implemented as written:

1. The dialog appears in the terminal exactly as today (the hook returns without a decision).
2. The phone's Allow/Deny maps to that dialog's accepted keys, **injected by the daemon into
   the PTY it owns**, after validating (a) the approval tuple is still unresolved and (b) the
   live VT grid still shows the permission dialog.
3. Resolution is emitted **only by observation**, never by the tap.
4. A post-injection watchdog surfaces a non-transitioning dialog.

The rejected alternative was the **held hook**: `swarm hook` keeps the `PermissionRequest`
connection open until the phone answers, and the daemon writes the decision back on it. It is
rejected on **co-presence** grounds and the reason is empirical, not stylistic: while a
`PermissionRequest` hook is undecided the CLI has **not drawn its own dialog**, so holding it
hides the terminal prompt. M0.1 proved both rooms really are live at once
(`TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive`); a held hook would have made the
terminal the blind one.

### What shipped

| Piece | Where |
|---|---|
| `adapter.ApprovalApplier` / `adapter.AsApprovalApplier` -- the optional per-CLI seam: `(grid, verdict) -> keys, ok` | `internal/adapter/interaction.go` |
| `claudeAdapter.ApprovalKeys` -- joins the normalized verdict to M1.1's recorded key map | `internal/adapter/claude/permdialog.go` |
| `Daemon.dialogTap` / `applyDecision` / `dialogStillOnGrid` / `watchInjection` | `internal/skeleton/inject.go` (new) |
| the reworked validate-then-apply path, the `applied`/`appliedOp` note, `clearAppliedNote` | `internal/skeleton/approval.go` |
| the observation that now attributes the resolution | `internal/skeleton/approval.go` `noteInteractionStatus` |

**The injection primitive is one short-lived `readWrite` subscription on the shared session tap**
(`sessiontap.go`), not a fresh shim dial and not a new shim protocol. Three properties fall out
of that choice and none of them are incidental:

- **It joins instead of stealing.** A shim serves one subscriber at a time; the tap is the tee
  that lets the owner's attach, the phone's peek and this injection coexist on one upstream.
  `copresence_test.go` is the standing proof that concurrent subscribers do not evict each other.
- **The gate and the keystroke share one seeded view.** A subscription is seeded with the grid
  as of the moment it joined and writes through the same handle, so the screen the recognizer
  judged is the screen the keys are typed at. No second dial can slip a repaint in between.
- **Nothing new crosses the wire.** No shim protocol change, no new op, no new error code.

### The refusal reasons, and which code each carries

`ok` from this op now means **APPLIED**, not **RESOLVED**. Every refusal below leaves the card
PENDING -- the daemon declined to type, it did not decide anything.

| refused because | code | new in M1.2 |
|---|---|---|
| no such pending approval (already resolved, expired, superseded, never existed) | `stale_approval` | no |
| the agent instance / content hash / echoed expiry does not match the stored tuple | `stale_approval` | no |
| the daemon's own window has passed (also resolves the request `expired`) | `stale_approval` | no |
| **an answer has already been typed and is awaiting observation** (`already_applied`) | `stale_approval` | **yes** |
| **the live grid no longer shows an answerable dialog** (`no_dialog`) | `stale_approval` | **yes** |
| the approve is routed to the wrong machine | `invalid_field` | no |
| the decision was never offered by the request (`unknown_decision`) | `invalid_field` | no |
| **the decision's verdict is neither allow nor deny, so no key answers it** (`unmappable_decision`) | `invalid_field` | **yes** |
| **this session's CLI is not answered by keystroke, or its PTY is unreachable** (`not_applicable`) | *(no code)* | **yes** |

`already_applied` is not defensive padding: it is the hole the design opened. A re-delivered
approve used to be caught by "already resolved", and the resolution no longer lands on the tap,
so for the whole observation interval the request is still pending. A second injection would
press a second key -- harmless while the dialog is up, a stray character in the composer the
moment it goes. M0's own finding stands unchanged: refusal words reach the phone's Outcome path.

`not_applicable` carries **no** D10 code, following `ApproveInteraction`'s existing rule: none of
D10's six describes a machine-side capability gap, and `not_authorized` would send a
correctly-paired owner off to re-pair a device that is fine.

### Resolution moved to observation, and kept its attribution

The tap emits nothing. §3.6's record lands in `noteInteractionStatus`, on the transition OUT of
the waiting state -- the same observation that has always driven `answered_locally`.

**It is attributed to the phone, and that is a correctness fix rather than a convenience.** The
daemon records what it is about to type BEFORE it types it (`applied`, `appliedOp` on the
pending tuple; cleared if the injection is refused). When the dialog then leaves, the daemon has
first-hand knowledge of which key it pressed, so the record carries `allowed`/`denied`,
`by: phone` and the phone's `operation_id`. Emitting `answered_locally` by `owner` there would
be a transcript line claiming a person answered at a keyboard nobody was sitting at.

The ordering matters and is deliberate: the status engine may already be mid-sample when the
keys land, so a note written after the write could be beaten by the observation it exists to
label.

M1.3 still owns what this does not: the `PermissionDenied` capture row, and verifying the
attribution of the paths where the OWNER answered.

### The watchdog

`injectWatchdogDelay` (5 s, a var so tests can shorten it). After that delay, if the same
request is still pending AND the recognizer still sees an answerable dialog, the daemon offers
the session's `session_status` (§3.8) -- the emission idiom the IS-ST-2 sweep already uses. It
resolves nothing, because nothing has been observed to resolve. The failure it exists for is the
honest one for any apply-by-keystroke path: the bytes are written and the CLI does nothing with
them. Silence is the worst outcome there, since a stuck card and a card being worked on look
identical on a phone.

### The instrument: a real PTY repainting a real recorded grid

`internal/skeleton/approval_inject_test.go`. Nothing in the loop is a double except the CLI's
own behaviour after the keystroke:

- the grid is M1.1's recorded snapshot, **rendered back to ANSI** (`vt.RenderSnapshotClipped`,
  absolute CUP per row, no newlines) and written into a real PTY at the size it was captured at
  (100x30) -- so the recognizer reads exactly the cells the daemon's tap read off the real CLI;
- the recognizer, the adapter seam and the tap are the shipped ones;
- the fake CLI then **blocks on stdin like the real CLI does and reports what it read**.

That last point is what makes "no stray keystroke" an observation instead of an absence. The
recorded keys carry no Enter, and the fake's PTY is in canonical mode, so the test flushes the
line itself and reads back what was already sitting in the session's stdin: `got: 1` when the
daemon typed the allow key, `got:` -- empty -- when a refusal typed nothing.

### RED, verbatim

    $ go test ./internal/adapter/claude/ -run TestApprovalKeys
    --- FAIL: TestApprovalKeys_TheAdapterAnswersARecognizedDialogWithItsRecordedKeys (0.00s)
        permapply_test.go:35: the claude adapter is not an adapter.ApprovalApplier. It is the only party holding the recorded key map for its own dialog, so nothing else can answer one
    --- FAIL: TestApprovalKeys_RefusesAGridThatIsNotARecognizedDialog (0.00s)
        permapply_test.go:55: the claude adapter is not an adapter.ApprovalApplier. ...
    --- FAIL: TestApprovalKeys_RefusesAVerdictItHasNoKeyFor (0.00s)
        permapply_test.go:76: the claude adapter is not an adapter.ApprovalApplier. ...
    FAIL	github.com/Nathandela/swarm/internal/adapter/claude	0.723s

    $ go test ./internal/skeleton/ -run TestApproveInjection
    --- FAIL: TestApproveInjection_AnAllowTypesTheRecordedDialogsAllowKeyIntoThePTY (4.00s)
        approval_inject_test.go:290: the session's stdin received ""; want "1" -- the allow key M1.1 recorded off claude 2.1.231, which selects option 1 AND submits it in one keystroke
        approval_inject_test.go:293: an approval_resolved was journalled by the TAP: map[by:phone decision:allowed interaction_id:01KZXTHZPS0MTWSK5HZ7BA1493 ... operation_id:op-allow ...]. Resolution comes ONLY from observing the dialog leave -- a resolution emitted on the tap is a card claiming an outcome nobody watched, which is what M1.2 exists to stop
    --- FAIL: TestApproveInjection_ADenyTypesTheRecordedDialogsDenyKeyIntoThePTY (0.68s)
        approval_inject_test.go:308: the session's stdin received ""; want "3" -- the deny key M1.1 recorded, which is ABSOLUTE ...
        approval_inject_test.go:312: an approval_resolved was journalled by the TAP: map[by:phone decision:denied ... operation_id:op-deny ...]
    --- FAIL: TestApproveInjection_AGridThatNoLongerShowsTheDialogIsRefusedAndTypesNothing (0.17s)
        approval_inject_test.go:327: an approve was applied to a session whose grid shows the IDLE COMPOSER, not a dialog. mirror-program.md section 3: the injection is gated on the live grid still showing the permission dialog, precisely so a terminal answer a beat earlier cannot turn the phone's tap into a keystroke in the composer
    --- FAIL: TestApproveInjection_ASecondTapBeforeTheFirstIsObservedTypesNothingMore (0.18s)
        approval_inject_test.go:389: the session's stdin received ""; want exactly one "1". Two keys for one dialog is one key too many, and the extra one is typed wherever focus lands next
    --- FAIL: TestApproveInjection_ADecisionWithNoVerdictCannotBeTypedAndIsRefused (0.17s)
        approval_inject_test.go:406: a decision carrying no verdict was applied. Nothing says which key answers it, and a daemon that picked one would be inventing the owner's intent
    --- FAIL: TestApproveInjection_TheResolutionLandsOnObservationAttributedToThePhone (0.67s)
        approval_inject_test.go:431: an approval_resolved was journalled by the TAP: map[by:phone decision:denied ... operation_id:op-observed ...]
    --- FAIL: TestApproveInjection_AWatchdogNotesADialogThatDidNotMove (10.19s)
        approval_inject_test.go:482: the injected keys changed nothing and the daemon said nothing. A dialog still on screen 250ms after the daemon typed at it is the one outcome the phone cannot see for itself, and a card left silent is indistinguishable from a card being worked on.
    FAIL	github.com/Nathandela/swarm/internal/skeleton	17.482s

**One of the eight was GREEN on its first run and ships as a pin, said plainly**:
`TestApproveInjection_AnAlreadyResolvedApprovalIsRefusedAndTypesNothing`. The old code refused
an already-resolved approve for the right reason, and "typed nothing" held VACUOUSLY because
nothing ever typed. It becomes a real fence the moment injection lands, which is why it was
written before and not after.

### The tests the new contract superseded, and what they used to assert

Four tests pinned the old rule. Each was rewritten to pin the new one, and each rewrite is
recorded here with the assertion it replaces.

1. **`TestApprove_AnOtherVerdictDecisionResolvesAllowedNotDenied`** ->
   `TestApprove_AnOtherVerdictDecisionCannotBeAppliedAndIsRefused`. The only SEMANTIC reversal.
   Old assertion, verbatim:

       decision = %v; want "allowed" (IS-RES-1). Only a deny verdict resolves denied -- a
       decision the adapter declared other says nothing about the owner's intent, and a
       transcript line claiming a refusal is a claim nobody made

   That was honest while the tap merely RECORDED an outcome. It cannot survive an answer that is
   APPLIED: the recorded dialog has exactly two answerable options, `other` has no key on it, and
   typing the allow key would RUN something on the owner's machine on the strength of a guess.
   Same premise, opposite conclusion, because the consequence changed.

2. **`TestApprove_AValidApproveIsAcceptedAndResolvesTheCard`** -- name and assertions kept; the
   session now shows a dialog (a valid approve is applied, so there must be something to apply it
   to) and the observation is driven rather than assumed. Its `by: phone` / `operation_id` /
   `allowed|denied` assertions are unchanged and still true.

3. **`TestApprove_ADenyVerdictDecisionResolvesDenied` / `...AnAllowVerdictDecisionResolvesAllowed`**
   -- kept, and STRENGTHENED: each now also asserts which recorded key reached the PTY. The
   verdict's job grew (it selects the key, not just the record), so the test grew with it. The
   vocabulary is still spike-SB's Codex ids, which remains the point: `cancel` is a refusal and
   nothing about the string says so.

4. **`TestClaudeChainE2E_...` legs 2 and 3, `TestApproveRoundTripE2E_...`, `TestI1_...`** --
   assertions untouched. Their sessions now paint a recorded dialog and their approves are
   followed by the machine's own observation. `TestApproveRoundTripE2E` and `TestI1` wait for the
   injection first (`awaitApplied`), because their op crosses a relay and a separate gateway
   process and the other order would resolve the card as `answered_locally` -- green for the
   wrong reason.

No test was weakened, and nothing was deleted. `mobile/interaction_screencoverage_test.go`'s ban
on the PHONE authoring an approving keystroke is untouched and still correct: the phone sends a
signed decision id, and the DAEMON types.

### GREEN, verbatim

    $ go test ./internal/adapter/claude/
    ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.800s

    $ go test ./internal/skeleton/ -run TestApproveInjection
    ok  	github.com/Nathandela/swarm/internal/skeleton	9.731s

    $ go test ./internal/skeleton/ -run 'TestApprove|TestApproveInjection|TestClaudeChainE2E|TestInteractionE2E'
    ok  	github.com/Nathandela/swarm/internal/skeleton	26.175s

### B94 collected its own debt

M1.1 allowlisted `claude.RecognizePermissionDialog` for exactly one wave, with the removal
condition written into the row. Wiring it made the bidirectional arm fire on the first run
afterwards:

    --- FAIL: TestB94_EveryExportedSymbolIsReachableFromProduction (13.30s)
        phaseb_reachability_test.go:362:
            STALE EXEMPTION: github.com/Nathandela/swarm/internal/adapter/claude.RecognizePermissionDialog is in b94Allowed but is now REACHABLE. Delete the row.

The row is gone; the comment in its place records that the instrument collected its own debt.

### GG-7

`docs/specifications/protocol.md`'s `approve` section is rewritten: the validate-then-APPLY
sequence, the refusal table above, and the sentence that changed meaning -- `ok` means APPLIED,
not RESOLVED, and no `approval_resolved` is journalled on the op. The wire FIELD table is
untouched because no field, op or error code was added, so the CI drift check against the `wire`
package has nothing to diff. There is no per-op conformance TSV row for `approve` (the repo
carries none for any op), so protocol.md is the whole of the obligation.

### What M1.3 inherits

- Attribution of the paths where the OWNER answered is still `answered_locally` / `by: owner`,
  which remains correct; the `PermissionDenied` capture row is unbuilt.
- The observation that resolves an injected answer is the STATUS TRANSITION. On a real claude
  session that is the engine's own claude grid signature seeing the dialog go. M1.3's hook-side
  attribution work is the second, independent witness.
- No real-CLI run was spent on this item: M1.1's five runs recorded the key map and the grids,
  and every claim here is testable against those recordings. The end-to-end handset moment stays
  M1.7's exit criterion.
