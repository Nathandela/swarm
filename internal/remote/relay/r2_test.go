package relay

// Relay Hardening R2 — FAILING-FIRST (TDD RED, GG-5) tests remediating the
// audit-committee findings CR-4 and CR-3 from
// docs/verification/remote-phase1-relay-review.md.
//
// These tests encode the REQUIRED post-fix behavior. They are RED against the
// reviewed relay (commit 8664f3b, plus the R1/R1b hardening) in two ways:
//
//  1. COMPILE-LEVEL RED (undefined symbols) — the CONTRACT the implementer must
//     supply. Because Go builds one test binary per package, referencing these
//     not-yet-existing symbols fails the whole relay test build — the same
//     undefined-symbol RED style as relay-red.txt / harden_test.go. The new
//     symbols this file references (none exist yet):
//
//     CR-4 (mailbox pagination + depth cap):
//       - Quotas.MailboxMaxItems int
//           Per-mailbox depth cap. Appending past it is a CLEAN
//           ErrQuotaExceeded, never unbounded growth / resource exhaustion.
//           (An operator may instead/also enforce Quotas.MailboxMaxBytes int64;
//           this suite pins the item cap because it is deterministic to count.)
//       - (*Client).MailboxReadPage(ctx, cursor uint64, limit int) ([]Item, bool, error)
//           A BOUNDED mailbox_read: returns at most a page of items plus a
//           has_more flag. A single reply must NEVER exceed MaxFrame. limit<=0
//           asks for the server's own default page bound (which itself must keep
//           the reply under MaxFrame).
//
//     CR-3 (sweeps wired on a timer, not called by hand):
//       - Config.SweepInterval time.Duration
//           Start launches a baseCtx-guarded goroutine that, every SweepInterval,
//           calls BOTH SweepPresence and SweepRetention using the INJECTED clock.
//           SweepInterval<=0 disables the loop (preserving today's manual-only
//           behavior the existing sweep tests depend on). DefaultConfig MUST keep
//           it 0 so TestPresence_TransitionsAndSilentPush / TestRelay_RetentionPurge
//           stay deterministic; the shipped binary (cmd/swarm-relay/main.go) MUST
//           set a non-zero value — that is the CR-3 wiring. The enforceable test
//           lives in-package and opts in explicitly.
//
//  2. BEHAVIORAL RED (once the contract symbols exist) — the assertions below
//     fail against the current logic: handleMailboxRead returns EVERY item in one
//     frame (server.go), so an oversized backlog trips WriteFrame's ErrFrameTooLarge
//     and tears the connection (the permanent brick, CR-4); appendItem has no depth
//     cap (store.go), so growth is unbounded (CR-4); and no goroutine ever calls the
//     sweeps, so on the injected clock a purge/silent-push never happens without a
//     manual Sweep* call (CR-3).
//
// This file adds NO implementation and modifies NO existing test.
//
// Real-clock note (CR-3 sweep-loop tests only): the wiring under test is a real
// time.Ticker, so these two tests use a SHORT real SweepInterval plus a real-time
// eventually-poll. Every TTL/retention DECISION the sweep makes still reads the
// injected fakeClock — only the tick cadence and the poll are wall-clock. This is
// the deterministic-outcome approach the R2 slice sanctions for the timer seam.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

// eventually polls fn every 5ms until it returns true or timeout elapses,
// reporting failure via msg. It is the real-time settle used ONLY by the
// CR-3 sweep-loop tests (the sweep DECISION still uses the injected clock).
func eventually(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s (not satisfied within %v)", msg, timeout)
}

// ---------------------------------------------------------------------------
// CR-4 — mailbox_read pagination (never bricks) + a per-mailbox depth cap.
// ---------------------------------------------------------------------------

// TestMailbox_ReadPaginatedNeverBricks (CR-4) is the anti-brick test. It stores a
// backlog whose FULL serialized form would exceed MaxFrame (1 MiB), then proves:
//   - a mailbox_read reply is BOUNDED and NEVER tears the connection (the reviewed
//     handleMailboxRead returns everything -> WriteFrame ErrFrameTooLarge -> the
//     non-nil dispatch error closes the socket -> the client never gets a cursor to
//     ack -> the next read re-serializes the same oversized backlog and fails
//     identically: a PERMANENT brick), and
//   - repeated bounded reads + acks fully DRAIN the mailbox, in ascending cursor
//     order, down to depth 0.
//
// Against the reviewed relay this FAILS twice over: MailboxReadPage does not exist
// (compile RED), and the unbounded single-frame read bricks the mailbox (behavioral
// RED) — the existing-signature MailboxRead below returns a connection-tearing error
// rather than a bounded page.
func TestMailbox_ReadPaginatedNeverBricks(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		// Keep the depth cap and append rate out of the way: this test is about the
		// READ side. The point is the backlog is legitimately large (a device offline
		// while traffic accrued), not that it is over any cap.
		c.Quotas.MailboxMaxItems = 100_000
		c.Quotas.MailboxAppendPerMin = 100_000
	})
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	// 24 envelopes of ~48 KiB each: the raw ciphertext alone sums to > 1 MiB, so the
	// JSON reply carrying all of them (base64 only inflates) is guaranteed to exceed
	// MaxFrame. A single unpaginated read therefore cannot be written in one frame.
	const (
		itemCount = 24
		itemSize  = 48 * 1024
	)
	plaintext := make([]byte, itemSize)
	for i := range plaintext {
		plaintext[i] = byte(i)
	}
	rawTotal := 0
	for i := uint64(1); i <= itemCount; i++ {
		env := sp.sealMailbox(t, i, plaintext, clk)
		rawTotal += len(env)
		if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
	}
	// Precondition: the full backlog genuinely overflows a single frame. Raw
	// ciphertext alone already exceeds MaxFrame; JSON+base64 makes the gap larger.
	if rawTotal <= MaxFrame {
		t.Fatalf("test precondition: backlog raw bytes %d must exceed MaxFrame %d so a single unpaginated read overflows", rawTotal, MaxFrame)
	}

	// (a) The connection must NOT be torn. Even the existing-signature MailboxRead
	// (which asks for no explicit bound) must come back cleanly with a BOUNDED page
	// rather than trip ErrFrameTooLarge and drop the socket. In the reviewed relay
	// this returns a transport error (the brick); post-fix it returns a subset.
	first, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("mailbox_read on an oversized backlog tore the connection (CR-4 brick): %v", err)
	}
	if len(first) == 0 || len(first) >= itemCount {
		t.Fatalf("mailbox_read returned %d items; want a BOUNDED page (0 < n < %d) that fits under MaxFrame (CR-4)", len(first), itemCount)
	}

	// (b) Bounded reads + acks fully drain the mailbox, in ascending cursor order.
	// limit 0 asks for the server default page bound; each page must stay under
	// MaxFrame (proven by the read not tearing the connection).
	var seen []uint64
	sawHasMore := false
	for iter := 0; iter < 1000; iter++ {
		page, hasMore, perr := device.MailboxReadPage(testCtx(t), 0, 0)
		if perr != nil {
			t.Fatalf("MailboxReadPage iter %d: %v", iter, perr)
		}
		if len(page) == 0 {
			if hasMore {
				t.Fatalf("MailboxReadPage returned 0 items but has_more=true (would spin forever); a non-empty backlog must yield progress (CR-4)")
			}
			break
		}
		for _, it := range page {
			if len(seen) > 0 && it.Cursor <= seen[len(seen)-1] {
				t.Fatalf("mailbox drained out of order: cursor %d after %d (CR-4 must preserve ascending storage order)", it.Cursor, seen[len(seen)-1])
			}
			seen = append(seen, it.Cursor)
		}
		if hasMore {
			sawHasMore = true
		}
		// Ack through this page's last cursor so the next read makes progress.
		if err := device.MailboxAck(testCtx(t), page[len(page)-1].Cursor); err != nil {
			t.Fatalf("MailboxAck: %v", err)
		}
		if !hasMore {
			break
		}
	}
	if !sawHasMore {
		t.Fatalf("drain never saw has_more=true, yet the backlog (%d items, %d raw bytes) cannot fit one frame; mailbox_read must paginate with has_more (CR-4)", itemCount, rawTotal)
	}
	if len(seen) != itemCount {
		t.Fatalf("paginated drain yielded %d items, want %d (every item must be drainable in order, no brick) (CR-4)", len(seen), itemCount)
	}
	if d := srv.MailboxDepth(devRID); d != 0 {
		t.Fatalf("mailbox not fully drained: depth %d, want 0 (CR-4)", d)
	}
}

// TestMailbox_DepthCapCleanQuota (CR-4) asserts a per-mailbox DEPTH cap: appending
// past Quotas.MailboxMaxItems is refused with a CLEAN ErrQuotaExceeded (not resource
// exhaustion, not a torn connection), the mailbox does not grow past the cap, and the
// connection stays usable afterward. Against the reviewed relay appendItem has no
// depth cap (store.go) so growth is unbounded — this FAILS (no ErrQuotaExceeded ever
// observed, and depth climbs past the cap).
func TestMailbox_DepthCapCleanQuota(t *testing.T) {
	const capN = 4
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.MailboxMaxItems = capN
		// Keep the per-minute APPEND rate out of the way: the DEPTH cap, not the
		// rate window, must be what refuses the overflow.
		c.Quotas.MailboxAppendPerMin = 100_000
	})
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	for i := uint64(1); i <= capN; i++ {
		env := sp.sealMailbox(t, i, []byte("under-cap"), clk)
		if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
			t.Fatalf("MailboxAppend #%d under the depth cap: %v", i, err)
		}
	}
	// The (capN+1)th append overflows the mailbox depth: a CLEAN refusal.
	over := sp.sealMailbox(t, capN+1, []byte("over-cap"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, over); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("MailboxAppend past the depth cap: got %v, want ErrQuotaExceeded (CR-4 depth cap)", err)
	}
	// The mailbox did not grow past the cap.
	if d := srv.MailboxDepth(devRID); d != capN {
		t.Fatalf("mailbox depth %d after an over-cap append, want %d (the overflow must not be stored) (CR-4)", d, capN)
	}
	// The refusal was clean, not a tear: the connection still serves a read.
	if _, err := device.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("connection unusable after an over-cap refusal: %v; a quota refusal must be clean, not a tear (CR-4)", err)
	}
	// And once the device drains an item, capacity is restored (the cap is on live
	// depth, not a lifetime total).
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead: %v", err)
	}
	if err := device.MailboxAck(testCtx(t), items[0].Cursor); err != nil {
		t.Fatalf("MailboxAck: %v", err)
	}
	again := sp.sealMailbox(t, capN+2, []byte("after-drain"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, again); err != nil {
		t.Fatalf("MailboxAppend after draining below the cap: %v; capacity must recover once depth falls (CR-4)", err)
	}
}

// ---------------------------------------------------------------------------
// CR-3 — the sweeps run on a timer inside Start, driven by the injected clock,
// with NO manual Sweep* call. The shipped binary must set Config.SweepInterval;
// the enforceable test opts in explicitly here.
// ---------------------------------------------------------------------------

// TestRelay_SweepLoopRetentionNoManualCall (CR-3) asserts Start's sweep loop purges
// retained mailbox items with NO manual SweepRetention call: with a short real
// SweepInterval, an appended item is purged once the INJECTED clock passes
// RetentionCap. Against the reviewed relay nothing ever calls SweepRetention (the
// shipped binary starts no ticker, server.go/main.go) so the item is retained
// forever — the eventually-poll never sees depth 0 and this FAILS.
func TestRelay_SweepLoopRetentionNoManualCall(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.SweepInterval = 5 * time.Millisecond // real tick cadence; decisions use fakeClock
		c.RetentionCap = 7 * 24 * time.Hour
		c.Quotas.MailboxMaxItems = 100_000
		c.Quotas.MailboxAppendPerMin = 100_000
	})
	machine, _, devRID, sp := mailboxFixture(t, srv, clk)

	env := sp.sealMailbox(t, 1, []byte("retained"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}
	if srv.MailboxDepth(devRID) != 1 {
		t.Fatalf("precondition: appended item not present (depth %d, want 1)", srv.MailboxDepth(devRID))
	}

	// Advance the injected clock past the retention cap. The background sweep loop —
	// NOT a manual call — must then purge the item on one of its ticks.
	clk.Advance(8 * 24 * time.Hour)
	eventually(t, 3*time.Second, func() bool {
		return srv.MailboxDepth(devRID) == 0
	}, "mailbox item never purged by the wired sweep loop (CR-3: Start must run SweepRetention on a timer with no manual call)")
}

// TestRelay_SweepLoopPresenceSilentPushNoManualCall (CR-3) asserts Start's sweep loop
// fires the machine-went-silent push with NO manual SweepPresence call: after a paired
// machine's gateway drops and the INJECTED clock passes PresenceTimeout, the loop
// delivers EXACTLY ONE silent push to the paired device's registered token (the
// presenceEntry.notified guard makes it exactly-once even though the loop ticks many
// times). Against the reviewed relay nothing calls SweepPresence, so the push never
// fires — the eventually-poll never sees a push and this FAILS.
func TestRelay_SweepLoopPresenceSilentPushNoManualCall(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, func(c *Config) {
		c.SweepInterval = 5 * time.Millisecond // real tick cadence; decisions use fakeClock
		c.PresenceTimeout = 30 * time.Second
	})

	// The device registers its push token so the silent-push path has a target.
	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "apns-token-device"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}

	// The machine authorizes the device (so it is a paired push target), goes online,
	// then its gateway drops.
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	if err := machine.Close(); err != nil {
		t.Fatalf("machine.Close: %v", err)
	}

	// Advance the injected clock past the presence timeout. The background sweep loop
	// — NOT a manual call — must fire exactly one silent push to the device token.
	clk.Advance(31 * time.Second)
	eventually(t, 3*time.Second, func() bool {
		return len(apns.all()) >= 1
	}, "machine-went-silent push never fired from the wired sweep loop (CR-3: Start must run SweepPresence on a timer with no manual call)")

	// Let several more ticks pass; the notified guard must keep it exactly-once.
	time.Sleep(100 * time.Millisecond)
	pushes := apns.all()
	if len(pushes) != 1 {
		t.Fatalf("silent-push count from the sweep loop: got %d, want exactly 1 (the transition must fire once, not once per tick) (CR-3)", len(pushes))
	}
	if pushes[0].token != "apns-token-device" {
		t.Fatalf("silent push aimed at %q, want the paired device token (CR-3)", pushes[0].token)
	}
}

// interfaceGuards references the new contract symbols in a value context so the
// compile-level RED points squarely at the missing surface (not only at deep call
// sites), documenting the exact shapes the implementer must add. It is never run.
func interfaceGuards(c *Client) {
	_ = Quotas{}.MailboxMaxItems // int: per-mailbox depth cap (CR-4)
	_ = Config{}.SweepInterval   // time.Duration: sweep-loop tick seam (CR-3)
	// (*Client).MailboxReadPage(ctx, cursor, limit) ([]Item, bool, error) (CR-4)
	items, hasMore, err := c.MailboxReadPage(context.Background(), 0, 0)
	_, _, _ = items, hasMore, err
}
