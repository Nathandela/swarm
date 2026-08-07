package protocol

// FAILING-FIRST protocol tests for ADR-010 Amendment 1 A2 / Phase 3 PIECE 1: the
// OWNER-TIER `send_input` op — a one-shot, daemon-mediated message write into a
// session, with the r3p submit-boundary discipline applied DAEMON-SIDE.
//
// WHY A NEW OP AND NOT A LEASE. ADR-010 A1 recorded that every lease-based control
// session (take_control included) acquires the ordinary attach lease via srv.attach and
// therefore SUPERSEDES an attached human — the exact defect the "simple lease-steal"
// alternative was rejected for. A2's answer: the handler never touches the lease. It
// writes the message through the SAME funnel a lease write uses (the per-session input
// serialization of forwardInput, then SessionStream.Input), serialized against concurrent
// owner-tier lease input so the whole message is atomic on that tier (the remote
// take_control writer is a separate serialization). An attached human sees the injected text
// appear before it submits — transparency by construction.
//
// FROZEN API (the implementer wires it):
//
//	// internal/protocol/types.go — vocabulary. NO new capability: like attach, the op is
//	// gated by TIER, not by a negotiated cap (handleAttach checks neither CapAttach nor
//	// any other), so a client that negotiated nothing may still steer on the main socket.
//	const OpSendInput = "send_input"
//
//	// internal/protocol/schema/schema.go — the op-specific payload, the LaunchReq pattern,
//	// aliased into package protocol by types.go exactly as LaunchReq is.
//	type SendInputReq struct {
//	    Text   string `json:"text,omitempty"`
//	    Submit bool   `json:"submit,omitempty"`
//	    Key    string `json:"key,omitempty"`
//	}
//	// ...and Control gains: SendInput *SendInputReq `json:"send_input,omitempty"`
//	// The SESSION is addressed the way every session-scoped op addresses one today:
//	// Control.SessionID (namespaced), resolved by resolveSession.
//
//	// The closed key vocabulary, in ONE place (the submitframe precedent — a rule with two
//	// callers gets one copy). The daemon maps a name to bytes; `swarm send` validates
//	// against the same function instead of restating the list.
//	func KeySequence(name string) ([]byte, bool)
//
//	// internal/protocol/client.go — the house style of Launch (:128) / Kill (:144).
//	func (c *Client) SendInput(id string, req SendInputReq) error
//
// FROZEN BEHAVIOR:
//
//   - EXACTLY ONE MODE per request. Key set means one named key; Text set means text,
//     submitted with a trailing CR when Submit. Both set, neither set, an unknown key name,
//     or text past the bound are refused invalid_field with NOTHING written.
//   - TEXT IS ONE FRAME, PASTE SEMANTICS (2026-08-07 design revision, below). A text-mode
//     message is at most TWO PTY writes: the text verbatim in one frame — embedded newlines
//     included, they are CONTENT the CLI's paste heuristic renders as a multi-line draft —
//     then, when Submit, the single CR that runs it, in a frame of its own.
//   - ONE GAP PER MESSAGE, SLEPT DAEMON-SIDE: submitframe.Gap elapses between the text frame
//     and the CR that follows it, so no hop's batching can recompress the two into one mixed
//     write (agents-tracker-r3p: Claude Code reads text+CR in one write as an unsubmitted
//     paste). The daemon sleeps it while holding the per-session input serialization, so that
//     hold is bounded by Gap plus two writes. The shim must NEVER sleep: its ptyWriter lock
//     is shared with the VT emulator's DSR/CPR reply pump.
//   - KEY MODE IS ONE IMMEDIATE FRAME. A key message carries no text to be spaced from —
//     `enter` included, though it is submit-only.
//   - REFUSED OUTRIGHT ON THE REMOTE TIER, before any session is resolved, mirroring
//     handleAttach (server.go:1559): the remote tier keeps its own full lane.
//   - A NON-RUNNING (or unknown) session is refused with nothing written.
//   - A MID-MESSAGE STREAM FAILURE IS ITS OWN REFUSAL. If the text landed and the CR write
//     then failed, the caller is told exactly that — the message is half-delivered and a
//     `send --key enter` recovers it — never a bare refusal indistinguishable from "nothing
//     was written".
//
// 2026-08-07 DESIGN REVISION (reviewed): TEXT IS ONE FRAME, and a message sleeps ONCE.
// The first implementation cut Text into submitframe.FrameLen runs and slept Gap before
// EVERY submit-only run, holding the per-session serialization throughout. A concurrency
// review found the hold unbounded in the input: 2048 embedded newlines is ~5 minutes of
// sleeping with attachMu and inMu held — past the client timeout, freezing an attached
// controller's connection and stalling Server.Close — and it also submitted text that
// merely ENDED in a newline twice. The semantics moved to the phone lane's already-frozen
// Paste+Enter precedent (phonecore.Insert keeps a multi-line paste in ONE unit, PB-INPUT-6,
// r3p_submit_boundary_test.go:188): the text is one paste, the submit is one CR after one
// gap. submitframe.FrameLen is simply no longer this path's tool; phonecore still uses it.
//
// THE TEXT BOUND — maxSendInputText = 4096. Two precedents exist: wire.MaxFrame (1 MiB) is
// the TRANSPORT bound, which would let one steering message paste a megabyte into a
// session, and phonecore.MaxInputPayload (4096) is the bound the INPUT path already
// imposes on a single PTY write. The input bound is the right one: send_input is a
// steering message, and since the revision its text IS a single PTY write.
// internal/protocol does not import internal/phonecore (the phone core is a deliberately
// daemon-free leaf, PB-BIND-0), so the value is restated with a pointer comment — the same
// re-homing submitframe did for the framing rule.
//
// RED today: OpSendInput, SendInputReq, Control.SendInput, KeySequence and Client.SendInput
// do not exist, so the package fails to COMPILE — the repo's "undefined-only" red
// (harness_test.go). Two tests survive past compilation and fail on their own terms:
// TestSendInput_SpecLockstep (the GG-7 protocol.md rows + the S2 amendment sentence are
// not written yet) and the pre-existing reflection drift check
// TestProtocolMD_ExistsAndDocumentsEveryField (the new `send_input` Control tag).
//
// RED for the 2026-08-07 revision: the four framing tests below were rewritten to the new
// semantics and run against the SHIPPED per-run implementation before it was touched, where
// they fail on frame counts and on the message's total span. Evidence:
// docs/verification/adr010/phase3-red.md, section "2026-08-07 — reviewed design change".

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/submitframe"
)

// ---------------------------------------------------------------------------
// Fixtures — a SessionStream that timestamps every write.
// ---------------------------------------------------------------------------

// inputWrite is one SessionStream.Input call: the exact payload the daemon handed the
// shim, and WHEN. The timestamps are the whole point of this suite — the r3p discipline
// is as much about the spacing between two writes as about their contents.
type inputWrite struct {
	payload []byte
	at      time.Time
}

// timedStream is a SessionStream recording (payload, timestamp) per Input. It is separate
// from harness_test.go's stubStream (which records payloads only) so the shared harness
// stays untouched.
type timedStream struct {
	snap   []byte
	frames chan []byte

	mu       sync.Mutex
	writes   []inputWrite
	resizes  [][2]int
	failAt   int // when > 0, the 1-based Input call that fails (the shim's input stream closing mid-message)
	closed   bool
	closedCh chan struct{}
}

func newTimedStream() *timedStream {
	return &timedStream{
		snap:     []byte("SNAPSHOT"),
		frames:   make(chan []byte, 64),
		closedCh: make(chan struct{}),
	}
}

func (s *timedStream) Snapshot() []byte      { return s.snap }
func (s *timedStream) Frames() <-chan []byte { return s.frames }

func (s *timedStream) Input(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAt > 0 && len(s.writes)+1 == s.failAt {
		return errors.New("shim input stream closed")
	}
	s.writes = append(s.writes, inputWrite{payload: append([]byte(nil), p...), at: time.Now()})
	return nil
}

func (s *timedStream) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resizes = append(s.resizes, [2]int{cols, rows})
	return nil
}

func (s *timedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closedCh)
	}
	return nil
}

// written returns a copy of every Input this stream received, in order.
func (s *timedStream) written() []inputWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]inputWrite(nil), s.writes...)
}

// waitClosed reports whether the stream was Closed within d.
func (s *timedStream) waitClosed(d time.Duration) bool {
	select {
	case <-s.closedCh:
		return true
	case <-time.After(d):
		return false
	}
}

// sendInputDaemon is a DaemonAPI whose Attach hands out timedStreams, so a test can read
// the exact write sequence AND its timing. It embeds *stubDaemon for List/Launch/Kill/
// Events and for the optional interfaces a remote-tier Server asserts at construction
// (KillSwitch, OperationClaimer, DeviceAuthenticator), so the SAME fixture serves the
// owner-tier and remote-tier cases.
type sendInputDaemon struct {
	*stubDaemon

	mu     sync.Mutex
	failAt int // armed before the server starts: the 1-based Input call every new stream fails on
	opened []*timedStream
}

func newSendInputDaemon() *sendInputDaemon {
	return &sendInputDaemon{stubDaemon: newStubDaemon()}
}

func (d *sendInputDaemon) Attach(id string) (SessionStream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := newTimedStream()
	st.failAt = d.failAt
	d.opened = append(d.opened, st)
	return st, nil
}

// attachCount is how many upstream streams the Server opened for this backend.
func (d *sendInputDaemon) attachCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.opened)
}

// stream returns the i-th opened stream (nil when there is none).
func (d *sendInputDaemon) stream(i int) *timedStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i < 0 || i >= len(d.opened) {
		return nil
	}
	return d.opened[i]
}

// onlyStream returns the single stream the Server must have opened.
func (d *sendInputDaemon) onlyStream(t *testing.T) *timedStream {
	t.Helper()
	if n := d.attachCount(); n != 1 {
		t.Fatalf("daemon opened %d upstream streams, want exactly 1", n)
	}
	return d.stream(0)
}

var _ DaemonAPI = (*sendInputDaemon)(nil)

// sendFixture is one owner-tier Server over one RUNNING session plus a handshaked raw
// connection: the setup every case below shares.
type sendFixture struct {
	t   *testing.T
	d   *sendInputDaemon
	rc  *rawConn
	ep  string
	sid string
}

func newSendFixture(t *testing.T) sendFixture {
	t.Helper()
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone)) // ProcessRunning
	rc := rawDial(t, serveOwnerAPI(t, d))
	rep := rc.hello(Version, nil) // NO capabilities: send_input is tier-gated, like attach
	return sendFixture{t: t, d: d, rc: rc, ep: rep.EndpointID, sid: rep.EndpointID + "/sess1"}
}

// send issues one send_input and returns the daemon's reply.
func (f sendFixture) send(req *SendInputReq) Control {
	f.t.Helper()
	f.rc.writeControl(Control{Op: OpSendInput, EndpointID: f.ep, SessionID: f.sid, SendInput: req})
	return nextControl(f.t, f.rc)
}

// concat joins every recorded payload — what the session's PTY actually received.
func concat(ws []inputWrite) []byte {
	var out []byte
	for _, w := range ws {
		out = append(out, w.payload...)
	}
	return out
}

// readRepoDoc reads a repo-relative document for the GG-7 lockstep assertions, reusing
// protocolmd_test.go's module-root walk.
func readRepoDoc(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot(t)}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s not found: %v", path, err)
	}
	return string(data)
}

// assertPasteThenSubmit is the r3p rule under the message's paste semantics: a submitted
// message is EXACTLY TWO writes — the text verbatim as one paste, then the CR alone — and
// the CR arrives a real interval after the text. Whatever the text contains, the byte that
// runs it is never in the same PTY write (which Claude Code would read as an unsubmitted
// paste), and the message sleeps at most ONCE, so the input serialization it holds is
// bounded by Gap however the text is shaped.
func assertPasteThenSubmit(t *testing.T, ws []inputWrite, text string) {
	t.Helper()
	if len(ws) != 2 {
		t.Fatalf("the message was %d PTY writes %q, want exactly 2 — the text as ONE paste, then the CR",
			len(ws), concat(ws))
	}
	if string(ws[0].payload) != text {
		t.Errorf("frame 0 = %q, want the text verbatim %q (embedded newlines are CONTENT, not frame boundaries)",
			ws[0].payload, text)
	}
	if string(ws[1].payload) != "\r" {
		t.Errorf("frame 1 = %q, want the submit alone %q", ws[1].payload, "\r")
	}
	if !submitframe.IsSubmitOnly(ws[1].payload) {
		t.Errorf("the submitting frame %q is not submit-only", ws[1].payload)
	}
	span := ws[1].at.Sub(ws[0].at)
	// The lower bound is generous (100ms, well under Gap=150ms) so scheduler jitter cannot
	// flake it; anything below it means no gap was slept at all.
	if span < 100*time.Millisecond {
		t.Errorf("the CR followed the text after %v; the daemon must sleep submitframe.Gap (%v) before it, "+
			"or a hop's batching recreates the mixed write at the PTY", span, submitframe.Gap)
	}
	// The upper bound is the concurrency property: ONE sleep per message, so the per-session
	// input serialization is held for Gap plus two writes and no longer.
	if span > 2*submitframe.Gap {
		t.Errorf("the message spanned %v; a message sleeps submitframe.Gap (%v) exactly ONCE, so the "+
			"per-session input serialization it holds stays bounded no matter what the text contains", span, submitframe.Gap)
	}
}

// ---------------------------------------------------------------------------
// Vocabulary and wire shape
// ---------------------------------------------------------------------------

// TestSendInput_WireShape pins the op string and the payload's place on Control: a
// send_input Control round-trips through the codec with its text/submit/key fields, and a
// Control carrying no send_input emits no `send_input` key at all (omitempty — an existing
// -shape Control must serialize byte-identically, GG-7).
func TestSendInput_WireShape(t *testing.T) {
	if OpSendInput != "send_input" {
		t.Errorf("OpSendInput = %q, want %q (snake_case, the op-vocabulary convention)", OpSendInput, "send_input")
	}

	in := Control{
		Op:         OpSendInput,
		EndpointID: "ep1",
		SessionID:  "ep1/sess1",
		SendInput:  &SendInputReq{Text: "run the tests", Submit: true},
	}
	body, err := EncodeControl(in)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	if !strings.Contains(string(body), `"send_input"`) {
		t.Errorf("encoded send_input control = %s; want a `send_input` payload key (the LaunchReq pattern)", body)
	}
	got, err := DecodeControl(body)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}
	if got.SendInput == nil {
		t.Fatalf("decoded control lost its send_input payload")
	}
	if got.SendInput.Text != in.SendInput.Text || !got.SendInput.Submit || got.SendInput.Key != "" {
		t.Errorf("round-tripped payload = %+v, want %+v", *got.SendInput, *in.SendInput)
	}

	bare, err := EncodeControl(Control{Op: OpList, EndpointID: "ep1"})
	if err != nil {
		t.Fatalf("EncodeControl(bare): %v", err)
	}
	if strings.Contains(string(bare), "send_input") {
		t.Errorf("a control with no send_input emitted the key anyway: %s (omitempty keeps the existing shape byte-identical)", bare)
	}
}

// TestSendInput_KeySequences pins the closed key vocabulary and its bytes. These are the
// sequences a terminal sends for the named keys; up/down are the NORMAL-mode cursor keys
// (CSI A / CSI B), the form ADR-010 A2 names.
func TestSendInput_KeySequences(t *testing.T) {
	want := map[string]string{
		"enter":  "\r",
		"esc":    "\x1b",
		"ctrl-c": "\x03",
		"tab":    "\t",
		"up":     "\x1b[A",
		"down":   "\x1b[B",
	}
	for name, seq := range want {
		got, ok := KeySequence(name)
		if !ok {
			t.Errorf("KeySequence(%q) not in the vocabulary; want %q", name, seq)
			continue
		}
		if string(got) != seq {
			t.Errorf("KeySequence(%q) = %q, want %q", name, got, seq)
		}
	}
	for _, bogus := range []string{"", "ENTER", "return", "ctrl-d", "f13", " enter", "enter "} {
		if seq, ok := KeySequence(bogus); ok {
			t.Errorf("KeySequence(%q) = %q, ok; the vocabulary is CLOSED — an unknown name must be refused", bogus, seq)
		}
	}
}

// ---------------------------------------------------------------------------
// Framing and the gap (PIECE 4 handler-level proof)
// ---------------------------------------------------------------------------

// TestSendInput_TextThenSubmitAfterGap is the canonical steering message: text, then the
// CR that runs it. EXACTLY TWO writes, in order, never mixed, and the CR arrives a real
// interval after the text — the daemon slept submitframe.Gap.
func TestSendInput_TextThenSubmitAfterGap(t *testing.T) {
	f := newSendFixture(t)

	if got := f.send(&SendInputReq{Text: "hello world", Submit: true}); got.Op != OpOK {
		t.Fatalf("send_input reply = op %q code %q error %q; want OpOK", got.Op, got.ErrorCode, got.Error)
	}
	assertPasteThenSubmit(t, f.d.onlyStream(t).written(), "hello world")
}

// TestSendInput_TextOnlyWritesNoSubmit: --no-submit leaves the text in the input box. One
// frame, no CR, and no gap to sleep.
func TestSendInput_TextOnlyWritesNoSubmit(t *testing.T) {
	f := newSendFixture(t)
	start := time.Now()

	if got := f.send(&SendInputReq{Text: "draft, do not run"}); got.Op != OpOK {
		t.Fatalf("send_input reply = op %q error %q; want OpOK", got.Op, got.Error)
	}

	ws := f.d.onlyStream(t).written()
	if len(ws) != 1 || string(ws[0].payload) != "draft, do not run" {
		t.Fatalf("unsubmitted text wrote %d frames %q; want exactly the text and no CR", len(ws), concat(ws))
	}
	if elapsed := ws[0].at.Sub(start); elapsed >= submitframe.Gap {
		t.Errorf("text-only write took %v; nothing submit-only follows it, so nothing may sleep", elapsed)
	}
}

// TestSendInput_EmbeddedNewlinesRideOnePasteFrame: a multi-line message is ONE paste plus
// one CR. The newlines inside the text are CONTENT — the CLI's paste heuristic renders them
// as a multi-line draft, which is the point of sending a multi-line message — so they are
// not frame boundaries and they submit nothing. The session receives exactly the bytes that
// were sent, in order.
func TestSendInput_EmbeddedNewlinesRideOnePasteFrame(t *testing.T) {
	f := newSendFixture(t)
	const text = "line one\nline two"

	if got := f.send(&SendInputReq{Text: text, Submit: true}); got.Op != OpOK {
		t.Fatalf("send_input reply = op %q error %q; want OpOK", got.Op, got.Error)
	}

	ws := f.d.onlyStream(t).written()
	assertPasteThenSubmit(t, ws, text)
	if got, want := string(concat(ws)), text+"\r"; got != want {
		t.Fatalf("the session received %q, want %q (framing may split writes, never alter bytes)", got, want)
	}
}

// TestSendInput_TextEndingInNewlineSubmitsExactlyOnce is the duplicate-Enter guard. Text
// that already ends in a newline used to be cut into "hi" + "\n" and then given a CR of its
// own — TWO submits, so the message ran and an empty prompt ran after it. Under paste
// semantics the trailing newline stays inside the paste and exactly one CR follows.
//
// The all-newline cases are the same rule with nothing else in the paste: the message's
// text is still its own write, and the CR still owes it the gap. The gap is owed because a
// TEXT frame precedes the CR, not because of what that frame happens to contain — text and
// submit co-arriving at the PTY is the hazard either way.
func TestSendInput_TextEndingInNewlineSubmitsExactlyOnce(t *testing.T) {
	for _, text := range []string{"hi\n", "hi\r", "hi\r\n", "hi\n\n", "\n", "\r\n"} {
		t.Run(strconv.Quote(text), func(t *testing.T) {
			f := newSendFixture(t)

			if got := f.send(&SendInputReq{Text: text, Submit: true}); got.Op != OpOK {
				t.Fatalf("send_input reply = op %q error %q; want OpOK", got.Op, got.Error)
			}
			assertPasteThenSubmit(t, f.d.onlyStream(t).written(), text)
		})
	}
}

// TestSendInput_ManyNewlinesStillSleepOnce is the concurrency bound, driven rather than
// asserted structurally: text full of newlines is still two writes one gap apart. Sleeping
// per newline while holding the session's input serialization made the hold a function of
// the ATTACKER'S text — 2048 newlines is ~5 minutes with attachMu and inMu held, past the
// client timeout, freezing an attached controller's connection and stalling Server.Close.
func TestSendInput_ManyNewlinesStillSleepOnce(t *testing.T) {
	f := newSendFixture(t)
	text := strings.Repeat("x\n", 20)

	if got := f.send(&SendInputReq{Text: text, Submit: true}); got.Op != OpOK {
		t.Fatalf("send_input reply = op %q error %q; want OpOK", got.Op, got.Error)
	}
	assertPasteThenSubmit(t, f.d.onlyStream(t).written(), text)
}

// TestSendInput_KeyModeIsOneImmediateFrame: a named key is a single write, sent at once.
// Gap is relative to text in the SAME message and key mode carries none — including
// `enter`, which is submit-only but has nothing to be spaced from.
func TestSendInput_KeyModeIsOneImmediateFrame(t *testing.T) {
	cases := []struct{ key, want string }{
		{"enter", "\r"},
		{"esc", "\x1b"},
		{"ctrl-c", "\x03"},
		{"tab", "\t"},
		{"up", "\x1b[A"},
		{"down", "\x1b[B"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			f := newSendFixture(t)
			start := time.Now()

			if got := f.send(&SendInputReq{Key: c.key}); got.Op != OpOK {
				t.Fatalf("send_input --key %s reply = op %q error %q; want OpOK", c.key, got.Op, got.Error)
			}

			ws := f.d.onlyStream(t).written()
			if len(ws) != 1 {
				t.Fatalf("key %q wrote %d frames %q, want exactly 1", c.key, len(ws), concat(ws))
			}
			if string(ws[0].payload) != c.want {
				t.Errorf("key %q wrote %q, want %q", c.key, ws[0].payload, c.want)
			}
			if elapsed := ws[0].at.Sub(start); elapsed >= submitframe.Gap {
				t.Errorf("key %q was written after %v; a key message carries no text, so it must never "+
					"sleep the gap", c.key, elapsed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validation — every refusal writes NOTHING
// ---------------------------------------------------------------------------

// TestSendInput_MalformedRequestsRefused: exactly one mode per request, a closed key
// vocabulary, and a bounded text. Each refusal carries invalid_field (handleRemoteSetControl's
// precedent for a structurally wrong payload) and — the security property — opens no
// upstream stream and writes no byte.
func TestSendInput_MalformedRequestsRefused(t *testing.T) {
	cases := []struct {
		name string
		req  *SendInputReq
	}{
		{"missing payload", nil},
		{"neither text nor key", &SendInputReq{}},
		{"neither, submit alone", &SendInputReq{Submit: true}},
		{"both text and key", &SendInputReq{Text: "hi", Key: "enter"}},
		{"unknown key", &SendInputReq{Key: "f13"}},
		{"key wrong case", &SendInputReq{Key: "Enter"}},
		{"key padded", &SendInputReq{Key: " enter"}},
		// 4097 bytes: one past maxSendInputText (4096 == phonecore.MaxInputPayload, the
		// bound the input path already imposes on a single PTY write).
		{"text past the bound", &SendInputReq{Text: strings.Repeat("a", 4097)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSendFixture(t)

			got := f.send(c.req)
			if got.Op != OpError || got.ErrorCode != CodeInvalidField {
				t.Fatalf("malformed send_input (%s) = op %q code %q; want error/invalid_field", c.name, got.Op, got.ErrorCode)
			}
			if n := f.d.attachCount(); n != 0 {
				t.Fatalf("a refused send_input opened %d upstream streams; want 0 (refused before any side effect)", n)
			}
		})
	}
}

// TestSendInput_TextAtTheBoundAccepted pins the bound itself rather than only its far side:
// text of exactly maxSendInputText (4096) is accepted and delivered whole.
func TestSendInput_TextAtTheBoundAccepted(t *testing.T) {
	f := newSendFixture(t)
	text := strings.Repeat("a", 4096)

	if got := f.send(&SendInputReq{Text: text}); got.Op != OpOK {
		t.Fatalf("send_input at the bound = op %q code %q error %q; want OpOK (4096 is INSIDE maxSendInputText)",
			got.Op, got.ErrorCode, got.Error)
	}
	if got := string(concat(f.d.onlyStream(t).written())); got != text {
		t.Errorf("the session received %d bytes, want the full %d", len(got), len(text))
	}
}

// TestSendInput_NonRunningSessionRefused: a session that cannot receive input is refused
// with nothing written and no stream opened. An exited session and an id no session has
// are the same case to the caller — there is nothing to steer.
func TestSendInput_NonRunningSessionRefused(t *testing.T) {
	cases := []struct {
		name  string
		metas []persist.Meta
	}{
		{"exited session", []persist.Meta{{
			ID: "sess1", AgentType: "claude", Cwd: "/tmp",
			Status: status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		}}},
		{"lost session", []persist.Meta{{
			ID: "sess1", AgentType: "claude", Cwd: "/tmp",
			Status: status.Status{Process: status.ProcessLost, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		}}},
		{"no such session", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newSendInputDaemon()
			d.setMetas(c.metas...)
			rc := rawDial(t, serveOwnerAPI(t, d))
			rep := rc.hello(Version, nil)

			rc.writeControl(Control{
				Op: OpSendInput, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1",
				SendInput: &SendInputReq{Text: "hello", Submit: true},
			})
			if got := nextControl(t, rc); got.Op != OpError {
				t.Fatalf("send_input to a %s = op %q; want an error refusal", c.name, got.Op)
			}
			if n := d.attachCount(); n != 0 {
				t.Fatalf("send_input to a %s opened %d upstream streams; want 0", c.name, n)
			}
		})
	}

	// The control case: the SAME request against a RUNNING session is accepted, so the
	// refusals above are about the target's state and not about the request.
	t.Run("running session accepts it", func(t *testing.T) {
		f := newSendFixture(t)
		if got := f.send(&SendInputReq{Text: "hello", Submit: true}); got.Op != OpOK {
			t.Fatalf("send_input to a RUNNING session = op %q error %q; want OpOK", got.Op, got.Error)
		}
	})
}

// TestSendInput_SubmitFailureNamesTheHalfDeliveredState: the ~Gap window between the text
// and its CR is real, and a session's input stream can close inside it. When it does, the
// text is already on the screen and only the submit is missing — a state a caller CAN
// recover from, but only if it is told. A bare refusal is indistinguishable from "nothing
// was written", so the message is a DISTINCT one naming the state and the recovery.
func TestSendInput_SubmitFailureNamesTheHalfDeliveredState(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	d.failAt = 2 // the text write lands; the CR that follows it does not
	rc := rawDial(t, serveOwnerAPI(t, d))
	rep := rc.hello(Version, nil)

	rc.writeControl(Control{
		Op: OpSendInput, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1",
		SendInput: &SendInputReq{Text: "run the tests", Submit: true},
	})
	got := nextControl(t, rc)
	if got.Op != OpError {
		t.Fatalf("send_input whose CR write failed = op %q; want an error — the message did not land whole", got.Op)
	}
	for _, want := range []string{"text delivered", "submit not sent", "--key enter"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("the refusal %q does not state %q; a half-delivered message must be told apart from "+
				"one that wrote nothing, and must name the recovery (peek, then send --key enter)", got.Error, want)
		}
	}
	if n := len(d.onlyStream(t).written()); n != 1 {
		t.Errorf("the stream took %d writes, want 1 (the text landed, the CR failed)", n)
	}

	// The control case: the SAME failure on the FIRST write wrote nothing, so it must NOT
	// claim the text was delivered.
	d2 := newSendInputDaemon()
	d2.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	d2.failAt = 1
	rc2 := rawDial(t, serveOwnerAPI(t, d2))
	rep2 := rc2.hello(Version, nil)
	rc2.writeControl(Control{
		Op: OpSendInput, EndpointID: rep2.EndpointID, SessionID: rep2.EndpointID + "/sess1",
		SendInput: &SendInputReq{Text: "run the tests", Submit: true},
	})
	got2 := nextControl(t, rc2)
	if got2.Op != OpError {
		t.Fatalf("send_input whose text write failed = op %q; want an error", got2.Op)
	}
	if strings.Contains(got2.Error, "text delivered") {
		t.Errorf("a message that wrote NOTHING reported %q; only a half-delivered message may say the "+
			"text landed", got2.Error)
	}
}

// TestSendInput_RefusedOnRemoteTier: the remote tier keeps its own full lane (signed
// take_control, device auth, kill switch), so send_input is refused there OUTRIGHT —
// before any session is resolved and before any authorization is consulted — exactly as
// handleAttach refuses interactive control (server.go:1559). Refusing with a signed,
// well-formed-looking request proves the gate is not merely the missing signature.
func TestSendInput_RefusedOnRemoteTier(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	rc := rawDial(t, serveRemoteAPI(t, d))
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(Control{
		Op: OpSendInput, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1",
		OperationID: "devA:01JSEND0000000000000000", DeviceID: "devA", DeviceSig: "sig",
		SendInput: &SendInputReq{Text: "rm -rf /", Submit: true},
	})
	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote-tier send_input = op %q code %q; want error/not_authorized (mirroring handleAttach)", got.Op, got.ErrorCode)
	}
	if n := d.attachCount(); n != 0 {
		t.Fatalf("remote-tier send_input opened %d upstream streams; want 0 (refused before any side effect)", n)
	}
	if n := len(d.authorizedTuples()); n != 0 {
		t.Fatalf("remote-tier send_input consulted the device authenticator %d times; want 0 — "+
			"the op is refused by TIER, before authorization, so no remote signature can ever unlock it", n)
	}
}

// ---------------------------------------------------------------------------
// The funnel: one upstream per session
// ---------------------------------------------------------------------------

// TestSendInput_ReusesTheAttachedLeaseFunnel: with a controller attached, send_input writes
// through the lease's EXISTING stream and opens nothing of its own. This is the L3 pin the
// harness already records — the Server opens exactly ONE upstream SessionStream per session
// while a lease is held — and it is why A2 calls the write "daemon-mediated": the shim keeps
// exactly one input connection, through which every writer serializes.
func TestSendInput_ReusesTheAttachedLeaseFunnel(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := serveOwnerAPI(t, d)

	owner := rawDial(t, sock)
	orep := owner.hello(Version, []string{CapAttach})
	sid := orep.EndpointID + "/sess1"
	owner.writeControl(Control{Op: OpAttach, EndpointID: orep.EndpointID, SessionID: sid})
	if lease := nextControl(t, owner); lease.Op != OpLease {
		t.Fatalf("owner attach = op %q; want a lease grant", lease.Op)
	}

	agent := rawDial(t, sock)
	arep := agent.hello(Version, nil)
	agent.writeControl(Control{
		Op: OpSendInput, EndpointID: arep.EndpointID, SessionID: arep.EndpointID + "/sess1",
		SendInput: &SendInputReq{Text: "keep going", Submit: true},
	})
	if got := nextControl(t, agent); got.Op != OpOK {
		t.Fatalf("send_input alongside an attached controller = op %q error %q; want OpOK — the op must "+
			"NEVER take or supersede the lease (ADR-010 A1)", got.Op, got.Error)
	}

	if n := d.attachCount(); n != 1 {
		t.Fatalf("the daemon held %d upstream streams for one session; want 1 — send_input must reuse the "+
			"lease's funnel, not open a second connection to a single-consumer shim (L3)", n)
	}
	if got, want := string(concat(d.stream(0).written())), "keep going\r"; got != want {
		t.Errorf("the lease's stream received %q, want %q", got, want)
	}
}

// TestSendInput_UnattachedOpensAndReleasesOneStream: with no controller there is no lease
// stream to borrow, so the daemon opens ONE upstream for the message and CLOSES it when the
// message is done — no lease is created and no upstream is left pinned open.
func TestSendInput_UnattachedOpensAndReleasesOneStream(t *testing.T) {
	f := newSendFixture(t)

	if got := f.send(&SendInputReq{Key: "esc"}); got.Op != OpOK {
		t.Fatalf("send_input on an unattached session = op %q error %q; want OpOK", got.Op, got.Error)
	}

	st := f.d.onlyStream(t)
	if got := string(concat(st.written())); got != "\x1b" {
		t.Errorf("the session received %q, want %q", got, "\x1b")
	}
	if !st.waitClosed(recvTimeout) {
		t.Fatal("send_input left its upstream stream open; a one-shot write must release the stream it " +
			"opened, or every steering message pins another upstream for the session's lifetime")
	}
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// TestClient_SendInput pins the client method in the house style of Launch/Kill: the bytes
// reach the session, and a daemon refusal comes back as a Go error rather than a silent
// success.
func TestClient_SendInput(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := serveOwnerAPI(t, d)
	c := dialClient(t, sock, nil)
	sid := NamespacedID(c.EndpointID(), "sess1")

	if err := c.SendInput(sid, SendInputReq{Text: "go", Submit: true}); err != nil {
		t.Fatalf("Client.SendInput: %v", err)
	}
	if got, want := string(concat(d.onlyStream(t).written())), "go\r"; got != want {
		t.Errorf("the session received %q, want %q", got, want)
	}

	if err := c.SendInput(sid, SendInputReq{Text: "both", Key: "enter"}); err == nil {
		t.Error("Client.SendInput returned nil for a refused request; a daemon refusal must surface as an error")
	}
}

// ---------------------------------------------------------------------------
// Spec lockstep (GG-7)
// ---------------------------------------------------------------------------

// TestSendInput_SpecLockstep: the op never lands silently. protocol.md documents the op,
// its payload fields and its closed key vocabulary; system-invariants.md's S2 carries the
// sentence ADR-010 A2 records — the daemon may perform serialized one-shot message writes,
// and the shim still has exactly one input connection.
func TestSendInput_SpecLockstep(t *testing.T) {
	t.Run("protocol.md documents the op", func(t *testing.T) {
		doc := readRepoDoc(t, "docs", "specifications", "protocol.md")
		// Backticked, the form protocol.md already writes a closed vocabulary in (the
		// `spawn_intent` row: "one of `handoff` or `delegate`"), so a short name like
		// `up` cannot be satisfied by an unrelated word elsewhere in the document.
		for _, want := range []string{
			"send_input",                                              // the op and the Control payload key
			"`enter`", "`esc`", "`ctrl-c`", "`tab`", "`up`", "`down`", // the closed key vocabulary
		} {
			if !strings.Contains(doc, want) {
				t.Errorf("protocol.md does not document %q (GG-7: an op lands with its rows or not at all)", want)
			}
		}
	})

	t.Run("S2 carries the A2 amendment", func(t *testing.T) {
		doc := readRepoDoc(t, "docs", "invariants", "system-invariants.md")
		var s2 string
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, "**S2") {
				s2 = line
				break
			}
		}
		if s2 == "" {
			t.Fatal("system-invariants.md has no S2 line")
		}
		for _, want := range []string{"send_input", "one input connection"} {
			if !strings.Contains(s2, want) {
				t.Errorf("S2 does not state %q; ADR-010 A2 amends it: the daemon itself may perform "+
					"serialized one-shot message writes (send_input), and the shim still has exactly one "+
					"input connection (the daemon), through which all writes serialize.\nS2 today: %s", want, s2)
			}
		}
	})
}
