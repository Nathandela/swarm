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
| M1.3 | Resolution attribution: the `PermissionDenied` capture row, and which of the five daemon paths fires when the terminal answers | `dwwv.2.3` | settled -- PARTIAL by finding, not by omission: the hook does not fire on the path this item needs (four real captures + binary analysis), owner-path and phone-path attribution both verified correct as shipped, one new test, follow-up `agents-tracker-hgyg` filed |
| M1.4 | Approval sheet lives in session detail (it used to navigate out to the inbox); `App.Approve`'s answer settles like `kill` and `take_control` already do | `dwwv.2.4` | settled -- `approvalHost` re-parented between the inbox list and the session detail on `statusHost`'s own slot pattern, `openApproval` no longer navigates, settle wired with a calm refusal-only notice (M1.2's `ok` means APPLIED, not RESOLVED), 6 new/expanded test files, all gates green |
| M1.5 | Approval push wake deep-links to the card: the notification's tap action | `dwwv.2.5` | settled -- PARTIAL by finding, not by omission: the wake envelope carries no session id (78-byte content-free constant, ADR-007 B20) so a direct deep-link to session detail is not honest; shipped a tap action that opens the app fresh on its default Inbox screen (`needs_input` first, PB-SEC-11's "reads nothing off the intent" boundary intact), 4 new tests, follow-up `agents-tracker-dwwv.3.2` filed for the envelope question |
| M1.6 | Channels spike (timeboxed, flagged, non-release): `claude/channel/permission` relay | `dwwv.2.6` | settled -- supported, yes: three of the four observations reproduced against real sessions, the fourth (terminal-first race) inconclusive within the timebox; findings and seven promotion criteria in `docs/research/channels-spike.md` |
| M1.7 | ADR-013 written: the architecture, the section 3 decision, the schema rulings, the GG-7 cross-check | `dwwv.2.7` | settled -- `docs/adr/ADR-013-mirror-capture-architecture.md`, both indexes updated; M1.2's GG-7 obligation cross-checked and found MET, no fix and no bead needed |

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

---

## M1.3 -- Resolution attribution: the PermissionDenied row, and which path answers the terminal

### The question, and the premise it rests on

The wave plan (`mirror-program.md` M1.3 row) named a concrete piece of work: add the claude
adapter's sixth `capture=raw` hook row, `PermissionDenied`, so that a terminal-side deny -- the
owner pressing "No" on the dialog M1.1 recorded -- produces `approval_resolved` with
`decision: denied, by: owner` instead of the generic `answered_locally`. That instruction rests on
a premise: that `PermissionDenied` is the event a real `claude` fires for exactly that keystroke.
**The premise is false**, and the rest of this section is the evidence, because asserting that and
moving on would be exactly the kind of claim this repo's own corpus discipline (IS-TOOL-2: "an
unclassifiable call is `other`, never guessed at") exists to prevent.

### Four real runs, cost-disciplined, against the installed 2.1.231

Every run used the same instrument shape M1.1 already proved (`internal/smoke`'s `dialogSession`:
predicate-driven waiting on a real PTY, real vt emulator, env stripped of `SWARM_*`/`CLAUDE*`),
plus a hook command this item added temporarily and removed after use -- a `sh` script that `cat`s
its own stdin (the CLI's raw hook body) to a dump file, wired via an inline `--settings` exactly
like `claude.go`'s own `hookSettingsJSON` shape. It was never committed: spike-SB's own capture
relay was thrown away after use for the same reason (`docs/verification/spike-SB.md`), and this
one is documented here instead, verbatim, in its place.

**Runs 1-2 -- the interactive dialog, denied for real.** `claude --permission-mode manual
--settings <7 events wired>`, the Bash marker-file prompt M1.1 used, the dialog's own recorded
deny digit (`3`) sent once the dialog rendered. Confirmed genuinely denied both times (no marker
file at `/tmp/swarm-m13-denied.marker`). `UserPromptSubmit`, `PreToolUse` and `PermissionRequest`
landed in the dump; `Notification`, `PermissionDenied` and `Stop` never did -- the second run added
a poll-until-the-dump-file-stops-growing wait (10 s budget) specifically to rule out a race between
the screen going idle and an async hook still landing, and the result did not change:

    $ SWARM_REALCLI=1 go test -tags realcli -run TestCapturePermissionDenied -timeout 5m -v ./internal/smoke
    dialog options: [❯ 1. Yes   2. Yes,mandealways allowpaccessttoytmp/ from this project   3. No]
    sending deny digit "3"
    hook dump (1943 bytes):
        {...,"hook_event_name":"UserPromptSubmit","prompt":"Use the Bash tool to run exactly this command and nothing else: touch /tmp/swarm-m13-denied.marker"}
        {...,"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"touch /tmp/swarm-m13-denied.marker","description":"Create marker file"},"tool_use_id":"toolu_01Fi1prVuYg6rT646kJHJtge"}
        {...,"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /tmp/swarm-m13-denied.marker",...},"permission_suggestions":[{"type":"addDirectories",...},{"type":"setMode","mode":"acceptEdits",...}]}
    --- PASS: TestCapturePermissionDenied (10.86s)

**Runs 3-4 -- a deterministic trigger, tried on purpose.** The CLI's own installed binary (below)
names "a deny rule" as one of `PermissionDenied`'s triggers, and a rule match is deterministic
where an interactive keystroke and a semantic classifier are not -- worth one more attempt each
under `--permission-mode manual` and `--permission-mode auto` (the owner's own real global default,
per M1.1's finding), with `"permissions":{"deny":["Bash(touch:*)"]}` in the same inline settings.
Both genuinely blocked the command (no marker file; the agent's own `Stop` reply named the block in
both) and neither produced a `PermissionDenied` body:

    $ SWARM_REALCLI=1 go test -tags realcli -run TestCapturePermissionDeniedByRule -timeout 3m -v ./internal/smoke
    argv: [claude --permission-mode manual ... "permissions":{"deny":["Bash(touch:*)"]} ...]
    hook dump (1878 bytes): UserPromptSubmit, PreToolUse, Stop only
    Stop: "last_assistant_message":"Permission denied — the tool call was blocked, not run. ..."
    --- PASS: TestCapturePermissionDeniedByRule (33.64s)

    $ SWARM_REALCLI=1 go test -tags realcli -run TestCapturePermissionDeniedByRule -timeout 3m -v ./internal/smoke
    argv: [claude --permission-mode auto ... "permissions":{"deny":["Bash(touch:*)"]} ...]
    hook dump (1915 bytes): UserPromptSubmit, PreToolUse, Stop only ("permission_mode":"auto" confirmed on both rows)
    Stop: "last_assistant_message":"The command was denied by your permission settings (a deny rule is likely blocking `/tmp` writes or `touch`). ..."
    --- PASS: TestCapturePermissionDeniedByRule (33.66s)

Under the RULE runs `PermissionRequest` did not fire either -- the deny rule short-circuits before
the dialog is even drawn, which is consistent with the CLI's own description below ("auto-denied
WITHOUT an interactive permission prompt") but still did not reach `tgn` (see next).

### Why: read off the installed binary, not inferred from its absence

`strings` on the actual installed CLI (`/Users/Nathan/.local/share/claude/versions/2.1.231`, the
same binary all four runs and M1.1's captures used) finds the dispatcher and its one real call
site:

    async function*tgn(e,t,r,n,o,i,s,a=Ng){
      let l=o.getAppState(),c=o.agentId??o.session.id;
      if(!Bz("PermissionDenied",l,c))return;
      let u={...hg(o.session,Gt(),i,o),hook_event_name:"PermissionDenied",
             tool_name:e,tool_input:r,tool_use_id:t,reason:n};
      yield*V2({session:o.session,...})
    }
    ...
    if(Y.decisionReason?.type==="classifier"&&Y.decisionReason.classifier==="auto-mode"){
      let X=!1;
      try{for await(let te of tgn(e.name,t,b,Y.decisionReason.reason??"Permission denied",n,z,n.abortController.signal))
        if(te.retry)X=!0
      }catch(te){...}
    }

and the event's own embedded schema description, verbatim:

    "Emitted when a tool call is auto-denied without an interactive permission prompt (e.g.
    auto-mode classifier, dontAsk mode, headless-agent auto-deny, or a deny rule). With a
    permission prompt surface (stdio/SDK canUseTool), the 'ask' path surfaces via a can_use_tool
    control_request and this event covers the 'deny' short-circuit. Without one (bare -p / SDK
    query() with no canUseTool), 'ask' decisions are terminal, so this event also covers those
    implicit denials."

A HOOK-originated deny -- `claudeAdapter.Decision`'s `replyDeny`, the `{"behavior":"deny"}` a
`PermissionRequest` hook can reply with (unused in production today, per
`a1-integration.md` §8.7) -- is a THIRD path again: the binary calls a different function
(`swn(...)`) for `decisionReason.type==="hook"`, not `tgn`. So of the three ways a claude tool call
can end up denied -- a human's interactive "No", a hook's reply, and an automatic classifier/rule
short-circuit -- `PermissionDenied` is wired to exactly the third, and this item's four runs prove
the fourth listed trigger (a `permissions.deny` string match) does not reach it either at 2.1.231,
leaving `dontAsk` mode and an actual headless-agent session as the two untried possibilities. The
schema's own field list (`tool_name`, `tool_input`, `tool_use_id`, `reason`) is genuine and
confirmed from the same source; it was never used to fabricate a fixture (see next section).

### What was NOT built, and why that is the correct outcome and not a shortfall

No `PermissionDenied` fixture was added to `internal/adapter/claude/testdata/interaction/`, and
neither `claude.go`'s `hookEvents` nor `interaction.go`'s `Interactions()` gained a case for it.
Two independent reasons, either one sufficient on its own:

1. **No real body exists to shape against.** This repo's producer states its own rule in its file
   header -- "EVERY FIELD READ HERE WAS OBSERVED IN A REAL CAPTURE" -- and IS-TOOL-2 forbids
   guessing a shape from documentation. The field names above are genuine, but genuine-from-a-
   decompiled-binary is not the same evidentiary class as a recorded capture, and this corpus has
   never mixed the two (PROVENANCE.md's `reconstructed` provenance is reserved for a modified copy
   of a REAL source, not a synthesized one). Building shaping code with no fixture to run it
   against would also leave it untested, which the TDD mandate does not permit working around.
2. **It would not change anything even if built.** `noteInteractionStatus` (`approval.go:295`) is
   the ONLY place a terminal-side resolution is authored, and it is driven by the STATUS
   TRANSITION (the session's interaction dimension leaving the waiting state), never by a specific
   hook event. Nothing reads `Interactions()`'s output back into the pending approval's resolution
   path today -- that plumbing (a marker parallel to `inject.go`'s `ap.applied`, set from a shaped
   `PermissionDenied` item instead of from the daemon's own keystroke) does not exist, and building
   it on top of an event that cannot be produced against the real CLI would be untestable scaffolding.

`interaction-schema.md` §3.6's own rule (IS-RES-1) already says what the owner path is allowed to
claim: *"The four remaining values [`cancelled`, `superseded`, `expired`, `answered_locally`] are
daemon-observed and carry no verdict."* `answered_locally`/`by: owner` for a terminal deny is not a
gap this item failed to close -- it is the spec's own honest answer to a question the daemon
cannot see the answer to from a screen alone.

Filed forward rather than dropped: `agents-tracker-hgyg` (P2, discovered-from `dwwv.2.3`) records
the exact shaping work to do IF a real body is ever captured (a future claude version, `dontAsk`
mode, or a genuine headless-agent session), and why not to build it speculatively before then.

### Goal 2 -- tracing the allow path (and the deny path: it is the same path)

`approval.go` has exactly five call sites of `resolveApprovalLocked`, matching IS-LIFE-2's five
enumerated resolutions:

| Line | Caller | Resolution |
|---|---|---|
| 151 | `openApprovalLocked` | `superseded` / `by: agent` |
| 262 | `sweepExpiredApprovals` | `expired` / `by: daemon` |
| **295** | **`noteInteractionStatus`** | **`answered_locally` / `by: owner`, OR `ap.applied` / `by: phone` when the daemon itself typed the answer** |
| 326 | `sweepSessionInteractions` (orphan sweep) | `cancelled` / `by: agent` |
| 463 | `approveInteraction`'s own expiry check | `expired` / `by: daemon` |

Line 295 is the one and only path for BOTH a terminal allow and a terminal deny -- the mechanism
has no way to tell them apart (previous section), so it does not try to. It already attributed
`by: owner` correctly before this item (`TestApprovalResolved_TheDesktopAnsweringResolvesLocally`,
`approval_r4_test.go`, pre-dates M1.2); no fix was needed, and none was made.

### Goal 3 -- by: phone

Already fully wired by M1.2: `approveInteraction` (`approval.go`) records `ap.applied`/`ap.appliedOp`
on the pending tuple BEFORE typing (`"RECORDED BEFORE IT IS TYPED"`, its own comment explains the
race this closes), and `noteInteractionStatus` reads it back to select `by: phone` over the
`answered_locally`/`by: owner` default. Confirmed by reading the code (nothing to add) and by the
existing `TestApproveInjection_TheResolutionLandsOnObservationAttributedToThePhone`
(`approval_inject_test.go`, M1.2).

### Goal 4 -- the new test, and its honestly-unexpected GREEN

`TestApproveInjection_ATerminalSideDenyResolvesAnsweredLocallyByOwner`
(`internal/skeleton/approval_inject_test.go`) is this item's one new test: the SAME `newInjectRig`
machinery M1.2 built (a real recorded claude grid, repainted into a real PTY, the real recognizer),
but the OWNER's own attach types the recorded dialog's deny digit directly into the session --
`r.att.Input([]byte("3"))` -- and `approveInteraction` (the phone path) is never called at all. It
is fixture-driven exactly like its M1.2 neighbors, just of the owner leg the file did not yet cover
rather than the phone one.

It asserts `decision: answered_locally, by: owner, no operation_id` -- not `denied` -- for the
reasons the sections above give in full. It is a PIN, not a RED-to-GREEN drive: the mechanism it
exercises (`noteInteractionStatus`) was unchanged by this item, so it passed on its first run.
Said plainly, matching M1.2's own precedent for `TestApproveInjection_AnAlreadyResolvedApprovalIsRefusedAndTypesNothing`:

    $ go test ./internal/skeleton/ -run TestApproveInjection_ATerminalSideDenyResolvesAnsweredLocallyByOwner -v
    --- PASS: TestApproveInjection_ATerminalSideDenyResolvesAnsweredLocallyByOwner (0.70s)
    PASS

    $ go test ./internal/skeleton/ -run TestApproveInjection -v
    --- PASS: TestApproveInjection_AnAllowTypesTheRecordedDialogsAllowKeyIntoThePTY (3.86s)
    --- PASS: TestApproveInjection_ADenyTypesTheRecordedDialogsDenyKeyIntoThePTY (0.66s)
    --- PASS: TestApproveInjection_AGridThatNoLongerShowsTheDialogIsRefusedAndTypesNothing (0.67s)
    --- PASS: TestApproveInjection_AnAlreadyResolvedApprovalIsRefusedAndTypesNothing (0.41s)
    --- PASS: TestApproveInjection_ASecondTapBeforeTheFirstIsObservedTypesNothingMore (0.21s)
    --- PASS: TestApproveInjection_ADecisionWithNoVerdictCannotBeTypedAndIsRefused (0.16s)
    --- PASS: TestApproveInjection_TheResolutionLandsOnObservationAttributedToThePhone (0.69s)
    --- PASS: TestApproveInjection_ATerminalSideDenyResolvesAnsweredLocallyByOwner (0.70s)
    --- PASS: TestApproveInjection_AWatchdogNotesADialogThatDidNotMove (0.94s)
    PASS
    ok  	github.com/Nathandela/swarm/internal/skeleton	9.233s

The "phone-injected allow -> resolved allowed by phone" half of this goal was already pinned before
this item, by `TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone`'s leg 2
(`interaction_chain_e2e_test.go`) and by `TestApproveInjection_AnAllowTypesTheRecordedDialogsAllowKeyIntoThePTY`
+ `TestApproveInjection_TheResolutionLandsOnObservationAttributedToThePhone` together (the allow leg
of the same observation test the new deny test is the owner-side twin of).

### Goal 5 -- the phone renderer

No Kotlin change, and none needed: `TranscriptPanel.kt`'s `APPROVAL_RESOLVED` arm renders
`joined(fields.decision, fields.by)` off the item's own JSON fields generically, with no
allow/deny/owner/phone vocabulary hardcoded, and `TranscriptPanelTest.kt` already carries a
`by: "phone"` case (`item("approval_resolved", body = """{"decision":"allowed","by":"phone"}""")`,
line 340) asserting the rendered line contains `"phone"`. Confirmed by reading; nothing to change,
nothing to run through Gradle.

### Gates

    $ go build ./...                                                            BUILD_OK
    $ go vet ./...                                                              VET_OK
    $ go test ./internal/skeleton/... ./internal/adapter/claude/...             EXIT:0
    $ PATH="$HOME/go/bin:$PATH" golangci-lint run                               LINT_EXIT:0

### What M1.4+ inherits

- The owner-answered path (allow AND deny alike) resolves `answered_locally`/`by: owner`; there is
  no daemon-side way to learn which button the owner pressed from the status transition alone, and
  no code exists that tries to.
- `agents-tracker-hgyg` carries the exact shaping work for `PermissionDenied` if a real body is
  ever captured. Nothing should build toward it speculatively before then.
- The temporary capture instrument used for this item's four real runs was never committed
  (`internal/smoke/permdenied_capture_test.go`, deleted after use) -- its method is recorded here
  in full instead, matching spike-SB's own precedent for a throwaway relay.

---

## M1.4 -- Approval sheet lives in session detail and settles on the op outcome

### The question

The M0/audit finding, re-verified against `cd648a7` (and against HEAD before this item's own
changes, since M1.1-M1.3 landed between the audit and this item): tapping an approval row in the
transcript called `openApproval` (`PhoneSurface.kt:1966-1969`), which called `closeSessionDetail()`
-- navigating OUT to the inbox, the only place `approvalHost` was ever composed
(`unrecomposedControls` at `:899`, hosted under `triageInboxView`'s `below:` at `:1813`).
`approvalAction` (`:2204-2210`) sealed `App.Approve` with `Press`'s default settle, dropping the
`Op` it returned -- unlike `takeControlOf` (`:2276`) and `kill` (`:2276` area,
`settle = { answer -> rememberKill(answer) }`), which both remember their operation id so
[CommandVerdict.of] can claim the machine's later answer.

**Both findings confirmed as stated, and one thing changed underneath them since the audit.**
M1.2 (`dwwv.2.2`) shipped between the audit and this item and changed what `App.Approve`'s `ok`
means: APPLIED (the daemon typed the dialog's keys), not RESOLVED. Resolution now arrives only by
observation, as an `approval_resolved` item -- which `TranscriptScreen`'s `APPROVAL_RESOLVED` arm
already renders (confirmed unchanged, `TranscriptPanel.kt:258-265`). So "wire the settle" is no
longer "toast on success" (M0's `CommandVerdict.of` idiom applied naively would have been wrong
here): the one fact this settle has to say anything about is a REFUSAL, and M1.2's own refusal
table (`already_applied`, `no_dialog`, `stale_approval`'s four other causes, `invalid_field`'s two,
`not_applicable`) collapses to one honest phone-side sentence -- "the card is no longer something a
tap here can act on" -- which the M1.2 evidence states in its own words: *"Already resolved,
expired, superseded, or never existed. All four are the same fact from the phone's side."*

### The design: one host, two hosts, on `statusHost`'s own pattern

`approvalHost` was a **captive, permanent child** of `unrecomposedControls` -- `addView`'d once at
construction, never touched again. That is structurally why it could only ever be reached from the
inbox list: `hostContent`'s `detachHostedViews` takes the whole column off screen on the way into a
session's drill-down, and the approval card went with it, with nowhere else to land.

The fix is not a second composition (the sheet's own KDoc already rules that out: "IT IS A
NAVIGATION AND NOT A SECOND COMPOSITION ... two surfaces able to disagree about one pending item").
It is the pattern `PhoneSurface` already uses for `statusHost` (`statusSlot()`): a `View` built
once, detached from whatever held it last and re-`addView`'d wherever the current draw wants it.

- `approvalHost` is no longer one of `unrecomposedControls`'s fixed children.
- `approvalSlot()` (new): detach-then-return, `statusSlot`'s own two lines.
- `drawInbox` re-`addView`s it into `unrecomposedControls`, at the position between the capability
  notice and the launch form the column always held it (found by `indexOfChild(launchHost)`, not a
  literal index) -- the inbox entry point is unchanged.
- `sessionDetailView` gains a new required parameter, `approval: View`, composed directly under the
  transcript (`DetailTag.APPROVAL`, tag order `TRANSCRIPT, APPROVAL, OUTCOME, ...`) -- the sheet
  sits right below the block that points at it, per `TranscriptView`'s own words: "the transcript's
  job is to say that a decision is waiting and to get the reader to it; the sheet is where it is
  taken."
- `drawDetail` passes `approval = approvalSlot()`. `drawApproval` is unchanged: it only ever fills
  or empties the host's CHILDREN and never asks which of the two screens currently holds it.
- `openApproval` no longer calls `closeSessionDetail()`. The card is already on the same screen, so
  there is nothing to navigate to; it calls `approvalHost.requestRectangleOnScreen(...)` instead --
  the platform's own "scroll me into view", a no-op wherever there is no scrolling ancestor to ask.

### Settle: `approvalAction` remembers the operation, on `kill`'s idiom and not `take_control`'s

`kill`'s own KDoc is the closer precedent, not the lease's: an approval's answer is a **one-shot
toast report about the operation**, not a standing per-session fact any screen redraws -- so
`approveOp` carries no session alongside it, the same as `killOp` and unlike `leaseSession`.

```kotlin
private fun approvalAction(panel: ApprovalSheetPanel, decision: ApprovalDecision): View =
    actionButton(decision.label, CtaKind.MORE) {
        Press(
            SendPlane.COMMAND,
            verb = { app -> app.approve(panel.sessionId, panel.itemId, decision.id) },
            settle = { answer -> rememberApproval(answer) },
        )
    }
```

`renderApprovalVerdict` (new, called from `renderVerdicts` beside kill's and the lease's) claims
`approveOp`'s answer through the same `CommandVerdict.of(outcome, approveOp,
CommandVerdict.ACCEPTED_OK)` idiom `renderKillVerdict` and `leaseVerdictFor` already use. Accepted
(`ok` = APPLIED) says nothing -- `ApprovalSheetScreen.refusalNoticeFor`'s and
`SessionDetailScreen.killNoticeFor`'s shared rule: a success nobody has observed the resolution of
yet is not a claim this phone can honestly make. Refused reads a new, calm sentence:

```kotlin
private const val ALREADY_ANSWERED = "This approval was already answered"

fun refusalNoticeFor(verdict: CommandVerdict): String =
    if (verdict.refused) verdict.sentence(ALREADY_ANSWERED) else ""

fun refusalDetailFor(verdict: CommandVerdict): String =
    if (verdict.refused) verdict.reason else ""
```

"This approval was already answered." is deliberately one sentence for every refusal code the verb
can carry (`stale_approval`, `invalid_field`, and `not_applicable`'s bare failure) rather than a
table this side would have to keep in sync with the daemon's: the dominant real case behind all of
them is the ordinary one IS-LIFE-2 exists for -- the owner answered at the terminal, or a second tap
raced the first one still crossing -- and it reads as "answered", not as an error. The machine's
own words follow underneath, verbatim, in the same demoted mono register `killDetailFor` already
uses (`agents-tracker-ksvb.10`'s idiom), so nothing about the specific cause is lost, only led with
calmly. `refusalNoticeFor` never appends `CommandVerdict.RETRY_HINT`: waiting does not make a stale
card answerable again.

### Ledger

`android/unbound-verbs.tsv` unchanged. `App.Approve` was already bound (called from production
Kotlin since I1); this item wires its existing settle, which is not a binding change.

### Tests -- built through the real factories (the qx9m lesson), not hand-fed enums

Every new `CommandVerdict` in the new tests is built through `CommandVerdict.of(OperationOutcome(...), opId,
accepted = ...)`, never a hand-assembled `CommandVerdict(result = ..., reason = ..., retryable = ...)`
literal -- `qx9m` shipped because "every unit test constructs the panel with an explicit
`ScannerState`, so nothing asserted what a NEW INSTALL gets"; the same shortcut here would prove the
copy function reads a field correctly and nothing about whether the real refusal table produces
that field the way this test assumes. The session-detail approval fixture
(`SessionDetailViewTest.panelWithApproval`, pre-existing, reused rather than duplicated) is built
the same way: through `ApprovalItem.of`'s own decode over a real wire body, `TranscriptScreen.of`'s
own fold, and `SessionDetailScreen.of`'s own assembly -- never a hand-built `TranscriptBlock`.

**What is, and is not, reachable in Robolectric.** `PhoneSurfaceSyncSlotTest`'s own bound restated:
`PhoneRuntime.phone()` answers `Unavailable` on every JVM run, so `renderReady` -- and with it
`drawDetail`, `drawApproval` fed real data, and `openApproval`'s guard -- is out of reach through
`PhoneActivity`. Every claim below is either (a) pushed to the view/model layer this JVM CAN build
(`sessionDetailView` called directly, `ApprovalSheetScreen`'s pure functions), matching this
codebase's own established answer to the same bound (`PhoneSurfaceSyncSlotTest`,
`PhoneSurfaceNavigationTest`), or (b) the one structural fact about the reparenting itself that IS
reachable on the `Unavailable` branch (`drawInbox` runs there; `drawDetail` does not).

**New test file**: `android/app/src/test/kotlin/dev/swarm/phone/PhoneSurfaceApprovalSlotTest.kt`
- `the approval host is on the inbox destination`
- `the approval host survives navigating away from and back to the inbox`

**Amended**: `android/app/src/test/kotlin/dev/swarm/phone/ui/screens/SessionDetailViewTest.kt`
(new tests; existing tests' `view()` fixture gained a defaulted `approval` parameter, no existing
assertion touched)
- `the approval sheet is composed inside this screen -- there is nothing to navigate to`
- `the approval sheet sits directly under the block that points at it`
- `a re-parented approval host is not refused for still having a parent`

**Amended**: `android/app/src/test/kotlin/dev/swarm/phone/ui/screens/ApprovalSheetPanelTest.kt`
(new tests only)
- `an approval nobody has answered yet says nothing`
- `an applied approval says nothing -- the resolution is the transcript's, not this sheet's`
- `a stale card reads as calmly answered, not as an error`
- `an invalid decision reads the same calm way, in the machine's own words`
- `a refused approval is never offered as worth retrying`

**Fixture-only edits** (no assertion changed): `ScreenAirSweepTest.kt`'s `sessionDetail()` and
`StreamingRedrawTest.kt`'s `host()` both call `sessionDetailView` directly and needed the new
required `approval` argument; both pass an empty placeholder view, which is the honest common case
(no pending approval) and, for the sweep suite, correct scoping -- the sheet's own air is already
independently checked by that same file's `approvalSheet()` / `"Approval sheet"` destination entry.

### RED, verbatim

Compile-time RED (Kotlin): the four new/changed symbols referenced by the tests above did not
exist yet, so `compileDebugUnitTestKotlin` failed the whole module before any test could run --
the same shape of RED this repo's own Go evidence uses (M1.2: "the claude adapter is not an
adapter.ApprovalApplier").

    $ bash scripts/o2-gradle-run.sh testDebugUnitTest
    > Task :app:compileDebugUnitTestKotlin
    e: .../PhoneSurfaceApprovalSlotTest.kt:87:53 Unresolved reference 'HOST'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:230:46 Unresolved reference 'refusalNoticeFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:231:46 Unresolved reference 'refusalDetailFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:245:46 Unresolved reference 'refusalNoticeFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:246:46 Unresolved reference 'refusalDetailFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:268:33 Unresolved reference 'refusalNoticeFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:275:33 Unresolved reference 'refusalDetailFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:284:81 Unresolved reference 'refusalNoticeFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:285:81 Unresolved reference 'refusalDetailFor'.
    e: .../ui/screens/ApprovalSheetPanelTest.kt:295:33 Unresolved reference 'refusalNoticeFor'.
    e: .../ui/screens/SessionDetailViewTest.kt:158:9 No parameter with name 'approval' found.
    e: .../ui/screens/SessionDetailViewTest.kt:512:36 Unresolved reference 'APPROVAL'.
    e: .../ui/screens/SessionDetailViewTest.kt:524:38 Unresolved reference 'APPROVAL'.
    e: .../ui/screens/SessionDetailViewTest.kt:530:37 Unresolved reference 'APPROVAL'.
    e: .../ui/screens/SessionDetailViewTest.kt:549:51 Unresolved reference 'APPROVAL'.
    > Task :app:compileDebugUnitTestKotlin FAILED
    gradle exit status: 1

After that RED, `SheetTag.HOST`, `ApprovalSheetScreen.refusalNoticeFor`/`refusalDetailFor`,
`DetailTag.APPROVAL` and `sessionDetailView`'s `approval` parameter were written, which is when the
compile error surfaced two MORE call sites naming `sessionDetailView` without the new required
argument (`ScreenAirSweepTest.kt:719`, `StreamingRedrawTest.kt:166`) -- fixed as fixture-only edits
per the section above.

### GREEN, verbatim

    $ bash scripts/o2-gradle-run.sh test
    > Task :app:testDebugUnitTest
    > Task :app:testReleaseUnitTest
    > Task :app:test
    BUILD SUCCESSFUL in 2m 53s
    gradle exit status: 0
    testDebugUnitTest: 143 result files, 143 written in the last hour
    testReleaseUnitTest: 143 result files, 143 written in the last hour

Aggregate JUnit XML counts, both variants (counted from the written files, not from Gradle's own
summary): **1165 tests, 0 failures, 0 errors** -- debug and release identically.
`android/app/libs/swarm.aar` mtime unchanged across the whole run (`Aug 9 21:01`, before and
after): no Gradle lane conflict, no AAR rebuild.

New/changed suites individually, from the written XML:

    PhoneSurfaceApprovalSlotTest:                 tests=2  failures=0 errors=0
    ui.screens.ApprovalSheetPanelTest:             tests=14 failures=0 errors=0
    ui.screens.SessionDetailViewTest:              tests=24 failures=0 errors=0
    ui.screens.ScreenAirSweepTest:                 tests=7  failures=0 errors=0
    ui.screens.StreamingRedrawTest:                tests=3  failures=0 errors=0

### Gates

    $ go build ./...                                                            BUILD_OK
    $ go vet ./...                                                              VET_OK
    $ TMPDIR=/tmp go test ./...                                                 EXIT:0 (all packages ok)
    $ TMPDIR=/tmp go test -race ./mobile/... ./internal/skeleton/... \
        ./internal/hookclient/...                                              EXIT:0
    $ go test ./android/gate/...                                               EXIT:0
    $ PATH="$HOME/go/bin:$PATH" golangci-lint run                              LINT_EXIT:0
    $ bash scripts/o2-gradle-run.sh test  (testDebugUnitTest + testReleaseUnitTest)  EXIT:0

No Go source changed in this item (M1.2/M1.3 already shipped the daemon-side semantics this settle
reads); the Go gates above confirm nothing regressed, not that anything moved.

### What M1.5+ inherits

- `App.Approve`'s settle is now claimed by operation id like `kill` and `take_control`; a future
  item adding a fourth "applied" visual state (a spinner, a disabled sheet) beyond the silent
  accepted case has `approveOp`/`approveSaid` to build on, and `renderApprovalVerdict`'s own KDoc
  records why accepted is silent today.
- `approvalHost`'s two-host pattern (`approvalSlot`) is the third view in this file to use
  `statusHost`'s reparenting idiom (`statusSlot`, `approvalSlot`); a fourth view needing the same
  treatment should read both rather than inventing a third variant.
- `openApproval`'s scroll-into-view (`requestRectangleOnScreen`) is unverified by any automated
  test -- Robolectric's bound on `renderReady` makes it unreachable through `PhoneActivity`, and
  there is no lower-level seam that exercises it either. It is a single call to a stock platform
  API with well-defined no-op behaviour absent a scrolling ancestor, recorded here as an honest gap
  rather than a claimed one.

---

## M1.5 -- Approval push wake deep-links to the card: the notification's tap action

### The question

M1.4 moved the approval sheet into session detail. This item's question, as posed: does an
approval push notification, when tapped, open the app on that session's detail with the approval
visible -- and if the wake does not carry enough to identify the session, what is the honest
fallback that still lands the user one tap from the card.

### Re-verified: what the envelope carries, and what the notification did on tap before this item

**The envelope carries no session identity, and this is a design constraint, not a gap.**
`internal/remotegw/push.go:484-496` (`sealWake`) builds the wake from `crypto.SealWake` over an
**empty plaintext** -- `n.cfg.Seq.Next()`, `EpochID`, `IssuedAt`, both key ids left zero -- with
no session id, no group, no count anywhere in it, and the KDoc above it is explicit: "the wake is a
constant 78 bytes over an EMPTY plaintext ... The zero key ids are the part that is easy to get
wrong and impossible to notice" (ADR-007 B20). `maybeWake` (`:272-357`) confirms the same wake
fires for the interactions group as a whole, not approval_request specifically: "It fires for EVERY
interaction record, not only approval_request ... because interaction-schema.md §10 forbids the
gateway parsing an item". So there is no approval-specific signal to carry even in principle without
the gateway parsing content it is forbidden to parse.

`mobile/pushwake.go`'s `App.HandlePushWake` decodes this into a `WakeAlert{Text, ContentReady}` --
two fields, neither identifying a session -- and the struct's own KDoc states the constraint as an
invariant rather than a TODO: "It carries no session id, no machine id, no group and no count, **and
it must not grow one**: every field here is provider-adjacent by construction". `WakeNotificationText`
is a single package constant ("Swarm has an update for you."), argument-less, with no
interpolation site for a session id (confirmed by the existing test `the notification strings offer
nowhere to interpolate a session`, unchanged by this item).

**What the tap did before this item: nothing.** `WakeNotifications.build` (`android/app/src/main/
kotlin/dev/swarm/phone/push/WakeNotifications.kt`) called `Notification.Builder(...).setAutoCancel
(true).build()` with no `setContentIntent`. `setAutoCancel` still dismisses the notification on
tap, but with no content intent Android has nothing to launch -- a tapped wake vanished and opened
the app on nothing. The KDoc above `build` said so as a stated decision at the time ("THERE IS NO
ACTION ON IT, in either state"), not as an oversight; re-verifying it against a real handset was
this item's starting question and the answer is: correct as read, and the thing this item exists to
change. This closes a second re-verification the bead asked for: the notification did NOT do a
"generic app open" -- it opened nothing at all.

### The decision: since the envelope carries no session identity, growing it is out of scope here

Per the item's own branching instruction, since the wake does not carry a session id, the envelope
is not grown on this bead's authority -- that is a security-relevant wire-format change (ADR-007
B20's own reasoning would have to be re-argued, not silently overridden by an Android-lane item) --
and the fallback applies: route to the inbox with the approvals section surfaced, document the
limit here, file the envelope question as a separate bead. `agents-tracker-dwwv.3.2` is filed under
`agents-tracker-dwwv.3` (M2) for that question.

**"Approvals surfaced" needed no new UI.** `TriageInbox.TRIAGE_ORDER` (`android/app/src/main/kotlin/
dev/swarm/phone/ui/TriageInbox.kt:116-117`) already sorts `needs_input` -- the group an
approval-blocked session sits in, per `push.go`'s own comment: "The category is needs_input because
that is what an approval IS" -- first among the four triage sections, ahead of `working`,
`ready_for_review` and `completed`. `PhoneSurface`'s own field defaults already land there:
`destination = Destination.INBOX` (`:660`) and `detail: String? = null` (`:594`) are the class's
initial values, so a **fresh** `PhoneSurface` shows the inbox with the needs-input section first and
no drill-down open, with zero code written to make that true. The gap was never "the inbox doesn't
surface approvals" -- it does, by construction -- the gap was that a tapped wake notification never
reached that screen at all.

### The routing shipped: a tap action that forces a fresh landing, reading nothing off the intent

`PhoneActivity`'s own KDoc states a hard boundary, enforced by `android/gate/s18_sec11_exported_
test.go` and by `PhoneActivityWindowTest.a_crafted_launch_intent_selects_nothing` (which drives a
hostile intent -- a `swarm://` data URI plus `session`/`action`/`relay` extras -- and asserts the
render is byte-identical to a plain launch): "IT READS NOTHING OFF THE INTENT. No extra, no data
URI, no action beyond the one the filter matched. ... What is shown comes from persisted local state
alone." `PhoneActivity` is `exported="true"` with a `LAUNCHER` filter -- "the single most reachable
surface an Android app has" -- so an extra naming a destination would be exactly the shape that
gate exists to reject, regardless of whether this item's own PendingIntent is the one carrying it.

So the routing does not use an extra. `WakeNotifications.build` now calls `setContentIntent` with a
`PendingIntent.getActivity` targeting `PhoneActivity` through an `Intent` that carries **no extra, no
data URI, no custom action** -- only two task flags: `FLAG_ACTIVITY_NEW_TASK or
FLAG_ACTIVITY_CLEAR_TASK`. That flag pair finishes whatever `PhoneActivity` instance is currently
running and starts a brand-new one, which is constructed with `PhoneSurface`'s own default field
values -- the same screen a plain launch renders. The mechanism is "make the destination the
default" rather than "tell the Activity where to go": nothing new is read off the intent, so the
existing `a_crafted_launch_intent_selects_nothing` gate needed no change and still passes.
`FLAG_IMMUTABLE` is required on this app's minSdk-33 floor (`PendingIntent.getActivity` without a
mutability flag throws on API 31+); `FLAG_UPDATE_CURRENT` lets a second wake arriving before the
first is tapped refresh the saved `PendingIntent` in place under the same request code
(`WakeNotifications.NOTIFICATION_ID`, already the single id every wake replaces under).

**The cost, stated rather than hidden**: `FLAG_ACTIVITY_CLEAR_TASK` discards whatever local screen
state the running `PhoneActivity` held -- an open session drill-down, unsent composer text, the
settings panel -- if the user taps the notification while the app was already running in the
background on a different screen. Given the alternative (an extra selecting the destination) is the
exact attack `a_crafted_launch_intent_selects_nothing` polices, and given the task's own floor is
"the user lands one tap from the card" rather than "no state is ever lost", this is the trade this
item takes, on a single-Activity app where the task and the Activity are the same thing.

`WakeNotifications.kt`'s KDoc above `build` is amended in place to state the new decision and why it
does not conflict with PB-PUSH-4 ("IT OFFERS NO NOTIFICATION ACTION ... IN EITHER STATE" is kept,
narrowed to the button row -- `Notification.Action` -- rather than the tap; "IT DOES OFFER A TAP
ACTION NOW" is the new clause). `openAppPendingIntent`'s own KDoc carries the PB-SEC-11 argument in
full, at the call site.

### Tests: RED then GREEN

Three new tests in `WakeNotificationTest.kt` (structural, over `WakeNotifications.build`) and one
new test in `PhoneActivityWindowTest.kt` (behavioural, over the real `Activity`, on
`a_crafted_launch_intent_selects_nothing`'s own idiom):

- `the wake notification offers a tap action` -- `notification.contentIntent` is non-null.
- `the tap action targets PhoneActivity and forces a fresh task` -- the `PendingIntent`'s saved
  `Intent` (via `Shadows.shadowOf(...).savedIntent`) names `PhoneActivity` as its component and
  carries `FLAG_ACTIVITY_NEW_TASK or FLAG_ACTIVITY_CLEAR_TASK`.
- `the tap intent carries no extras` -- the saved `Intent`'s `extras` is null or empty.
- `the_wake_notification_tap_intent_renders_the_same_as_a_plain_launch` -- launches `PhoneActivity`
  through the exact saved `Intent` the notification carries and asserts the rendered text is
  identical to a plain `ActivityScenario.launch`, on `a_crafted_launch_intent_selects_nothing`'s own
  comparison.

RED, verbatim (`bash scripts/o2-gradle-run.sh testDebugUnitTest --tests
"dev.swarm.phone.push.WakeNotificationTest" --tests "dev.swarm.phone.PhoneActivityWindowTest"`,
against the tests with no implementation yet):

    > Task :app:testDebugUnitTest

    PhoneActivityWindowTest > the_wake_notification_tap_intent_renders_the_same_as_a_plain_launch FAILED
        java.lang.NullPointerException at PhoneActivityWindowTest.kt:147

    WakeNotificationTest > the tap intent carries no extras FAILED
        java.lang.NullPointerException at WakeNotificationTest.kt:349

    WakeNotificationTest > the wake notification offers a tap action FAILED
        java.lang.AssertionError at WakeNotificationTest.kt:303

    WakeNotificationTest > the tap action targets PhoneActivity and forces a fresh task FAILED
        java.lang.NullPointerException at WakeNotificationTest.kt:324

    16 tests completed, 4 failed
    BUILD FAILED in 1m 7s
    gradle exit status: 1

All four failed on the missing `contentIntent` (null `PendingIntent` -> `AssertionError` on the
existence check, `NullPointerException` on the three that unwrap it with `!!`) -- the right reason,
not a compile error and not a test bug.

GREEN, verbatim, same command, after `openAppPendingIntent` and the `setContentIntent` call were
added:

    > Task :app:testDebugUnitTest
    BUILD SUCCESSFUL in 1m 5s
    gradle exit status: 0
    testDebugUnitTest: 2 result files, 2 written in the last hour

### Gates

    $ bash scripts/o2-gradle-run.sh testDebugUnitTest --tests \
        "dev.swarm.phone.push.WakeNotificationTest" \
        --tests "dev.swarm.phone.PhoneActivityWindowTest"                        EXIT:0 (16/16)
    $ bash scripts/o2-gradle-run.sh test  (testDebugUnitTest + testReleaseUnitTest)  EXIT:0
    $ go build ./...                                                              BUILD_OK
    $ go vet ./...                                                                VET_OK
    $ go test ./android/gate/...                                                  EXIT:0
    $ PATH="$HOME/go/bin:$PATH" golangci-lint run                                 LINT_EXIT:0

Aggregate JUnit XML counts, full android suite, both variants: **1169 tests total** (debug + release
counted separately from the written files), **0 failures, 0 errors**. `android/app/libs/swarm.aar`
mtime unchanged across the whole run (`Aug 9 21:01`, before and after this item's runs) -- no Go
binding rebuild, consistent with a Kotlin-only change; no Gradle lane conflict (`pgrep -f
gradle-wrapper.jar` empty before each run).

New/changed suites individually, from the written XML:

    push.WakeNotificationTest:                    tests=12 failures=0 errors=0
    PhoneActivityWindowTest:                       tests=4  failures=0 errors=0

No Go source changed in this item (`internal/remotegw/push.go` and `mobile/pushwake.go` were read,
not written, to re-verify the envelope's contents); the Go gates above confirm nothing regressed,
not that anything moved.

### What M1.5+ inherits

- The envelope question is filed and not answered: `agents-tracker-dwwv.3.2` (under `dwwv.3`, M2)
  asks whether M2's chat-feel work changes the tradeoff enough to justify growing the wake envelope
  with a session id (still AEAD-covered, still no group/count) so a tap can deep-link straight to
  one session's detail. That needs an ADR if the answer is yes, per ADR-007 B20's own reasoning.
- `FLAG_ACTIVITY_CLEAR_TASK`'s cost (dropped local screen state on a warm tap) is stated above and
  unmitigated. A future item that wants to preserve state on a warm tap needs a different mechanism
  than an intent extra, given `PhoneActivityWindowTest.a_crafted_launch_intent_selects_nothing`'s
  fence -- persisted local state written by trusted in-process code (the `PermissionAsks.kt`
  pattern) is the shape that survives it, not read directly off the intent.
- `openApproval`'s scroll-into-view gap (recorded in M1.4's section above) is unaffected by this
  item: the tap action lands on the inbox, not on a session detail, so this item never reaches that
  code path at all.

## M1.6 -- Channels spike

Supported, yes -- three of the four requested observations reproduced against a real session
(sidecar receipt, simultaneous terminal dialog, sidecar-allow proceeding the tool); the fourth
(terminal-first race) was inconclusive within the timebox. Full findings, verbatim evidence, and
promotion criteria in `docs/research/channels-spike.md`.

---

## M1.7 -- ADR-013: the architecture, written after the facts it records

`docs/adr/ADR-013-mirror-capture-architecture.md` is the wave's decision record, and it is written
from this file rather than ahead of it: the sacred-PTY rule and the per-CLI channel table (claude
hooks plus the non-load-bearing transcript tail, opencode SSE, the AGY probe, the owner-ruled status
card for everything else -- with Codex's app-server second-client topology marked explicitly as a
GATED INTENTION riding on M4.0, not a made decision); co-presence as proven fact, citing
`TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive` and its first-run GREEN, with R3's
lease-as-plumbing/dead-as-UX consequence and `nx44.8`'s closure; the held hook rejected on
co-presence grounds with mirror-program.md section 3's reasoning reproduced, the grid-gated
injection as M1.2 shipped it (recorded key map, refusing recognizer, the stray-keystroke race and
the one-seeded-view gate that closes it, `ok` means APPLIED, resolution by observation, the
watchdog), and Channels as designated successor carrying M1.6's four hard blockers out of its seven
promotion criteria plus the re-check signal; and the R1/R2 schema rulings with their reasoning
(fold-by-ref already exists; `verification-metadata.xml` churn is a deliberate human-review gate).
The item's cross-check is recorded in the ADR's Conformance section: **M1.2 met its GG-7
obligation** -- protocol.md's `approve` section already carries the validate-then-APPLY sequence,
the full refusal table and the `ok` means APPLIED sentence (lines 369-423 at `791852d`), and
`git diff 24ef9b1..HEAD -- internal/wire` is empty across the whole wave, so the CI field-table
drift check has nothing to diff. No row was missing, no correction was needed, and no bead was
filed. Docs-only item: no test to write RED, and the gates were run as a formality
(`go build ./...`, `go test ./...`, `go vet ./...`, `golangci-lint run` -- all exit 0). Both indexes
updated (`docs/adr/README.md` row plus its next-free-number line, now ADR-015 because
mirror-program.md M3.1 has reserved 014; `docs/INDEX.md` Decisions list).
