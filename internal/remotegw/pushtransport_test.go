package remotegw

// Bead agents-tracker-hggx.4 (Wave R3, machine side) -- FAILING-FIRST (TDD RED, GG-5)
// tests for item (5): per-pairing push_transport selection (ADR-015 P12,
// docs/specifications/push-gateway-api.md §10.1). Selection is durable and atomic, and
// wake DELIVERY rides exactly one transport -- a gateway pairing never also fires the
// legacy relay's push_trigger, and foreground_only fires nothing at all.
//
// SCOPE HONESTY: real per-pairing conveyance (the phone allocating an address and
// handing it to the machine over an authenticated pairing-update, PG-MIG-2) is a LATER
// slice -- internal/phonecore, internal/remote/mobile and internal/remote/android are
// untouched here. This wave's transport selection is a single durable value for THIS
// machine, sourced from gateway params/config; see the TODO on TransportStore below.
//
// THE SEAM this file pins:
//
//	type PushTransport string
//	const (
//		TransportLegacyRelay    PushTransport = "legacy_relay"
//		TransportGateway        PushTransport = "gateway"
//		TransportForegroundOnly PushTransport = "foreground_only"
//	)
//
//	// TransportStore is the durable, atomic custody of one machine's selected
//	// push_transport (PG-MIG-1). A malformed or unrecognised value must never land
//	// durably -- the same fail-closed discipline as errCorruptOutbox/errCorruptSeqFile
//	// (outbox.go, seqstore.go) -- and an in-flight SetTransport must never leave a torn
//	// file: on failure, the PREVIOUS durable value is what a reopen still reports.
//	//
//	// TODO(pairing-conveyance): a later slice replaces the single static value this
//	// wave's config supplies with the real per-pairing PG-MIG-2 transition (address
//	// allocation, pairing-update ack, gateway test wake). TransportStore's contract does
//	// not change when that lands; only what calls SetTransport does.
//	type TransportStore interface {
//		Transport() (PushTransport, error)
//		SetTransport(PushTransport) error
//	}
//	func OpenTransportStore(path string) (TransportStore, error) // "" => in-memory, defaults to legacy_relay
//
//	// TransportRouter is the PushTriggerer PushNotifier is configured with (PushConfig.
//	// Pusher) once a pairing's transport may be anything other than legacy_relay. It is
//	// the seam that makes selection EXCLUSIVE: PushNotifier calls PushTrigger exactly as
//	// it always has (push.go is UNTOUCHED by this file), and the router alone decides
//	// which of the two channels -- or neither -- ever fires, reading the durable
//	// TransportStore fresh on every call rather than caching a choice at construction.
//	type TransportRouter struct {
//		Transport TransportStore
//		Legacy    PushTriggerer            // the relay's push_trigger op; P12's surviving legacy path
//		Gateway   gatewayObligationTrigger  // *WakeObligationMachine satisfies this
//	}
//	var _ PushTriggerer = (*TransportRouter)(nil)
//	func (r *TransportRouter) PushTrigger(ctx context.Context, target string, env []byte) error
//
// This file contains NO implementation.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// spyGateway counts Trigger/Drive calls without doing anything durable, for the routing
// tests below -- obligation_test.go already exercises what a REAL machine does with
// those calls.
type spyGateway struct {
	mu               sync.Mutex
	triggers, drives int
}

func (g *spyGateway) Trigger() error {
	g.mu.Lock()
	g.triggers++
	g.mu.Unlock()
	return nil
}
func (g *spyGateway) Drive(context.Context) error {
	g.mu.Lock()
	g.drives++
	g.mu.Unlock()
	return nil
}
func (g *spyGateway) counts() (triggers, drives int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.triggers, g.drives
}

// --- TransportStore: durability and atomicity ---------------------------------------

// TestTransportStore_PersistsAcrossReopen is the file-durability half, in the same
// reopen-the-same-path shape as TestObligationStore_PersistsAcrossReopen and
// TestRelaySink_OutboxCommitsDeliveredCursorsAcrossRestart.
func TestTransportStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "push-transport")

	st1, err := OpenTransportStore(path)
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := st1.SetTransport(TransportGateway); err != nil {
		t.Fatalf("SetTransport: %v", err)
	}

	st2, err := OpenTransportStore(path)
	if err != nil {
		t.Fatalf("reopen OpenTransportStore: %v", err)
	}
	got, err := st2.Transport()
	if err != nil {
		t.Fatalf("Transport after reopen: %v", err)
	}
	if got != TransportGateway {
		t.Fatalf("Transport after reopen = %q, want %q", got, TransportGateway)
	}
}

// TestTransportStore_DefaultsToLegacyRelay pins PG-MIG-1/PG-MIG-2's starting state: a
// pairing begins on legacy_relay and leaves it only through the four-precondition
// migration, so a store nothing has ever written to must not silently read as
// gateway or foreground_only.
//
// NOT DISCRIMINATING beyond the compile step: a trivial stub that always answers
// legacy_relay would satisfy this one test by accident, exactly the way a do-nothing
// stub can satisfy any single-value default assertion. The SIBLING tests in this file
// (reopen, rejection, corruption) are what actually exercise real behaviour.
func TestTransportStore_DefaultsToLegacyRelay(t *testing.T) {
	st, err := OpenTransportStore("") // in-memory, first run
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	got, err := st.Transport()
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if got != TransportLegacyRelay {
		t.Fatalf("default Transport = %q, want %q (PG-MIG-1/2)", got, TransportLegacyRelay)
	}
}

// TestTransportStore_RejectsAnUnknownValueWithoutTearingTheDurableOne is the atomicity
// half: PG-MIG-1 closes the value set to exactly three strings. A rejected SetTransport
// must leave the PREVIOUSLY durable value intact -- not a corrupt file, not a silently
// adopted invalid one.
func TestTransportStore_RejectsAnUnknownValueWithoutTearingTheDurableOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "push-transport")
	st, err := OpenTransportStore(path)
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := st.SetTransport(TransportGateway); err != nil {
		t.Fatalf("SetTransport(gateway): %v", err)
	}
	if err := st.SetTransport(PushTransport("not_one_of_the_three")); err == nil {
		t.Fatal("SetTransport accepted an unrecognised value, want a refusal (PG-MIG-1's closed value set)")
	}
	got, err := st.Transport()
	if err != nil {
		t.Fatalf("Transport after the rejected write: %v", err)
	}
	if got != TransportGateway {
		t.Fatalf("Transport after a rejected SetTransport = %q, want the untouched previous value %q", got, TransportGateway)
	}

	// Reopen, so this also proves the rejected write never reached disk at all.
	st2, err := OpenTransportStore(path)
	if err != nil {
		t.Fatalf("reopen OpenTransportStore: %v", err)
	}
	if got, _ := st2.Transport(); got != TransportGateway {
		t.Fatalf("reopened Transport = %q, want %q", got, TransportGateway)
	}
}

// TestTransportStore_CorruptFileFailsClosed mirrors errCorruptOutbox/errCorruptSeqFile:
// an unreadable or malformed durable file is an error at open, never a silent default.
// A silent default here would be worse than the outbox/seq cases -- it could resurrect
// legacy_relay for a pairing that already migrated off it, double-firing wakes on two
// transports at once, which is exactly what P12 forbids.
func TestTransportStore_CorruptFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "push-transport")
	if err := os.WriteFile(path, []byte("not a valid push_transport file"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := OpenTransportStore(path); err == nil {
		t.Fatal("OpenTransportStore over a corrupt file returned nil error, want a fail-closed refusal")
	}
}

// --- TransportRouter: exclusive delivery --------------------------------------------

func routerWithTransport(t *testing.T, transport PushTransport) (*TransportRouter, *fakePusher, *spyGateway) {
	t.Helper()
	ts, err := OpenTransportStore("")
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := ts.SetTransport(transport); err != nil {
		t.Fatalf("SetTransport(%s): %v", transport, err)
	}
	legacy := &fakePusher{}
	gw := &spyGateway{}
	return &TransportRouter{Transport: ts, Legacy: legacy, Gateway: gw}, legacy, gw
}

// TestTransportRouter_LegacyRelayUsesOnlyTheRelayPusher pins the surviving legacy path
// (P12): under legacy_relay the router forwards to the relay's push_trigger op, byte for
// byte, and the gateway obligation machine is never touched.
func TestTransportRouter_LegacyRelayUsesOnlyTheRelayPusher(t *testing.T) {
	r, legacy, gw := routerWithTransport(t, TransportLegacyRelay)
	env := []byte("legacy-78-byte-wake-stand-in-for-a-routing-only-test")
	if err := r.PushTrigger(context.Background(), "phone-routing-id", env); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	if got := legacy.count(); got != 1 {
		t.Fatalf("legacy relay pusher called %d times, want 1", got)
	}
	if calls := legacy.all(); len(calls) == 1 && string(calls[0].env) != string(env) {
		t.Fatalf("legacy pusher received %q, want the exact envelope %q it was handed", calls[0].env, env)
	}
	if triggers, drives := gw.counts(); triggers != 0 || drives != 0 {
		t.Fatalf("gateway obligation machine touched (trigger=%d, drive=%d) under legacy_relay, want (0, 0): "+
			"P12 forbids delivering on both channels", triggers, drives)
	}
}

// TestTransportRouter_GatewayTransportNeverCallsTheRelayPusher is the mirror: under
// gateway transport, the legacy relay op is never invoked (so it can never fire
// push_trigger for a pairing that has moved off it), and the obligation machine is
// driven exactly once per trigger.
func TestTransportRouter_GatewayTransportNeverCallsTheRelayPusher(t *testing.T) {
	r, legacy, gw := routerWithTransport(t, TransportGateway)
	if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("ignored under gateway transport")); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	if got := legacy.count(); got != 0 {
		t.Fatalf("legacy relay pusher called %d times under gateway transport, want 0 -- a gateway pairing "+
			"must never also fire push_trigger", got)
	}
	triggers, drives := gw.counts()
	if triggers != 1 || drives != 1 {
		t.Fatalf("gateway obligation machine (trigger=%d, drive=%d), want (1, 1)", triggers, drives)
	}
}

// TestTransportRouter_ForegroundOnlyFiresNothing pins PG-MIG-4: a user-chosen degraded
// state that fires neither channel, honestly.
//
// NOT DISCRIMINATING beyond the compile step: an all-negative assertion ("nothing was
// called") is satisfied by any do-nothing implementation as well as a correct one --
// the same limit TestPBPUSH0_PushConfigCarriesNoContentKey documents for its own
// negative half. TestTransportRouter_LegacyRelayUsesOnlyTheRelayPusher and
// ...GatewayTransportNeverCallsTheRelayPusher each pair this negative half with a
// POSITIVE control (something WAS called), which is what actually discriminates.
func TestTransportRouter_ForegroundOnlyFiresNothing(t *testing.T) {
	r, legacy, gw := routerWithTransport(t, TransportForegroundOnly)
	if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("should go nowhere")); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	if got := legacy.count(); got != 0 {
		t.Fatalf("legacy relay pusher called %d times under foreground_only, want 0", got)
	}
	triggers, drives := gw.counts()
	if triggers != 0 || drives != 0 {
		t.Fatalf("gateway obligation machine (trigger=%d, drive=%d) under foreground_only, want (0, 0)", triggers, drives)
	}
}

// TestTransportRouter_NilArmsDoNotPanic is the regression proof for the reviewer's
// finding: service.go leaves Legacy nil whenever cfg.Relay does not implement
// PushTriggerer -- the SUPPORTED no-push configuration (PB-PUSH-5, "no push transport
// is not a failure"), not a construction error -- and legacy_relay is
// OpenTransportStore's fresh-file DEFAULT (PG-MIG-1). Before the nil guards, the first
// wake-worthy record on a router built this way panicked on a nil interface inside
// RunJournal's read loop, crashing swarm-remote rather than merely skipping a push. The
// same class applies to a nil Gateway (gateway transport) and a nil Transport
// (externally-constructed router): every arm must degrade to the documented no-push nil.
func TestTransportRouter_NilArmsDoNotPanic(t *testing.T) {
	t.Run("nil Legacy under the default (legacy_relay) transport", func(t *testing.T) {
		ts, err := OpenTransportStore("") // fresh, in-memory: defaults to legacy_relay
		if err != nil {
			t.Fatalf("OpenTransportStore: %v", err)
		}
		r := &TransportRouter{Transport: ts, Gateway: &spyGateway{}}
		if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("env")); err != nil {
			t.Fatalf("PushTrigger with nil Legacy = %v, want nil (PB-PUSH-5's no-push semantics)", err)
		}
	})

	t.Run("nil Gateway under gateway transport", func(t *testing.T) {
		ts, err := OpenTransportStore("")
		if err != nil {
			t.Fatalf("OpenTransportStore: %v", err)
		}
		if err := ts.SetTransport(TransportGateway); err != nil {
			t.Fatalf("SetTransport(gateway): %v", err)
		}
		r := &TransportRouter{Transport: ts, Legacy: &fakePusher{}}
		if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("env")); err != nil {
			t.Fatalf("PushTrigger with nil Gateway = %v, want nil", err)
		}
		if err := r.PreAppendObligation(); err != nil {
			t.Fatalf("PreAppendObligation with nil Gateway = %v, want nil", err)
		}
	})

	t.Run("nil Transport on an externally-constructed router", func(t *testing.T) {
		r := &TransportRouter{Legacy: &fakePusher{}, Gateway: &spyGateway{}}
		if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("env")); err != nil {
			t.Fatalf("PushTrigger with nil Transport = %v, want nil", err)
		}
		if err := r.PreAppendObligation(); err != nil {
			t.Fatalf("PreAppendObligation with nil Transport = %v, want nil", err)
		}
	})
}

// TestTransportRouter_SelectionIsReadFreshOnEveryCall proves selection is not cached at
// construction: a durable transport flip between two triggers (as PG-MIG-2's atomic
// transition performs) is honoured on the very next call, and the two channels still
// never both fire across the flip.
func TestTransportRouter_SelectionIsReadFreshOnEveryCall(t *testing.T) {
	ts, err := OpenTransportStore("")
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := ts.SetTransport(TransportLegacyRelay); err != nil {
		t.Fatalf("SetTransport(legacy_relay): %v", err)
	}
	legacy := &fakePusher{}
	gw := &spyGateway{}
	r := &TransportRouter{Transport: ts, Legacy: legacy, Gateway: gw}

	if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("first, under legacy_relay")); err != nil {
		t.Fatalf("PushTrigger #1: %v", err)
	}
	if err := ts.SetTransport(TransportGateway); err != nil {
		t.Fatalf("SetTransport(gateway): %v", err)
	}
	if err := r.PushTrigger(context.Background(), "phone-routing-id", []byte("second, under gateway")); err != nil {
		t.Fatalf("PushTrigger #2: %v", err)
	}

	if got := legacy.count(); got != 1 {
		t.Fatalf("legacy relay pusher called %d times across the flip, want exactly 1 (only the first call)", got)
	}
	triggers, drives := gw.counts()
	if triggers != 1 || drives != 1 {
		t.Fatalf("gateway obligation machine (trigger=%d, drive=%d) across the flip, want (1, 1) (only the second call)", triggers, drives)
	}
}
