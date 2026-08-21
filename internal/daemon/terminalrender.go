package daemon

import "time"

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
// daemon-local TerminalView (terminalview.go); the terminal_subscribe handler drives that
// loop over a read-only tap and maps each view onto BOTH protocol.TerminalSnapshot and
// protocol.TerminalViewV1 on the daemon->gateway side where those types are visible.

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

// THE LEGACY RENDER LOOP AND ITS PROJECTION TYPE ARE GONE (Wave R8 CLOSING round).
//
// `RenderTerminal` and `TerminalRender` were kept through the wave's first three rounds on
// the stated ground that ADR-017 T4 "keeps the legacy TerminalSnapshot path on the wire
// unchanged, so the versioned view is a SECOND CONSUMER of the same choke point rather than a
// replacement for it". That reason stopped being true the moment finding 5 was fixed: the peek
// handler now drives `RenderTerminalView` directly and builds BOTH wire bodies -- the frozen
// `TerminalSnapshot` and the versioned `TerminalViewV1` -- from the one view it is handed. The
// legacy BODY is still on the wire, byte for byte; what had no caller left was this
// projection.
//
// The whole-repo gate is what said so: `internal/verify`'s B94 reachability check failed with
// "internal/daemon.RenderTerminal -- 1 unreachable exported symbol", which is precisely the
// fence-rot class this wave's own closing review is about. B94 offers two answers, DELETE or a
// ledger row with a stated reason, and a ledger row would have had to say "kept because the
// legacy path is on the wire" -- a sentence that reads true and is not. So it is deleted, and
// the render corpus in terminalrender_test.go now drives `RenderTerminalView`, which is the
// function production actually runs.
//
// What stays here is what BOTH loops always shared and what only this file declares: the
// debounce and poll constants, the default geometry, and the `TerminalStream` seam.
