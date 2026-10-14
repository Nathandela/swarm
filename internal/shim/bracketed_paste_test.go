package shim

// FAILING-FIRST for the send-submit defect (ADR-010 Amendment 9).
//
// THE DEFECT, measured against codex 0.162.1: `swarm send --text ... ` writes the text, waits
// submitframe.Gap, then writes CR. codex reads its input key by key and treats a burst of
// characters as a paste it is still assembling; a CR that arrives while it is still assembling
// is inserted as a NEWLINE instead of submitting. 15 characters submitted, 300 did not, 900 did
// not even at a 500 ms gap. The same 900 and 3000 characters submitted at the ordinary 150 ms
// gap once they arrived inside bracketed-paste markers: the markers tell the CLI where the
// paste ends, so it has nothing left to guess when the CR arrives.
//
// THE MODE DECIDES. Markers are only meaningful to an agent that enabled DECSET 2004; any
// other program would print them. The shim's emulator parses every byte the agent writes, so
// the shim asks it, and wraps exactly when the agent asked for bracketed paste.

import (
	"net"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/vt"
)

// newPasteRig is newControlInputRig with the agent's bracketed-paste mode under the test's
// control, standing in for the emulator that tracks it in production.
func newPasteRig(t *testing.T, mode *atomic.Bool) *controlInputRig {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pty pipe: %v", err)
	}
	s := &server{ptyIn: &ptyWriter{f: pw, bracketedPaste: mode.Load}, conns: map[net.Conn]struct{}{}}
	cl, sv := net.Pipe()
	go s.serveConn(sv)
	t.Cleanup(func() { _ = cl.Close(); _ = pr.Close(); _ = pw.Close() })

	r := &controlInputRig{t: t, conn: cl, pty: pr}
	r.write(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	r.hello = r.readControl()
	if r.hello.Type != shimwire.TypeHello {
		t.Fatalf("hello reply = %+v, want a hello", r.hello)
	}
	return r
}

func pasteModeOn() *atomic.Bool {
	var b atomic.Bool
	b.Store(true)
	return &b
}

func (r *controlInputRig) paste(text string) {
	r.t.Helper()
	r.write(shimwire.Control{Type: shimwire.TypePasteInput, Text: text})
}

func TestHello_AdvertisesPasteInput(t *testing.T) {
	r := newControlInputRig(t)
	if !r.hello.PasteInput {
		t.Fatalf("hello reply %+v does not advertise paste_input", r.hello)
	}
	if !r.hello.Caps().PasteInput {
		t.Fatalf("Caps() of %+v drops PasteInput", r.hello)
	}
}

// The composer's submit transaction: the text travels inside the markers and the CR that runs
// it travels OUTSIDE them, after the gap -- a CR inside the paste would be content.
func TestSubmitMessage_BracketedPasteOn_WrapsTheTextNotTheReturn(t *testing.T) {
	r := newPasteRig(t, pasteModeOn())
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit refused %q; want delivered", refused)
	}
	r.ptyBytes("\x1b[200~ship it\x1b[201~\r")
}

func TestSubmitMessage_BracketedPasteOff_IsVerbatim(t *testing.T) {
	r := newPasteRig(t, new(atomic.Bool))
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit refused %q; want delivered", refused)
	}
	r.ptyBytes("ship it\r")
}

// A multi-line message is one paste: its newlines are content, its submit is the trailing CR.
func TestSubmitMessage_BracketedPasteOn_MultiLineIsOnePaste(t *testing.T) {
	r := newPasteRig(t, pasteModeOn())
	if refused := r.submit("one\ntwo"); refused != "" {
		t.Fatalf("submit refused %q; want delivered", refused)
	}
	r.ptyBytes("\x1b[200~one\ntwo\x1b[201~\r")
}

// `swarm send --text` rides the paste op: the text, wrapped when the mode is on, and no CR --
// the daemon sends the submit as its own frame after the gap.
func TestPasteInput_BracketedPasteOn_Wraps(t *testing.T) {
	r := newPasteRig(t, pasteModeOn())
	r.paste("ship it")
	r.ptyBytes("\x1b[200~ship it\x1b[201~")
}

func TestPasteInput_BracketedPasteOff_IsVerbatim(t *testing.T) {
	r := newPasteRig(t, new(atomic.Bool))
	r.paste("ship it")
	r.ptyBytes("ship it")
}

// Text that carries a paste marker of its own must not end the paste early: the bytes after
// an embedded end marker would otherwise be read as keys, the very thing the markers exist to
// prevent. Terminals strip the markers from pasted content for the same reason.
func TestPasteInput_StripsEmbeddedMarkers(t *testing.T) {
	r := newPasteRig(t, pasteModeOn())
	r.paste("a\x1b[201~b\x1b[200~c")
	r.ptyBytes("\x1b[200~abc\x1b[201~")
}

// The pasted text is a draft the owner can see: a later submit transaction must still refuse
// rather than run its own text together with it.
func TestPasteInput_LeavesADraftThatRefusesSubmit(t *testing.T) {
	r := newPasteRig(t, pasteModeOn())
	r.paste("half a thought")
	r.ptyBytes("\x1b[200~half a thought\x1b[201~")
	if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
		t.Fatalf("submit over a pasted draft answered %q, want %q", refused, shimwire.RefusedInputBusy)
	}
}

// The paste op never runs before hello, like every other control frame.
func TestPasteInput_IgnoredBeforeHello(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pty pipe: %v", err)
	}
	s := &server{ptyIn: &ptyWriter{f: pw}, conns: map[net.Conn]struct{}{}}
	cl, sv := net.Pipe()
	go s.serveConn(sv)
	t.Cleanup(func() { _ = cl.Close(); _ = pr.Close(); _ = pw.Close() })
	r := &controlInputRig{t: t, conn: cl, pty: pr}
	r.paste("early")
	r.write(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	r.hello = r.readControl()
	r.paste("late")
	r.ptyBytes("late")
}

// The production server reads the mode from the emulator the agent's output is parsed into,
// so the decision follows whatever the agent last asked for.
func TestNewServer_PasteModeFollowsTheEmulator(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	defer func() { _ = emu.Close() }()
	s := newServer(nil, "", emu, nil, nil, 0, 0, nil)
	if s.ptyIn.pasteOn() {
		t.Fatal("the writer reports bracketed paste on before the agent enabled it")
	}
	emu.Feed([]byte("\x1b[?2004h"))
	if !s.ptyIn.pasteOn() {
		t.Fatal("the agent enabled bracketed paste and the writer does not see it")
	}
	emu.Feed([]byte("\x1b[?2004l"))
	if s.ptyIn.pasteOn() {
		t.Fatal("the agent disabled bracketed paste and the writer still sees it on")
	}
}
