package daemon

import (
	"context"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/vt"
)

// terminalrender.go is the daemon-side render loop (A7 renderer slice E, ADR-007
// Decision 2): it turns a session's raw VT output stream into sanitized
// plain-text snapshots, server side, and pushes one snapshot per debounced
// change. It is read-only — no input ever flows back to the session.
//
// This is the SECURITY choke point: raw, potentially hostile PTY bytes meet the
// real vt.Emulator and SnapText here. Every byte the loop pushes has passed
// through SnapText, which strips every C0/C1 control, DEL, and embedded newline,
// so no terminal escape sequence can reach the phone regardless of what the
// session emits.
//
// Package seam: internal/protocol already imports internal/daemon, so this
// package cannot import protocol. The loop therefore takes a daemon-local
// TerminalStream (a structural subset of protocol.SessionStream) and emits a
// daemon-local TerminalRender; the terminal_subscribe handler (slice F2) drives
// this loop over a read-only tap and maps each TerminalRender onto
// protocol.TerminalSnapshot on the daemon->gateway side where both types are visible.

const (
	// renderDebounceWindow coalesces a burst of output frames into a single
	// snapshot: frames arriving within the window of the first un-rendered frame
	// render once, when the window elapses.
	renderDebounceWindow = 16 * time.Millisecond
	// renderPollInterval is how often the loop checks whether the debounce
	// window has elapsed. It is well under the window so a settled burst renders
	// promptly.
	renderPollInterval = 4 * time.Millisecond
	// renderDefaultCols/Rows size the emulator when the initial snapshot cannot
	// be decoded (e.g. an empty stream), so the loop still renders live frames.
	renderDefaultCols = 80
	renderDefaultRows = 24
)

// TerminalStream is the read-only half of a session's shim pipe the render loop
// consumes: the initial grid snapshot and the live output frames. It is a
// structural subset of protocol.SessionStream, so a real SessionStream satisfies
// it without this package importing protocol.
type TerminalStream interface {
	Snapshot() []byte
	Frames() <-chan []byte
}

// TerminalRender is one server-rendered, sanitized terminal snapshot: a session's
// VT grid flattened to plain-text rows. It mirrors protocol.TerminalSnapshot
// (which this package cannot name, see the seam note above); the terminal_subscribe
// handler maps one to the other at the daemon->gateway boundary.
type TerminalRender struct {
	Session string
	Lines   []string
	Cols    int
	Rows    int
}

// RenderTerminal runs the render loop until ctx is cancelled or the stream's
// Frames() channel closes. It pushes the stream's initial snapshot first, then
// feeds each output frame into a private emulator, coalesces bursts with the
// debouncer, and pushes a sanitized snapshot per debounced change. A final
// snapshot is flushed when the stream closes with unrendered output pending, so
// the last push always reflects the latest state. It owns and closes its
// emulator, leaving no goroutine behind.
func RenderTerminal(ctx context.Context, session string, stream TerminalStream, push func(TerminalRender)) {
	// Decode + push the initial snapshot ONCE and reuse the SAME decoded grid to seed the
	// emulator, so the live loop starts from the real initial screen. Without the seed the
	// first live frame is applied onto a BLANK grid and every initial cell is lost (mirrors
	// skeleton.seedMirror, which feeds vt.RenderSnapshot(s) into a fresh emulator).
	initial := renderInitial(session, stream, push)

	cols, rows := renderDefaultCols, renderDefaultRows
	if initial != nil {
		cols, rows = initial.Cols, initial.Rows
	}
	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()
	if initial != nil {
		emu.Feed(vt.RenderSnapshot(initial)) // seed: repaint the initial grid into the emulator
	}

	// Reuse the journal delivery-layer debouncer for its window/coalesce timing
	// rather than hand-rolling one. It only coalesces group_transition records,
	// so each output frame is offered as a synthetic group_transition keyed by
	// session: a burst collapses to a single pending record whose window is
	// anchored at the first frame, and Drain reports it once the window elapses.
	// The record is a timing pulse only; the rendered content comes from the
	// emulator's current state at Drain time, never from the record.
	deb := journal.NewDebouncer(renderDebounceWindow, nil)
	ticker := time.NewTicker(renderPollInterval)
	defer ticker.Stop()

	frames := stream.Frames()
	dirty := false // output fed since the last render
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-frames:
			if !ok {
				if dirty {
					renderEmulator(emu, session, push) // flush the final state
				}
				return
			}
			emu.Feed(chunk)
			deb.Offer(journal.Record{Type: journal.TypeGroupTransition, SessionID: session})
			dirty = true
		case now := <-ticker.C:
			if len(deb.Drain(now)) > 0 {
				renderEmulator(emu, session, push)
				dirty = false
			}
		}
	}
}

// renderInitial decodes and pushes the stream's initial snapshot, returning the decoded
// Snap so the caller can SEED the emulator from the SAME grid (so the first live frame is
// applied on top of the initial screen, not a blank one). An undecodable snapshot pushes
// nothing and returns nil, so the loop falls back to default dimensions and an empty grid.
func renderInitial(session string, stream TerminalStream, push func(TerminalRender)) *vt.Snap {
	snap, err := vt.DecodeSnapshot(stream.Snapshot())
	if err != nil {
		return nil
	}
	push(TerminalRender{Session: session, Lines: vt.SnapText(snap), Cols: snap.Cols, Rows: snap.Rows})
	return snap
}

// renderEmulator snapshots the emulator's current grid, flattens it to sanitized
// plain text, and pushes it. A snapshot/decode error pushes nothing rather than
// emitting a partial or unsanitized render.
func renderEmulator(emu *vt.Emulator, session string, push func(TerminalRender)) {
	b, err := emu.Snapshot()
	if err != nil {
		return
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		return
	}
	push(TerminalRender{Session: session, Lines: vt.SnapText(snap), Cols: snap.Cols, Rows: snap.Rows})
}
