package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for PB-GW-7's APPEND BUDGET: the machine->phone
// numbers do not currently close.
//
// THE DEFECT. `renderDebounceWindow = 16ms` (internal/daemon/terminalrender.go) lets one
// live terminal peek emit ~62 snapshots/s. The relay caps appends at
// `MailboxAppendPerMin: 600` per TARGET (relay/config.go) on a TUMBLING one-minute window
// (relay/server.go rateWindow.allow), and journal records, terminal snapshots and command
// replies all share ONE RelaySink, ONE target and ONE seq stream. So a peek:
//   (a) exhausts the target's whole minute-window in ~10s and is refused for the rest of it,
//   (b) starves the journal, whose records are refused alongside the snapshots, and
//   (c) MANUFACTURES GAPS: RelaySink.sealAtSeq allocates the seq BEFORE the append and
//       returns on error, so every refused snapshot permanently burns an outbound seq. The
//       phone's crypto.MailboxReceiver reports Gap on the next frame it does receive, and
//       PB-SYNC-1 must conservatively stale BOTH journal and terminal off that one bit.
//
// THE BUDGET (§6.0): <= 8 appends/s sustained across journal AND terminal combined, i.e.
// terminal snapshots coalesced to <= 125ms, against a render loop that can emit ~62/s.
//
// THE SEAM these tests pin: a CoalescingSink wrapper (remotegw.NewCoalescingSink) sitting
// between the Gateway and the RelaySink. It is a wrapper rather than a change inside
// RelaySink so that sealing, seq allocation and outbound durability stay one concern and
// the admission policy stays another:
//
//	const DefaultAppendWindow = 125 * time.Millisecond   // §6.0: <= 8 appends/s combined
//	type OutboundSink interface{ JournalSink; TerminalSink }
//	type CoalesceConfig struct {
//		Inner  OutboundSink
//		Window time.Duration    // 0 => DefaultAppendWindow
//		Now    func() time.Time // 0 => time.Now (clock seam; these tests drive virtual time)
//	}
//	func NewCoalescingSink(cfg CoalesceConfig) *CoalescingSink
//	func (c *CoalescingSink) Snapshot([]protocol.JournalRecord, uint64) error
//	func (c *CoalescingSink) Event(protocol.JournalRecord) error
//	func (c *CoalescingSink) Terminal(session string, lines []string, cols, rows int) error
//	func (c *CoalescingSink) Flush() error         // force any stashed snapshot out now
//	func (c *CoalescingSink) DeliveredCursor() uint64 // forwards the inner sink's PB-GW-8 cursor
//
// Pinned semantics:
//   - Event/Snapshot are forwarded IMMEDIATELY and are never coalesced or dropped (R-GW.5:
//     journal records are never lost, and Gateway.deliver still gates its cursor on the
//     returned error). Each one CONSUMES the shared slot, which is what makes the budget
//     "combined" rather than per-stream.
//   - Terminal is latest-wins: a snapshot arriving inside the window replaces the stashed
//     one and returns nil without appending.
//   - A stashed snapshot must still reach the relay when production stops (Flush; RunTerminal
//     calls it on every idle wake -- see TestGatewayRunTerminal_CoalescedPeekShowsLatestGrid).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// renderDebounceRate mirrors internal/daemon/terminalrender.go's renderDebounceWindow. It
// is duplicated rather than imported because internal/daemon does not export it; if that
// constant moves, this test's premise (~62 snapshots/s) moves with it.
const renderDebounceRate = 16 * time.Millisecond

// vclock is a test-driven virtual clock. The sustained-peek scenario models 150 s of a
// live peek at the real debounce rate (~9400 snapshots) and must cross the relay's
// TUMBLING minute boundary more than once; on wall-clock time that is a two-and-a-half
// minute test whose rate assertions would be load-dependent. Virtual time makes it
// deterministic and instant.
type vclock struct {
	mu sync.Mutex
	t  time.Time
}

func newVClock() *vclock { return &vclock{t: time.Unix(1_700_000_000, 0)} }

func (c *vclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *vclock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// quotaAppender models the RELAY's real append admission for one target, so the budget is
// measured against the constraint that actually exists rather than an idealized one: a
// TUMBLING one-minute window of MailboxAppendPerMin (relay/server.go handleMailboxAppend ->
// rateWindow.allow), refused with the very sentinel a real relay.Client surfaces
// (relay.ErrQuotaExceeded) and refused BEFORE the item is stored. Every admitted envelope
// and its append time are kept so a test can measure the rate and replay the accepted
// stream into a phone-side receiver.
type quotaAppender struct {
	now    func() time.Time
	perMin int

	mu       sync.Mutex
	winStart time.Time
	winCount int
	stored   [][]byte
	at       []time.Time
	refused  int
}

func (a *quotaAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	// rateWindow.allow: the window RESETS (rather than sliding) once a minute has passed.
	if now.Sub(a.winStart) >= time.Minute {
		a.winStart = now
		a.winCount = 0
	}
	if a.winCount >= a.perMin {
		a.refused++
		return 0, relay.ErrQuotaExceeded
	}
	a.winCount++
	a.stored = append(a.stored, append([]byte(nil), env...))
	a.at = append(a.at, now)
	return uint64(len(a.stored)), nil
}

func (a *quotaAppender) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.stored)
}

func (a *quotaAppender) snapshotState() (stored [][]byte, refused int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.stored...), a.refused
}

// outboundFrame is an INDEPENDENT decode of whatever the sink sealed: the kind
// discriminator tells a terminal snapshot from a bare journal record, and Cursor
// identifies which journal record it was.
type outboundFrame struct {
	Kind   string `json:"kind"`
	Cursor uint64 `json:"cursor"`
}

func budgetTestKey() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 11)
	}
	return key
}

// TestRelaySink_SustainedPeekStaysUnderAppendBudget drives 150 s (>= the required 60 s, and
// enough to cross the relay's tumbling minute boundary twice) of a live terminal peek at the
// REAL 16 ms render debounce, with an ordinary journal record once a second, and asserts the
// three things PB-GW-7 demands of a sustained peek: no quota refusal, no manufactured gap,
// and no journal starvation -- all under the §6.0 budget of <= 8 appends/s combined.
func TestRelaySink_SustainedPeekStaysUnderAppendBudget(t *testing.T) {
	clk := newVClock()
	start := clk.Now()
	key := budgetTestKey()
	sender := [8]byte{9, 10, 11, 12, 13, 14, 15, 16}
	app := &quotaAppender{now: clk.Now, perMin: relay.DefaultConfig().Quotas.MailboxAppendPerMin}

	inner := NewRelaySink(RelayConfig{
		Appender:    app,
		Target:      "phone-routing-id",
		EpochID:     7,
		Key:         key,
		SenderKeyID: sender,
		Now:         clk.Now,
	})
	sink := NewCoalescingSink(CoalesceConfig{Inner: inner, Window: DefaultAppendWindow, Now: clk.Now})

	const peek = 150 * time.Second
	var (
		journalSent    int
		journalErrs    int
		journalStarved int
		terminalErrs   int
	)
	nextJournal := start.Add(time.Second)
	for frame := 0; clk.Now().Sub(start) < peek; frame++ {
		// An ordinary journal record once a second, riding the SAME sink and target.
		if !clk.Now().Before(nextJournal) {
			journalSent++
			before := app.count()
			if err := sink.Event(protocol.JournalRecord{Cursor: uint64(journalSent), SessionID: "m/s1", Type: "launched"}); err != nil {
				journalErrs++
			}
			if app.count() != before+1 {
				journalStarved++
			}
			nextJournal = nextJournal.Add(time.Second)
		}
		if err := sink.Terminal("m/s1", []string{fmt.Sprintf("frame %d", frame)}, 80, 24); err != nil {
			terminalErrs++
		}
		clk.Advance(renderDebounceRate)
	}
	if err := sink.Flush(); err != nil {
		t.Fatalf("final flush: %v", err)
	}
	elapsed := clk.Now().Sub(start)
	stored, refused := app.snapshotState()

	// (a) NO QUOTA REFUSAL. A peek at the render rate exhausts MailboxAppendPerMin in ~10 s
	// and is then refused for the remaining ~50 s of every tumbling window.
	if refused != 0 {
		t.Errorf("the relay refused %d of %d appends over a %s peek: an uncoalesced peek at the "+
			"%s render debounce (~%d snapshots/s) exhausts MailboxAppendPerMin=%d per tumbling "+
			"minute (PB-GW-7)", refused, refused+len(stored), elapsed, renderDebounceRate,
			int(time.Second/renderDebounceRate), app.perMin)
	}

	// (b) UNDER BUDGET: <= 8 appends/s sustained across journal AND terminal combined
	// (§6.0). The +2 absorbs the first append and the trailing flush at the boundaries.
	budget := int(elapsed/DefaultAppendWindow) + 2
	if got := len(stored); got > budget {
		t.Errorf("%d appends over %s = %.1f/s, over the §6.0 budget of %d (<= 8/s combined, "+
			"i.e. terminal snapshots coalesced to <= %s)", got, elapsed,
			float64(got)/elapsed.Seconds(), budget, DefaultAppendWindow)
	}

	// (c) NO JOURNAL STARVATION: every journal record reached the relay at the moment it was
	// offered. The journal must never be coalesced, deferred or refused behind a peek.
	if journalErrs != 0 || journalStarved != 0 {
		t.Errorf("journal: %d of %d records errored and %d never reached the relay during the "+
			"peek; journal and terminal share one sink and one target, so a saturating peek must "+
			"not starve the journal (PB-GW-7, R-GW.5)", journalErrs, journalSent, journalStarved)
	}
	if terminalErrs != 0 {
		t.Errorf("%d terminal snapshots returned an error; a coalesced-away snapshot is admitted "+
			"(nil), not failed -- only a real seal/append failure is an error", terminalErrs)
	}

	// (d) NO MANUFACTURED GAP. Replay everything the relay accepted into the phone's real
	// receiver: a burned seq (allocated, then refused) shows up here as Gap, which PB-SYNC-1
	// must conservatively charge to BOTH journal and terminal.
	phone := crypto.NewMailboxReceiver()
	var gaps int
	journalSeen := map[uint64]int{}
	var terminalFrames int
	for i, raw := range stored {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("append %d does not parse: %v", i, err)
		}
		res, err := phone.Accept(key, env)
		if err != nil {
			t.Fatalf("the phone rejected append %d (seq %d): %v", i, env.Header.Seq, err)
		}
		if res.Gap {
			gaps++
		}
		var f outboundFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			t.Fatalf("append %d plaintext is not decodable: %v", i, err)
		}
		if f.Kind == "terminal_snapshot" {
			terminalFrames++
			continue
		}
		journalSeen[f.Cursor]++
	}
	if gaps != 0 {
		t.Errorf("the phone saw %d GAPS over a peek with no relay failure: seqs are allocated "+
			"before the append and burned when it is refused, manufacturing gaps that PB-SYNC-1 "+
			"charges to BOTH journal and terminal (PB-GW-7)", gaps)
	}

	// (e) The peek is still LIVE and the journal is COMPLETE: coalescing must throttle the
	// stream, not silence it, and must not lose a single journal record.
	if floor := int(elapsed.Seconds()) * 4; terminalFrames < floor {
		t.Errorf("only %d terminal snapshots reached the phone over %s (< %d, i.e. 4/s): "+
			"coalescing must throttle the live tail, not silence it (PB-APP-4)", terminalFrames, elapsed, floor)
	}
	for c := 1; c <= journalSent; c++ {
		if n := journalSeen[uint64(c)]; n != 1 {
			t.Fatalf("journal record cursor=%d reached the phone %d times, want exactly 1 "+
				"(%d of %d records arrived)", c, n, len(journalSeen), journalSent)
		}
	}
}

// snapshotRecorder is an OutboundSink that records every terminal snapshot forwarded to it,
// in order, so a test can assert WHICH frames survived coalescing and in what order.
type snapshotRecorder struct {
	mu  sync.Mutex
	got []protocol.TerminalSnapshot
}

func (r *snapshotRecorder) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (r *snapshotRecorder) Event(protocol.JournalRecord) error              { return nil }

func (r *snapshotRecorder) Terminal(session string, lines []string, cols, rows int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, protocol.TerminalSnapshot{Session: session, Lines: lines, Cols: cols, Rows: rows})
	return nil
}

func (r *snapshotRecorder) all() []protocol.TerminalSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.TerminalSnapshot(nil), r.got...)
}

// TestGatewayRunTerminal_CoalescedPeekShowsLatestGrid pins the half of coalescing that is
// easy to get wrong: dropping frames is allowed, showing a STALE GRID is not. A burst of
// renders is coalesced to a handful of appends, but the LAST frame of the burst must still
// reach the phone once production stops -- otherwise the user watches an out-of-date screen
// until the session happens to emit again, which for an idle terminal is never.
//
// It drives the real RunTerminal path (fake daemon, real socket, real time, the production
// DefaultAppendWindow), so the trailing flush must be driven by the gateway itself: pinned
// as RunTerminal calling Flush on every idle read wake, with that wake at or under the
// coalescing window.
func TestGatewayRunTerminal_CoalescedPeekShowsLatestGrid(t *testing.T) {
	// /tmp keeps the socket under the 104-byte sun_path limit (macOS $TMPDIR is long).
	dir, err := os.MkdirTemp("/tmp", "gwcoal")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	const frames = 50
	snaps := make([]protocol.TerminalSnapshot, frames)
	for i := range snaps {
		snaps[i] = protocol.TerminalSnapshot{Session: "s1", Lines: []string{fmt.Sprintf("frame-%02d", i)}, Cols: 80, Rows: 24}
	}
	gotSub := make(chan protocol.Control, 1)
	go serveFakeTerminalDaemon(t, ln, "m", snaps, gotSub)

	rec := &snapshotRecorder{}
	sink := NewCoalescingSink(CoalesceConfig{Inner: rec})
	gw := New(sock, sink)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- gw.RunTerminal(ctx, "m/s1") }()

	// The newest grid must arrive on its own, with no further frames produced.
	want := fmt.Sprintf("frame-%02d", frames-1)
	deadline := time.Now().Add(3 * time.Second)
	var got []protocol.TerminalSnapshot
	for time.Now().Before(deadline) {
		got = rec.all()
		if n := len(got); n > 0 && len(got[n-1].Lines) == 1 && got[n-1].Lines[0] == want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-errc
	<-gotSub

	if len(got) == 0 {
		t.Fatal("no terminal snapshot was forwarded at all")
	}
	last := got[len(got)-1]
	if len(last.Lines) != 1 || last.Lines[0] != want {
		t.Fatalf("the newest snapshot the phone would show is %v, want [%s]: coalescing dropped the "+
			"LAST frame of the burst, so the peek is stuck on a stale grid until the session emits "+
			"again -- for an idle terminal, never (PB-GW-7)", last.Lines, want)
	}
	// A burst of 50 renders inside one 125 ms window must not become 50 appends.
	if len(got) > 8 {
		t.Errorf("%d of %d burst frames were forwarded; a burst inside one %s window must coalesce "+
			"to a handful (§6.0: <= 8 appends/s)", len(got), frames, DefaultAppendWindow)
	}
	// Latest-wins, never last-wins-late: forwarded frames must be strictly newer over time.
	prev := -1
	for i, s := range got {
		if len(s.Lines) != 1 {
			t.Fatalf("forwarded snapshot %d has lines %v, want exactly one", i, s.Lines)
		}
		var idx int
		if _, err := fmt.Sscanf(s.Lines[0], "frame-%d", &idx); err != nil {
			t.Fatalf("forwarded snapshot %d line %q is not a frame marker: %v", i, s.Lines[0], err)
		}
		if idx <= prev {
			t.Fatalf("forwarded snapshot %d is frame %d after frame %d: a coalescer must forward the "+
				"LATEST stashed snapshot, never an older one it was still holding", i, idx, prev)
		}
		prev = idx
	}
	if got[0].Session != "m/s1" {
		t.Errorf("forwarded session = %q, want m/s1: the wrapper must not lose the endpoint namespacing", got[0].Session)
	}
}
