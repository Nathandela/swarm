package relay

// A relay mailbox cursor is a coordinate in one particular persisted mailbox log. If the
// store is reinitialised while a client keeps its durable cursor, an empty page is ambiguous:
// it can mean "caught up" or "asked past this mailbox's end". These tests pin the relay's
// continuity verdict at the real wire boundary, including the correlated wait reply.

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func appendContinuityItems(t *testing.T, machine *Client, target string, sp sealParty, clk *fakeClock, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, err := machine.MailboxAppend(testCtx(t), target,
			sp.sealMailbox(t, uint64(i), []byte{byte(i)}, clk)); err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
	}
}

func TestMailboxCursorContinuity_ReadVerdicts(t *testing.T) {
	t.Run("cursor ahead of high-water requires reset", func(t *testing.T) {
		srv, _, _, clk := startTestRelay(t, nil)
		machine, device, rid, sp := mailboxFixture(t, srv, clk)
		appendContinuityItems(t, machine, rid, sp, clk, 3)

		if _, err := device.MailboxRead(testCtx(t), 4); !errors.Is(err, ErrMailboxCursorResetRequired) {
			t.Fatalf("MailboxRead(cursor 4, high-water 3) = %v, want ErrMailboxCursorResetRequired", err)
		}
	})

	t.Run("acked cursor at high-water is a clean empty tail", func(t *testing.T) {
		srv, _, _, clk := startTestRelay(t, nil)
		machine, device, rid, sp := mailboxFixture(t, srv, clk)
		appendContinuityItems(t, machine, rid, sp, clk, 3)
		if err := device.MailboxAck(testCtx(t), 3); err != nil {
			t.Fatalf("MailboxAck(3): %v", err)
		}

		items, err := device.MailboxRead(testCtx(t), 3)
		if err != nil {
			t.Fatalf("MailboxRead(cursor == high-water): %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("MailboxRead(cursor == high-water) returned %d items, want empty", len(items))
		}
	})

	t.Run("retained items at or below cursor are normal before async ack", func(t *testing.T) {
		srv, _, _, clk := startTestRelay(t, nil)
		machine, device, rid, sp := mailboxFixture(t, srv, clk)
		appendContinuityItems(t, machine, rid, sp, clk, 3)

		items, err := device.MailboxRead(testCtx(t), 2)
		if err != nil {
			t.Fatalf("MailboxRead(cursor 2, retained history through 2): %v", err)
		}
		if len(items) != 1 || items[0].Cursor != 3 {
			t.Fatalf("MailboxRead(cursor 2) = %+v, want exactly cursor 3", items)
		}
	})

	t.Run("first retained item after cursor reads normally", func(t *testing.T) {
		srv, _, _, clk := startTestRelay(t, nil)
		machine, device, rid, sp := mailboxFixture(t, srv, clk)
		appendContinuityItems(t, machine, rid, sp, clk, 3)
		if err := device.MailboxAck(testCtx(t), 2); err != nil {
			t.Fatalf("MailboxAck(2): %v", err)
		}

		items, err := device.MailboxRead(testCtx(t), 2)
		if err != nil {
			t.Fatalf("MailboxRead(cursor 2, first retained 3): %v", err)
		}
		if len(items) != 1 || items[0].Cursor != 3 {
			t.Fatalf("MailboxRead(cursor 2) = %+v, want exactly cursor 3", items)
		}
	})

	t.Run("max uint64 cannot wrap and requires reset", func(t *testing.T) {
		srv, _, _, clk := startTestRelay(t, nil)
		machine, device, rid, sp := mailboxFixture(t, srv, clk)
		appendContinuityItems(t, machine, rid, sp, clk, 1)

		if _, err := device.MailboxRead(testCtx(t), math.MaxUint64); !errors.Is(err, ErrMailboxCursorResetRequired) {
			t.Fatalf("MailboxRead(MaxUint64) = %v, want ErrMailboxCursorResetRequired", err)
		}
	})
}

func TestMailboxCursorContinuity_WaitRepliesPromptlyAndReleasesSlot(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(cfg *Config) {
		cfg.MaxServerWait = 2 * time.Second
	})
	machine, device, rid, sp := mailboxFixture(t, srv, clk)
	appendContinuityItems(t, machine, rid, sp, clk, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := device.MailboxWait(ctx, 2); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("MailboxWait(cursor 2, high-water 1) = %v, want ErrMailboxCursorResetRequired", err)
	}

	// A reset verdict concludes (and unregisters) the first wait before its reply is sent.
	// A replacement wait must therefore be admitted immediately and return the retained item,
	// not ErrWaitInProgress.
	items, _, err := device.MailboxWait(testCtx(t), 0)
	if err != nil {
		t.Fatalf("replacement MailboxWait after reset verdict: %v", err)
	}
	if len(items) != 1 || items[0].Cursor != 1 {
		t.Fatalf("replacement MailboxWait returned %+v, want cursor 1", items)
	}
}

func TestMailboxCursorContinuity_AckPastHighWaterCannotPurgeNewStore(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, rid, sp := mailboxFixture(t, srv, clk)
	appendContinuityItems(t, machine, rid, sp, clk, 1)

	if err := device.MailboxAck(testCtx(t), 2); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("MailboxAck(cursor 2, high-water 1) = %v, want ErrMailboxCursorResetRequired", err)
	}
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead after refused stale ack: %v", err)
	}
	if len(items) != 1 || items[0].Cursor != 1 {
		t.Fatalf("stale ack purged the new mailbox item: got %+v, want cursor 1 retained", items)
	}
}

func TestMailboxCursorContinuity_IncarnationMismatchWinsWhenReplacementHighWaterCaughtUp(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, rid, sp := mailboxFixture(t, srv, clk)
	appendContinuityItems(t, machine, rid, sp, clk, 3)

	// Model a client checkpoint from another store whose numeric cursor happens to
	// equal this replacement store's high-water. A numeric-only check sees a clean
	// tail here and a stale ack would delete all three never-consumed replacement
	// items; the durable incarnation must make both operations refuse instead.
	device.SetMailboxIncarnation("retired-mailbox-incarnation")
	if _, err := device.MailboxRead(testCtx(t), 3); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("MailboxRead(cursor == replacement high-water, old incarnation) = %v, want reset", err)
	}
	if err := device.MailboxAck(testCtx(t), 3); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("MailboxAck(cursor == replacement high-water, old incarnation) = %v, want reset", err)
	}
	device.ResetMailboxIncarnation()
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead after incarnation reset: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("stale equal-high-water ack purged replacement items: got %d, want 3", len(items))
	}
	if got := device.MailboxIncarnation(); got == "" {
		t.Fatal("successful read did not adopt the relay's durable mailbox incarnation")
	}
}

func TestMailboxCursorContinuity_WaitIncarnationMismatchWinsAtEqualHighWater(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, rid, sp := mailboxFixture(t, srv, clk)
	appendContinuityItems(t, machine, rid, sp, clk, 1)
	device.SetMailboxIncarnation("retired-mailbox-incarnation")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := device.MailboxWait(ctx, 1); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("MailboxWait(cursor == replacement high-water, old incarnation) = %v, want reset", err)
	}
}

func TestMailboxCursorContinuity_LegacyNonzeroCheckpointRewindsBeforeFirstIncarnationAdoption(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, rid, sp := mailboxFixture(t, srv, clk)
	appendContinuityItems(t, machine, rid, sp, clk, 3)
	device.SetMailboxIncarnation("") // generation-aware legacy checkpoint
	if _, err := device.MailboxRead(testCtx(t), 3); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("legacy cursor 3 at replacement high-water 3 = %v, want one-time reset", err)
	}
	if got := device.MailboxIncarnation(); got != "" {
		t.Fatalf("legacy reset adopted incarnation %q before durable rewind", got)
	}
	device.ResetMailboxIncarnation()
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil || len(items) != 3 {
		t.Fatalf("post-migration rewind read items=%d err=%v, want all 3", len(items), err)
	}
	if device.MailboxIncarnation() == "" {
		t.Fatal("cursor-zero retry did not adopt incarnation")
	}
}

func TestMailboxCursorContinuity_ResetInvalidatesAnInFlightOldIncarnationReply(t *testing.T) {
	// Model the response half of a read/wait that started under the retired mailbox
	// incarnation. A manual recovery may reset the live client while that request is in
	// flight; its late response must not silently install the retired incarnation again.
	c := &Client{}
	c.SetMailboxIncarnation("retired-mailbox-incarnation")
	_, _, generation := c.mailboxContinuity()
	c.ResetMailboxIncarnation()

	if err := c.adoptMailboxIncarnation("retired-mailbox-incarnation", generation); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("late old-incarnation response = %v, want reset verdict", err)
	}
	if got := c.MailboxIncarnation(); got != "" {
		t.Fatalf("late response reinstalled retired incarnation %q", got)
	}
}

func TestMailboxCursorContinuity_RejectsMalformedResponseIncarnationBeforeAdoption(t *testing.T) {
	for name, incarnation := range map[string]string{
		"short":     strings.Repeat("a", 31),
		"long":      strings.Repeat("a", 33),
		"uppercase": "0123456789ABCDEF0123456789ABCDEF",
		"non-hex":   "0123456789abcdef0123456789abcdeg",
		"oversized": strings.Repeat("a", MaxFrame/2),
	} {
		t.Run(name, func(t *testing.T) {
			c := &Client{}
			_, _, generation := c.mailboxContinuity()
			if err := c.adoptMailboxIncarnation(incarnation, generation); err == nil {
				t.Fatal("malformed response incarnation was accepted")
			}
			if got := c.MailboxIncarnation(); got != "" {
				t.Fatalf("malformed response was persisted in client state: %q", got)
			}
		})
	}

	c := &Client{}
	_, _, generation := c.mailboxContinuity()
	const valid = "0123456789abcdef0123456789abcdef"
	if err := c.adoptMailboxIncarnation(valid, generation); err != nil {
		t.Fatalf("valid response incarnation: %v", err)
	}
	if got := c.MailboxIncarnation(); got != valid {
		t.Fatalf("valid response incarnation = %q, want adopted", got)
	}
}

func TestMailboxCursorContinuity_LatePollAckIsRejectedBeforeItCanUseNewIncarnation(t *testing.T) {
	c := &Client{}
	c.SetMailboxIncarnation("0123456789abcdef0123456789abcdef")
	oldGeneration := c.MailboxGeneration()
	c.ResetMailboxIncarnation()

	// The mismatch must be decided before touching c.conn (nil in this unit test).
	// Reaching the wire would either panic here or, in production, send cursor 53
	// beside the replacement generation learned after the reset.
	if err := c.MailboxAckGeneration(context.Background(), 53, oldGeneration); !errors.Is(err, ErrMailboxCursorResetRequired) {
		t.Fatalf("late poll ack = %v, want reset verdict before wire send", err)
	}
}
