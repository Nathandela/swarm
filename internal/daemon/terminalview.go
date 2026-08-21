package daemon

// WAVE R8 -- TerminalViewV1's PRODUCER (ADR-017 T4 and amendment T4-a).
//
// THE DEFECT T4-a IS ABOUT, AND IT IS NOT HYPOTHETICAL. The render loop is PER INVOCATION:
// it builds a fresh vt.Emulator on every call and seeds it from whatever initial snapshot
// the stream happens to carry. The gateway's watcher RE-RUNS it after every transport
// hiccup, forever, silently. So the second run's snapshots are a NEW screen with a counter
// that starts again -- and the phone's only sane rule, "drop anything not strictly greater
// than what I hold", then discards every snapshot of the second run. There is no error, no
// reconnect banner, nothing to report: the user is looking at a plausible screen that
// stopped being true minutes ago, on a session they may be about to type into.
//
// So a VIEW EPOCH minted per render-loop START, a REVISION strictly increasing within it,
// and reset=true on the FIRST EMISSION OF EVERY EPOCH ON EVERY PATH -- which has to be
// LOOP STATE rather than a property of whichever function happened to push, because
// renderInitial pushes NOTHING when the initial snapshot cannot be decoded and the epoch's
// first snapshot then arrives from the emulator, which has no idea it is first.
//
// THIS IS THE ONLY RENDER LOOP IN THE DAEMON. The first three rounds of R8 kept
// `RenderTerminal` and `TerminalRender` beside it on the stated ground that ADR-017 T4 keeps
// the legacy `TerminalSnapshot` path on the wire, so the versioned view was "a second
// consumer of the same choke point". The closing round's fix for finding 5 made that reason
// false: the peek handler drives THIS function and builds BOTH wire bodies from the one view
// it is handed, so the legacy projection had no caller left and the whole-repo B94
// reachability gate said so. It is deleted; the legacy BODY is unchanged on the wire.
//
// The flattening still goes through vt.SnapText, which stays the one sanitization choke point
// (ADR-009 decision 2 as re-scoped: sanitization is machine-side, no VT emulator crosses the
// gomobile boundary, and raw PTY bytes never reach the phone).

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/vt"
)

// TerminalView is one full coalesced snapshot of a terminal_fallback session's sanitized
// screen, in the daemon's own terms. The gateway projects it onto schema.TerminalViewV1.
//
// There is no patch language and no delta, deliberately (T4): a slow observer drops
// superseded snapshots and receives the newest COMPLETE revision, which is what the
// gateway's coalescer already does and what T4 makes a wire contract.
type TerminalView struct {
	Session         string
	SessionInstance string
	ViewEpoch       uint64
	Revision        uint64
	Reset           bool
	Lines           []string
	Cols, Rows      int
	RenderedAt      time.Time
}

// viewEpochSeq mints a fresh epoch per render-loop start. It is process-global and strictly
// increasing, so no two runs of the loop WITHIN ONE DAEMON PROCESS -- for the same session or
// any other -- can share one.
//
// ACROSS PROCESSES IT COLLIDES, AND THAT IS WHAT `reset` IS FOR (closing round 2). The counter
// starts again at zero in every daemon process, and sessions surviving a daemon crash, restart
// or upgrade is a designed property of this system: a restarted daemon re-mints epoch 1 under a
// phone still holding epoch 1 at revision N, so an ordering rule written over (epoch, revision)
// alone would discard the restarted daemon's whole first run and leave the user reading a
// plausible, frozen, pre-restart screen. `Reset` is set on the first emission of every epoch on
// every path, and the phone's cache adopts a reset frame unconditionally
// (internal/phonecore/snapshot.go SnapshotCache.Apply). Persisting the counter would be a second
// way to say the same thing, and the marker is the one the protocol already carries.
var viewEpochSeq atomic.Uint64

// RenderTerminalView is RenderTerminal's versioned sibling: the same loop, the same
// debouncer, the same sanitization choke point, emitting TerminalView.
//
// stillAllowed is polled EVERY tick, unchanged from the pre-R8 loop, and T4-b/T6-e widen
// what the CALLER puts behind that predicate: kill switch AND capability AND watch
// liveness, one predicate on the tick that already exists, so the watch horizon costs no
// new loop. A nil stillAllowed means always-allowed.
func RenderTerminalView(ctx context.Context, session, instance string, stream TerminalStream, stillAllowed func() bool, push func(TerminalView)) {
	// The epoch is minted HERE, at loop start, and not inside the emission helper. Minting
	// it downstream is the frozen-screen defect above: every re-run would reuse an epoch
	// while restarting its revision.
	epoch := viewEpochSeq.Add(1)
	var revision uint64
	first := true // "this epoch has emitted nothing yet" -- LOOP STATE, not a function's identity
	emit := func(lines []string, cols, rows int) {
		revision++
		push(TerminalView{
			Session:         session,
			SessionInstance: instance,
			ViewEpoch:       epoch,
			Revision:        revision,
			Reset:           first,
			Lines:           lines,
			Cols:            cols,
			Rows:            rows,
			// The MACHINE's clock. T4-b's staleness indicator is derived from the
			// snapshot's own age: a replayed backlog arrives all at once and a held relay
			// delivers old content at a new instant, so arrival time would render a quiet
			// machine as an idle terminal.
			RenderedAt: time.Now().UTC(),
		})
		first = false
	}

	initial := renderInitialView(stream, emit)

	cols, rows := renderDefaultCols, renderDefaultRows
	if initial != nil {
		cols, rows = initial.Cols, initial.Rows
	}
	emu := vt.NewEmulator(cols, rows)
	defer func() { _ = emu.Close() }()
	if initial != nil {
		emu.Feed(vt.RenderSnapshotClipped(initial, 0, 0))
	}

	deb := journal.NewDebouncer(renderDebounceWindow, nil)
	ticker := time.NewTicker(renderPollInterval)
	defer ticker.Stop()

	frames := stream.Frames()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-frames:
			if !ok {
				if dirty {
					renderEmulatorView(emu, emit) // flush the final state
				}
				return
			}
			emu.Feed(chunk)
			deb.Offer(journal.Record{Type: journal.TypeGroupTransition, SessionID: session})
			dirty = true
		case now := <-ticker.C:
			// Liveness gate FIRST, before the drain, and before any emission: a watch whose
			// lease expired, whose capability was withdrawn or whose session was replaced
			// must stop rendering, sealing and appending on the loop's OWN clock. The phone
			// may be gone -- that is the case the horizon exists for.
			if stillAllowed != nil && !stillAllowed() {
				return
			}
			if len(deb.Drain(now)) > 0 {
				renderEmulatorView(emu, emit)
				dirty = false
			}
		}
	}
}

// renderInitialView decodes and emits the stream's initial snapshot, returning the decoded
// Snap so the caller can SEED the emulator from the SAME grid. An undecodable snapshot
// emits nothing and returns nil -- and the reset marker survives that, because `first` is
// loop state that this function never gets to clear.
func renderInitialView(stream TerminalStream, emit func([]string, int, int)) *vt.Snap {
	snap, err := vt.DecodeSnapshot(stream.Snapshot())
	if err != nil {
		return nil
	}
	emit(vt.SnapText(snap), snap.Cols, snap.Rows)
	return snap
}

// renderEmulatorView snapshots the emulator's current grid, flattens it to sanitized
// plain text, and emits it. A snapshot/decode error emits nothing rather than shipping a
// partial or unsanitized render -- and, because the revision is incremented inside emit,
// a suppressed emission consumes no revision either.
func renderEmulatorView(emu *vt.Emulator, emit func([]string, int, int)) {
	b, err := emu.Snapshot()
	if err != nil {
		return
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		return
	}
	emit(vt.SnapText(snap), snap.Cols, snap.Rows)
}
