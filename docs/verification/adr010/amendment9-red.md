# ADR-010 Amendment 9 — bracketed-paste message framing: TDD evidence

**Decision**: [ADR-010 Amendment 9](../../adr/ADR-010-inter-session-orchestration.md).
**Baseline**: v0.17.7 (`e538d1e9`). Date: 2026-10-14.

## Production symptom

On 2026-10-14 two codex sessions steered each other with `swarm send --text`. Long messages stayed
in the recipient's composer, unsent, until a separate `swarm send --key enter`; both sessions
carried Swarm's own unsubmitted-draft marker. One recovery attempt, `--text '\x1b[13u'`, appended
the literal characters to the draft (single quotes keep the escape literal). An earlier 8,510-byte
message had been refused outright by the 4,096-byte bound; that bound is unchanged here.

## Characterization against the real CLIs

A Python PTY driver reproduced the exact A2 sequence: the text in one write, a pause, then a lone
CR. codex 0.162.1 ran with a scratch `CODEX_HOME` and a model provider pointed at a closed local
port; Claude Code 2.1.299 ran with a scratch `CLAUDE_CONFIG_DIR`, a dummy API key and a closed
local port as the API base URL. Nothing reached a model provider. "Submitted" means the message
left the composer and the CLI began a turn (codex: `Working`; Claude Code: the connection-refused
retry).

| CLI | Message | Framing | Pause before CR | Submitted |
|---|---|---|---|---|
| codex | 15 chars | raw | 150 ms | yes |
| codex | 300 chars | raw | 150 ms | **no** |
| codex | 900 chars | raw | 150 ms | **no** |
| codex | 900 chars | raw | 500 ms | **no** |
| codex | 900 chars | raw | 1 s | yes |
| codex | 26 chars | bracketed | 150 ms | yes |
| codex | 900 chars | bracketed | 150 ms | yes |
| codex | 906 chars, multi-line | bracketed | 150 ms | yes |
| codex | 3000 chars | bracketed | 150 ms | yes |
| Claude Code | 906 chars | raw | 150 ms | yes |
| Claude Code | 26 chars | bracketed | 150 ms | yes |
| Claude Code | 906 chars | bracketed | 150 ms | yes |
| Claude Code | 906 chars, multi-line | bracketed | 150 ms | yes |
| Claude Code | 3006 chars | bracketed | 150 ms | yes |

Both CLIs emitted `CSI ? 2004 h` (bracketed paste on) during startup in every run.

## RED

The regression surface, written before the implementation:

- `internal/vt/bracketed_paste_test.go`: the emulator tracks DECSET/DECRST 2004, clears it on a
  full reset (RIS), and the read is race-free against `Feed`.
- `internal/shim/bracketed_paste_test.go`: hello advertises `paste_input`; the submit transaction
  wraps the text (single- and multi-line) and leaves the CR outside the markers; the paste op
  wraps with the mode on and is verbatim with it off; embedded markers are stripped; a pasted
  draft still refuses a later submit; the op is ignored before hello; the production server's
  writer follows its emulator's mode.
- `internal/protocol/sendinput_paste_test.go`: a text message's first frame rides the paste op and
  its CR stays a separate frame after the gap; text without submit is one paste; a key is never
  pasted; an old shim degrades to raw input; `shimStream.PasteInput` follows the negotiated
  capability.
- `internal/skeleton/sessiontap_paste_test.go`: the tap forwards the paste op, degrades to `Input`
  for an old upstream, and a read-only peek writes nothing.

First run, before any implementation: every package failed to compile on the new symbols only
(`Emulator.BracketedPaste`, `shimwire.TypePasteInput`, `Control.PasteInput`, `Caps.PasteInput`,
`ptyWriter.bracketedPaste`/`pasteOn`, `ErrPasteInputUnsupported`, `PasteInputWriter`,
`shimStream.PasteInput`, `tapSub.PasteInput`).

Behavioural RED: with the implementation in place but its three decisions disabled (the shim
never wraps, `sendMessage` never uses the paste op, the tap never forwards it), the suite fails on
its own assertions:

```
--- FAIL: TestSubmitMessage_BracketedPasteOn_WrapsTheTextNotTheReturn
    the PTY received "ship it\r" (8 bytes) and then nothing; want exactly "\x1b[200~ship it\x1b[201~\r"
--- FAIL: TestSubmitMessage_BracketedPasteOn_MultiLineIsOnePaste
    the PTY received "one\ntwo\r" (8 bytes) and then nothing; want exactly "\x1b[200~one\ntwo\x1b[201~\r"
--- FAIL: TestPasteInput_BracketedPasteOn_Wraps
    the PTY received "ship it" (7 bytes) and then nothing; want exactly "\x1b[200~ship it\x1b[201~"
--- FAIL: TestPasteInput_StripsEmbeddedMarkers
    the PTY received "a\x1b[201~b\x1b[200~c", want "\x1b[200~abc\x1b[201~"
--- FAIL: TestPasteInput_LeavesADraftThatRefusesSubmit
--- FAIL: TestSendInput_TextRidesThePasteOp_SubmitStaysAFrameAfterTheGap
--- FAIL: TestSendInput_TextWithoutSubmitIsOnePaste
    writes = "draft only" (pasted=false), want exactly one pasted "draft only"
--- FAIL: TestTapSub_PasteInput_ForwardsToThePastingUpstream
    upstream pasted=[] inputs=["ship it"], want one paste of "ship it"
```

The verbatim-mode, key, degrade and read-only cases passed in that run, as they should: they pin
behaviour the fix must keep.

## GREEN

With the decisions restored, the new tests and the existing send_input, submit, control-input,
hello and tap suites pass under `-race`. Full-suite results are recorded in the pull request.

## Not covered here

- A shim launched before this release keeps its old behaviour until the session restarts, because
  the capability is negotiated per shim (the `control_input` precedent).
- Agents that do not enable bracketed paste receive exactly the bytes they did before.
