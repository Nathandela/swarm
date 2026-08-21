package remotegw

// WAVE R8 / CLOSING ROUND -- TerminalViewV1 ON THE WIRE (closing review, finding 5).
//
// THE FINDING. `schema.TerminalViewV1` -- the wave's named deliverable -- was declared,
// documented, bounded and produced, and NEVER SENT. `RenderTerminalView` had exactly one
// caller, `RenderTerminal`, which DISCARDED ViewEpoch, Revision and Reset and passed
// instance "". No producer and no consumer of `view_epoch` existed on any wire path.
//
// WHY IT IS NOT COSMETIC, and this is the whole reason the closing round builds it rather
// than parking it with the control half. A phone watching a session that is REPLACED under
// the SAME id sees the new incarnation as a SEAMLESS CONTINUATION: the render loop restarts
// with a fresh emulator, the phone's latest-wins cache overwrites, and nothing on the wire
// says "this is a different screen". That is precisely what T4-a's epoch and T8-a's instance
// exist to prevent, and it is a correctness property of the READ half -- the half that ships.
//
// THE TEST DRIVES THE REAL ASSEMBLED PATH, because this wave already shipped a claim measured
// at a stub: a real `protocol.ServeRemote` over a real socket, a real `remotegw.Gateway`
// running `RunTerminal`, and the fields read off what the SINK received. Nothing here calls
// the daemon renderer directly.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/vt"
)

// r8r4Grid encodes one real vt snapshot carrying text, so the peek renders something the
// assertions can find. It goes through the REAL emulator: a hand-written Snap would let the
// test pass over a path the product's sanitizer never touched.
func r8r4Grid(text string) []byte {
	emu := vt.NewEmulator(80, 24)
	defer func() { _ = emu.Close() }()
	emu.Feed([]byte(text))
	b, err := emu.Snapshot()
	if err != nil {
		panic(err)
	}
	return b
}

// TerminalTap is the read-only tap the terminal_subscribe handler opens. The backend hands
// out whichever stream the test last installed for that session, which is how a REPLACEMENT
// is expressed: same id, different incarnation, different screen.
func (b *r8Backend) TerminalTap(local string) (protocol.SessionStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tapped = true
	s, ok := b.streams[local]
	if !ok {
		return nil, errTerminalReadOnly{}
	}
	return s, nil
}

func (b *r8Backend) setStream(local string, s *r8r4Stream) {
	b.mu.Lock()
	if b.streams == nil {
		b.streams = map[string]*r8r4Stream{}
	}
	b.streams[local] = s
	b.mu.Unlock()
}

// r8r4Stream is one session's read-only tap: an initial grid plus a live frame channel. It
// is the structural subset `protocol.SessionStream` requires; Input and Resize are refused
// because this is the READ path and nothing on it may write.
type r8r4Stream struct {
	snap   []byte
	frames chan []byte
	once   sync.Once
}

func (s *r8r4Stream) Snapshot() []byte      { return s.snap }
func (s *r8r4Stream) Frames() <-chan []byte { return s.frames }
func (s *r8r4Stream) Input([]byte) error    { return errNoInputOnAReadPath }
func (s *r8r4Stream) Resize(_, _ int) error { return errNoInputOnAReadPath }
func (s *r8r4Stream) Close() error          { s.once.Do(func() { close(s.frames) }); return nil }

var errNoInputOnAReadPath = errTerminalReadOnly{}

type errTerminalReadOnly struct{}

func (errTerminalReadOnly) Error() string { return "remotegw: the terminal tap is read-only" }

// r8r4Sink records every view the gateway forwarded, in order.
type r8r4Sink struct {
	mu    sync.Mutex
	views []protocol.TerminalViewV1
}

func (s *r8r4Sink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (s *r8r4Sink) Event(protocol.JournalRecord) error              { return nil }

func (s *r8r4Sink) Terminal(v protocol.TerminalViewV1) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.views = append(s.views, v)
	return nil
}

func (s *r8r4Sink) all() []protocol.TerminalViewV1 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.TerminalViewV1(nil), s.views...)
}

// firstNonEmpty is the first view carrying rows. The blank frame the daemon sends when a peek
// ends is a legitimate view with no lines, and it is not what these assertions are about.
func (s *r8r4Sink) firstNonEmpty() (protocol.TerminalViewV1, bool) {
	for _, v := range s.all() {
		if len(v.Lines) > 0 {
			return v, true
		}
	}
	return protocol.TerminalViewV1{}, false
}

// runR8R4Watch drives ONE real peek to its first non-empty view and returns it. The peek's
// context is cancelled and its goroutine joined in t.Cleanup, so nothing this rig starts
// outlives the test (standing constraint 9).
func runR8R4Watch(t *testing.T, sock string, sink *r8r4Sink, session string) {
	t.Helper()
	gw := New(sock, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.RunTerminal(ctx, session) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("the peek goroutine did not exit")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := sink.firstNonEmpty(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no terminal view reached the sink within 5s (got %d frames)", len(sink.all()))
}

// TestR8R4_TheVersionedViewReachesThePhoneOverTheRealGateway is finding 5's core assertion:
// the fields exist on the wire, with the values the ruling names.
func TestR8R4_TheVersionedViewReachesThePhoneOverTheRealGateway(t *testing.T) {
	sock, backend := serveR8Remote(t)
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true,
	})
	backend.setStream("sess1", &r8r4Stream{snap: r8r4Grid("hello from the machine"), frames: make(chan []byte)})

	sink := &r8r4Sink{}
	runR8R4Watch(t, sock, sink, r8Endpoint+"/sess1")

	v, _ := sink.firstNonEmpty()
	if v.ViewEpoch == 0 {
		t.Errorf("ADR-017 T4-a: the view that reached the phone carries view_epoch 0. The epoch is "+
			"minted per RENDER-LOOP START and it is the phone's only way to tell a re-run of the loop "+
			"from a continuation of it: a bare revision restarts at 1 while the phone holds N, and the "+
			"phone's only sane rule -- drop anything not strictly greater -- then discards every "+
			"snapshot of the second run with no error on either side.\ngot: %+v", v)
	}
	if v.Revision == 0 {
		t.Errorf("ADR-017 T4-a: the view carries revision 0; the revision is strictly increasing "+
			"WITHIN an epoch and the first emission is 1.\ngot: %+v", v)
	}
	if !v.Reset {
		t.Errorf("ADR-017 T4-a: the FIRST view of an epoch does not carry reset. Reset on the first "+
			"emission of every epoch on every path is what tells the phone to discard prior state "+
			"rather than merge a new screen into an old one.\ngot: %+v", v)
	}
	if v.SessionInstance != "inst-1" {
		t.Errorf("ADR-017 T8-a: the view names session_instance %q, want %q. Without the instance a "+
			"phone cannot tell the session it is watching from the one that REPLACED it under the same "+
			"id.", v.SessionInstance, "inst-1")
	}
	if v.RenderedAt.IsZero() {
		t.Errorf("ADR-017 T4-b: the view carries no rendered_at, so the phone cannot say how old the " +
			"screen it is showing is. Arrival time is not a substitute: a replayed backlog arrives all " +
			"at once and a held relay delivers old content at a new instant.")
	}
	if !strings.Contains(strings.Join(v.Lines, "\n"), "hello from the machine") {
		t.Errorf("the view carried no rendered content: %q", v.Lines)
	}
	if v.Session != r8Endpoint+"/sess1" {
		t.Errorf("the view names session %q, want the namespaced id %q", v.Session, r8Endpoint+"/sess1")
	}
}

// TestR8R4_AReplacedSessionIsANewEpochAndANewInstance is the ruling's own failure case, over
// the same real path: a session REPLACED under the same id must not look like a continuation.
func TestR8R4_AReplacedSessionIsANewEpochAndANewInstance(t *testing.T) {
	sock, backend := serveR8Remote(t)
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true,
	})
	backend.setStream("sess1", &r8r4Stream{snap: r8r4Grid("first incarnation"), frames: make(chan []byte)})

	first := &r8r4Sink{}
	runR8R4Watch(t, sock, first, r8Endpoint+"/sess1")
	before, _ := first.firstNonEmpty()

	// THE REPLACEMENT: same session id, a new incarnation, a new screen.
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-2", TerminalFallback: true,
	})
	backend.setStream("sess1", &r8r4Stream{snap: r8r4Grid("second incarnation"), frames: make(chan []byte)})

	second := &r8r4Sink{}
	runR8R4Watch(t, sock, second, r8Endpoint+"/sess1")
	after, _ := second.firstNonEmpty()

	if after.SessionInstance == before.SessionInstance {
		t.Errorf("ADR-017 T8-a: the replacement carries session_instance %q, the same as the "+
			"incarnation it replaced. A phone watching a session replaced under the same id then reads "+
			"the new screen as a seamless continuation of the old one -- which is the exact failure the "+
			"instance exists to make legible.", after.SessionInstance)
	}
	if after.ViewEpoch == before.ViewEpoch {
		t.Errorf("ADR-017 T4-a: the replacement reuses view_epoch %d. The epoch is minted per "+
			"render-loop start precisely so two runs can never share one.", after.ViewEpoch)
	}
	if !after.Reset {
		t.Errorf("ADR-017 T4-a: the replacement's first view does not carry reset, so the phone has " +
			"nothing telling it to discard the screen it holds.")
	}
}

// ---------------------------------------------------------------------------
// FINDING 7 -- THE PER-EMISSION CAPABILITY RE-CHECK, FENCED ON ITS OWN.
// ---------------------------------------------------------------------------
//
// THE FINDING. Deleting the emission-callback re-check at server.go left the whole of
// ./internal/protocol/ green, INCLUDING `TestR8Legacy_TheGateIsReCheckedPerEmission` -- the
// test named for it. That test is satisfied by the PER-TICK clause alone (deleting both does
// fail it), so the fence survived its own mutation and the emission half had none.
//
// WHY THE TWO CLAUSES LOOK INSEPARABLE AND ARE NOT. Both call `terminalWatchAllowed`, so a
// record withdrawn mid-stream trips whichever runs first, and which one that is comes down to
// a 4 ms poll race. But the render loop's FIRST emission -- the stream's initial snapshot --
// happens at loop START, BEFORE the ticker has fired even once. So there is exactly one moment
// where the emission gate is the only gate there is, and this test lives in it: the backend
// starts refusing the capability lookup the instant the TAP OPENS, which is after the
// subscribe-time gate has passed and before any emission. With the emission re-check present,
// nothing is written. Without it, the initial screen goes out over the real gateway to the
// real sink, and the per-tick clause only stops the SECOND one.

// r8r4RefuseAfterTap makes the backend refuse every capability lookup from the moment the
// read-only tap is opened.
func (b *r8Backend) refuseAfterTap() {
	b.mu.Lock()
	b.refuseOnTap = true
	b.mu.Unlock()
}

// TestR8R4_ARecordWithdrawnBetweenSubscribeAndTheFirstEmissionShipsNothing is finding 7's
// fence, over the real assembled path.
func TestR8R4_ARecordWithdrawnBetweenSubscribeAndTheFirstEmissionShipsNothing(t *testing.T) {
	sock, backend := serveR8Remote(t)
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true,
	})
	backend.setStream("sess1", &r8r4Stream{snap: r8r4Grid("secret screen"), frames: make(chan []byte)})
	backend.refuseAfterTap()

	sink := &r8r4Sink{}
	gw := New(sock, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.RunTerminal(ctx, r8Endpoint+"/sess1") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("the peek goroutine did not exit")
		}
	})

	// Long enough for the initial emission (immediate) and several 4 ms ticks to have run.
	time.Sleep(400 * time.Millisecond)
	for _, v := range sink.all() {
		if len(v.Lines) > 0 {
			t.Fatalf("ADR-017 T6-e: a session whose capability record was withdrawn between the "+
				"subscribe gate and the FIRST emission still shipped a rendered screen to the phone: "+
				"%q.\nThe render loop's first emission happens at loop start, before the per-tick "+
				"liveness poll has fired even once, so the per-emission re-check is the ONLY gate "+
				"covering it. A gate checked only per tick has the kill switch's own hole one field "+
				"over.", v.Lines)
		}
	}
}
