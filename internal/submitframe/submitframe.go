// Package submitframe is the shared submit-boundary framing rule (bead
// agents-tracker-abyz, ADR-010 Amendment 1 A2): how input bytes are cut into PTY writes so a
// CLI reads a submit as a submit, and how far a submit is held off the text before it.
//
// THE MIXTURE IS THE DEFECT (bead agents-tracker-r3p, spike-SA finding #1, measured against
// the real CLIs). A PTY write carrying text AND the carriage return that submits it is read
// by Claude Code's TUI as a multi-line PASTE: the CR is inserted into the input box as a
// literal newline instead of submitting, the prompt sits there unsent, and the next turn's
// text is appended to the SAME unsent draft. Nothing reports it on either side.
//
// It is a leaf -- stdlib only -- because the rule now has more than one caller: the phone's
// coalescer (internal/phonecore), the gateway's lease writer (internal/remotegw), and the
// daemon-side send_input writer of ADR-010 A2. Two copies of a rule this quiet drift apart
// without anything failing loudly enough to notice.
package submitframe

import "time"

// Gap is the minimum spacing between a text frame and a submit-only frame that follows it,
// at the PTY. It is spike S-A's measured value (docs/verification/spike-SA.md finding #1):
// the harness that made a real Claude Code submit reliably wrote the text, slept 150 ms,
// then wrote the CR. Nothing establishes that less is enough, so this is the number the fix
// owes rather than one tuned for latency.
//
// SEPARATE FRAMES ARE NECESSARY AND NOT SUFFICIENT: the paste heuristic keys on co-arrival
// in one read tick at the PTY, so any hop whose batching compresses two separately-emitted
// frames back together recreates the mixed write. The gap belongs at the last hop that can
// guarantee it survives to the PTY.
const Gap = 150 * time.Millisecond

// IsSubmit reports whether b is a byte a CLI reads as "run what I typed".
func IsSubmit(b byte) bool { return b == '\r' || b == '\n' }

// IsSubmitOnly reports whether buf is nothing but submit bytes -- the whole class of frames
// Gap applies to. An empty buffer is not one of them: there is nothing to hold off anything.
func IsSubmitOnly(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	for _, b := range buf {
		if !IsSubmit(b) {
			return false
		}
	}
	return true
}

// FrameLen is how many leading bytes of buf may share ONE write: a maximal run of submit
// bytes or a maximal run of ordinary ones, never a mixture, capped at max. A caller drains a
// buffer by taking FrameLen bytes, writing them, and repeating while bytes remain; every run
// it hands back is homogeneous, so IsSubmitOnly classifies it the same way its first byte
// does.
//
// A RUN, NOT ONE BYTE PER WRITE. A held Enter is a ~30 Hz stream of submits; one submit per
// paced frame would drain slower than they arrive, so the backlog would grow without bound
// and land minutes after the key was pressed. A run keeps output rate equal to input rate,
// and a write of nothing but submit bytes carries no text for the paste heuristic to
// swallow.
func FrameLen(buf []byte, max int) int {
	if len(buf) == 0 {
		return 0
	}
	submit := IsSubmit(buf[0])
	n := 1
	for n < len(buf) && IsSubmit(buf[n]) == submit {
		n++
	}
	return min(n, max)
}
