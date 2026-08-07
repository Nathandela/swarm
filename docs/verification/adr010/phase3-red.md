# RED evidence: ADR-010 Phase 3 — send_input + owner-tier peek (agents-tracker-6uz5)

date: 2026-08-07
HEAD: 5b5a044
worktree: .claude/worktrees/inter-session-orchestration

Scope: ADR-010 Amendment 1 A2/A3 Phase 3 — the owner-tier `send_input` op (a one-shot,
daemon-mediated write applying the r3p submit-boundary discipline daemon-side), the
authorization relaxation that lets an owner-tier connection peek, the `swarm send` /
`swarm peek` verbs, and the GG-7 spec lockstep (protocol.md rows + the S2 amendment).

Test files (all new):

| File | Piece |
| --- | --- |
| `internal/protocol/sendinput_test.go` | 1 — vocabulary, wire shape, key table, framing + gap, validation, tier refusal, funnel, client, spec lockstep |
| `internal/protocol/sendinput_atomicity_test.go` | 4 — a message is atomic against concurrent lease input |
| `internal/protocol/ownerpeek_test.go` | 2 — owner-tier authorization relaxation, remote-tier gate unchanged, client method |
| `cmd/swarm/sendpeek_test.go` | 3 — the `swarm send` and `swarm peek` verbs, usage/dispatch |

## Frozen API the tests are written against

```go
// internal/protocol/types.go — vocabulary. NO new capability: like attach, the op is gated
// by TIER, not by a negotiated cap (handleAttach checks none), so a client that negotiated
// nothing may still steer on the main socket.
const OpSendInput = "send_input"

// internal/protocol/schema/schema.go — the op-specific payload, the LaunchReq pattern,
// aliased into package protocol by types.go exactly as LaunchReq is. The SESSION is
// addressed the way every session-scoped op addresses one: Control.SessionID.
type SendInputReq struct {
    Text   string `json:"text,omitempty"`
    Submit bool   `json:"submit,omitempty"`
    Key    string `json:"key,omitempty"`
}
// Control gains: SendInput *SendInputReq `json:"send_input,omitempty"`

// The closed key vocabulary, in ONE place (the submitframe precedent). The daemon maps a
// name to bytes; `swarm send` validates against the same function instead of restating it.
func KeySequence(name string) ([]byte, bool)
//   enter -> "\r"   esc -> "\x1b"   ctrl-c -> "\x03"
//   tab   -> "\t"   up  -> "\x1b[A" down   -> "\x1b[B"

// internal/protocol/client.go — the house style of Launch (:128) / Kill (:144).
func (c *Client) SendInput(id string, req SendInputReq) error
func (c *Client) TerminalSnapshot(id string) (*TerminalSnapshot, error)

// cmd/swarm — the Phase 1/2 narrow-interface pattern, widened by two methods
type agentClient interface {
    List() ([]protocol.SessionView, error)
    Subscribe() (<-chan protocol.Event, error)
    Kill(id string) error
    Launch(protocol.LaunchReq) (id, name string, err error)
    SendInput(id string, req protocol.SendInputReq) error
    TerminalSnapshot(id string) (*protocol.TerminalSnapshot, error)
}
func runSend(args []string, c agentClient, stdout, stderr io.Writer) int
func runPeek(args []string, c agentClient, stdout, stderr io.Writer) int
```

## Decisions the tests pin, and why

- **The text bound is 4096** (`maxSendInputText`). Two precedents existed: `wire.MaxFrame`
  (1 MiB) is the TRANSPORT bound and would let one steering message paste a megabyte into a
  session; `phonecore.MaxInputPayload` (4096) is the bound the INPUT path already imposes on
  a single PTY write, and the daemon frames the text with `FrameLen(buf, 4096)` anyway.
  `internal/protocol` must not import `internal/phonecore` (the phone core is a deliberately
  daemon-free leaf, PB-BIND-0), so the value is restated with a pointer comment — the same
  re-homing `submitframe` did for the framing rule. 4096 is accepted, 4097 refused.
- **One upstream per session.** With a controller attached, `send_input` writes through the
  lease's EXISTING stream and opens nothing — the L3 pin the harness already records (the
  Server opens exactly one upstream `SessionStream` per session while a lease is held), and
  the reason A2 can call the shim's single input connection intact. With no controller, the
  daemon opens one stream for the message and CLOSES it, so a steering message never pins an
  upstream for the session's lifetime.
- **The gap is relative to text in the SAME message.** Key mode carries no text, so a key
  write — `enter` included, though it is submit-only — is a single immediate frame. Only a
  submit-only frame that follows a text frame owes `submitframe.Gap`.
- **Refusal codes.** Structurally wrong payloads (both modes, neither, unknown key, oversize
  text, missing payload) are `invalid_field`, following `handleRemoteSetControl`'s precedent;
  the remote tier is `not_authorized`, mirroring `handleAttach` (server.go:1559). A
  non-running or unknown target is pinned only as "an error refusal with nothing written" —
  the closed `ErrorCode` vocabulary has no better fit and the ADR names none.
- **Verb argument order is session-first** (`swarm send local/a1 --text "..."`), the form
  ADR-010 A2/A3 and the usage block write. Go's `flag` package stops at the first non-flag
  argument, so the id is taken off the front before parsing; a leading `-...` means no
  session was named and is misuse (exit 2, the `runSpawn` convention). Misuse never reaches
  the daemon.
- **`swarm send` success is silent** (exit 0, no output): an agent fanning out reads nothing
  back, and stdout stays free for the verbs that print. `swarm peek` prints the rendered
  lines verbatim, one per line; `--lines N` prints the LAST N (a screen's tail is what a
  steering agent reads), and `N <= 0` is misuse.

## The peek backend is reachable locally — nothing new to wire

A3's relaxation is authorization only, and the investigation confirms no backend piece is
missing on the local path: the owner-tier Server and the remote-tier Server are built on the
SAME `coreAPI` (`internal/skeleton/serve.go:251` and `:276`), and `coreAPI` already
implements `protocol.TerminalTapper` by subscribing read-only to the shared per-session tap
(`internal/skeleton/api.go:794`). The tap is a daemon-side multiplexer over the single-
consumer shim, not a remote-gateway component, so it opens with no gateway running. What the
handler must stop requiring on the owner tier is the kill switch and the `remote-gateway`
capability; the `TerminalTapper` backend itself stays required, and a backend without one
must refuse rather than leave a caller waiting (pinned).

## Commands

```
$ go build ./...
BUILD OK

$ go test ./internal/protocol/ -run 'TestSendInput|TestOwnerPeek|TestClient_SendInput|TestClient_TerminalSnapshot' -count=1
$ go test ./cmd/swarm/ -run 'TestRunSend|TestRunPeek|TestUsage_ListsSteeringVerbs' -count=1
```

## Failing output (compile-fail red, trimmed)

```
# github.com/Nathandela/swarm/internal/protocol [github.com/Nathandela/swarm/internal/protocol.test]
internal/protocol/sendinput_test.go:241:32: undefined: SendInputReq
internal/protocol/sendinput_test.go:243:32: undefined: OpSendInput
internal/protocol/sendinput_test.go:243:81: unknown field SendInput in struct literal of type Control
internal/protocol/ownerpeek_test.go:165:17: c.TerminalSnapshot undefined (type *Client has no field or method TerminalSnapshot)
internal/protocol/sendinput_atomicity_test.go:53:7: undefined: OpSendInput
internal/protocol/sendinput_atomicity_test.go:54:3: unknown field SendInput in struct literal of type Control
internal/protocol/sendinput_test.go:297:15: too many errors
FAIL	github.com/Nathandela/swarm/internal/protocol [build failed]

# github.com/Nathandela/swarm/cmd/swarm [github.com/Nathandela/swarm/cmd/swarm.test]
cmd/swarm/sendpeek_test.go:62:54: undefined: protocol.SendInputReq
cmd/swarm/sendpeek_test.go:146:13: undefined: runSend
cmd/swarm/sendpeek_test.go:219:15: too many errors
FAIL	github.com/Nathandela/swarm/cmd/swarm [build failed]
```

`KeySequence` does not appear in the trimmed list only because the compiler stops at "too
many errors"; it is undefined too.

## RED interpretation

`go build ./...` is green — production code is untouched. Every failure is the repo's
"undefined-only" red (`internal/protocol/harness_test.go`): the test binaries fail to
COMPILE on production symbols that do not exist yet (`OpSendInput`, `SendInputReq`,
`Control.SendInput`, `KeySequence`, `Client.SendInput`, `Client.TerminalSnapshot`, `runSend`,
`runPeek`). All test scaffolding — the timestamping `timedStream`, the `sendInputDaemon`
backend, the `fakeSteerClient` recorder, the emulator-backed peek fixtures — compiles.

**Behavioral red was verified, not assumed.** The suite was run once against a throwaway
stub (the two schema fields, the op constant, and no-op method bodies), which was then
deleted and the touched files restored — `git status` shows only the four new test files.
Under that stub every test failed for its intended reason and none hung: `send_input` came
back `error "unknown op \"send_input\""` from the dispatch switch, the owner-tier peek came
back `error/kill_switch` and `remote gateway capability not negotiated`, and the spec
lockstep reported the missing protocol.md rows and the unamended S2 line verbatim.

Three checks in the new files already PASS and are regression guards, not red — they pin
behavior the change must NOT alter (the `remote_input_refused_test.go` precedent):
`TestOwnerPeek_RemoteTierKeepsItsFullGate` (both remote refusals, no tap opened),
`TestOwnerPeek_BackendWithoutTapperRefused`, and `TestRunPeek_EmptyScreenIsNotAFailure`.

No existing test was modified. `agentClient` gains two methods, so the Phase 1/2 assertions
`var _ agentClient = (*fakeAgentClient)(nil)` and `(*fakeSpawnClient)(nil)` keep compiling via
base methods declared in the NEW file (a call landing there is a wiring mistake and says so);
the recording fake for these verbs is `fakeSteerClient`.

Two failures survive past compilation once the symbols land and are therefore listed
separately: `TestSendInput_SpecLockstep` (protocol.md carries no `send_input` rows and S2 is
unamended) and the pre-existing reflection drift check
`TestProtocolMD_ExistsAndDocumentsEveryField`, which fails on the new `send_input` Control
tag (GG-7 lockstep). `TestUsage_ListsSteeringVerbs` fails until `cmd/swarm/main.go`'s usage
and dispatch carry the verbs.

## What each test pins

| Test | Behavior |
| --- | --- |
| `TestSendInput_WireShape` | The op string is `send_input`; the payload rides `Control.send_input` and round-trips; a control without one emits no key (omitempty keeps the existing shape byte-identical). |
| `TestSendInput_KeySequences` | The six key names map to their exact byte sequences (up/down are the normal-mode cursor keys CSI A / CSI B); the vocabulary is CLOSED — empty, wrong case, padded and unlisted names are refused. |
| `TestSendInput_TextThenSubmitAfterGap` | `{Text:"hello world", Submit:true}` is EXACTLY two writes — the text, then the CR — never mixed, with the CR at least 100ms (well under Gap=150ms) after the text. |
| `TestSendInput_TextOnlyWritesNoSubmit` | `--no-submit` writes the text alone, with nothing slept: nothing submit-only follows it. |
| `TestSendInput_EmbeddedNewlinesStayHomogeneous` | Multi-line text is framed into maximal runs — every write is all-submit or all-text — the byte stream is unaltered, and every submit-only run that follows text owes the gap. |
| `TestSendInput_KeyModeIsOneImmediateFrame` | Each of the six keys is a single immediate write; `enter` is submit-only but has no text in its message to be spaced from, so it never sleeps. |
| `TestSendInput_MalformedRequestsRefused` | Missing payload, neither mode, both modes, and an unknown/miscased/padded key, and text past the bound are each `invalid_field` with NO stream opened. |
| `TestSendInput_TextAtTheBoundAccepted` | 4096 bytes of text is inside `maxSendInputText` and is delivered whole. |
| `TestSendInput_NonRunningSessionRefused` | An exited, lost or unknown session is refused with nothing written; the same request against a RUNNING session is accepted (the control case). |
| `TestSendInput_RefusedOnRemoteTier` | The remote tier refuses `not_authorized` before resolving the session and WITHOUT consulting the device authenticator — no signature can ever unlock it (mirrors `handleAttach`). |
| `TestSendInput_ReusesTheAttachedLeaseFunnel` | With a controller attached the message writes through the lease's existing stream, opens no second upstream (L3), and never takes or supersedes the lease (ADR-010 A1). |
| `TestSendInput_UnattachedOpensAndReleasesOneStream` | With no controller, exactly one upstream is opened for the message and CLOSED when it is done. |
| `TestSendInput_AtomicAgainstConcurrentLeaseInput` | Under continuous lease input, the message's text and CR frames are ADJACENT in the shim's write sequence — the per-session input serialization is held across the sleep — while the controller's own keystrokes still reach the shim. |
| `TestClient_SendInput` | The client method delivers the bytes and surfaces a daemon refusal as a Go error. |
| `TestSendInput_SpecLockstep` | GG-7: protocol.md documents the op and its closed key vocabulary; system-invariants.md's S2 carries the A2 sentence (`send_input`, "one input connection"). |
| `TestOwnerPeek_NeedsNoRemotePreconditions` | On the main socket, a connection with NO negotiated capabilities gets its snapshot even with the remote kill switch OFF; the render is the session's text and is escape-free. |
| `TestOwnerPeek_RemoteTierKeepsItsFullGate` | Remote-tier peek still refuses `kill_switch` with the switch off, and still refuses without the negotiated `remote-gateway` capability; neither refusal opens a tap. |
| `TestOwnerPeek_BackendWithoutTapperRefused` | The relaxation is authorization only: a backend with no `TerminalTapper` refuses instead of hanging. |
| `TestOwnerPeek_SnapshotSanitized` | Hostile output rendered for a LOCAL caller is still escape-free plain text. |
| `TestClient_TerminalSnapshot` | The one-shot client method returns the CURRENT screen without waiting for new output, and the snapshot frames the daemon keeps pushing afterwards are not mistaken for a later request's reply. |
| `TestRunSend_TextSubmitsByDefault` | `swarm send <id> --text s` builds `{Text:s, Submit:true}` for that session and is SILENT on success. |
| `TestRunSend_NoSubmitLeavesTheTextUnsent` | `--no-submit` clears Submit. |
| `TestRunSend_KeyVocabulary` | All six key names are accepted and passed through verbatim, with no text and no submit. |
| `TestRunSend_Misuse` | Exit 2 with zero daemon calls for: no arguments, no instruction, no session, both `--text` and `--key`, `--no-submit` with `--key`, an unknown or miscased key, an unknown flag, an extra positional. |
| `TestRunSend_EmptyTextIsMisuse` | `--text ""` names no message and is refused rather than written. |
| `TestRunSend_TextMayLookLikeFlagsOrSpanLines` | The message is a value: flag-looking, multi-line and tabbed text travels verbatim. |
| `TestRunSend_DaemonError` | A daemon refusal is exit 1 naming the cause, distinct from the exit 2 that means the command line was wrong. |
| `TestRunPeek_PrintsRenderedLines` | The whole screen, one line per line, in order, exit 0, one snapshot call for the named session. |
| `TestRunPeek_LinesPrintsTheTail` | `--lines N` prints the LAST N; N past the screen height prints what there is. |
| `TestRunPeek_EmptyScreenIsNotAFailure` | A session that has printed nothing exits 0 with no output. |
| `TestRunPeek_Misuse` | Exit 2 with zero daemon calls for: no arguments, no session, `--lines 0`, a negative or non-numeric `--lines`, an unknown flag, an extra positional. |
| `TestRunPeek_DaemonError` | Exit 1 naming the cause, nothing on stdout. |
| `TestUsage_ListsSteeringVerbs` | `usage` documents `swarm send` and `swarm peek` with their flags. |

## 2026-08-07 — reviewed design change: text is ONE paste frame, a message sleeps ONCE

date: 2026-08-07
HEAD: 5b5a044
worktree: .claude/worktrees/inter-session-orchestration

This section records the one sanctioned modification of already-written tests in this
phase: a DISCLOSED DESIGN REVISION, not accommodation. An adversarial concurrency review of
the shipped Phase 3 slice found the framing semantics themselves wrong, so the tests that
pinned them were rewritten to the new semantics FIRST and run against the untouched
implementation, which fails them (below). No test was weakened to let code pass.

### What the review found

- **Unbounded serialization hold (blocker).** `sendInputFrames` cut `Text` into
  `submitframe.FrameLen` runs and `sendMessage` slept `submitframe.Gap` before EVERY
  submit-only run while holding `attachMu` and `inMu`. The hold was therefore a function of
  the CALLER'S TEXT: 4 KiB with 2048 newlines is ~5 minutes of sleeping with the session's
  input serialization held — past the 10 s client timeout, freezing an attached
  controller's connection for the duration and stalling `Server.Close`. The code comment
  claiming the wait was "at most Gap" was true per frame and false per message.
- **Duplicate Enter.** Text that already ended in a newline was framed as `"hi"` + `"\n"`
  and then given a CR of its own — two submits, so the message ran and an empty prompt ran
  after it.

### The decided semantics (paste + single submit)

The `Text` portion is written as ONE frame — it is inside the existing 4096-byte bound, and
its embedded newlines are CONTENT the CLI's paste heuristic renders as a multi-line draft,
which is what sending a multi-line message means. Then, when `submit`, exactly ONE
`submitframe.Gap` sleep and ONE `"\r"` frame. Never more than one sleep per message, so the
hold is bounded by `Gap` plus two writes regardless of the text. This is the phone lane's
already-frozen `Paste`+`Enter` precedent (`phonecore.Insert` keeps a multi-line paste in ONE
unit, PB-INPUT-6, `internal/phonecore/r3p_submit_boundary_test.go:188`). Key mode is
unchanged: a single immediate frame. `submitframe.FrameLen` simply stops being used by
`sendinput.go`; `internal/phonecore` keeps using it and the package is untouched.

The atomicity test keeps its structure (CR adjacent to text under concurrent OWNER-tier
lease input) and the `>= 100ms` gap lower bound is kept, now with an upper bound
(`<= 2*Gap`) that pins the "one sleep per message" property.

### Tests revised, and the two added

| Test | Change |
| --- | --- |
| `TestSendInput_TextThenSubmitAfterGap` | Now asserts through `assertPasteThenSubmit` (2 writes, text verbatim, CR alone, `100ms <= span <= 2*Gap`). |
| `TestSendInput_EmbeddedNewlinesStayHomogeneous` -> `TestSendInput_EmbeddedNewlinesRideOnePasteFrame` | Multi-line text is ONE paste frame plus one CR, not a run per line. |
| `TestSendInput_TextEndingInNewlineSubmitsExactlyOnce` (new) | The duplicate-Enter guard: `"hi\n"`, `"hi\r"`, `"hi\r\n"`, `"hi\n\n"`, `"\n"`, `"\r\n"` each produce the text then exactly one CR, one gap later. |
| `TestSendInput_ManyNewlinesStillSleepOnce` (new) | 20 newlines is still two writes one gap apart — the concurrency bound, driven rather than asserted structurally. |
| `TestSendInput_SubmitFailureNamesTheHalfDeliveredState` (new) | A CR write failing after the text landed is a DISTINCT refusal naming the state and the recovery; a first-write failure must not claim the text was delivered. |
| `assertHomogeneous` -> `assertPasteThenSubmit` | The r3p rule under paste semantics: the byte that RUNS the message is never in the text's write. Embedded newlines are content. |

### Failing run against the untouched implementation

```
$ go test ./internal/protocol/ -count=1 -run 'TestSendInput_TextThenSubmitAfterGap|TestSendInput_EmbeddedNewlinesRideOnePasteFrame|TestSendInput_TextEndingInNewlineSubmitsExactlyOnce|TestSendInput_ManyNewlinesStillSleepOnce|TestSendInput_SubmitFailureNamesTheHalfDeliveredState' -v

--- PASS: TestSendInput_TextThenSubmitAfterGap (0.17s)
=== RUN   TestSendInput_EmbeddedNewlinesRideOnePasteFrame
    sendinput_test.go:458: the message was 4 PTY writes "line one\nline two\r", want exactly 2 — the text as ONE paste, then the CR
--- FAIL: TestSendInput_EmbeddedNewlinesRideOnePasteFrame (0.31s)
=== RUN   TestSendInput_TextEndingInNewlineSubmitsExactlyOnce
    sendinput_test.go:476: the message was 3 PTY writes "hi\n\r", want exactly 2 — the text as ONE paste, then the CR
    sendinput_test.go:476: the message was 3 PTY writes "hi\r\r", want exactly 2 — the text as ONE paste, then the CR
    sendinput_test.go:476: the message was 3 PTY writes "hi\r\n\r", want exactly 2 — the text as ONE paste, then the CR
    sendinput_test.go:476: the message was 3 PTY writes "hi\n\n\r", want exactly 2 — the text as ONE paste, then the CR
--- FAIL: TestSendInput_TextEndingInNewlineSubmitsExactlyOnce (0.61s)
=== RUN   TestSendInput_ManyNewlinesStillSleepOnce
    sendinput_test.go:493: the message was 41 PTY writes "x\nx\n...x\n\r", want exactly 2 — the text as ONE paste, then the CR
--- FAIL: TestSendInput_ManyNewlinesStillSleepOnce (3.43s)
=== RUN   TestSendInput_SubmitFailureNamesTheHalfDeliveredState
    sendinput_test.go:656: the refusal "send_input: shim input stream closed" does not state "text delivered"; ...
    sendinput_test.go:656: the refusal "send_input: shim input stream closed" does not state "submit not sent"; ...
    sendinput_test.go:656: the refusal "send_input: shim input stream closed" does not state "--key enter"; ...
--- FAIL: TestSendInput_SubmitFailureNamesTheHalfDeliveredState (0.16s)
FAIL	github.com/Nathandela/swarm/internal/protocol	5.466s
```

`TestSendInput_ManyNewlinesStillSleepOnce` also SHOWS the blocker rather than only naming
it: 20 newlines took 3.43 s of held serialization under the old framing, against a budget of
one 150 ms gap. `TestSendInput_TextThenSubmitAfterGap` passes both ways — single-line text
was already two frames — and is listed to show the revision did not move the canonical case.

One further hole was found while implementing and pinned the same way (red, then fixed): the
gap condition was inherited from the per-run framing and keyed on the frame's CONTENT
(`IsSubmitOnly`), so an all-newline text (`"\n"`, `"\r\n"`) wrote its paste and its CR
back-to-back — 667 ns apart — recreating the co-arrival the gap exists to prevent. Those two
sub-cases were added to `TestSendInput_TextEndingInNewlineSubmitsExactlyOnce`, failed, and
the condition now keys on frame POSITION: a message has at most two frames, and the second
one always owes the first a gap.

### Two more review defects fixed in the same slice (their red)

- **Stale peek snapshot (blocker).** `Client.TerminalSnapshot` reused a buffered cap-1
  `peekCh` that a PREVIOUS peek could still be filling, so peeking session B right after
  session A returned A's screen with no error. New regression test
  `TestClient_TerminalSnapshot_SecondPeekIsNotTheFirstScreen`
  (`internal/protocol/ownerpeek_test.go`) peeks A, lets A push again, then peeks B; against
  the untouched client it fails with A's screen:

```
$ go test ./internal/protocol/ -count=1 -run TestClient_TerminalSnapshot_SecondPeekIsNotTheFirstScreen -v
=== RUN   TestClient_TerminalSnapshot_SecondPeekIsNotTheFirstScreen
    ownerpeek_test.go:263: the second peek returned a snapshot of session "sessA"; want sessB — a peek must never answer with another session's screen
    ownerpeek_test.go:267: the second peek returned ["SCREEN AA KEEPS PRINTING                " ...]; want sessB's screen, not the screen left over from the peek of sessA
--- FAIL: TestClient_TerminalSnapshot_SecondPeekIsNotTheFirstScreen (0.14s)
```

  The fix is a FRESH channel per call PLUS discarding any snapshot whose
  `Terminal.Session` is not the session that was asked for (both are needed: the fresh
  channel drops what was already buffered, the session match drops what is still in flight).
- **Atomicity was overstated (docs only, no mechanism change).** The owner and remote
  `Server`s are distinct values with distinct per-session `inMu` over one shared tap, so a
  remote `take_control` keystroke CAN land between the text and the CR. Accepted for the
  personal single-owner model — a remote take-control means the human deliberately grabbed
  the session — and the claim is now scoped to OWNER-TIER lease input in all four places
  that stated it (`sendinput.go`, `docs/specifications/protocol.md`,
  `docs/invariants/system-invariants.md` S2, ADR-010 Amendment 1 A2).
