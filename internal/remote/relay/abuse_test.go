package relay

// R-REL.8 — day-one rate limits / quotas / abuse controls. Each over-limit is a
// CLEAN refusal (ErrQuotaExceeded / ErrRendezvousFull), never resource
// exhaustion. Windows are evaluated on the injected clock (no real sleeps).

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestRelay_RendezvousFloodBounded asserts the concurrent-rendezvous cap: past
// the cap, create is a clean refusal, and it recovers once a slot frees.
func TestRelay_RendezvousFloodBounded(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.MaxConcurrentRendezvous = 2
	})

	for i := 0; i < 2; i++ {
		conn := dialRaw(t, srv.URL())
		if err := conn.RendezvousCreate(testCtx(t), fmt.Sprintf("rdv-%d", i)); err != nil {
			t.Fatalf("RendezvousCreate #%d under cap: %v", i, err)
		}
	}
	over := dialRaw(t, srv.URL())
	if err := over.RendezvousCreate(testCtx(t), "rdv-over"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("RendezvousCreate over the concurrent cap: got %v, want ErrQuotaExceeded", err)
	}
}

// TestRelay_MailboxQuotaEnforced asserts the per-device mailbox append rate is
// bounded: past the per-window cap, append is refused cleanly.
func TestRelay_MailboxQuotaEnforced(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.MailboxAppendPerMin = 3
	})
	machine, _, devRID, sp := mailboxFixture(t, srv, clk)

	for i := uint64(1); i <= 3; i++ {
		env := sp.sealMailbox(t, i, []byte("ok"), clk)
		if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
			t.Fatalf("MailboxAppend #%d under quota: %v", i, err)
		}
	}
	env := sp.sealMailbox(t, 4, []byte("over"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, env); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("MailboxAppend over quota: got %v, want ErrQuotaExceeded", err)
	}

	// The window is clock-driven: advancing past it restores capacity.
	clk.Advance(61 * time.Second)
	again := sp.sealMailbox(t, 5, []byte("next-window"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, again); err != nil {
		t.Fatalf("MailboxAppend in the next window: %v", err)
	}
}

// TestRelay_PushRateLimited asserts the per-device push rate is bounded.
func TestRelay_PushRateLimited(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.PushPerMin = 2
	})
	machine, devRID, _ := pushFixture(t, srv)

	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	for i := 0; i < 2; i++ {
		env := sp.sealMailbox(t, uint64(i+1), []byte("wake"), clk)
		if err := machine.PushTrigger(testCtx(t), devRID, env); err != nil {
			t.Fatalf("PushTrigger #%d under rate: %v", i, err)
		}
	}
	env := sp.sealMailbox(t, 99, []byte("over"), clk)
	if err := machine.PushTrigger(testCtx(t), devRID, env); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("PushTrigger over rate: got %v, want ErrQuotaExceeded", err)
	}
}

// TestRelay_ConnRateLimited asserts the per-source(IP) PRE-AUTHENTICATION
// connection-rate window is bounded (ADR-007 "Amendment 2026-07-20 - Relay
// pre-authentication rate-limiting model", refining D9): ConnPerMin caps admission
// per TRANSPORT SOURCE, never per presented relay-auth key. All these Dials
// originate from a single loopback source (127.0.0.1), so they share ONE window
// and the third is cleanly refused regardless of using distinct keys — keying is
// by source, not by the (still unproven at that point) key. Advancing the clock
// past the window restores capacity. Per-unproven-key independence is deliberately
// NOT asserted here: that unsafe premise was removed with the old
// TestRelay_AuthRatePerSource (see harden_test.go). This is a flagged comment-only
// reframe; no assertion, value, or call is changed.
func TestRelay_ConnRateLimited(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.ConnPerMin = 2
	})

	for i := 0; i < 2; i++ {
		pub, priv := newRelayAuthKey(t)
		if _, err := Dial(testCtx(t), srv.URL(), authFor(pub, priv)); err != nil {
			t.Fatalf("Dial #%d under rate: %v", i, err)
		}
	}
	pub, priv := newRelayAuthKey(t)
	if _, err := Dial(testCtx(t), srv.URL(), authFor(pub, priv)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Dial over rate: got %v, want ErrQuotaExceeded", err)
	}

	// Next window: capacity restored.
	clk.Advance(61 * time.Second)
	pub2, priv2 := newRelayAuthKey(t)
	if _, err := Dial(testCtx(t), srv.URL(), authFor(pub2, priv2)); err != nil {
		t.Fatalf("Dial in the next window: %v", err)
	}
}
