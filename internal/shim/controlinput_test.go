package shim

// FAILING-FIRST (TDD RED) for W2.1 of the phone refit
// (docs/specifications/phone-refit-playbook.md §3, bead agents-tracker-d45a.2): daemon-authored
// control keys carry their provenance.
//
// THE DEFECT. The phone's Stop (ESC) and its Allow/Deny answers ("1"/"3") reach the shim as
// wire.TDataIn, the frame for bytes somebody TYPED, so ptyWriter.WriteInput counts them as a
// dirty input line and every later submit is refused input_busy until someone presses Enter at
// the machine. The shim may not judge what a byte does to the input line (ptyWriter's own
// comment; internal/skeleton/chat.go, THE QUESTION THAT IS ANSWERABLE): an owner's Escape at
// the terminal is the identical byte. What differs is WHO SENT THE FRAME, so provenance rides
// the frame: shimwire.TypeControlInput carries daemon-authored keys and the shim writes them
// through the owner-tracker-bypassing path. A typed draft still refuses byte-for-byte.

import (
	"bytes"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// controlInputRig is a bare server whose PTY is the write end of a pipe and whose one client
// connection is an in-memory pipe, so every byte the shim writes "to the PTY" is read back
// exactly, with no agent process and no socket in the way.
type controlInputRig struct {
	t     *testing.T
	conn  net.Conn // the daemon's end of the connection
	pty   *os.File // the read end of the PTY
	hello shimwire.Control
}

func newControlInputRig(t *testing.T) *controlInputRig {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pty pipe: %v", err)
	}
	s := &server{ptyIn: &ptyWriter{f: pw}, conns: map[net.Conn]struct{}{}}
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

func (r *controlInputRig) write(c shimwire.Control) {
	r.t.Helper()
	b, err := shimwire.Encode(c)
	if err != nil {
		r.t.Fatalf("encode %+v: %v", c, err)
	}
	if err := wire.WriteFrame(r.conn, wire.TControl, b); err != nil {
		r.t.Fatalf("write %+v: %v", c, err)
	}
}

func (r *controlInputRig) readControl() shimwire.Control {
	r.t.Helper()
	_ = r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, payload, err := wire.ReadFrame(r.conn)
	if err != nil {
		r.t.Fatalf("read control frame: %v", err)
	}
	if typ != wire.TControl {
		r.t.Fatalf("read frame type %d, want control", typ)
	}
	c, err := shimwire.Decode(payload)
	if err != nil {
		r.t.Fatalf("decode control frame: %v", err)
	}
	return c
}

// control sends daemon-authored keys on the provenance-carrying frame.
func (r *controlInputRig) control(keys string) {
	r.t.Helper()
	r.write(shimwire.Control{Type: shimwire.TypeControlInput, Keys: keys})
}

// typed sends bytes the way an owner's keystrokes arrive.
func (r *controlInputRig) typed(b string) {
	r.t.Helper()
	if err := wire.WriteFrame(r.conn, wire.TDataIn, []byte(b)); err != nil {
		r.t.Fatalf("write typed bytes: %v", err)
	}
}

// submit runs one submit transaction and returns the shim's refusal token ("" on success).
func (r *controlInputRig) submit(text string) string {
	r.t.Helper()
	r.write(shimwire.Control{Type: shimwire.TypeSubmit, Text: text})
	res := r.readControl()
	if res.Type != shimwire.TypeSubmitResult {
		r.t.Fatalf("submit answered %+v, want a submit_result", res)
	}
	return res.Refused
}

// ptyBytes reads exactly len(want) bytes off the PTY and then proves nothing else follows
// within a short quiet window, so the assertion is byte-EXACT in both directions.
func (r *controlInputRig) ptyBytes(want string) {
	r.t.Helper()
	got := make([]byte, len(want))
	_ = r.pty.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := io.ReadFull(r.pty, got); err != nil {
		r.t.Fatalf("the PTY received %q (%d bytes) and then nothing: %v; want exactly %q", got[:n], n, err, want)
	}
	if !bytes.Equal(got, []byte(want)) {
		r.t.Fatalf("the PTY received %q, want %q", got, want)
	}
	_ = r.pty.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	extra := make([]byte, 64)
	if n, _ := r.pty.Read(extra); n > 0 {
		r.t.Fatalf("the PTY received %q after %q; want nothing more", extra[:n], want)
	}
}

// TestHello_AdvertisesControlInput: the capability is negotiated at hello, exactly as
// SubmitTransaction is, so a daemon facing an old shim degrades knowingly.
func TestHello_AdvertisesControlInput(t *testing.T) {
	r := newControlInputRig(t)
	if !r.hello.ControlInput {
		t.Fatalf("hello reply %+v does not advertise control_input", r.hello)
	}
	if !r.hello.Caps().ControlInput {
		t.Fatalf("Caps() of %+v drops ControlInput", r.hello)
	}
}

// TestControlInput_ReachesThePTYByteExact: the frame's keys land on the PTY verbatim -- an
// interrupt and a dialog answer, in order, and nothing else.
func TestControlInput_ReachesThePTYByteExact(t *testing.T) {
	r := newControlInputRig(t)
	r.control("\x1b")
	r.control("3")
	r.ptyBytes("\x1b3")
}

// TestSubmitMessage_AfterInterruptKeys_IsAccepted is phone Stop then phone send: the ESC
// reaches the PTY and the message is still delivered, because a daemon-authored key is not
// somebody typing.
func TestSubmitMessage_AfterInterruptKeys_IsAccepted(t *testing.T) {
	r := newControlInputRig(t)
	r.control("\x1b")
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after a daemon-authored interrupt was refused %q; want delivered. "+
			"The phone's Stop counted as typing, and every later send is input_busy until "+
			"someone presses Enter at the machine", refused)
	}
	r.ptyBytes("\x1bship it\r")
}

// TestSubmitMessage_AfterApprovalKeys_IsAccepted is the same for a dialog answer.
func TestSubmitMessage_AfterApprovalKeys_IsAccepted(t *testing.T) {
	r := newControlInputRig(t)
	r.control("1")
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after a daemon-authored dialog answer was refused %q; want delivered", refused)
	}
	r.ptyBytes("1ship it\r")
}

// TestSubmitMessage_AfterTypedText_IsRefused is the negative control: an owner's draft, sent
// the way keystrokes arrive, still refuses the submit and survives byte-for-byte.
func TestSubmitMessage_AfterTypedText_IsRefused(t *testing.T) {
	r := newControlInputRig(t)
	r.typed("half a thought")
	if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
		t.Fatalf("submit over an owner's draft answered %q, want %q", refused, shimwire.RefusedInputBusy)
	}
	r.ptyBytes("half a thought")
}

// TestSubmitMessage_AfterOwnerDeletesTheWholeDraft_IsAccepted is the physical-handset
// regression behind v0.13.11's permanently "sending" bubbles. The old tracker counted bytes
// written since Enter; it did not track what the editor did with them. Typing and then deleting
// a draft therefore left the old dirty-byte count non-zero even though the logical line was empty,
// and every later phone message was refused input_busy until the owner pressed Enter.
func TestSubmitMessage_AfterOwnerDeletesTheWholeDraft_IsAccepted(t *testing.T) {
	t.Run("ASCII", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("draft")
		r.typed("\x7f\x7f\x7f\x7f\x7f")
		if refused := r.submit("ship it"); refused != "" {
			t.Fatalf("submit after owner deleted the whole draft was refused %q, want delivered", refused)
		}
		r.ptyBytes("draft\x7f\x7f\x7f\x7f\x7fship it\r")
	})

	t.Run("UTF-8 runes", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("é半")
		r.typed("\x7f\x7f")
		if refused := r.submit("ship it"); refused != "" {
			t.Fatalf("submit after two runes and two deletes was refused %q, want delivered", refused)
		}
		r.ptyBytes("é半\x7f\x7fship it\r")
	})
}

// TestSubmitMessage_TracksCursorEditingToAnEmptyLine pins the minimum editor model rather
// than a backspace counter. The owner inserts two runes, moves left, deletes the rune under
// the cursor, then deletes the rune before it. The line is empty although the PTY received
// more bytes than it did in the simple type/delete case.
func TestSubmitMessage_TracksCursorEditingToAnEmptyLine(t *testing.T) {
	r := newControlInputRig(t)
	r.typed("ab")
	r.typed("\x1b[D")  // left: cursor between a and b
	r.typed("\x1b[3~") // delete: remove b
	r.typed("\x7f")    // backspace: remove a
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after cursor edit emptied the line was refused %q, want delivered", refused)
	}
	r.ptyBytes("ab\x1b[D\x1b[3~\x7fship it\r")
}

// TestSubmitMessage_CompleteNavigationOnAnEmptyLineDoesNotInventADraft covers complete,
// provider-independent horizontal/home/end sequences. A lone Escape is deliberately excluded:
// at the submit boundary it is indistinguishable from the prefix of a split terminal sequence.
func TestSubmitMessage_CompleteNavigationOnAnEmptyLineDoesNotInventADraft(t *testing.T) {
	r := newControlInputRig(t)
	r.typed("\x1b[D\x1b[C\x1b[H\x1b[F")
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after empty-line navigation was refused %q, want delivered", refused)
	}
	r.ptyBytes("\x1b[D\x1b[C\x1b[H\x1b[Fship it\r")
}

// TestSubmitMessage_LineKillAndSubmitRestoreCleanliness pins the two ordinary ways a known
// draft stops being a draft: the editor deletes it, or the owner submits it. Both transitions
// must be understood without weakening the real-draft refusal above.
func TestSubmitMessage_LineKillAndSubmitRestoreCleanliness(t *testing.T) {
	t.Run("ctrl-u at end", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("throw this away\x15")
		if refused := r.submit("ship it"); refused != "" {
			t.Fatalf("submit after Ctrl-U cleared the draft was refused %q, want delivered", refused)
		}
		r.ptyBytes("throw this away\x15ship it\r")
	})

	t.Run("owner enter", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("owner line\r")
		if refused := r.submit("ship it"); refused != "" {
			t.Fatalf("submit after owner submitted their line was refused %q, want delivered", refused)
		}
		r.ptyBytes("owner line\rship it\r")
	})
}

// TestSubmitMessage_UnknownHistoryRecallRemainsBusy is the fail-safe control. Up/down history
// may populate Claude's composer with text the shim cannot observe. The tracker must not turn
// "we do not know" into "empty" merely to avoid false positives.
func TestSubmitMessage_UnknownHistoryRecallRemainsBusy(t *testing.T) {
	r := newControlInputRig(t)
	r.typed("\x1b[A")
	if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
		t.Fatalf("submit after history recall answered %q, want conservative %q", refused, shimwire.RefusedInputBusy)
	}
	r.ptyBytes("\x1b[A")
}

// TestSubmitMessage_OwnerEnterClearsUnknownHistoryRecall proves that the conservative
// history state is not itself a permanent latch. The tracker cannot know what Up recalled,
// so it refuses while that editor state remains; once the owner submits the recalled line,
// Enter is an unambiguous clean boundary and later phone input may proceed.
func TestSubmitMessage_OwnerEnterClearsUnknownHistoryRecall(t *testing.T) {
	r := newControlInputRig(t)
	r.typed("\x1b[A\r")
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after owner ran recalled history was refused %q, want delivered", refused)
	}
	r.ptyBytes("\x1b[A\rship it\r")
}

// TestSubmitMessage_IncompleteTerminalSequenceRemainsBusy protects the atomic boundary at
// frame splits. Escape can be a complete lifecycle key, but at this seam a lone ESC is also
// indistinguishable from the prefix of a split sequence; ESC-[ is unambiguously incomplete.
// Inserting phone text after either prefix could turn it into key parameters, not a prompt.
func TestSubmitMessage_IncompleteTerminalSequenceRemainsBusy(t *testing.T) {
	t.Run("lone escape", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("\x1b")
		if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
			t.Fatalf("submit after a lone Escape answered %q, want %q", refused, shimwire.RefusedInputBusy)
		}
		r.ptyBytes("\x1b")
	})

	t.Run("split CSI", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("\x1b[")
		if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
			t.Fatalf("submit inside a split CSI sequence answered %q, want %q", refused, shimwire.RefusedInputBusy)
		}
		r.ptyBytes("\x1b[")
	})
}

// TestSubmitMessage_ProviderDependentWordEditingRemainsBusy protects punctuation-sensitive
// editor semantics. The shim cannot assume Claude's word boundaries match unicode-space
// boundaries: for "foo-bar", Alt-B may stop after the hyphen, so Ctrl-K can leave "foo-" even
// though a whitespace-only model incorrectly deletes the whole line and declares it clean.
func TestSubmitMessage_ProviderDependentWordEditingRemainsBusy(t *testing.T) {
	t.Run("punctuation Alt-B then Ctrl-K", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("foo-bar\x1bb\x0b")
		if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
			t.Fatalf("submit after provider-dependent Alt-B/Ctrl-K answered %q, want %q", refused, shimwire.RefusedInputBusy)
		}
		r.ptyBytes("foo-bar\x1bb\x0b")
	})

	for name, keys := range map[string]string{
		"Ctrl-W":          "\x17",
		"Alt-B":           "\x1bb",
		"Alt-F":           "\x1bf",
		"Alt-Backspace":   "\x1b\x7f",
		"Alt-Left CSI":    "\x1b[1;3D",
		"Ctrl-Left CSI":   "\x1b[1;5D",
		"Ctrl-Delete CSI": "\x1b[3;5~",
	} {
		t.Run(name+" on empty line", func(t *testing.T) {
			r := newControlInputRig(t)
			r.typed(keys)
			if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
				t.Fatalf("submit after uncharacterized %s answered %q, want %q", name, refused, shimwire.RefusedInputBusy)
			}
			r.ptyBytes(keys)
		})
	}
}

// TestSubmitMessage_BracketedPasteIsContentAndItsMarkersAreNot ensures pasted newlines do
// not masquerade as the Enter key that runs a line. An empty completed paste leaves the line
// empty; a paste carrying text (including LF) remains a real draft and refuses atomically.
func TestSubmitMessage_BracketedPasteIsContentAndItsMarkersAreNot(t *testing.T) {
	t.Run("empty paste", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("\x1b[200~\x1b[201~")
		if refused := r.submit("ship it"); refused != "" {
			t.Fatalf("submit after empty bracketed paste was refused %q, want delivered", refused)
		}
		r.ptyBytes("\x1b[200~\x1b[201~ship it\r")
	})

	t.Run("multiline content", func(t *testing.T) {
		r := newControlInputRig(t)
		r.typed("\x1b[200~first\nsecond\x1b[201~")
		if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
			t.Fatalf("submit over multiline pasted draft answered %q, want %q", refused, shimwire.RefusedInputBusy)
		}
		r.ptyBytes("\x1b[200~first\nsecond\x1b[201~")
	})
}
