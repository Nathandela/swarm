package relay

// Rendezvous mailbox for PAIRING (R-PAIR.6; the relay provides it). The relay
// forwards opaque bytes between at most two participants keyed by rendezvous_id,
// with a hard 60s relay-side TTL, burned on first completion. Pairing peers are
// NOT yet relay-registered, so rendezvous rides an unauthenticated Conn. The
// relay never sees the pairing secret (that property is proven at the pairing
// crypto layer — the PSK is never on the wire; here the relay only forwards).

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// TestRendezvous_TwoPartyForward asserts opaque bytes are forwarded verbatim in
// both directions between the creator and the single claimer.
func TestRendezvous_TwoPartyForward(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	const id = "rdv-forward"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousCreate: %v", err)
	}
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousClaim: %v", err)
	}

	msg1 := []byte("noise-handshake-msg-1-opaque")
	if err := creator.RendezvousSend(testCtx(t), id, msg1); err != nil {
		t.Fatalf("creator.RendezvousSend: %v", err)
	}
	got1, err := claimer.RendezvousRecv(testCtx(t))
	if err != nil {
		t.Fatalf("claimer.RendezvousRecv: %v", err)
	}
	if !bytes.Equal(got1, msg1) {
		t.Fatalf("forward creator->claimer: got %q, want %q", got1, msg1)
	}

	msg2 := []byte("noise-handshake-msg-2-opaque")
	if err := claimer.RendezvousSend(testCtx(t), id, msg2); err != nil {
		t.Fatalf("claimer.RendezvousSend: %v", err)
	}
	got2, err := creator.RendezvousRecv(testCtx(t))
	if err != nil {
		t.Fatalf("creator.RendezvousRecv: %v", err)
	}
	if !bytes.Equal(got2, msg2) {
		t.Fatalf("forward claimer->creator: got %q, want %q", got2, msg2)
	}
}

// TestRendezvous_ThirdPartyRejected asserts at most two participants: a third
// claim on an active rendezvous is refused.
func TestRendezvous_ThirdPartyRejected(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	const id = "rdv-twoonly"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousCreate: %v", err)
	}
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousClaim: %v", err)
	}
	third := dialRaw(t, srv.URL())
	if err := third.RendezvousClaim(testCtx(t), id); !errors.Is(err, ErrRendezvousFull) {
		t.Fatalf("third-party claim: got %v, want ErrRendezvousFull", err)
	}
}

// TestRendezvous_ExpiredClaimRejected asserts the hard 60s relay-side TTL,
// evaluated on the injected clock: a claim after expiry is refused.
func TestRendezvous_ExpiredClaimRejected(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.RendezvousTTL = 60 * time.Second
	})
	const id = "rdv-expiry"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousCreate: %v", err)
	}

	clk.Advance(61 * time.Second)
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); !errors.Is(err, ErrRendezvousExpired) {
		t.Fatalf("claim after TTL: got %v, want ErrRendezvousExpired", err)
	}
}

// TestRendezvous_BurnedAfterUse asserts single use: after the rendezvous
// completes, the same id cannot be claimed again.
func TestRendezvous_BurnedAfterUse(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	const id = "rdv-burn"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousCreate: %v", err)
	}
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousClaim: %v", err)
	}
	if err := creator.RendezvousComplete(testCtx(t), id); err != nil {
		t.Fatalf("RendezvousComplete: %v", err)
	}

	// The id is burned: a fresh claim is refused as burned (not merely full).
	late := dialRaw(t, srv.URL())
	if err := late.RendezvousClaim(testCtx(t), id); !errors.Is(err, ErrRendezvousBurned) {
		t.Fatalf("claim after burn: got %v, want ErrRendezvousBurned", err)
	}
}
