package skeleton

// APPLY BY INJECTION -- mirror-program.md section 3, the M1 design decision.
//
// The phone's Allow/Deny is applied by writing the CLI's OWN dialog keys into the PTY the
// daemon already owns, gated on the live grid still showing that dialog. This file is the
// daemon-side primitive; approval.go holds the validation that decides whether to call it.
//
// WHY NOT THE HELD HOOK. The audit's alternative was for `swarm hook` to hold the
// PermissionRequest connection open until the phone answered, and for the daemon to write the
// decision back on it. mirror-program.md rejects that for the interactive path on CO-PRESENCE
// grounds: while a PermissionRequest hook is undecided the CLI has not drawn its own dialog, so
// holding it indefinitely HIDES THE TERMINAL PROMPT -- and both rooms staying live is the
// program's central ruling. The dialog therefore appears in the terminal exactly as before, and
// the phone answers the same dialog the owner is looking at. First-answer-wins falls out by
// construction.
//
// WHY IT GOES THROUGH THE SHARED TAP AND NOT A FRESH SHIM DIAL. A session's shim serves ONE
// subscriber at a time; the tap (sessiontap.go) is the tee that lets the owner's attach, the
// phone's peek and this injection coexist on one upstream. Subscribing here therefore JOINS
// whatever is already open instead of stealing it -- copresence_test.go is the proof that
// concurrent subscribers do not evict each other -- and needs no new shim protocol.
//
// WHY THE GRID READ AND THE WRITE SHARE ONE SUBSCRIPTION. The gate ("is the dialog still up?")
// and the keystroke go through ONE handle: a tap subscription is SEEDED with the grid as of the
// moment it joined and writes back through that same handle, so no SECOND DIAL interleaves
// between the read and the write.
//
// WHAT THAT DOES NOT BUY, said here rather than left to be inferred from the sentence above.
// The seed is either the shim's snapshot fetched over the wire during the dial or this daemon's
// MIRROR of a frame stream that arrives with transport latency, and sub.Input travels back over
// the same wire. The screen the recognizer judged is therefore the screen as of the SEED and
// not as of the write, so the terminal-answered-first race is NARROWED TO ONE TAP ROUND TRIP,
// not closed -- and it cannot be closed from here, because this side owns neither the glass nor
// the keyboard. What bounds the residue is M1.1's recording rather than this file: the keys
// carry NO ENTER, so a digit that lands after the dialog has gone sits in the composer
// un-submitted, visible and deletable, instead of being an answer the agent acts on.

import (
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// injectWatchdogDelay is how long the daemon waits before looking again at a dialog it has
// just typed at. It is not a timeout on anything: the injection is complete the moment the
// bytes are written, and this only decides how soon SILENCE becomes a note on the transcript.
//
// A var rather than a const so a test can shorten it; production never writes it.
var injectWatchdogDelay = 5 * time.Second

var (
	// errNoDialog is the GATE's refusal: the live grid does not positively show THIS request's
	// own dialog, as a screen the session's adapter has a recorded key map for. It covers the
	// terminal-answered-first race, the unknown-screen case (a claude that moved off the
	// recorded version), and the chained-dialog case where the screen shows an answerable
	// dialog raised by a different tool than the request's (M1.8). All three must refuse rather
	// than type, and the message names none of them: the owner's card is refused for the same
	// reason in every case, and the daemon does not know which.
	errNoDialog = errors.New("the session's screen does not show the permission dialog this request raised")
	// errNoApplier is the CAPABILITY refusal: this session's CLI is not answered by keystroke at
	// all (mirror-program.md's table answers Codex by native RPC and opencode over HTTP). It is
	// absence, not breakage -- ADR-010 §5's posture -- and it carries no D10 code for
	// ApproveInteraction's reason: none of the six describes a machine-side capability gap.
	errNoApplier = errors.New("this session's CLI has no keystroke answer for an approval")
)

// dialogTap opens ONE short-lived readWrite subscription on the session's shared tap and reads
// the keys the session's adapter answers verdict with, off the grid that subscription was
// SEEDED with. The subscription is returned OPEN so the caller can write those keys back
// through the SAME handle; the caller closes it.
func (d *Daemon) dialogTap(session, verdict, action string) (*tapSub, string, error) {
	if d.api == nil {
		return nil, "", fmt.Errorf("%w: this daemon has no session tap wired", errNoApplier)
	}
	m, ok := d.core.Get(session)
	if !ok {
		return nil, "", fmt.Errorf("session %q is not one this daemon runs", session)
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return nil, "", fmt.Errorf("%w: agent %q has no adapter", errNoApplier, m.AgentType)
	}
	ap, ok := adapter.AsApprovalApplier(ad)
	if !ok {
		return nil, "", fmt.Errorf("%w: agent %q", errNoApplier, m.AgentType)
	}
	sub, err := d.api.tap.subscribe(session, readWrite)
	if err != nil {
		return nil, "", fmt.Errorf("tap session %q: %w", session, err)
	}
	snap, err := vt.DecodeSnapshot(sub.Snapshot())
	if err != nil {
		_ = sub.Close()
		return nil, "", fmt.Errorf("%w: its screen could not be read (%v)", errNoDialog, err)
	}
	keys, ok := ap.ApprovalKeys(snap, verdict, action)
	if !ok || keys == "" {
		_ = sub.Close()
		return nil, "", errNoDialog
	}
	return sub, keys, nil
}

// applyDecision types the dialog's own keys for verdict into the session's PTY. It is the whole
// of "applying" a phone approval on this path: the recorded keys are complete answers, each
// selecting its option AND submitting it, so nothing follows them -- no Enter, no second write.
//
// action is the pending request's own ToolAction.Type and is carried all the way to the
// adapter, which refuses a dialog that is not that request's (M1.8).
func (d *Daemon) applyDecision(session, verdict, action string) error {
	sub, keys, err := d.dialogTap(session, verdict, action)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	if err := sub.ControlKeys([]byte(keys)); err != nil {
		return fmt.Errorf("writing the dialog's keys into session %q: %w", session, err)
	}
	return nil
}

// dialogStillOnGrid reports whether the session's screen STILL shows an answerable dialog. It
// is the watchdog's read and writes nothing.
func (d *Daemon) dialogStillOnGrid(session, verdict, action string) bool {
	sub, _, err := d.dialogTap(session, verdict, action)
	if err != nil {
		return false
	}
	_ = sub.Close()
	return true
}

// watchInjection looks once more at a dialog the daemon has just typed at, and puts the
// session's status on the transcript if nothing moved.
//
// THE FAILURE MODE THIS EXISTS FOR is the honest one for any apply-by-keystroke path: the bytes
// are written, and the CLI does nothing with them -- a version whose key map moved, a dialog
// that swallowed the byte. SILENCE is the worst outcome there, because the phone's card stays
// up with no explanation and a card being worked on looks exactly the same. So the daemon says
// what it can see.
//
// It does NOT resolve the request, and that is the rule and not a shortcut: resolution comes
// only from observing the dialog leave (mirror-program.md section 3, step 3), and nothing has
// been observed to leave. It also emits nothing at all when the request resolved in the
// meantime, which is the normal case.
func (d *Daemon) watchInjection(session, itemID, verdict, action string) {
	if injectWatchdogDelay <= 0 {
		return
	}
	time.AfterFunc(injectWatchdogDelay, func() {
		d.itemMu.Lock()
		ap := d.approvals[session]
		pending := ap != nil && ap.itemID == itemID
		d.itemMu.Unlock()
		if !pending || !d.dialogStillOnGrid(session, verdict, action) {
			return
		}
		d.offerAll(session, d.sessionStatusItem(session, time.Now().UTC()))
	})
}
