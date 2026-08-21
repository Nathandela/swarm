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
		if err := sink.Terminal(protocol.TerminalViewV1{Session: "m/s1", Lines: []string{fmt.Sprintf("frame %d", frame)}, Cols: 80, Rows: 24}); err != nil {
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

func (r *snapshotRecorder) Terminal(v protocol.TerminalViewV1) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, protocol.TerminalSnapshot{Session: v.Session, Lines: v.Lines, Cols: v.Cols, Rows: v.Rows})
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
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

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

// --- ADR-010 §7: the PRODUCER-side append floor for interaction items --------
//
// FAILING-FIRST (TDD RED, GG-5). Everything below is undefined at RED: the admission queue
// exists nowhere in the tree.
//
// THE DEFECT §7 names. The peek case above bounds the TERMINAL stream at the gateway, where
// CoalescingSink may coalesce it latest-wins. A journal record may not be coalesced there at
// all (R-GW.5; coalesce.go's Event forwards immediately and never drops), and ADR-009 makes
// the journal dense: an item per user message, per agent-message increment, per tool-run
// open, per tool-run close, per file change, per approval. `PreToolUse` plus `PostToolUse`
// alone is two appends per tool call, and each consumes a slot in the SAME <= 8 appends/s
// combined budget the peek used to spend (§6.0, PB-GW-7). So the rate is bound in the one
// place the merge is lossless: the PRODUCER, upstream of the sink that is forbidden to help.
//
// THE SEAM these tests pin -- one admission queue per TARGET (IS-DELTA-2a: admission is
// bounded per target across every session and every kind; a per-item_id window does not
// bind, because N concurrent sessions multiply straight past it):
//
//	type ItemAdmissionConfig struct {
//		Append func(session string, item json.RawMessage) error // the release seam
//		Window time.Duration                                    // 0 => DefaultAppendWindow
//		Now    func() time.Time                                 // clock seam
//	}
//	func NewItemAdmission(cfg ItemAdmissionConfig) *ItemAdmission
//	func (a *ItemAdmission) Offer(session string, item json.RawMessage) error
//	func (a *ItemAdmission) Flush() error
//	func (a *ItemAdmission) Pending() int
//
// Pinned semantics (ADR-010 §7; interaction-schema.md IS-DELTA-1/-2/-2a/-3):
//   - at most one release per window per TARGET, across all sessions and kinds;
//   - a SPACING FLOOR, not a batching delay: an item offered a full window after the last
//     release is admitted at once;
//   - `agent_message` is the ONLY kind merged by text concatenation, and only within one
//     `item_id`; text is never concatenated across item_ids or across kinds;
//   - every other kind merges by RECORD COLLAPSE within one `item_id` (a `tool_run` open and
//     its close inside one window become one record) and never by text;
//   - `approval_request` is never merged and takes the HEAD of the queue, so it waits at
//     most one window and never behind a backlog of prose;
//   - nothing is ever dropped: the merge is lossless, or the record keeps its own slot.

// itemJSON builds one serialized interaction item: the §2 envelope with the §3 kind fields
// flat beside it, which is the shape internal/daemon marshals and the queue receives.
func itemJSON(t *testing.T, id, kind string, fields map[string]any) json.RawMessage {
	t.Helper()
	m := map[string]any{"v": 1, "item_id": id, "kind": kind, "ts": "2026-08-07T12:00:00Z"}
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("itemJSON(%s, %s): %v", id, kind, err)
	}
	return b
}

// releasedItem is one item the queue admitted, with the virtual instant it was released at
// so a test can measure how long an approval waited.
type releasedItem struct {
	at      time.Time
	session string
	item    json.RawMessage
}

// releaseLog is the release seam: what the queue admitted, in order.
type releaseLog struct {
	now func() time.Time

	mu  sync.Mutex
	got []releasedItem
}

func (l *releaseLog) append(session string, item json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, releasedItem{at: l.now(), session: session, item: append(json.RawMessage(nil), item...)})
	return nil
}

func (l *releaseLog) all() []releasedItem {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]releasedItem(nil), l.got...)
}

func (l *releaseLog) count() int { return len(l.all()) }

// transcriptRecord is an INDEPENDENT decode of one released item: only the fields these
// tests assert on, so no assertion inherits the producer's own struct.
type transcriptRecord struct {
	ItemID     string `json:"item_id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Text       string `json:"text"`
	Tool       string `json:"tool"`
	ExitCode   *int   `json:"exit_code"`
	Path       string `json:"path"`
	StopReason string `json:"stop_reason"`
}

func decodeItem(t *testing.T, raw json.RawMessage) transcriptRecord {
	t.Helper()
	var rec transcriptRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("released item %s does not decode: %v", raw, err)
	}
	return rec
}

func offerItem(t *testing.T, a *ItemAdmission, session string, item json.RawMessage) {
	t.Helper()
	if err := a.Offer(session, item); err != nil {
		t.Fatalf("Offer(%s, %s): %v", session, item, err)
	}
}

// newAdmissionHarness returns an admission queue over a release log, both on one virtual
// clock, at the production DefaultAppendWindow: these tests measure the real floor.
func newAdmissionHarness() (*ItemAdmission, *releaseLog, *vclock) {
	clk := newVClock()
	log := &releaseLog{now: clk.Now}
	adm := NewItemAdmission(ItemAdmissionConfig{Append: log.append, Window: DefaultAppendWindow, Now: clk.Now})
	return adm, log, clk
}

// TestItemAdmission_IsASpacingFloorNotABatchingDelay pins IS-DELTA-2's own words: an item
// offered more than one window after the last release is admitted AT ONCE. A queue that
// always waited for its window would add 125 ms to every item in an idle session -- the
// transcript's whole latency budget spent on a stream that was never near the ceiling.
func TestItemAdmission_IsASpacingFloorNotABatchingDelay(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	offerItem(t, adm, "m/s1", itemJSON(t, "u1", "user_message", map[string]any{"text": "hi", "source": "owner"}))
	if got := log.count(); got != 1 {
		t.Fatalf("the first item released %d times, want 1: the window is a spacing floor, not a batching delay (IS-DELTA-2)", got)
	}
	clk.Advance(DefaultAppendWindow)
	offerItem(t, adm, "m/s1", itemJSON(t, "u2", "user_message", map[string]any{"text": "again", "source": "owner"}))
	if got := log.count(); got != 2 {
		t.Fatalf("an item offered a full %s after the last release produced %d appends, want 2: it must not wait a second window", DefaultAppendWindow, got)
	}
	if got := adm.Pending(); got != 0 {
		t.Fatalf("Pending() = %d after both items were released, want 0", got)
	}
}

// TestItemAdmission_AgentMessageMergesByTextConcatenation is IS-DELTA-1/-2: increments for
// one item_id inside one window become ONE record whose text is their lossless
// concatenation, and the terminal increment's own fields survive the merge. Losing one
// increment here is unrecoverable -- the phone rebuilds the message by concatenating in
// cursor order and has no way to notice a hole.
func TestItemAdmission_AgentMessageMergesByTextConcatenation(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	// The first increment consumes the free slot.
	offerItem(t, adm, "m/s1", itemJSON(t, "am1", "agent_message", map[string]any{"text": "Hel", "status": "in_progress"}))
	for _, tok := range []string{"lo, ", "wor"} {
		clk.Advance(10 * time.Millisecond)
		offerItem(t, adm, "m/s1", itemJSON(t, "am1", "agent_message", map[string]any{"text": tok, "status": "in_progress"}))
	}
	clk.Advance(10 * time.Millisecond)
	offerItem(t, adm, "m/s1", itemJSON(t, "am1", "agent_message", map[string]any{"text": "ld", "status": "completed", "stop_reason": "end_turn"}))
	if got := log.count(); got != 1 {
		t.Fatalf("%d appends for four increments inside one %s window, want 1 (IS-DELTA-2)", got, DefaultAppendWindow)
	}
	clk.Advance(DefaultAppendWindow)
	if err := adm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got := log.all()
	if len(got) != 2 {
		t.Fatalf("%d appends in total, want 2 (one immediate, one merged)", len(got))
	}
	first, merged := decodeItem(t, got[0].item), decodeItem(t, got[1].item)
	if first.Text+merged.Text != "Hello, world" {
		t.Errorf("the phone reconstructs %q, want %q: the merge is LOSSLESS text concatenation in cursor order (IS-DELTA-1)",
			first.Text+merged.Text, "Hello, world")
	}
	if merged.ItemID != "am1" {
		t.Errorf("merged item_id = %q, want am1: every record of a streamed item repeats it (IS-ENV-2)", merged.ItemID)
	}
	if merged.Status != "completed" || merged.StopReason != "end_turn" {
		t.Errorf("merged status/stop_reason = %q/%q, want completed/end_turn: the terminal increment's own fields must survive the merge (IS-ST-1)",
			merged.Status, merged.StopReason)
	}
}

// TestItemAdmission_NeverConcatenatesAcrossItemIDsOrKinds is IS-DELTA-3's hard prohibition:
// `agent_message` is the ONLY kind merged by text, and only within one item_id. Two
// sessions' prose merged into one record would be a transcript attributing one agent's words
// to another, and no consumer could detect it.
func TestItemAdmission_NeverConcatenatesAcrossItemIDsOrKinds(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	offerItem(t, adm, "m/s1", itemJSON(t, "seed", "session_status", map[string]any{"process": "running"}))
	offerItem(t, adm, "m/s1", itemJSON(t, "am1", "agent_message", map[string]any{"text": "alpha"}))
	offerItem(t, adm, "m/s2", itemJSON(t, "am2", "agent_message", map[string]any{"text": "beta"}))
	offerItem(t, adm, "m/s1", itemJSON(t, "fc1", "file_change", map[string]any{"path": "a.go", "change": "modify"}))
	offerItem(t, adm, "m/s1", itemJSON(t, "fc2", "file_change", map[string]any{"path": "b.go", "change": "modify"}))

	for i := 0; i < 6 && adm.Pending() > 0; i++ {
		clk.Advance(DefaultAppendWindow)
		if err := adm.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}
	byID := map[string]transcriptRecord{}
	for _, r := range log.all() {
		rec := decodeItem(t, r.item)
		if _, dup := byID[rec.ItemID]; dup {
			t.Fatalf("item_id %q was released twice; nothing here shares a window twice", rec.ItemID)
		}
		byID[rec.ItemID] = rec
	}
	if len(byID) != 5 {
		t.Fatalf("%d records released, want 5: distinct item_ids are never merged, only spaced", len(byID))
	}
	if byID["am1"].Text != "alpha" || byID["am2"].Text != "beta" {
		t.Errorf("prose came back as %q/%q, want alpha/beta: text is never concatenated across item_ids (IS-DELTA-3)",
			byID["am1"].Text, byID["am2"].Text)
	}
	if byID["fc1"].Path != "a.go" || byID["fc2"].Path != "b.go" {
		t.Errorf("file_change paths came back as %q/%q, want a.go/b.go: collapsing two changes into one record loses a path "+
			"(IS-DELTA-3: lossless, or its own slot)", byID["fc1"].Path, byID["fc2"].Path)
	}
}

// TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord is ADR-010 §7's record collapse:
// whole records merge, never text. A tool call is two appends (`PreToolUse`, `PostToolUse`),
// which is exactly what makes the transcript dense enough to need a floor.
func TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	offerItem(t, adm, "m/s1", itemJSON(t, "seed", "session_status", map[string]any{"process": "running"}))
	offerItem(t, adm, "m/s1", itemJSON(t, "tr1", "tool_run", map[string]any{
		"tool": "Bash", "status": "in_progress", "action": map[string]any{"type": "execute", "command": "go test ./..."},
	}))
	clk.Advance(20 * time.Millisecond)
	offerItem(t, adm, "m/s1", itemJSON(t, "tr1", "tool_run", map[string]any{
		"status": "completed", "output_excerpt": "ok\tswarm\t0.4s", "exit_code": 0,
	}))
	clk.Advance(DefaultAppendWindow)
	if err := adm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got := log.all()
	if len(got) != 2 {
		t.Fatalf("%d appends, want 2 (the seed, then ONE collapsed tool_run): an open and its close inside one window become "+
			"one record (ADR-010 §7, IS-DELTA-3)", len(got))
	}
	rec := decodeItem(t, got[1].item)
	if rec.Tool != "Bash" {
		t.Errorf("collapsed tool = %q, want Bash: the OPEN's fields must survive the collapse -- a card with no tool name is the "+
			"open record silently dropped", rec.Tool)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 0 || rec.Status != "completed" {
		t.Errorf("collapsed exit_code/status = %v/%q, want 0/completed: the CLOSE's fields must win", rec.ExitCode, rec.Status)
	}
	var full map[string]any
	if err := json.Unmarshal(got[1].item, &full); err != nil {
		t.Fatalf("collapsed item does not decode: %v", err)
	}
	if _, ok := full["action"]; !ok {
		t.Errorf("collapsed item lost `action`: a collapse is a UNION of the two records, not a replacement (§7)")
	}
	if _, ok := full["output_excerpt"]; !ok {
		t.Errorf("collapsed item lost `output_excerpt`")
	}
}

// TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged is IS-DELTA-3's ordering
// rule, and the reason the floor is affordable at all: an approval is never merged, takes
// the head of the queue, and waits at most ONE window -- never behind a backlog of prose.
// The expiry budget (spike-SC: Codex 120 s, Claude >= 300 s) is what makes one window cheap;
// a backlog would not be.
func TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	offerItem(t, adm, "m/s1", itemJSON(t, "seed", "session_status", map[string]any{"process": "running"}))
	// A backlog: prose from one session and tool runs from another, all held by the floor.
	for i := 0; i < 5; i++ {
		offerItem(t, adm, "m/s1", itemJSON(t, fmt.Sprintf("am%d", i), "agent_message", map[string]any{"text": "prose "}))
		offerItem(t, adm, "m/s2", itemJSON(t, fmt.Sprintf("tr%d", i), "tool_run", map[string]any{"tool": "Read", "status": "in_progress"}))
	}
	approval := itemJSON(t, "ap1", "approval_request", map[string]any{
		"summary": "Bash: rm -rf build", "content_hash": "sha256:beef", "expires_at": "2026-08-07T12:02:00Z",
		"mode": "card", "decisions": []any{map[string]any{"id": "accept", "label": "Allow"}},
	})
	offeredAt := clk.Now()
	offerItem(t, adm, "m/s2", approval)

	clk.Advance(DefaultAppendWindow)
	if err := adm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got := log.all()
	if len(got) < 2 {
		t.Fatalf("only %d appends; the approval never came out", len(got))
	}
	next := got[1]
	rec := decodeItem(t, next.item)
	if rec.Kind != "approval_request" {
		t.Fatalf("the first record released after the approval was offered is a %q (%s), want approval_request: every kind other "+
			"than agent_message takes the head of the queue, approval_request first of all (IS-DELTA-3)", rec.Kind, rec.ItemID)
	}
	if waited := next.at.Sub(offeredAt); waited > DefaultAppendWindow {
		t.Errorf("the approval waited %s behind a backlog of 10 items, want <= one %s window (IS-DELTA-3)", waited, DefaultAppendWindow)
	}
	if string(next.item) != string(approval) {
		t.Errorf("the approval was rewritten in flight:\n got %s\nwant %s\nan approval_request is NEVER merged -- its bytes are the "+
			"content the daemon hashed (IS-APR-2)", next.item, approval)
	}
	if pending := adm.Pending(); pending != 10 {
		t.Errorf("Pending() = %d after the approval jumped the queue, want 10: jumping the queue must not DROP what it jumped", pending)
	}
}

// TestItemAdmission_CeilingIsPerTargetAcrossSessionsAndKinds is IS-DELTA-2a, the governing
// rule and the mutation a per-item_id window would pass everything else with: six sessions
// each streaming their own item_ids share ONE ceiling, because a quota-refused append burns
// an outbound seq (PB-GW-7) and the gap it manufactures stales journal AND terminal
// (PB-SYNC-1). Oldest-first release is the other half: the ceiling must not be a queue one
// loud session monopolizes.
func TestItemAdmission_CeilingIsPerTargetAcrossSessionsAndKinds(t *testing.T) {
	adm, log, clk := newAdmissionHarness()
	start := clk.Now()
	sessions := []string{"m/s1", "m/s2", "m/s3", "m/s4", "m/s5", "m/s6"}
	for frame := 0; frame < 600; frame++ { // 600 * 16 ms = 9.6 s of six dense sessions
		for _, s := range sessions {
			offerItem(t, adm, s, itemJSON(t, fmt.Sprintf("%s-%d", s, frame), "tool_run",
				map[string]any{"tool": "Read", "status": "completed"}))
		}
		clk.Advance(renderDebounceRate)
	}
	elapsed := clk.Now().Sub(start)
	budget := int(elapsed/DefaultAppendWindow) + 2
	got := log.all()
	if len(got) > budget {
		t.Errorf("%d appends over %s = %.1f/s from %d sessions, over the per-TARGET budget of %d: admission is bounded per target "+
			"across every session and kind -- a per-item_id window multiplies by the number of sessions (IS-DELTA-2a)",
			len(got), elapsed, float64(len(got))/elapsed.Seconds(), len(sessions), budget)
	}
	if len(got)*2 < budget {
		t.Errorf("only %d appends over %s against a budget of %d: the floor must SPACE the stream, not stall it", len(got), elapsed, budget)
	}
	served := map[string]int{}
	for _, r := range got {
		served[r.session]++
	}
	for _, s := range sessions {
		if served[s] == 0 {
			t.Errorf("session %s was never released in %s while five others were: the queue releases OLDEST-FIRST, so no session "+
				"is starved (ADR-010 §7)", s, elapsed)
		}
	}
}

// outboundItemFrame is an INDEPENDENT decode of a sealed journal frame carrying an item, so
// the end-to-end assertions read what the PHONE would read, not what the producer kept.
type outboundItemFrame struct {
	Cursor    uint64          `json:"cursor"`
	SessionID string          `json:"session_id"`
	Type      string          `json:"type"`
	Item      json.RawMessage `json:"item"`
}

// TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget is ADR-010 §7's fence: the
// transcript case beside the peek case above. It drives 150 s (crossing the relay's tumbling
// minute boundary twice) of three sessions streaming agent prose at the REAL 16 ms render
// rate, a tool call every ~320 ms, and one approval in the middle -- through the admission
// queue, a real RelaySink and the relay's real per-target quota -- then replays everything
// the relay accepted into the phone's real receiver.
//
// It asserts the four things the floor exists for: no quota refusal, no manufactured gap, no
// LOST content (prose reconstructs byte-for-byte, every tool run keeps both halves), and the
// approval out within one window.
func TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget(t *testing.T) {
	clk := newVClock()
	start := clk.Now()
	key := budgetTestKey()
	app := &quotaAppender{now: clk.Now, perMin: relay.DefaultConfig().Quotas.MailboxAppendPerMin}
	inner := NewRelaySink(RelayConfig{
		Appender:    app,
		Target:      "phone-routing-id",
		EpochID:     7,
		Key:         key,
		SenderKeyID: [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Now:         clk.Now,
	})
	sink := NewCoalescingSink(CoalesceConfig{Inner: inner, Window: DefaultAppendWindow, Now: clk.Now})

	log := &releaseLog{now: clk.Now}
	var cursor uint64
	var sinkErrs int
	adm := NewItemAdmission(ItemAdmissionConfig{
		Window: DefaultAppendWindow,
		Now:    clk.Now,
		Append: func(session string, item json.RawMessage) error {
			_ = log.append(session, item)
			cursor++
			// The gateway forwards a journal record IMMEDIATELY and never coalesces it
			// (R-GW.5): the queue upstream is the only thing spacing this stream.
			if err := sink.Event(protocol.JournalRecord{Cursor: cursor, SessionID: session, Type: "interaction", Item: item}); err != nil {
				sinkErrs++
				return err
			}
			return nil
		},
	})

	const run = 150 * time.Second
	sessions := []string{"m/s1", "m/s2", "m/s3"}
	prose := map[string]string{}
	toolRuns := map[string]bool{}
	approval := itemJSON(t, "ap1", "approval_request", map[string]any{
		"summary": "Bash: rm -rf build", "content_hash": "sha256:beef", "mode": "card",
	})
	approvalOffered := false
	var approvalOfferedAt time.Time
	approvalAt := start.Add(45 * time.Second)

	for frame := 0; clk.Now().Sub(start) < run; frame++ {
		for _, s := range sessions {
			tok := fmt.Sprintf("%d ", frame)
			prose[s] += tok
			offerItem(t, adm, s, itemJSON(t, "am-"+s, "agent_message", map[string]any{"text": tok, "status": "in_progress"}))
		}
		if frame%20 == 0 { // a tool call roughly every 320 ms: open, then close
			id := fmt.Sprintf("tr-%d", frame)
			toolRuns[id] = true
			offerItem(t, adm, "m/s1", itemJSON(t, id, "tool_run", map[string]any{"tool": "Read", "status": "in_progress"}))
			offerItem(t, adm, "m/s1", itemJSON(t, id, "tool_run", map[string]any{"status": "completed", "exit_code": 0}))
		}
		if !approvalOffered && !clk.Now().Before(approvalAt) {
			approvalOffered, approvalOfferedAt = true, clk.Now()
			offerItem(t, adm, "m/s2", approval)
		}
		clk.Advance(renderDebounceRate)
	}
	for i := 0; adm.Pending() > 0; i++ {
		if i > 100_000 {
			t.Fatalf("the queue would not drain: %d items still pending", adm.Pending())
		}
		clk.Advance(DefaultAppendWindow)
		if err := adm.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}
	elapsed := clk.Now().Sub(start)
	stored, refused := app.snapshotState()

	// (a) NO QUOTA REFUSAL and (b) UNDER BUDGET. Two appends per tool call plus a delta per
	// render tick is ~200 items/s offered; unbounded, that is refused within seconds.
	if refused != 0 || sinkErrs != 0 {
		t.Errorf("the relay refused %d of %d appends (%d sink errors) over %s of transcript: the producer must hold the stream "+
			"under MailboxAppendPerMin=%d itself, because the gateway may not coalesce a journal record (R-GW.5, PB-GW-7)",
			refused, refused+len(stored), sinkErrs, elapsed, app.perMin)
	}
	budget := int(elapsed/DefaultAppendWindow) + 2
	if len(stored) > budget {
		t.Errorf("%d appends over %s = %.1f/s, over the §6.0 budget of %d (<= 8/s combined)", len(stored), elapsed,
			float64(len(stored))/elapsed.Seconds(), budget)
	}
	if floor := int(elapsed.Seconds()) * 4; len(stored) < floor {
		t.Errorf("only %d appends reached the phone over %s (< %d, i.e. 4/s): the floor must SPACE the transcript, not silence it",
			len(stored), elapsed, floor)
	}

	// (c) Replay what the relay accepted into the phone's real receiver: no manufactured gap,
	// and the transcript reconstructs LOSSLESSLY from what arrived.
	phone := crypto.NewMailboxReceiver()
	var gaps int
	gotProse := map[string]string{}
	toolSeen := map[string]int{}
	toolTool := map[string]string{}
	toolExit := map[string]bool{}
	approvals := 0
	var approvalRaw json.RawMessage
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
		var f outboundItemFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			t.Fatalf("append %d plaintext is not decodable: %v", i, err)
		}
		if f.Type != "interaction" {
			t.Fatalf("append %d carries type %q, want interaction", i, f.Type)
		}
		rec := decodeItem(t, f.Item)
		switch rec.Kind {
		case "agent_message":
			gotProse[f.SessionID] += rec.Text
		case "tool_run":
			toolSeen[rec.ItemID]++
			if rec.Tool != "" {
				toolTool[rec.ItemID] = rec.Tool
			}
			if rec.ExitCode != nil {
				toolExit[rec.ItemID] = true
			}
		case "approval_request":
			approvals++
			approvalRaw = f.Item
		}
	}
	if gaps != 0 {
		t.Errorf("the phone saw %d GAPS with no relay failure: a refused append burns an outbound seq and the gap stales journal "+
			"AND terminal (PB-SYNC-1)", gaps)
	}
	for _, s := range sessions {
		if gotProse[s] != prose[s] {
			t.Errorf("session %s reconstructed %d bytes of prose, want %d: merging agent_message increments is LOSSLESS "+
				"concatenation, and a hole is invisible to a consumer that rebuilds by concatenating in cursor order (IS-DELTA-1)",
				s, len(gotProse[s]), len(prose[s]))
			break
		}
	}
	for id := range toolRuns {
		if toolSeen[id] == 0 {
			t.Fatalf("tool run %s never reached the phone: the floor MERGES, it never drops (ADR-010 §7)", id)
		}
		if toolSeen[id] > 2 {
			t.Fatalf("tool run %s produced %d records from 2 offers", id, toolSeen[id])
		}
		if toolTool[id] == "" || !toolExit[id] {
			t.Fatalf("tool run %s came back without its tool name (%q) or its exit code (%v): a collapse is a lossless UNION of "+
				"the open and the close, never a replacement (ADR-010 §7)", id, toolTool[id], toolExit[id])
		}
	}
	if approvals != 1 {
		t.Errorf("the approval reached the phone %d times, want exactly 1 (never merged, never duplicated)", approvals)
	} else if string(approvalRaw) != string(approval) {
		t.Errorf("the approval arrived rewritten:\n got %s\nwant %s", approvalRaw, approval)
	}
	var approvalReleasedAt time.Time
	for _, r := range log.all() {
		if decodeItem(t, r.item).Kind == "approval_request" {
			approvalReleasedAt = r.at
			break
		}
	}
	if waited := approvalReleasedAt.Sub(approvalOfferedAt); waited > DefaultAppendWindow {
		t.Errorf("the approval waited %s under a saturated transcript, want <= one %s window: approval_request is never merged and "+
			"takes the HEAD of the queue, so it never waits behind a backlog of prose (IS-DELTA-3)", waited, DefaultAppendWindow)
	}
}

// --- R3: ONE ceiling per target, shared by item releases AND snapshot releases ---------
//
// FAILING-FIRST (TDD RED, GG-5) for review finding R3.
//
// THE DEFECT. The two admissions above are INDEPENDENT and each one spends the WHOLE budget.
// ItemAdmission releases up to one item per DefaultAppendWindow (8/s); CoalescingSink admits
// terminal snapshots at one per DefaultAppendWindow (8/s); and an item release BECOMES a
// journal record, which the sink forwards IMMEDIATELY and never coalesces (R-GW.5). Two 8/s
// admissions against ONE target is 16/s worst case, against a relay that caps that target at
// MailboxAppendPerMin=600 (10/s) on a tumbling minute. The overrun is not merely late: a
// quota-refused append burns an outbound seq (PB-GW-7) and the manufactured gap stales
// journal AND terminal alike (PB-SYNC-1).
//
// §6.0 binds the two TOGETHER -- "<= 8 appends/s sustained across journal AND terminal
// combined (they share one sink and one target)" -- and IS-DELTA-2a is the governing rule:
// "admission SHALL be bounded per target and SHALL govern every kind ... IS-DELTA-3 orders
// the queue; it exempts nobody from it."
//
// THE ARBITRATION this test pins, and where it comes from. A journal record may not be
// delayed at the sink (R-GW.5), so under simultaneous pressure the TERMINAL is the stream
// that yields -- which is also the direction ADR-009 (2) already committed to: "no snapshot
// frames are appended to a phone ... the machine->phone append budget in (7) is spent by the
// journal alone", and (7): "with no snapshot appends, the transcript inherits the whole of
// what the peek used to spend". Yielding is not loss: the stash is latest-wins per session,
// so the peek ships its newest grid the moment the transcript goes quiet -- asserted below,
// because "the terminal yields" must not decay into "the terminal is dead".
func TestAppendBudget_ItemReleasesAndSnapshotsShareOneCeiling(t *testing.T) {
	clk := newVClock()
	start := clk.Now()
	key := budgetTestKey()
	app := &quotaAppender{now: clk.Now, perMin: relay.DefaultConfig().Quotas.MailboxAppendPerMin}
	inner := NewRelaySink(RelayConfig{
		Appender:    app,
		Target:      "phone-routing-id",
		EpochID:     7,
		Key:         key,
		SenderKeyID: [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Now:         clk.Now,
	})
	sink := NewCoalescingSink(CoalesceConfig{Inner: inner, Window: DefaultAppendWindow, Now: clk.Now})

	var (
		cursor       uint64
		itemReleases int
		offerErrs    int
		terminalErrs int
	)
	adm := NewItemAdmission(ItemAdmissionConfig{
		Window: DefaultAppendWindow,
		Now:    clk.Now,
		Append: func(session string, item json.RawMessage) error {
			cursor++
			itemReleases++
			// R-GW.5: an item release IS an append the instant the floor lets it go. The
			// gateway forwards a journal record immediately and may not coalesce it.
			return sink.Event(protocol.JournalRecord{Cursor: cursor, SessionID: session, Type: "interaction", Item: item})
		},
	})

	// SIMULTANEOUS PRESSURE on one target: a live peek at the real 16 ms render debounce
	// (~62 snapshots/s) and a transcript streaming prose at the same rate. 150 s crosses the
	// relay's tumbling minute boundary twice.
	const run = 150 * time.Second
	for frame := 0; clk.Now().Sub(start) < run; frame++ {
		if err := sink.Terminal(protocol.TerminalViewV1{Session: "m/s1", Lines: []string{fmt.Sprintf("frame %d", frame)}, Cols: 80, Rows: 24}); err != nil {
			terminalErrs++
		}
		item := itemJSON(t, "am1", "agent_message", map[string]any{"text": fmt.Sprintf("%d ", frame), "status": "in_progress"})
		if err := adm.Offer("m/s1", item); err != nil {
			offerErrs++ // the RELEASE's error surfaces here; being queued is never one
		}
		clk.Advance(renderDebounceRate)
	}
	elapsed := clk.Now().Sub(start)
	duringStorm, refused := app.snapshotState()

	// (a) THE COMBINED CEILING. Both streams pressing at once must not buy two budgets.
	budget := int(elapsed/DefaultAppendWindow) + 2
	if got := len(duringStorm); got > budget {
		t.Errorf("%d appends over %s = %.1f/s against ONE target with a transcript and a peek pressing simultaneously, over the "+
			"§6.0 ceiling of %d (<= 8/s COMBINED across journal and terminal): the item floor and the terminal coalescer are two "+
			"INDEPENDENT %s admissions, i.e. 2x the ceiling -- admission is bounded PER TARGET and exempts nobody (IS-DELTA-2a)",
			got, elapsed, float64(got)/elapsed.Seconds(), budget, DefaultAppendWindow)
	}

	// (b) NO QUOTA REFUSAL. 16/s against MailboxAppendPerMin=600 (10/s) exhausts the tumbling
	// window in ~37 s and is refused for the rest of every minute after that.
	if refused != 0 || offerErrs != 0 || terminalErrs != 0 {
		t.Errorf("the relay refused %d of %d appends (%d item offers and %d snapshots saw the error) over %s: two independent "+
			"admissions against one MailboxAppendPerMin=%d target overrun it (PB-GW-7)",
			refused, refused+len(duringStorm), offerErrs, terminalErrs, elapsed, app.perMin)
	}

	// The transcript goes quiet, and the peek renders one last grid. Idle wakes (RunTerminal
	// calls Flush on every one) must ship it: the shared ceiling THROTTLES the live tail, it
	// does not silence it permanently, and the debt a saturated transcript ran up must be
	// bounded by the ceiling itself rather than accumulate without limit.
	const finalGrid = "the last grid"
	if err := sink.Terminal(protocol.TerminalViewV1{Session: "m/s1", Lines: []string{finalGrid}, Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("final terminal frame: %v", err)
	}
	for i := 0; i < 8; i++ {
		clk.Advance(DefaultAppendWindow)
		if err := sink.Flush(); err != nil {
			t.Fatalf("idle-wake flush %d: %v", i, err)
		}
	}
	stored, _ := app.snapshotState()

	// (c) Replay everything the relay accepted into the phone's REAL receiver: a burned seq
	// shows up here as a Gap, which PB-SYNC-1 charges to journal and terminal alike.
	phone := crypto.NewMailboxReceiver()
	var gaps int
	journalSeen := map[uint64]int{}
	lastGrid := ""
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
		var kind outboundFrame
		if err := json.Unmarshal(res.Plaintext, &kind); err != nil {
			t.Fatalf("append %d plaintext is not decodable: %v", i, err)
		}
		if kind.Kind == "terminal_snapshot" {
			var snap protocol.TerminalSnapshot
			if err := json.Unmarshal(res.Plaintext, &snap); err != nil {
				t.Fatalf("append %d does not decode as a terminal snapshot: %v", i, err)
			}
			if len(snap.Lines) > 0 {
				lastGrid = snap.Lines[0]
			}
			continue
		}
		var f outboundItemFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			t.Fatalf("append %d plaintext is not decodable: %v", i, err)
		}
		if f.Type != "interaction" {
			t.Fatalf("append %d carries type %q, want interaction", i, f.Type)
		}
		journalSeen[f.Cursor]++
	}
	if gaps != 0 {
		t.Errorf("the phone saw %d GAPS with no relay failure: a refused append burns an outbound seq and the gap stales journal "+
			"AND terminal (PB-GW-7, PB-SYNC-1)", gaps)
	}

	// (d) THE JOURNAL IS NEVER THE STREAM THAT YIELDS. Every item the floor released reached
	// the phone exactly once: the shared ceiling may not be paid for by delaying or dropping a
	// journal record (R-GW.5).
	for c := uint64(1); c <= uint64(itemReleases); c++ {
		if n := journalSeen[c]; n != 1 {
			t.Fatalf("interaction record cursor=%d reached the phone %d times, want exactly 1 (%d of %d releases arrived): a "+
				"journal record is never coalesced, deferred or dropped (R-GW.5)", c, n, len(journalSeen), itemReleases)
		}
	}

	// (e) THE PEEK YIELDS, IT DOES NOT DIE. Latest-wins means a yielded frame is superseded,
	// never lost, so the newest grid must arrive once the transcript stops competing for the
	// slot (PB-APP-4).
	if lastGrid != finalGrid {
		t.Errorf("the newest grid the phone would show is %q, want %q: under the shared ceiling the terminal yields to the journal "+
			"(R-GW.5, ADR-009 (2)), but a yielded snapshot is superseded, not lost -- it must ship on the first idle wake after the "+
			"transcript goes quiet", lastGrid, finalGrid)
	}
}
