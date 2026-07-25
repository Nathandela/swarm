package swarmmobile_test

// Slice S11 REVIEW ROUND 1 -- FAILING-FIRST (TDD RED, GG-5) call-site guards for the two
// review defects whose SHAPE is the defect, and which a behavioural test can only catch by
// timing.
//
// They are deliberately paired with the behavioural halves in ./conformance
// (s11r_disconnect_test.go), which drive a real link and cut it. A source guard alone is
// weak; a timing test alone is flaky. Together they say "the wrong call is absent AND the
// right behaviour is present", which is the pair the S7b lesson asks for and the same shape
// s11_inputwiring_test.go already uses in this directory.
//
// This file contains NO implementation.

import (
	"strings"
	"testing"
)

// s11rRefuseCalls fails when body mentions any forbidden identifier.
func s11rRefuseCalls(t *testing.T, label, body string, forbidden map[string]string) {
	t.Helper()
	for ident, why := range forbidden {
		if strings.Contains(body, ident) {
			t.Errorf("%s still calls %s.\n%s", label, ident, why)
		}
	}
}

// TestS11R_InputNeverWaitsForAConnection is ADR-007 D7's live-only rule at the one place it
// was being broken. sendContext resolves the destination through awaitConn, which POLLS FOR
// UP TO FIVE SECONDS for a connection to come up and then appends -- so with the link down a
// keystroke blocks, rides the RECONNECTED link and lands at the machine ~1 s later while
// SendInput returns nil.
//
// The wait is correct for a COMMAND: a screen that issues one immediately after Start must
// not be refused by a race it cannot see, and a command is idempotent and queued by design.
// It is exactly wrong for input, which is live-only. So the two paths must resolve their
// destination differently, and this pins that they do.
func TestS11R_InputNeverWaitsForAConnection(t *testing.T) {
	src := loadFacade(t)

	for _, name := range []string{"SendInput", "Resize", "drainHeldInput"} {
		body := s11FuncSource(t, src, "App", name)
		label := s11FuncLabel("App", name)
		s11rRefuseCalls(t, label, body, map[string]string{
			"a.sendContext(": "ADR-007 D7: input is LIVE-ONLY. sendContext waits up to five seconds " +
				"in awaitConn for a connection, so a keystroke typed offline is delivered on the " +
				"RECONNECTED link -- a keystroke surviving a disconnect, which is the hazard the " +
				"rule is structural about. Resolve the live destination without waiting.",
		})
		s11RequireCalls(t, label, body, map[string]string{
			"liveSendContext": "the live-only destination lookup, which fails immediately when there " +
				"is no connection rather than waiting for one.",
		})
	}

	// ... and the COMMAND path must keep the wait, or fixing input breaks the race that
	// awaitConn exists to close.
	body := s11FuncSource(t, src, "App", "sealSignedCommand")
	s11RequireCalls(t, s11FuncLabel("App", "sealSignedCommand"), body, map[string]string{
		"a.sendContext(": "a command is idempotent and queued by design, so the brief wait for a " +
			"connection Start is bringing up is correct for it. Only INPUT must fail fast.",
	})
}

// TestS11R_TheSkewRefusalCannotBlockItsOwnRemedy is PB-TIME-1's latch, pinned structurally.
//
// THE DEFECT. sealSignedCommand consulted SkewMonitor.Check and returned on error, while
// SkewMonitor.Sent -- the ONLY producer of a measurement bracket anywhere in the tree -- sits
// downstream in the same function. So once the skew verdict went bad, no command was sent,
// no reply came back, Observe was never reached, and the verdict could never clear: the user
// fixed their clock exactly as the error instructed and every mutating op stayed refused
// until the process restarted. skew.go's own comment names that shape ("A latched refusal is
// the permanent brick PB-STATE-10 forbids elsewhere").
//
// THE RESOLUTION, and why it is not a probe. Any local gate has the same defect in a
// different order, because the only authenticated machine time rides a REPLY and a reply only
// exists in answer to a command: a gate that lets one command through per measurement is a
// gate that lets EVERY command through as soon as the machine keeps replying, which it does.
// So the phone EXPLAINS and the machine ENFORCES -- which is the split
// s11_skew_test.go already states for the unmeasured case ("the daemon's own ExpiresAt check
// remains the backstop for a phone that has never measured"). The measurement keeps running,
// and the verdict reaches the user through the event plane instead of by blocking a button.
func TestS11R_TheSkewRefusalCannotBlockItsOwnRemedy(t *testing.T) {
	src := loadFacade(t)
	body := s11FuncSource(t, src, "App", "sealSignedCommand")
	label := s11FuncLabel("App", "sealSignedCommand")

	s11rRefuseCalls(t, label, body, map[string]string{
		"SkewMonitor().Check()": "PB-TIME-1 / PB-STATE-10: refusing a command on the skew verdict " +
			"stops the command that would have re-measured the clock, so the refusal outlives the " +
			"broken clock it was reporting and only a process restart clears it.",
	})
	// The SEND half must remain, or the bracket has no T1 and skew is never measurable at all.
	s11RequireCalls(t, label, body, map[string]string{
		"SkewMonitor().Sent(": "PB-TIME-3: the phone's send instant, correlated by operation id. " +
			"It is the only producer of a bracket; without it every machine stamp is uncorrelated.",
	})
}

// TestS11R_TheSkewVerdictReachesTheUser is the other half: dropping the gate must not drop
// the REPORT. PB-TIME-1's criterion is "a distinct, user-legible error (not the generic
// authorization failure)", and an error no surface carries is not user-legible. The event
// plane is where an asynchronous verdict belongs -- it is discovered by a REPLY arriving,
// not by the user pressing anything.
// Both links of the chain are pinned, because either one alone regresses silently: a reply
// handler that reports nothing, or a reporter nothing calls.
func TestS11R_TheSkewVerdictReachesTheUser(t *testing.T) {
	src := loadFacade(t)

	onReply := s11FuncSource(t, src, "App", "onReply")
	s11RequireCalls(t, s11FuncLabel("App", "onReply"), onReply, map[string]string{
		"reportSkew()": "PB-TIME-1: the verdict is produced when a machine-stamped reply closes the " +
			"bracket, so a reply landing is the only moment it can become reportable.",
	})

	report := s11FuncSource(t, src, "App", "reportSkew")
	s11RequireCalls(t, s11FuncLabel("App", "reportSkew"), report, map[string]string{
		"SkewMonitor()": "the verdict has one source. A phone two minutes out must be told to fix " +
			"its CLOCK, not handed the daemon's opaque \"not authorized\".",
		"a.events.emit": "an error no surface carries is not user-legible, which is the whole of " +
			"PB-TIME-1's criterion. The verdict is discovered by a reply ARRIVING, not by the user " +
			"pressing anything, so the event plane is where it belongs.",
	})
}
