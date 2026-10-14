package protocol

// FAILING-FIRST for the send-submit defect (ADR-010 Amendment 9): a send_input's TEXT frame
// rides the shim's paste op, which wraps it in bracketed-paste markers when the agent enabled
// DECSET 2004, so a CLI that assembles pasted keys one by one (codex) knows where the message
// ends before the CR arrives. Only the shim knows the mode, so the daemon hands it the text as
// text; the CR stays its own frame after submitframe.Gap, exactly as before. A stream that
// cannot paste (an old shim mid-upgrade, a test double) gets today's raw write: a disclosed
// degrade, never a refusal.

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/submitframe"
	"github.com/Nathandela/swarm/internal/wire"
)

// pastingStream is a timedStream that also accepts the paste op, recording which writes
// arrived through it.
type pastingStream struct {
	*timedStream
	unsupported bool

	pmu    sync.Mutex
	pasted map[int]bool // index into timedStream.writes
}

func (s *pastingStream) PasteInput(text []byte) error {
	if s.unsupported {
		return ErrPasteInputUnsupported
	}
	s.mu.Lock()
	idx := len(s.writes)
	s.writes = append(s.writes, inputWrite{payload: append([]byte(nil), text...), at: time.Now()})
	s.mu.Unlock()
	s.pmu.Lock()
	s.pasted[idx] = true
	s.pmu.Unlock()
	return nil
}

func (s *pastingStream) wasPasted(i int) bool {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return s.pasted[i]
}

type pastingDaemon struct {
	*stubDaemon
	unsupported bool

	mu     sync.Mutex
	opened []*pastingStream
}

func (d *pastingDaemon) Attach(id string) (SessionStream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := &pastingStream{timedStream: newTimedStream(), unsupported: d.unsupported, pasted: map[int]bool{}}
	d.opened = append(d.opened, st)
	return st, nil
}

func (d *pastingDaemon) only(t *testing.T) *pastingStream {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.opened) != 1 {
		t.Fatalf("daemon opened %d upstream streams, want exactly 1", len(d.opened))
	}
	return d.opened[0]
}

func newPasteFixture(t *testing.T, unsupported bool) (sendFixture, *pastingDaemon) {
	t.Helper()
	d := &pastingDaemon{stubDaemon: newStubDaemon(), unsupported: unsupported}
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	rc := rawDial(t, serveOwnerAPI(t, d))
	rep := rc.hello(Version, nil)
	return sendFixture{t: t, rc: rc, ep: rep.EndpointID, sid: rep.EndpointID + "/sess1"}, d
}

func TestSendInput_TextRidesThePasteOp_SubmitStaysAFrameAfterTheGap(t *testing.T) {
	f, d := newPasteFixture(t, false)
	text := "a long steering message\nwith a second line"
	if rep := f.send(&SendInputReq{Text: text, Submit: true}); rep.Error != "" {
		t.Fatalf("send_input refused: %q", rep.Error)
	}
	st := d.only(t)
	ws := st.written()
	assertPasteThenSubmit(t, ws, text)
	if !st.wasPasted(0) {
		t.Errorf("the text frame was a raw Input write; it must ride the paste op so the shim can " +
			"bracket it when the agent enabled bracketed paste")
	}
	if st.wasPasted(1) {
		t.Errorf("the submit rode the paste op; a CR inside a paste is content, not a submit")
	}
}

func TestSendInput_TextWithoutSubmitIsOnePaste(t *testing.T) {
	f, d := newPasteFixture(t, false)
	if rep := f.send(&SendInputReq{Text: "draft only"}); rep.Error != "" {
		t.Fatalf("send_input refused: %q", rep.Error)
	}
	st := d.only(t)
	ws := st.written()
	if len(ws) != 1 || string(ws[0].payload) != "draft only" || !st.wasPasted(0) {
		t.Fatalf("writes = %q (pasted=%v), want exactly one pasted %q", concat(ws), st.wasPasted(0), "draft only")
	}
}

// A key is not text: it is never pasted, or the CLI would insert "\r" or ESC as content.
func TestSendInput_KeyIsNeverPasted(t *testing.T) {
	f, d := newPasteFixture(t, false)
	if rep := f.send(&SendInputReq{Key: "enter"}); rep.Error != "" {
		t.Fatalf("send_input refused: %q", rep.Error)
	}
	st := d.only(t)
	if ws := st.written(); len(ws) != 1 || string(ws[0].payload) != "\r" || st.wasPasted(0) {
		t.Fatalf("writes = %q (pasted=%v), want one raw CR", concat(ws), st.wasPasted(0))
	}
}

// An old shim proves no paste op: the text goes as today's raw write and the message still
// submits. The degrade costs the codex fix, not the message.
func TestSendInput_PasteUnsupported_DegradesToRawInput(t *testing.T) {
	f, d := newPasteFixture(t, true)
	if rep := f.send(&SendInputReq{Text: "ship it", Submit: true}); rep.Error != "" {
		t.Fatalf("send_input refused: %q", rep.Error)
	}
	st := d.only(t)
	assertPasteThenSubmit(t, st.written(), "ship it")
	if st.wasPasted(0) {
		t.Fatal("an unsupported paste op was recorded as pasted")
	}
}

func TestErrPasteInputUnsupported_IsDistinct(t *testing.T) {
	if errors.Is(ErrPasteInputUnsupported, ErrControlInputUnsupported) || errors.Is(ErrPasteInputUnsupported, ErrSubmitUnsupported) {
		t.Fatal("the paste degrade must be its own sentinel")
	}
	_ = submitframe.Gap
}

// The daemon's real shim client sends the paste op only to a shim that advertised it, and
// answers the degrade sentinel otherwise -- the ControlInput precedent.
func TestShimStream_PasteInput_FollowsTheCapability(t *testing.T) {
	cl, sv := net.Pipe()
	defer func() { _ = cl.Close(); _ = sv.Close() }()

	old := &shimStream{conn: cl, caps: shimwire.Caps{}}
	if err := old.PasteInput([]byte("x")); !errors.Is(err, ErrPasteInputUnsupported) {
		t.Fatalf("PasteInput on a shim without the capability = %v, want ErrPasteInputUnsupported", err)
	}

	st := &shimStream{conn: cl, caps: shimwire.Caps{PasteInput: true}}
	got := make(chan shimwire.Control, 1)
	go func() {
		typ, payload, err := wire.ReadFrame(sv)
		if err != nil || typ != wire.TControl {
			close(got)
			return
		}
		c, _ := shimwire.Decode(payload)
		got <- c
	}()
	if err := st.PasteInput([]byte("ship it\nnow")); err != nil {
		t.Fatalf("PasteInput: %v", err)
	}
	c, ok := <-got
	if !ok || c.Type != shimwire.TypePasteInput || c.Text != "ship it\nnow" {
		t.Fatalf("the shim received %+v, want a paste_input carrying the text verbatim", c)
	}
}
