package remotegw

// WAVE R8 / ROUND 3 -- A REAPED WATCH MUST NOT LEAVE A GRID THE SCREEN CALLS FRESH.
//
// MODERATE 5, stated as the harm. DefaultWatchHorizon is 60 seconds and the renewal is issued
// only from PhoneSurface.reconcileTerminalWatch, called from drawContent -- and the phone has
// no guaranteed redraw inside 60 seconds for an IDLE fallback screen on an IDLE session.
// When the horizon lapses, Reap -> Unwatch -> ctx cancel ends the peek FROM THE GATEWAY SIDE,
// so the daemon sends no OpError and the sink's blanking path never runs. The phone keeps the
// last grid. And it is not labelled: Core.StreamStale is set by explicit desync events, not by
// a clock, and MachineSilentAt reads the machine heartbeat, which keeps arriving.
//
// That is "the machine went quiet rendered as the terminal is idle" -- the precise harm
// amendment T4-b's staleness indicator was written to prevent -- introduced by this wave's own
// horizon.
//
// THE MACHINE-SIDE HALF IS HERE: a reap blanks the phone's copy, so whatever the screen does
// next, it cannot present a screen the machine stopped rendering as a live terminal. The
// phone-side half (a renewal the live foreground screen issues on its own tick, so an idle
// screen KEEPS its watch) is in PhoneSurface and fenced in android/gate.
//
// The two are not alternatives. The tick is what stops a watched screen going dark; the blank
// is what stops an unwatched screen lying. A tick alone would be a promise about a process
// that can be killed, backgrounded or descheduled.

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// blankingRunner is a terminalRunner that also records blanks, so the reaper's obligation is
// observable without a daemon.
type blankingRunner struct {
	*r8FakeRunner

	mu     sync.Mutex
	blanks []string
}

func newBlankingRunner() *blankingRunner {
	return &blankingRunner{r8FakeRunner: newR8FakeRunner()}
}

func (b *blankingRunner) BlankTerminal(session string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blanks = append(b.blanks, session)
	return nil
}

func (b *blankingRunner) blanked() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.blanks...)
}

// TestR8R3_AReapedWatchBlanksThePhonesCopy is the machine-side half of MODERATE 5.
func TestR8R3_AReapedWatchBlanksThePhonesCopy(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newBlankingRunner()
	const horizon = 30 * time.Second
	w := newTerminalWatcher(runner, time.Millisecond, horizon, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	w.Watch("m/sess1")
	if got := runner.blanked(); len(got) != 0 {
		t.Fatalf("a fresh watch blanked the phone's copy: %v", got)
	}

	clk.advance(horizon + time.Second)
	w.Reap()

	got := runner.blanked()
	if len(got) != 1 || got[0] != "m/sess1" {
		t.Fatalf("a reaped watch blanked %v, want exactly [m/sess1].\n"+
			"Reap ends the peek from the GATEWAY side, so the daemon sends nothing and the "+
			"phone keeps the last grid -- with no staleness signal, because the stream-stale "+
			"flag is set by desync events and the machine heartbeat keeps arriving. The screen "+
			"then renders a machine that stopped as a terminal that is idle.", got)
	}
}

// TestR8R3_AnExplicitUnwatchDoesNotBlank draws the line the fix must not cross: an unwatch is
// the PHONE saying it stopped looking (leaving the screen, or backgrounding), and blanking
// there would fight the screen's own teardown -- and would blank a cached grid the user may
// legitimately come back to. Only an expiry the phone did not ask for blanks.
func TestR8R3_AnExplicitUnwatchDoesNotBlank(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newBlankingRunner()
	w := newTerminalWatcher(runner, time.Millisecond, 30*time.Second, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	w.Watch("m/sess1")
	w.Unwatch("m/sess1")
	if got := runner.blanked(); len(got) != 0 {
		t.Fatalf("an explicit unwatch blanked the phone's copy: %v", got)
	}
}

// TestR8R3_TransportLossBlanksEveryWatchItEnds: UnwatchAll is not the phone saying it stopped
// looking, it is the LINK dying under a phone that never said anything. Every screen it ends
// is a screen that would otherwise keep its last grid, so every one of them is blanked.
func TestR8R3_TransportLossBlanksEveryWatchItEnds(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newBlankingRunner()
	w := newTerminalWatcher(runner, time.Millisecond, 30*time.Second, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	w.Watch("m/sess1")
	w.Watch("m/sess2")
	w.UnwatchAll("transport loss")

	got := runner.blanked()
	if len(got) != 2 {
		t.Fatalf("transport loss blanked %v, want both watched sessions", got)
	}
}

// TestR8R3_ABlankIsNotSentForASessionWithNoWatch is the vacuity guard: a reaper that blanked
// unconditionally would blank sessions nobody was watching and would erase a grid the phone
// is legitimately holding for a screen that is up.
func TestR8R3_ABlankIsNotSentForASessionWithNoWatch(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newBlankingRunner()
	w := newTerminalWatcher(runner, time.Millisecond, 30*time.Second, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	clk.advance(time.Hour)
	w.Reap()
	if got := runner.blanked(); len(got) != 0 {
		t.Fatalf("the reaper blanked %v with no watch to reap", got)
	}
}

// TestR8R3_TheGatewayCanBlankThePhonesCopy is the production half of the seam: the watcher's
// runner IS the gateway, so the blanking obligation must be one the real Gateway implements
// against its own sink rather than one only a fake can satisfy.
func TestR8R3_TheGatewayCanBlankThePhonesCopy(t *testing.T) {
	sink := &r8BlankSink{}
	gw := New("/tmp/does-not-need-to-exist.sock", sink)
	if err := gw.BlankTerminal("m/sess1"); err != nil {
		t.Fatalf("Gateway.BlankTerminal: %v", err)
	}
	if got := sink.calls(); len(got) != 1 || got[0].session != "m/sess1" || got[0].lines != nil {
		t.Fatalf("Gateway.BlankTerminal published %+v; want one blanking snapshot for m/sess1 "+
			"with no lines -- which is what the phone's snapshot cache reads as 'the machine is "+
			"not rendering this session'", got)
	}
	var _ terminalBlanker = gw
}

// r8BlankSink is a JournalSink+TerminalSink that records what it was handed.
type r8BlankSink struct {
	mu   sync.Mutex
	seen []r8BlankCall
}

type r8BlankCall struct {
	session    string
	lines      []string
	cols, rows int
}

func (s *r8BlankSink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (s *r8BlankSink) Event(protocol.JournalRecord) error              { return nil }

func (s *r8BlankSink) Terminal(v protocol.TerminalViewV1) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, r8BlankCall{session: v.Session, lines: v.Lines, cols: v.Cols, rows: v.Rows})
	return nil
}

func (s *r8BlankSink) calls() []r8BlankCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]r8BlankCall(nil), s.seen...)
}
