package remotegw

// Regression tests for the BLOCKING PG-OBL-2 finding of the R3 GREEN review (bd
// agents-tracker-hggx.4.2): for a gateway-transport pairing, the durable wake obligation
// must be appended BEFORE the mailbox record it announces is published, not after.
// TransportRouter.PreAppendObligation (pushtransport.go) plus PushNotifier.Event's
// pre-publish hook (push.go's preAppendObligation/wouldWakeNow) are what these tests pin.
//
// Both doubles below append to one SHARED, ordered log rather than independent call
// counters: a count cannot distinguish "trigger happened before publish" from "trigger
// happened after publish", which is exactly the property that regressed.

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// orderLoggingSink is an OutboundSink whose Event appends "publish" to a log shared with
// an orderLoggingGateway, so a test can assert the two calls' relative order.
type orderLoggingSink struct {
	mu  *sync.Mutex
	log *[]string
}

func (s *orderLoggingSink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (s *orderLoggingSink) Event(protocol.JournalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.log = append(*s.log, "publish")
	return nil
}
func (s *orderLoggingSink) Terminal(string, []string, int, int) error { return nil }

// orderLoggingGateway is a gatewayObligationDriver double standing in for a REAL
// WakeObligationMachine's Trigger/Drive, appending to the SAME shared log.
type orderLoggingGateway struct {
	mu  *sync.Mutex
	log *[]string
}

func (g *orderLoggingGateway) Trigger() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	*g.log = append(*g.log, "trigger")
	return nil
}
func (g *orderLoggingGateway) Drive(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	*g.log = append(*g.log, "drive")
	return nil
}

// orderLoggingLegacyPusher is the relay's push_trigger seam, logging into the same
// shared log, so the legacy path's ordering can be checked against the same log shape.
type orderLoggingLegacyPusher struct {
	mu  *sync.Mutex
	log *[]string
}

func (p *orderLoggingLegacyPusher) PushTrigger(context.Context, string, []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.log = append(*p.log, "legacy")
	return nil
}

// newOrderingHarness wires a PushNotifier whose sink appends "publish" to mu/log --
// the SAME mu/log the caller's router-driving doubles append to -- so the two are
// directly comparable in one ordered sequence. It delivers no record itself; the caller
// does, once router is fully assembled.
func newOrderingHarness(t *testing.T, mu *sync.Mutex, log *[]string, router PushTriggerer) *PushNotifier {
	t.Helper()
	sink := &orderLoggingSink{mu: mu, log: log}
	sp := &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}}
	clk := newTestClock()
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	return NewPushNotifier(sink, PushConfig{
		Pusher: router, Target: "phone-routing-id", WakeKey: testWakeKey(), EpochID: 7,
		Now: clk.Now, Seq: seq, Prefs: sp,
	})
}

// TestPushNotifier_GatewayTransportAppendsTheObligationBeforePublishingTheMailboxRecord
// is the regression proof: under gateway transport, the FIRST entry in the call log is
// "trigger" (the durable pre-append), strictly before the "publish" entry -- so a crash
// between the two can no longer land in the gap PG-OBL-2 forbids. The post-publish
// trigger+drive pair (maybeWake's own send()) still follows, unchanged.
func TestPushNotifier_GatewayTransportAppendsTheObligationBeforePublishingTheMailboxRecord(t *testing.T) {
	var mu sync.Mutex
	var log []string
	gw := &orderLoggingGateway{mu: &mu, log: &log}
	ts, err := OpenTransportStore("")
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := ts.SetTransport(TransportGateway); err != nil {
		t.Fatalf("SetTransport(gateway): %v", err)
	}
	router := &TransportRouter{Transport: ts, Legacy: &orderLoggingLegacyPusher{mu: &mu, log: &log}, Gateway: gw}
	n := newOrderingHarness(t, &mu, &log, router)

	if err := n.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
		t.Fatalf("Event(needs_input): %v", err)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	want := []string{"trigger", "publish", "trigger", "drive"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("call order = %v, want %v -- PG-OBL-2 requires the obligation appended BEFORE the "+
			"mailbox publish, with the ordinary post-publish trigger+drive (maybeWake's send()) still "+
			"following it", got, want)
	}
}

// TestPushNotifier_LegacyRelayTransportOrderIsUnaffectedByThePreAppendHook is the
// non-regression control: legacy_relay's ordering guarantee ("publish then push") is
// untouched by preAppendObligation existing, because TransportRouter.PreAppendObligation
// is a no-op for any transport but gateway.
func TestPushNotifier_LegacyRelayTransportOrderIsUnaffectedByThePreAppendHook(t *testing.T) {
	var mu sync.Mutex
	var log []string
	legacy := &orderLoggingLegacyPusher{mu: &mu, log: &log}
	gw := &orderLoggingGateway{mu: &mu, log: &log}
	ts, err := OpenTransportStore("")
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := ts.SetTransport(TransportLegacyRelay); err != nil {
		t.Fatalf("SetTransport(legacy_relay): %v", err)
	}
	router := &TransportRouter{Transport: ts, Legacy: legacy, Gateway: gw}
	n := newOrderingHarness(t, &mu, &log, router)

	if err := n.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
		t.Fatalf("Event(needs_input): %v", err)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	want := []string{"publish", "legacy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("call order = %v, want %v -- a legacy_relay pairing must still publish before it "+
			"pushes, and the obligation machine must never be touched at all", got, want)
	}
}
