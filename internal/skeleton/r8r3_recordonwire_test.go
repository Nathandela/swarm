package skeleton

// WAVE R8 / ROUND 3 -- THE RECORD ON THE WIRE, OVER THE REAL ASSEMBLED PATH.
//
// THE PROCESS DEFECT. The approved plan's S2 required "Real assembled path: a launched
// opencode session's record reaches App.Session over the real gateway". What shipped was
// `TestR8Publication_EverySessionCreationPathAuthorsARecord` -- a REGEX over four filenames
// for the literal `authorSessionCapabilities(`. No test anywhere launched a real session and
// asserted a record on the wire; `grep 'Capabilities' internal/skeleton/*_test.go` found no
// assertion on a wire record at all. That is the same shape as round-3 blocker 1 on the read
// side, and it is the test that would have caught it.
//
// So this file launches a REAL session on the REAL assembled daemon, bridges it with the REAL
// gateway, and asserts on what the gateway's own sink received:
//
//	daemon.Launch -> shim spawn -> registerSession -> authorSessionCapabilities
//	  -> coreAPI.JournalReadFrom's roster stamp -> protocol remote tier
//	  -> remotegw.Gateway.RunJournal -> namespaceRoster (T2-b's gateway seam) -> the sink
//
// and then hands the record to the PHONE'S OWN ROUTER, so the assertion is the destination a
// user actually gets rather than a field being non-nil.
//
// THE SESSION IS THE RESERVED "fake" AGENT, which is a terminal_fallback provider by exactly
// the derivation OpenCode and AGY use: it proves no InteractionSource, so
// deriveSessionCapabilities answers {structured_chat:false, terminal_fallback:true}. Using it
// rather than a real opencode binary keeps the rig hermetic; what is under test is the
// PUBLICATION PATH, which is identical for every provider that lands on that arm.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// recordSink keeps the newest capability record the gateway bridged for each session.
type recordSink struct {
	mu      sync.Mutex
	records map[string]*protocol.SessionCapabilities
	seen    map[string]bool
	changed chan struct{}
}

func newRecordSink() *recordSink {
	return &recordSink{
		records: map[string]*protocol.SessionCapabilities{},
		seen:    map[string]bool{},
		changed: make(chan struct{}, 64),
	}
}

func (s *recordSink) note(rec protocol.JournalRecord) {
	if rec.SessionID == "" {
		return
	}
	s.mu.Lock()
	s.seen[rec.SessionID] = true
	if rec.Capabilities != nil {
		cp := *rec.Capabilities
		s.records[rec.SessionID] = &cp
	}
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *recordSink) Snapshot(roster []protocol.JournalRecord, _ uint64) error {
	for _, r := range roster {
		s.note(r)
	}
	return nil
}

func (s *recordSink) Event(rec protocol.JournalRecord) error { s.note(rec); return nil }

func (s *recordSink) recordFor(id string) (*protocol.SessionCapabilities, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	return rec, ok
}

// waitForRecord blocks until the sink holds a capability record for id.
func (s *recordSink) waitForRecord(t *testing.T, id string, within time.Duration) *protocol.SessionCapabilities {
	t.Helper()
	deadline := time.After(within)
	for {
		if rec, ok := s.recordFor(id); ok {
			return rec
		}
		select {
		case <-s.changed:
		case <-deadline:
			s.mu.Lock()
			seen := len(s.seen)
			s.mu.Unlock()
			t.Fatalf("no capability record reached the gateway's sink for %s within %s "+
				"(%d session(s) bridged with no record).\n"+
				"ADR-017 T2 rule 3 says the phone renders from the record and infers nothing. "+
				"A record that never crosses the wire is a record every session routes AROUND: "+
				"T2-a's fail-closed default sends all of them to the status card, so OpenCode "+
				"and AGY have no fallback and Claude and Codex lose the composer.", id, within, seen)
			return nil
		}
	}
}

// TestR8R3_ALaunchedSessionsRecordReachesTheGatewaySinkAndRoutesToTheFallback is the
// end-to-end publication assertion S2 asked for.
func TestR8R3_ALaunchedSessionsRecordReachesTheGatewaySinkAndRoutesToTheFallback(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	registerPhone(t, sk, device.CapFull) // the journal bridge is kill-switch gated

	launched := launchFake(t, sk, "print HELLO\nidle 60s\n")

	sink := newRecordSink()
	gw := remotegw.New(rsock, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gw.RunJournal(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("RunJournal did not return after cancel")
		}
	})

	wireID := protocol.NamespacedID(sk.api.endpointID, launched.ID)
	rec := sink.waitForRecord(t, wireID, 20*time.Second)

	if err := rec.Validate(); err != nil {
		t.Fatalf("the record that crossed the wire does not validate: %v (%+v)", err, *rec)
	}
	if rec.SessionInstance == "" {
		t.Errorf("the record on the wire binds no session instance; T8-a's whole binding -- the "+
			"generation, the epoch reset, the stale-instance refusal -- has no referent: %+v", *rec)
	}
	if rec.StructuredChat {
		t.Errorf("the fake agent proves no InteractionSource and must not advertise structured "+
			"chat: %+v", *rec)
	}
	if !rec.TerminalFallback {
		t.Errorf("a provider with no structured plane must reach the phone as terminal_fallback, "+
			"which is the whole of what this wave makes useful: %+v", *rec)
	}

	// THE DESTINATION, not the field: hand it to the phone's own router under the profile the
	// machine actually publishes.
	profile := schema.RemoteProfileV1{
		Version:                 schema.CurrentProfileVersion,
		CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		TerminalViewVersion:     schema.CurrentTerminalViewVersion,
	}
	if got := phonecore.RouteSession(rec, profile); got != phonecore.DestinationTerminalFallback {
		t.Fatalf("the phone routes this launched session to %s, not the terminal fallback.\n"+
			"That is the wave's exit condition read backwards: OpenCode and AGY are supposed to "+
			"be launchable and monitorable, and every provider on this arm lands here.", got)
	}
}

// TestR8R3_AStructuredSessionsRecordCrossesTheWireAndKeepsTheComposer is the same path for the
// other destination, and it is the R6/R7 regression guard: round 2's blocker 3 was a wiring
// defect that silently removed the chat composer from EVERY session, and the fence that caught
// it drove the profile rather than the wire. This one drives both.
func TestR8R3_AStructuredSessionsRecordCrossesTheWireAndKeepsTheComposer(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	registerPhone(t, sk, device.CapFull)
	launched := launchFake(t, sk, "print HELLO\nidle 60s\n")

	sink := newRecordSink()
	gw := remotegw.New(rsock, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gw.RunJournal(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("RunJournal did not return after cancel")
		}
	})

	wireID := protocol.NamespacedID(sk.api.endpointID, launched.ID)
	rec := sink.waitForRecord(t, wireID, 20*time.Second)

	// A fallback session has NO composer, and a gate written over one boolean would say
	// otherwise. This is the same predicate mobile/app.go hands the detail panel.
	profile := schema.RemoteProfileV1{
		Version:                 schema.CurrentProfileVersion,
		CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		TerminalViewVersion:     schema.CurrentTerminalViewVersion,
	}
	if phonecore.ComposerAvailable(rec, profile) {
		t.Errorf("a terminal_fallback session offered the chat composer; every send would be " +
			"refused and the screen would look healthy while typing into nothing")
	}
	// And the control predicate is the record's, resolved through the router -- never the raw
	// field (round-3 major 4).
	if !phonecore.TerminalControlAvailable(rec, profile) {
		t.Errorf("the fake agent's record grants terminal_control (%+v) and the phone's own "+
			"predicate refused it", *rec)
	}
	zero := schema.RemoteProfileV1{}
	if phonecore.TerminalControlAvailable(rec, zero) {
		t.Errorf("a ZERO profile -- every machine deployed before this wave -- still answered " +
			"true for terminal control. T5-a reads a zero version as 'no fallback exists', and " +
			"a control predicate that ignores it hands Kotlin a keyboard the router refused.")
	}
}
