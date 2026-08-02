package relay

// ADR-007 B46 part 3 / B47b: expiry DELETES a rendezvous slot without BURNING its id.
//
// The claim in handleRendezvousCreate reads "a burned (completed, single-use) id or a
// live slot is refused so the original creator's in-flight pairing is never orphaned or
// hijacked". True of Complete, and exactly false past TTL: purgeExpiredRendezvous drops
// the slot and leaves the id free, rendezvous_create carries no requireAuth, and the
// machine's `swarm remote pair` blocks with the QR still on screen. So past TTL an
// unauthenticated stranger re-creates the same label and the phone that finally scans
// attaches to the STRANGER, with the real machine orphaned on Recv.
//
// The fence is: an id whose slot died of old age is as dead as one that completed.

import (
	"errors"
	"testing"
	"time"
)

// TestB46_AnExpiredRendezvousIDIsBurnedNotSilentlyFreed is the direct fence on the
// reviewer's probe: the stranger's re-create must be REFUSED, not accepted.
func TestB46_AnExpiredRendezvousIDIsBurnedNotSilentlyFreed(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)

	const id = "0123456789abcdef0123456789abcdef"

	machine := dialRaw(t, srv.URL())
	if err := machine.RendezvousCreate(ctx, id); err != nil {
		t.Fatalf("machine create: %v", err)
	}

	// While it is live the fence already exists: a stranger cannot take it.
	stranger := dialRaw(t, srv.URL())
	if err := stranger.RendezvousCreate(ctx, id); !errors.Is(err, ErrRendezvousExists) {
		t.Fatalf("live re-create: got %v, want ErrRendezvousExists", err)
	}

	// Nothing completed this pairing, so nothing burned the id the old way. Time passes.
	clk.Advance(cfg.RendezvousTTL + time.Second)

	if err := stranger.RendezvousCreate(ctx, id); !errors.Is(err, ErrRendezvousBurned) {
		t.Fatalf("re-create of an EXPIRED id: got %v, want ErrRendezvousBurned.\n"+
			"  A stranger that owns the responder side of a label whose QR is still on the "+
			"owner's screen receives the next phone to scan it, and the real machine is left "+
			"orphaned on Recv with no error.", err)
	}

	// And the phone that scans the stale QR is refused rather than mis-routed.
	phone := dialRaw(t, srv.URL())
	if err := phone.RendezvousClaim(ctx, id); err == nil {
		t.Fatal("a phone claimed an expired rendezvous id; it must be refused, not attached")
	}
}

// TestB46_AClaimPastTTLBurnsTheIDItReportsExpired covers the OTHER site that drops a
// slot for age. handleRendezvousClaim evaluates the TTL inline and deletes the slot
// itself, so a fix applied only to purgeExpiredRendezvous leaves this path free — and
// this is the path the victim phone itself walks, which is what makes it reachable.
//
// The reported error is unchanged (ErrRendezvousExpired is what the claimer must see);
// the assertion is that the id is dead AFTERWARDS.
func TestB46_AClaimPastTTLBurnsTheIDItReportsExpired(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)

	const id = "fedcba9876543210fedcba9876543210"

	machine := dialRaw(t, srv.URL())
	if err := machine.RendezvousCreate(ctx, id); err != nil {
		t.Fatalf("machine create: %v", err)
	}
	clk.Advance(cfg.RendezvousTTL + time.Second)

	phone := dialRaw(t, srv.URL())
	if err := phone.RendezvousClaim(ctx, id); !errors.Is(err, ErrRendezvousExpired) {
		t.Fatalf("claim past TTL: got %v, want ErrRendezvousExpired", err)
	}
	stranger := dialRaw(t, srv.URL())
	if err := stranger.RendezvousCreate(ctx, id); !errors.Is(err, ErrRendezvousBurned) {
		t.Fatalf("re-create after an expired CLAIM: got %v, want ErrRendezvousBurned", err)
	}
}

// TestB46_ABurnedIDIsForgottenOnceItsWindowHasClosed is the bound the burn must not be
// shipped without. rendezvous_create has no requireAuth, so burning on expiry hands an
// UNAUTHENTICATED stranger a way to mint permanent server-side map entries at will --
// the burn would trade a hijack for a memory leak driven by the same anonymous caller.
//
// So a burn is a WINDOW, not a tombstone: it outlives the announced pairing window (the
// whole point) and is then forgotten. The re-create succeeding here is the deliberate
// price, and it is safe because the QR that named this id expired an entire RendezvousTTL
// earlier -- internal/skeleton's defaultPairTTL is capped at the relay slot.
func TestB46_ABurnedIDIsForgottenOnceItsWindowHasClosed(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)

	const id = "aaaaaaaabbbbbbbbccccccccdddddddd"

	machine := dialRaw(t, srv.URL())
	if err := machine.RendezvousCreate(ctx, id); err != nil {
		t.Fatalf("machine create: %v", err)
	}
	clk.Advance(cfg.RendezvousTTL + time.Second)

	stranger := dialRaw(t, srv.URL())
	if err := stranger.RendezvousCreate(ctx, id); !errors.Is(err, ErrRendezvousBurned) {
		t.Fatalf("re-create inside the burn window: got %v, want ErrRendezvousBurned", err)
	}

	// Past the burn window the tombstone is collected and the id is usable again.
	clk.Advance(2 * cfg.RendezvousTTL)
	if err := stranger.RendezvousCreate(ctx, id); err != nil {
		t.Fatalf("re-create past the burn window: %v -- burns must not accumulate forever, "+
			"they are mintable by an unauthenticated caller", err)
	}

	srv.mu.Lock()
	n := len(srv.burned)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("the burn set still holds %d entries after its window closed", n)
	}
}
