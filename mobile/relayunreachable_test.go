package swarmmobile

// agents-tracker-n4vs -- the field event, as tests. The owner's first real pairing failed
// because the phone was not on the home WiFi: the relay is a LAN address, the dial died
// before a single handshake byte, and the screen said "ask your machine for a new code" --
// sending them to regenerate a code that was never the problem. PB-PAIR-5's own rule
// (finish's comment): a state earns its own value when the USER'S NEXT MOVE differs. Here it
// does: fix the network, not the code.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

func TestFinish_ADialFailureIsItsOwnState(t *testing.T) {
	p := &Pairing{app: &App{}, state: pairPairing, confirmed: make(chan struct{})}
	p.finish(nil, fmt.Errorf("dial relay: %w", errRelayUnreachable), context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != pairRelayUnreachable {
		t.Fatalf("a dial-stage failure landed on %q, want %q: the screen tells the user to "+
			"regenerate a code that was never the problem", p.state, pairRelayUnreachable)
	}
}

func TestFinish_TheDialSentinelOutranksTheWindowDeadline(t *testing.T) {
	// The cellular case in the field is a CONNECT TIMEOUT: the LAN address black-holes and
	// the pairing window expires around the dial. Classified as rendezvous_timeout, the
	// screen says "check your machine is awake", which it was; the network is the story.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	p := &Pairing{app: &App{}, state: pairPairing, confirmed: make(chan struct{})}
	p.finish(nil, fmt.Errorf("dial relay: %w", errRelayUnreachable), ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != pairRelayUnreachable {
		t.Fatalf("a dial failure under an expired window landed on %q, want %q", p.state, pairRelayUnreachable)
	}
}

func TestJoin_AnUnreachableRelayReachesTheStateThroughTheRealDial(t *testing.T) {
	// End to end through join(): 127.0.0.1:9 refuses immediately (discard port, nothing
	// listens in a test environment), which is the wrong-network failure compressed to
	// something a unit test can wait for.
	p := &Pairing{
		app:       &App{},
		state:     pairPairing,
		deadline:  time.Now().Add(30 * time.Second),
		payload:   pairing.QRPayload{RelayURL: "wss://127.0.0.1:9"},
		confirmed: make(chan struct{}),
	}
	close(p.confirmed)
	p.join(context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != pairRelayUnreachable {
		t.Fatalf("join against a dead relay landed on %q, want %q", p.state, pairRelayUnreachable)
	}
	if p.err == nil || !errors.Is(p.err, errRelayUnreachable) {
		t.Fatalf("the routed error does not carry the unreachable sentinel: %v", p.err)
	}
}
