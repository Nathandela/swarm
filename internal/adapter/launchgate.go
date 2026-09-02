package adapter

// The LAUNCH GATE is an optional adapter extension (ADR-025): a modal a CLI raises ON ITS
// OWN, before any prompt, that the launch itself already answers. Claude Code's folder-trust
// dialog is the one recorded today: swarm launched the session in that directory because
// the owner named it, or an agent chose it under the daemon's own policy, so "do you trust
// this folder" is a question the launch has answered before the CLI asked it.
//
// It is pure data out over the rendered grid, exactly like ApprovalApplier, and for the same
// reason: the core does the typing, and the adapter -- which owns no fd -- says only which
// keys answer WHICH screen. ABSENCE IS A SIGNAL (ADR-010 section 5): an adapter that
// implements nothing here has no startup gate anybody recorded, and the daemon types
// nothing rather than guessing.

import "github.com/Nathandela/swarm/internal/vt"

// LaunchGateAnswerer is implemented by an adapter whose CLI can put a recorded startup
// gate on the glass, and knows the keys that answer it.
type LaunchGateAnswerer interface {
	// LaunchGateKeys returns the keystrokes that ACCEPT the gate currently on snap, to be
	// written to the session's PTY verbatim as one complete answer (select and confirm).
	//
	// ok is false for any grid the adapter cannot positively identify as that gate, with
	// its options on screen and the selection state readable. A refusal types nothing; a
	// key returned for the wrong screen is typed into whatever has focus.
	LaunchGateKeys(snap *vt.Snap) (keys string, ok bool)
}

// AsLaunchGateAnswerer reports whether a records a startup gate answer.
func AsLaunchGateAnswerer(a Adapter) (LaunchGateAnswerer, bool) {
	g, ok := a.(LaunchGateAnswerer)
	return g, ok
}
