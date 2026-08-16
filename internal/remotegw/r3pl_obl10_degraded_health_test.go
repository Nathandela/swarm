package remotegw

// Bead agents-tracker-hggx.4.5 -- FAILING-FIRST (TDD RED, GG-5) tests for PG-OBL-10
// (docs/specifications/push-gateway-api.md section 6.4): "The obligation SHALL be
// observable in machine health: a pairing whose last obligation reached `expired` or
// `abandoned` is a visible degraded push state."
//
// The degraded-state surface that already exists -- and that these tests read -- is
// Service.Err (service.go, ADR-007 B114): the one reader cmd/swarm-remote prints to the
// unit's log. Today it joins CommandBridge.Err, RelaySink.Err and PushNotifier.Err and
// observes NOTHING of the wake-obligation store, so a pairing whose phone has stopped
// receiving wakes -- every obligation expiring or abandoned -- is indistinguishable from
// a healthy one. The obligation store's own doc comment (obligation.go, "sufficient for
// PG-OBL-10") is the other end of the gap: Get answers the pairing's LAST obligation,
// and nothing reads it.
//
// These tests exercise ONLY symbols that already exist; they fail at ASSERTION level
// (Service.Err() == nil where PG-OBL-10 requires a visible degraded state).
// This file contains NO implementation.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// driveObligationTo drives one freshly minted obligation for addr to the requested
// terminal state through the REAL machine -- never by hand-writing store records -- and
// returns the store the Service under test is then configured with.
//
//   - expired:   mint, advance the virtual clock past WakeV1Expiry, Drive (the expiry
//     branch marks it expired without a submit).
//   - abandoned: mint, Drive against a gateway refusing retryable=false
//     (410 address_revoked, spec section 4).
//   - delivered: mint, Drive against a gateway that accepts.
//   - pending:   mint only (a live obligation still being retried).
func driveObligationTo(t *testing.T, addr PushAddress, state ObligationState) ObligationStore {
	t.Helper()
	store, err := OpenObligationStore("")
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	clk := newTestClock()
	sub := &fakeSubmitter{}
	if state == ObligationAbandoned {
		sub.outcomes = []error{&WakeSubmitError{Code: "address_revoked", Retryable: false, Message: "test refusal"}}
	}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: seq, Now: clk.Now,
	})
	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	switch state {
	case ObligationPending:
		// minted and live; nothing more.
	case ObligationExpired:
		clk.advance(WakeV1Expiry + time.Second)
		if err := m.Drive(context.Background()); err != nil {
			t.Fatalf("Drive(expiry): %v", err)
		}
	default:
		if err := m.Drive(context.Background()); err != nil && state == ObligationDelivered {
			t.Fatalf("Drive: %v", err)
		}
	}
	ob, ok, err := store.Get(addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after driving: ok=%v err=%v", ok, err)
	}
	if ob.State != state {
		t.Fatalf("setup: drove the obligation to %q, want %q", ob.State, state)
	}
	return store
}

// newObligationHealthService assembles a migrated (gateway-transport) Service over the
// given obligation store for addr -- the configuration in which the obligation's
// terminal state IS the pairing's push health.
func newObligationHealthService(t *testing.T, addr PushAddress, store ObligationStore) *Service {
	t.Helper()
	ts, err := OpenTransportStore("")
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := ts.SetTransport(TransportGateway); err != nil {
		t.Fatalf("SetTransport(gateway): %v", err)
	}
	return NewService(ServiceConfig{
		Relay:       &scriptedMailbox{},
		PhoneTarget: "phone",
		PushGateway: &PushGatewayConfig{
			GatewayURL:       "https://gateway.invalid",
			SubmitCapability: "test-submit-capability",
			Address:          addr,
			Transport:        ts,
			Obligations:      store,
		},
	})
}

// TestOBL10_ExpiredLastObligationIsAVisibleDegradedPushState pins the expired half of
// PG-OBL-10: a pairing whose last wake obligation ran out its five minutes undelivered
// has a phone that was never woken, and the operator must be able to read that from the
// runtime's one degraded-state surface.
func TestOBL10_ExpiredLastObligationIsAVisibleDegradedPushState(t *testing.T) {
	addr := testPushAddress(0x5E)
	store := driveObligationTo(t, addr, ObligationExpired)
	svc := newObligationHealthService(t, addr, store)

	err := svc.Err()
	if err == nil {
		t.Fatalf("Service.Err() = nil for a pairing whose last wake obligation reached %q: PG-OBL-10 requires "+
			"a visible degraded push state -- the phone has stopped receiving wakes and nothing reports it "+
			"(bd agents-tracker-hggx.4.5)", ObligationExpired)
	}
	if !strings.Contains(err.Error(), string(ObligationExpired)) {
		t.Fatalf("Service.Err() = %q: the degraded state must NAME the terminal state (%q) so the operator "+
			"reading the unit log learns what happened, not merely that something did", err, ObligationExpired)
	}
}

// TestOBL10_AbandonedLastObligationNamesTheStateAndItsOutcomeCode pins the abandoned
// half, plus the repair path: PG-OBL-1 persists the last outcome code precisely so the
// operator learns WHY (address_revoked means re-pair; push_token_unregistered means the
// handset must rotate), and a degraded state that withholds it surfaces a symptom with
// no repair.
func TestOBL10_AbandonedLastObligationNamesTheStateAndItsOutcomeCode(t *testing.T) {
	addr := testPushAddress(0x5F)
	store := driveObligationTo(t, addr, ObligationAbandoned)
	svc := newObligationHealthService(t, addr, store)

	err := svc.Err()
	if err == nil {
		t.Fatalf("Service.Err() = nil for a pairing whose last wake obligation reached %q: PG-OBL-10 requires "+
			"a visible degraded push state (bd agents-tracker-hggx.4.5)", ObligationAbandoned)
	}
	if !strings.Contains(err.Error(), string(ObligationAbandoned)) {
		t.Fatalf("Service.Err() = %q: the degraded state must name the terminal state (%q)", err, ObligationAbandoned)
	}
	if !strings.Contains(err.Error(), "address_revoked") {
		t.Fatalf("Service.Err() = %q: the degraded state must carry the obligation's last outcome code "+
			"(%q) -- it is what tells the operator the repair path", err, "address_revoked")
	}
}

// TestOBL10_HealthyAndLiveObligationsDoNotDegradeServiceErr is the discriminating
// control: a delivered last obligation is a working push path, a pending one is still
// being retried, and an unmigrated service has no obligation machine at all -- none of
// the three is degraded, so none may pollute the operator's one error surface.
//
// NOT A RED TEST beyond the compile step (the same documented-fence category as
// TestPBPUSH0_PushConfigCarriesNoContentKey): it passes against today's Err() because
// nothing reads the obligation store at all. Its two POSITIVE siblings above are what
// discriminate; this one exists so GREEN's wiring cannot overshoot and mark every
// migrated pairing degraded.
func TestOBL10_HealthyAndLiveObligationsDoNotDegradeServiceErr(t *testing.T) {
	addrD := testPushAddress(0x60)
	svcDelivered := newObligationHealthService(t, addrD, driveObligationTo(t, addrD, ObligationDelivered))
	if err := svcDelivered.Err(); err != nil {
		t.Fatalf("Service.Err() = %v for a pairing whose last obligation was DELIVERED, want nil", err)
	}

	addrP := testPushAddress(0x61)
	svcPending := newObligationHealthService(t, addrP, driveObligationTo(t, addrP, ObligationPending))
	if err := svcPending.Err(); err != nil {
		t.Fatalf("Service.Err() = %v for a pairing with a live (pending) obligation, want nil: a wake still "+
			"inside its retry horizon is not a degraded state", err)
	}

	svcUnmigrated := NewService(ServiceConfig{Relay: &scriptedMailbox{}, PhoneTarget: "phone"})
	if err := svcUnmigrated.Err(); err != nil {
		t.Fatalf("Service.Err() = %v for an unmigrated (no PushGateway) service, want nil", err)
	}
}
