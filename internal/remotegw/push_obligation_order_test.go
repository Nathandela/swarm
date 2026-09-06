package remotegw

// Regression tests for the BLOCKING PG-OBL-2 finding of the R3 GREEN review (bd
// agents-tracker-hggx.4.2): for a gateway-transport pairing, the durable wake obligation
// must be appended BEFORE the mailbox record it announces is published, not after.
// WakeRetryScheduler.PreAppendObligation plus PushNotifier.Event's
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
func (s *orderLoggingSink) Terminal(protocol.TerminalViewV1) error { return nil }

// orderLoggingGateway is a direct provider double, appending to the shared log.
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
func (g *orderLoggingGateway) PushTrigger(ctx context.Context) error {
	if err := g.Trigger(); err != nil {
		return err
	}
	return g.Drive(ctx)
}
func (g *orderLoggingGateway) PreAppendObligation() error { return g.Trigger() }

// newOrderingHarness wires a PushNotifier whose sink appends "publish" to mu/log --
// the SAME mu/log the caller's router-driving doubles append to -- so the two are
// directly comparable in one ordered sequence. It delivers no record itself; the caller
// does, once router is fully assembled.
func newOrderingHarness(t *testing.T, mu *sync.Mutex, log *[]string, pusher PushTriggerer) *PushNotifier {
	t.Helper()
	sink := &orderLoggingSink{mu: mu, log: log}
	sp := &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}}
	clk := newTestClock()
	return NewPushNotifier(sink, PushConfig{
		Pusher: pusher, Now: clk.Now, Prefs: sp,
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
	n := newOrderingHarness(t, &mu, &log, gw)

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

// TestPushNotifier_ForegroundOnlyDoesNotCreateAnObligation preserves the ordering safety
// control for the remaining no-provider route.
func TestPushNotifier_ForegroundOnlyDoesNotCreateAnObligation(t *testing.T) {
	var mu sync.Mutex
	var log []string
	n := newOrderingHarness(t, &mu, &log, nil)

	if err := n.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
		t.Fatalf("Event(needs_input): %v", err)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	want := []string{"publish"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("call order = %v, want %v -- foreground_only must not create an obligation", got, want)
	}
}
