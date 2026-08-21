package protocol

// WAVE R8 / SLICE S3 -- THE CAPABILITY GATE BINDS THE LEGACY TERMINAL PATH TOO.
// Failing-first (TDD RED, GG-5), driven over the REAL ASSEMBLED remote-tier server.
//
// THE HOLE, STATED AS THE ATTACK. ADR-017 T4 keeps `TerminalSnapshot` / `terminal_watch`
// alive "only under the legacy remote profile". Two facts make that sentence unenforceable
// as written:
//
//  1. The production profile ships as a ZERO VALUE. `cmd/swarm-remote/config.go:141-147`
//     constructs `RemoteProfileV1` with three ADR-016 fields set and the rest zero, so "the
//     legacy profile" is presently indistinguishable from "any profile".
//  2. The legacy path carries NO SESSION-SCOPED GATE AT ALL. `internal/remotegw/
//     command_loop.go:612-621` routes `ActionTerminalWatch` straight to `Watchers.Watch`
//     without reaching the device authenticator, and `handleTerminalSubscribe`
//     (server.go:2190-2208) gates the kill switch, the negotiated remote-gateway capability
//     and the presence of a tapper -- and nothing about the SESSION.
//
// So a downlevel, rolled-back or compromised app that merely ASKS gets a live sanitized peek
// onto a healthy Claude session -- which is exactly the route Wave R8's exit says does not
// exist ("Claude and Codex expose no route to it when their structured capabilities are
// healthy", playbook:826-827) and exactly the escape hatch ADR-017's alternatives section
// rejects by name. Amendment T2-c closes it: the session capability gate applies to
// `terminal_subscribe` / `terminal_watch` UNCONDITIONALLY and regardless of profile, scoped
// to the REMOTE TIER exactly as the kill-switch gate already is (server.go:2195-2201), so
// the owner's view of the owner's own machine is untouched.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	type SessionCapabilityLookup interface {
//	    SessionCapabilities(local string) (SessionCapabilities, bool)
//	}
//	CodeCapabilityRefused = "capability_refused"   // the sealed stable refusal (playbook:450-451)
//
// WHY THIS TEST DRIVES THE REAL SERVER AND NOT A UNIT. R5, R6 and R7 each lost a round to a
// defect only the real composition revealed, and this gate has three independent ways to be
// wrong that a unit test cannot see: it can be placed before `peekGateOpen` (and blank the
// owner), it can be placed after the tap is opened (and leak one frame), or it can be wired
// onto the owner tier (and break the TUI). Each of those is a different line of this file.

import (
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// capabilityTapStub is a terminalTapStub that ALSO answers the per-session capability
// lookup, which is the seam the gate reads. Records are keyed by LOCAL session id, because
// that is what `resolveSession` hands the handler.
type capabilityTapStub struct {
	*terminalTapStub
	// GUARDED, and the mutex is the point of the per-emission test rather than tidiness: the
	// gate is re-read from the RENDER goroutine on every emission while the test withdraws
	// the record from its own goroutine, which is exactly the mid-stream revocation T6-e is
	// about. An unguarded map here is a race the detector reports instead of the behaviour.
	mu      sync.Mutex
	records map[string]SessionCapabilities
}

func newCapabilityTapStub() *capabilityTapStub {
	return &capabilityTapStub{
		terminalTapStub: newTerminalTapStub(),
		records:         map[string]SessionCapabilities{},
	}
}

func (c *capabilityTapStub) SessionCapabilities(local string) (SessionCapabilities, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[local]
	return rec, ok
}

// setRecord publishes one session's record.
func (c *capabilityTapStub) setRecord(local string, rec SessionCapabilities) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records[local] = rec
}

// withdrawRecord is the mid-stream revocation: the machine stops answering for this session.
func (c *capabilityTapStub) withdrawRecord(local string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.records, local)
}

// r8SubscribeReply drives one terminal_subscribe over an assembled server and returns the
// reply control frame.
func r8SubscribeReply(t *testing.T, sock, cap string, session string) Control {
	t.Helper()
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{cap})
	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/" + session})
	return nextControl(t, rc)
}

// TestR8Legacy_TheLegacyPeekIsRefusedForAHealthyStructuredSession is amendment T2-c's
// central assertion, and the one the wave's exit is written about.
func TestR8Legacy_TheLegacyPeekIsRefusedForAHealthyStructuredSession(t *testing.T) {
	stub := newCapabilityTapStub() // kill switch ON: the refusal below is NOT the kill switch
	stub.setRecord("sess1", SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", SessionInstance: "inst-1",
		StructuredChat: true, TerminalFallback: false,
	})
	sock := serveRemoteAPI(t, stub)

	ack := r8SubscribeReply(t, sock, CapRemoteGateway, "sess1")
	if ack.Op == OpOK {
		t.Fatalf("ADR-017 T2-c / playbook:826-827: the LEGACY terminal_subscribe opened a peek onto a " +
			"HEALTHY CLAUDE session over the real remote-tier server. Wave R8's exit is the absence of " +
			"this route, and closing it in the Kotlin router alone leaves it open to any downlevel, " +
			"rolled-back or compromised client that simply asks.")
	}
	if ack.ErrorCode != CodeCapabilityRefused {
		t.Errorf("refusal code = %q, want the sealed stable %q. playbook:450-451: an old client degrades "+
			"legibly and never receives a malformed screen, which needs a code it can recognise rather "+
			"than a message it must parse.", ack.ErrorCode, CodeCapabilityRefused)
	}
	if stub.tapCount() != 0 {
		t.Errorf("ADR-017 T2-c: the refusal opened %d read-only tap(s). The gate must run BEFORE the tap, "+
			"like the kill switch does -- a tap opened and then closed has already read the session.",
			stub.tapCount())
	}
}

// TestR8Legacy_TheLegacyPeekStillWorksForAFallbackSession is the other direction, and it is
// what stops the gate from being implemented as "refuse everything".
func TestR8Legacy_TheLegacyPeekStillWorksForAFallbackSession(t *testing.T) {
	stub := newCapabilityTapStub()
	stub.setRecord("sess1", SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", SessionInstance: "inst-1",
		StructuredChat: false, TerminalFallback: true,
	})
	sock := serveRemoteAPI(t, stub)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, rc); ack.Op != OpOK {
		t.Fatalf("a terminal_fallback session must still be watchable: reply op %q code %q", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("no tap opened for a fallback session")
	}
	tap.frames <- []byte("\x1b[32mOPENCODE\x1b[0m")
	snap := readTerminalSnapshot(t, rc)
	if snap.Terminal == nil || !strings.Contains(strings.Join(snap.Terminal.Lines, ""), "OPENCODE") {
		t.Fatalf("the fallback session's rendered grid did not arrive")
	}
	assertNoControlBytes(t, snap.Terminal.Lines)
}

// TestR8Legacy_AnAbsentOrInconsistentRecordIsRefused is T2-a and T2-b at this seam.
//
// The nil row is not hypothetical: no production path authors a capability record at all
// today (internal/skeleton/capability.go:334-344), so "no record" is the state of EVERY live
// session until S2 lands -- and if nil means allow, this gate ships open.
func TestR8Legacy_AnAbsentOrInconsistentRecordIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		rec     SessionCapabilities
		why     string
	}{
		{
			name:    "absent_record",
			present: false,
			why:     "T2-a: absence and terminal_fallback=false take ONE code path; 'unknown, therefore allow' opens the peek on every pre-R8 session",
		},
		{
			name:    "inconsistent_record",
			present: true,
			rec:     SessionCapabilities{Provider: "claude", SessionInstance: "i", StructuredChat: true, TerminalFallback: true},
			why:     "T2-b: a gate that tests terminal_fallback alone lets a malformed, stale or attacker-supplied record open the route",
		},
		{
			name:    "neither_destination",
			present: true,
			rec:     SessionCapabilities{Provider: "somecli", SessionInstance: "i"},
			why:     "T1's third destination: a session that is neither structured nor fallback keeps the honest status card",
		},
		{
			name:    "no_session_instance",
			present: true,
			rec:     SessionCapabilities{Provider: "opencode", TerminalFallback: true},
			why:     "T8-a: a record that binds no instance cannot bind a watch to an incarnation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newCapabilityTapStub()
			if tc.present {
				stub.records["sess1"] = tc.rec
			}
			sock := serveRemoteAPI(t, stub)
			ack := r8SubscribeReply(t, sock, CapRemoteGateway, "sess1")
			if ack.Op == OpOK {
				t.Errorf("ADR-017 %s\nterminal_subscribe was ACCEPTED for a %s over the real remote-tier server.",
					tc.why, tc.name)
			}
			if ack.Op != OpOK && ack.ErrorCode != CodeCapabilityRefused {
				t.Errorf("refusal code = %q, want %q", ack.ErrorCode, CodeCapabilityRefused)
			}
			if stub.tapCount() != 0 {
				t.Errorf("the refusal opened %d tap(s); the gate runs before the tap", stub.tapCount())
			}
		})
	}
}

// TestR8Legacy_TheOwnerTierIsUnaffected is the scoping half, and it is the mutation fence for
// "remote tier only".
//
// The kill switch is REMOTE-TIER ONLY for a reason server.go:2195-2201 states in as many
// words: it "is the REMOTE tier's master override, so it must never blank the owner's own
// view of the owner's own machine". The capability gate inherits that scoping exactly. An
// owner-tier peek is the TUI looking at its own machine; a capability record that routes a
// PHONE has no authority over it.
func TestR8Legacy_TheOwnerTierIsUnaffected(t *testing.T) {
	stub := newCapabilityTapStub()
	stub.setRecord("sess1", SessionCapabilities{
		Provider: "claude", SessionInstance: "i", StructuredChat: true,
	})
	sock := serveOwner(t, stub)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, rc); ack.Op != OpOK {
		t.Fatalf("ADR-017 T2-c: the capability gate refused an OWNER-TIER terminal_subscribe (op %q code "+
			"%q). The gate is scoped to the remote tier exactly as the kill switch is; scoping it wider "+
			"blanks the owner's own view of the owner's own machine.", ack.Op, ack.ErrorCode)
	}
}

// TestR8Legacy_TheGateIsReCheckedPerEmission is amendment T6-e at the read seam.
//
// The precedent is already in this file: `peekGateOpen` is re-checked before EVERY emission
// (server.go:2245-2258) because "the FIRST gate only covers subscribe time". A capability
// gate checked only at subscribe time has the same hole one field over: a session degraded,
// revoked or replaced mid-stream keeps streaming until the phone happens to send something.
func TestR8Legacy_TheGateIsReCheckedPerEmission(t *testing.T) {
	stub := newCapabilityTapStub()
	stub.setRecord("sess1", SessionCapabilities{
		Provider: "opencode", SessionInstance: "i", TerminalFallback: true,
	})
	sock := serveRemoteAPI(t, stub)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
	if ack := nextControl(t, rc); ack.Op != OpOK {
		t.Fatalf("subscribe refused: %q/%q", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("no tap")
	}
	tap.frames <- []byte("BEFORE")
	if snap := readTerminalSnapshot(t, rc); snap.Terminal == nil {
		t.Fatalf("no snapshot before the revocation")
	}

	// The record is withdrawn mid-stream, with NO phone action of any kind: a replacement, a
	// revocation, or the session simply ending. The stream must stop on the render loop's own
	// tick, not at the next trigger the phone happens to send.
	// THE POSITIVE CONTROL FIRST, and this suite would be worthless without it. The negative
	// assertion below is "a marked frame does NOT arrive", which a slow render loop, a
	// coalescing window or a read deadline satisfies just as well as a working gate -- so it
	// is asserted against a run where the SAME frame, over the SAME timings, DOES arrive.
	// Without this the test passes whether or not the gate exists.
	tap.frames <- []byte("STILL-ALLOWED")
	if !awaitPeekLine(t, rc, "STILL-ALLOWED") {
		t.Fatalf("CONTROL BROKEN: a snapshot did not reach the phone while the record still " +
			"permitted the watch, so the negative assertion below would pass vacuously")
	}

	stub.withdrawRecord("sess1")
	tap.frames <- []byte("AFTER")

	if awaitPeekLine(t, rc, "AFTER") {
		t.Fatalf("ADR-017 T6-e: a snapshot rendered AFTER the session's capability record was " +
			"withdrawn still reached the phone. Authority is re-evaluated per emission, matching the " +
			"kill switch's own discipline in this handler, so a session degraded, revoked or replaced " +
			"mid-stream stops within a tick rather than at whichever trigger the phone next sends.")
	}
}

// awaitPeekLine reads until a terminal snapshot carrying want arrives, the connection closes,
// or the read deadline elapses. It reports whether the line reached the phone.
//
// A CLOSED CONNECTION IS "DID NOT ARRIVE", not an error: severing the peek is one of the
// permitted ways for the machine to stop, and a test that treated it as a failure would
// forbid the stronger answer.
func awaitPeekLine(t *testing.T, rc *rawConn, want string) bool {
	t.Helper()
	for i := 0; i < 64; i++ {
		typ, payload, err := rc.readFrame()
		if err != nil {
			return false
		}
		// wire.TControl BY NAME, and this is the line the positive control above caught:
		// the literal `0` here read as "any control frame" and matched NOTHING, because
		// TControl is 1 -- so every snapshot was skipped and the negative assertion passed
		// whether or not the gate existed.
		if typ != wire.TControl {
			continue
		}
		c, derr := DecodeControl(payload)
		if derr != nil {
			continue
		}
		if c.Op == OpTerminalSnapshot && c.Terminal != nil && strings.Contains(strings.Join(c.Terminal.Lines, ""), want) {
			return true
		}
	}
	return false
}
