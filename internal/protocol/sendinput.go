package protocol

// The owner-tier `send_input` op (ADR-010 Amendment 1 A2): a one-shot, DAEMON-MEDIATED
// write of one message into a session.
//
// WHY NOT A LEASE. A1 recorded that every lease-based control session — take_control
// included — acquires the ordinary attach lease via Server.attach and therefore
// SUPERSEDES an attached human, the exact defect the "simple lease-steal" alternative was
// rejected for. So this handler never takes, bumps or touches the lease. It writes through
// the SAME funnel a lease write uses (the per-session input serialization of forwardInput,
// then SessionStream.Input), holding that serialization across both frames of one message
// so the whole message is atomic against concurrent OWNER-TIER lease input. An attached
// human sees the injected text appear before it submits — transparency by construction.
//
// THE SCOPE OF THAT ATOMICITY, honestly stated: the owner-tier Server and the remote-tier
// Server are DISTINCT values with distinct per-session leases (and so distinct inMu) over
// one shared tap, so a remote take_control keystroke CAN land between the text and its CR.
// Accepted for the personal single-owner model this daemon serves: a remote take-control
// means the human deliberately grabbed the session, and a human at the controls outranks an
// agent's steering message. The guarantee is against the OWNER tier's lease input, which is
// the interleaving an agent and its user actually produce together.
//
// FRAMING IS PASTE SEMANTICS (2026-08-07 revision). A text message is at most TWO writes:
// the text verbatim, then the CR. Embedded newlines are CONTENT — the CLI's paste heuristic
// renders them as a multi-line draft, which is what sending a multi-line message means — so
// they are not frame boundaries and they submit nothing. This is the phone lane's frozen
// Paste+Enter precedent (phonecore.Insert keeps a multi-line paste in one unit, PB-INPUT-6).
// The earlier per-run framing slept a gap per newline while holding the session's input
// serialization, which made the hold a function of the CALLER'S TEXT (2048 newlines is ~5
// minutes) and submitted text ending in a newline twice.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/submitframe"
)

// maxSendInputText bounds one steering message's text. Two precedents exist and only one
// fits: wire.MaxFrame (1 MiB) is the TRANSPORT bound, which would let a single steering
// message paste a megabyte into somebody's session, while phonecore.MaxInputPayload (4096)
// is the bound the INPUT path already imposes on one PTY write — and this text is framed
// for the PTY below. So the input bound is the one taken. The value is RESTATED rather
// than imported: internal/phonecore is a deliberately daemon-free leaf (PB-BIND-0) and
// internal/protocol must not pull it in, the same re-homing submitframe did for the
// framing rule itself.
const maxSendInputText = 4096

// keySequences is the CLOSED named-key vocabulary of ADR-010 A2, in ONE place: the daemon
// maps a name to bytes here and `swarm send` validates against KeySequence rather than
// restating the list (the submitframe precedent — a rule with two callers gets one copy).
// up/down are the NORMAL-mode cursor keys (CSI A / CSI B), the form A2 names.
var keySequences = map[string][]byte{
	"enter":  {'\r'},
	"esc":    {0x1b},
	"ctrl-c": {0x03},
	"tab":    {'\t'},
	"up":     {0x1b, '[', 'A'},
	"down":   {0x1b, '[', 'B'},
}

// KeySequence returns the bytes a terminal sends for a named key, and whether the name is
// in the vocabulary at all. The vocabulary is closed: an unknown name (or a differently
// cased or padded spelling of a known one) is refused, never guessed at.
func KeySequence(name string) ([]byte, bool) {
	seq, ok := keySequences[name]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), seq...), true
}

// handleSendInput serves the owner-tier send_input op. Gate order mirrors handleAttach:
// REFUSED OUTRIGHT on the remote tier — before any session is resolved and before any
// authorization is consulted, so no remote signature can ever unlock it — then the session
// is resolved, the payload validated, and the target checked for being able to receive
// input at all. Every refusal happens before the first byte is written and before any
// upstream stream is opened.
func (cc *clientConn) handleSendInput(c Control) {
	// Fail closed on the remote tier: the remote tier keeps its own full lane (signed
	// take_control, device auth, kill switch), so this owner convenience never appears
	// there (mirrors handleAttach).
	if cc.srv.remoteTier {
		cc.replyErrorCode("send_input is not permitted on the remote tier (remote control goes through take_control)", CodeNotAuthorized)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	frames, why := sendInputFrames(c.SendInput)
	if why != "" {
		cc.replyErrorCode("send_input: "+why, CodeInvalidField)
		return
	}
	if !cc.srv.sessionRunning(local) {
		cc.replyError("send_input: session " + strconv.Quote(local) + " is not running")
		return
	}
	if err := cc.srv.sendMessage(local, frames); err != nil {
		cc.replyError("send_input: " + err.Error())
		return
	}
	cc.replyOK(c.SessionID)
}

// sendInputFrames validates one request and cuts it into the exact PTY writes the message
// becomes, or returns why it is malformed (an empty reason means valid). EXACTLY ONE MODE
// per request. Text is ONE frame — it is within maxSendInputText, the bound the input path
// imposes on a single PTY write — and a Submit adds the CR as its OWN frame, so the byte
// that RUNS the message never shares a write with it (agents-tracker-r3p: Claude Code reads
// text+CR arriving in one write as an unsubmitted paste). A key is a single frame — it
// carries no text to keep apart from anything.
func sendInputFrames(req *SendInputReq) ([][]byte, string) {
	switch {
	case req == nil:
		return nil, "missing request"
	case req.Text != "" && req.Key != "":
		return nil, "carries both text and key; a message is exactly one of them"
	case req.Text == "" && req.Key == "":
		return nil, "requires either text or key"
	case req.Key != "":
		seq, ok := KeySequence(req.Key)
		if !ok {
			return nil, "unknown key " + strconv.Quote(req.Key)
		}
		return [][]byte{seq}, ""
	case len(req.Text) > maxSendInputText:
		return nil, "text is " + strconv.Itoa(len(req.Text)) + " bytes, over the " + strconv.Itoa(maxSendInputText) + "-byte bound"
	}

	frames := [][]byte{[]byte(req.Text)}
	if req.Submit {
		frames = append(frames, []byte{'\r'})
	}
	return frames, ""
}

// sessionRunning reports whether the session can receive input at all. An exited or lost
// session and an id no session has are the same case to the caller: there is nothing to
// steer, and the refusal must precede any write.
func (s *Server) sessionRunning(local string) bool {
	for _, m := range s.d.List() {
		if m.ID == local {
			return m.Status.Process == status.ProcessRunning
		}
	}
	return false
}

// sendMessage writes one message's frames to a session's shim, atomically against
// concurrent OWNER-TIER lease input (ADR-010 A2; the remote-tier caveat is below). It
// is the whole reason send_input is safe to add
// beside the attach lease, so the locking is spelled out:
//
// WHICH LOCKS, AND WHY. The message takes the session's attachMu and then its inMu — the
// order Server.attach documents and takes (attachMu -> inMu -> s.mu), so the two can never
// deadlock. inMu is the SAME per-session input serialization forwardInput takes, and
// holding it for the whole message is what keeps THIS Server's controller keystrokes from
// landing BETWEEN the text and the CR that submits it (which would submit whatever the
// human typed in the interval, or trail the human's line into the agent's message).
// attachMu excludes a concurrent attach for the same span, so the "one input connection to
// the shim" property survives the unattached case below, where this message opens an
// upstream of its own. A REMOTE take_control controller is a different Server value with
// its own leases and its own inMu, so it can still interleave — accepted and documented at
// the top of this file: a remote take-control is a human deliberately grabbing the session.
//
// WHICH STREAM. With a controller attached, the message rides the lease's EXISTING upstream
// and closes nothing: the Server keeps exactly one upstream SessionStream per session (L3),
// and the shim serves one input connection. With no controller there is no stream to
// borrow, so one is opened for this message and released when it is done — a steering
// message must never pin another upstream for the session's lifetime.
//
// THE SLEEP, AND ITS BOUND. Separate frames are necessary but not sufficient: the paste
// heuristic keys on co-arrival in one read tick at the PTY, so any hop whose batching
// recompresses two separately-emitted frames recreates the mixed write. submitframe.Gap
// must therefore elapse between the text frame and the CR that follows it, and the daemon
// is the last hop that can guarantee it. Sleeping here means holding attachMu and inMu
// across the sleep. That is the accepted, documented cost of A2, and it is bounded ONCE PER
// MESSAGE: a message is at most two frames, so the hold is at most Gap (150 ms) plus two
// writes, whatever the text contains. Nothing is waited on but the clock — no I/O, no other
// lock, no client. What it delays is exactly what must be delayed (this Server's lease input
// for this session) plus an attach on this session; s.mu, every other session and every
// other connection are untouched. The alternative is worse: the shim must NEVER sleep,
// because its ptyWriter lock is shared with the VT emulator's DSR/CPR reply pump, so a
// sleeping shim stalls the emulator's replies.
//
// A FAILURE INSIDE THE GAP has its own error. The window between the two writes is real, and
// a session's input stream can close inside it; when it does the text is on the screen and
// only the submit is missing. That is recoverable — peek, then send --key enter — but only
// if the caller is told, so it is reported distinctly from a message that wrote nothing.
func (s *Server) sendMessage(local string, frames [][]byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("server closed")
	}
	ls := s.leases[local]
	if ls == nil {
		ls = &sessionLease{}
		s.leases[local] = ls
	}
	s.mu.Unlock()

	ls.attachMu.Lock()
	defer ls.attachMu.Unlock()
	ls.inMu.Lock()
	defer ls.inMu.Unlock()

	s.mu.Lock()
	stream := ls.stream
	s.mu.Unlock()
	if stream == nil {
		fresh, err := s.d.Attach(local)
		if err != nil {
			return err
		}
		// Released before inMu/attachMu are (defers run last-in-first-out), so the next
		// attach still finds the shim's single input connection free.
		defer func() { _ = fresh.Close() }()
		stream = fresh
	}

	var wrote time.Time // when this message's PRECEDING frame was written
	for i, f := range frames {
		// The gap separates a message's text from the CR that submits it, and a message has
		// at most those two frames. It is owed because a TEXT frame precedes the CR, not
		// because of what that text contains — text and submit co-arriving in one PTY read
		// tick is the hazard whatever the text is. A key message (enter included, though it
		// is submit-only) is frame 0 with nothing before it, so it is written at once.
		if i > 0 {
			if wait := submitframe.Gap - time.Since(wrote); wait > 0 {
				time.Sleep(wait)
			}
		}
		if err := stream.Input(f); err != nil {
			if i > 0 {
				return fmt.Errorf("text delivered, submit not sent — the session's input stream closed "+
					"mid-message; peek and send --key enter to recover: %w", err)
			}
			return err
		}
		wrote = time.Now()
	}
	return nil
}
