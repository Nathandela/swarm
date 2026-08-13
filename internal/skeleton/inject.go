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
// and the keystroke must not straddle a repaint. A tap subscription is SEEDED with the grid as
// of the moment it joined and writes through that same handle, so the screen the recognizer
// judged is the screen the keys are typed at, with no second dial in between.

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
	// errNoDialog is the GATE's refusal: the live grid does not positively show a dialog the
	// session's adapter has a recorded key map for. It is the terminal-answered-first race and
	// the unknown-screen case alike, and both must refuse rather than type.
	errNoDialog = errors.New("the session's screen does not show a permission dialog this CLI can be answered on")
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
func (d *Daemon) dialogTap(session, verdict string) (*tapSub, string, error) {
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
	keys, ok := ap.ApprovalKeys(snap, verdict)
	if !ok || keys == "" {
		_ = sub.Close()
		return nil, "", errNoDialog
	}
	return sub, keys, nil
}

// applyDecision types the dialog's own keys for verdict into the session's PTY. It is the whole
// of "applying" a phone approval on this path: the recorded keys are complete answers, each
// selecting its option AND submitting it, so nothing follows them -- no Enter, no second write.
func (d *Daemon) applyDecision(session, verdict string) error {
	sub, keys, err := d.dialogTap(session, verdict)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Input([]byte(keys)); err != nil {
		return fmt.Errorf("writing the dialog's keys into session %q: %w", session, err)
	}
	return nil
}

// dialogStillOnGrid reports whether the session's screen STILL shows an answerable dialog. It
// is the watchdog's read and writes nothing.
func (d *Daemon) dialogStillOnGrid(session, verdict string) bool {
	sub, _, err := d.dialogTap(session, verdict)
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
func (d *Daemon) watchInjection(session, itemID, verdict string) {
	if injectWatchdogDelay <= 0 {
		return
	}
	time.AfterFunc(injectWatchdogDelay, func() {
		d.itemMu.Lock()
		ap := d.approvals[session]
		pending := ap != nil && ap.itemID == itemID
		d.itemMu.Unlock()
		if !pending || !d.dialogStillOnGrid(session, verdict) {
			return
		}
		d.offerAll(session, d.sessionStatusItem(session, time.Now().UTC()))
	})
}
