package conformance_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// A full mailbox whose authenticated head is older than InboundMaxAge is a closed cycle:
// the phone must not ack content it refused, while the machine cannot append the requested
// replacement roster until capacity is freed. Pulling the Inbox is the explicit destructive
// recovery. It discards only this phone's current mailbox generation, keeps the last durable
// roster visible, and asks for the reconcile+roster pair that makes the replacement contiguous.
func TestRefreshRoster_RecoversAFullStaleAgeMailboxAndPreservesCachedRows(t *testing.T) {
	h := newHarnessWithRelayConfig(t, func(cfg *relay.Config) {
		cfg.Quotas.MailboxMaxItems = 2
	})

	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/cached", Type: "roster", Group: "working"})
	eventually(t, "initial roster never reached the phone", func() bool {
		return phoneRosterSawSession(t, h, "/cached")
	})
	// The async ack is intentionally off the delivery path and flushes at most once/second.
	// Let the two-frame initial Snapshot compact before using the depth-two mailbox as the
	// exact quota boundary under test.
	time.Sleep(1500 * time.Millisecond)

	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/old-a", Type: "launched"})
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testMachineID + "/old-b", Type: "launched"})
	eventually(t, "the stale-age head never raised the inbound refusal", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "offline"
	})
	if !phoneRosterSawSession(t, h, "/cached") {
		t.Fatal("the refused stale backlog cleared the cached roster before recovery")
	}

	if err := h.App.RefreshRoster(); err != nil {
		t.Fatalf("RefreshRoster through stale full mailbox: %v", err)
	}
	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if !cmd.RosterOnly {
		t.Fatalf("recovery command = %#v, want roster_only", cmd)
	}
	if !cmd.DiscardedBacklog {
		t.Fatalf("recovery command = %#v, want sealed discarded_backlog proof", cmd)
	}
	if cmd.DiscardRecoveryToken == "" {
		t.Fatalf("recovery command = %#v, want durable discard recovery token", cmd)
	}
	if !phoneRosterSawSession(t, h, "/cached") {
		t.Fatal("explicit recovery removed cached rows before replacement state landed")
	}

	// Model Gateway.Resync's roster-only Snapshot: fresh reconcile first, roster reseed next.
	// Both appends fit only because RefreshRoster compacted the exact stale mailbox above.
	h.SealOffset(0)
	if err := h.sink.RecoverySnapshot([]schema.JournalRecord{{SessionID: testMachineID + "/fresh", Type: "roster", Group: "working"}}, 2, cmd.DiscardRecoveryToken); err != nil {
		t.Fatalf("gateway recovery Snapshot at daemon final cursor: %v", err)
	}
	eventually(t, "fresh roster did not land after destructive recovery", func() bool {
		return phoneRosterSawSession(t, h, "/fresh")
	})
	if phoneRosterSawSession(t, h, "/cached") {
		t.Fatal("authoritative replacement merged with the cached roster instead of replacing it")
	}
	// The discarded daemon range ended at cursor 2, which the recovery roster adopted
	// without carrying either discarded event. The first live event is therefore cursor 3:
	// it must land normally and leave the repaired journal live, not be rejected as stale.
	next := schema.JournalRecord{Cursor: 3, SessionID: testMachineID + "/next-live", Type: "launched", Group: "working"}
	eventually(t, "recovery snapshot was not acknowledged to make room for the next live event", func() bool {
		err := h.sink.Event(next)
		if err == nil {
			return true
		}
		if errors.Is(err, relay.ErrQuotaExceeded) {
			return false
		}
		t.Fatalf("append first event after recovery: %v", err)
		return false
	})
	eventually(t, "first event after the fast-forwarded roster was not accepted", func() bool {
		return phoneRosterSawSession(t, h, "/next-live")
	})
	if state, err := h.App.StreamState(phonecore.StreamJournal); err != nil || state != "live" {
		t.Fatalf("journal state after contiguous cursor 3 = %q, %v; want live", state, err)
	}
}

// This is the Android kill seam: the relay discard and its durable local adoption succeed,
// but the replacement command cannot be appended. Restart loses the in-memory stale-age
// verdict, so only the persisted token can make the next explicit Refresh reissue the
// idempotent discard and author the same fast-forward request.
func TestRefreshRoster_RestartAfterDiscardBeforeCommandRetriesDurableRecovery(t *testing.T) {
	h := newHarnessWithRelayConfig(t, func(cfg *relay.Config) {
		cfg.Quotas.MailboxMaxItems = 2
	})
	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/cached", Type: "roster", Group: "working"})
	eventually(t, "initial roster never reached the phone", func() bool {
		return phoneRosterSawSession(t, h, "/cached")
	})
	time.Sleep(1500 * time.Millisecond)

	// Fill the phone->machine mailbox so the recovery command fails only AFTER the separate
	// machine->phone mailbox has been discarded and adopted.
	if err := h.App.TerminalWatch(testMachineID + "/fill-a"); err != nil {
		t.Fatalf("fill command A: %v", err)
	}
	if err := h.App.TerminalWatch(testMachineID + "/fill-b"); err != nil {
		t.Fatalf("fill command B: %v", err)
	}
	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/old-a", Type: "launched"})
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testMachineID + "/old-b", Type: "launched"})
	eventually(t, "the stale-age head never raised the inbound refusal", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "offline"
	})
	if err := h.App.RefreshRoster(); !errors.Is(err, relay.ErrQuotaExceeded) {
		t.Fatalf("RefreshRoster at post-discard command seam = %v, want relay quota refusal", err)
	}

	durableRecovery := func() string {
		t.Helper()
		store, err := phonecore.OpenStore(filepath.Join(h.CoreDir, phonecore.StateFileName), h.Machine,
			h.Custody.wakeSealer(), h.Custody.contentSealer())
		if err != nil {
			t.Fatalf("open phone state: %v", err)
		}
		return store.Load().DiscardRecoveryToken
	}
	token := durableRecovery()
	if token == "" {
		t.Fatal("post-discard command failure left no durable recovery token")
	}
	h.Drain() // release the two filler commands so the retried recovery command can append
	if err := h.App.Close(); err != nil {
		t.Fatalf("close at simulated process death: %v", err)
	}
	h.App = h.openApp()
	eventually(t, "restarted phone did not reconnect", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "online"
	})

	if err := h.App.RefreshRoster(); err != nil {
		t.Fatalf("RefreshRoster after restart with no live age refusal: %v", err)
	}
	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if !cmd.DiscardedBacklog || cmd.DiscardRecoveryToken != token {
		t.Fatalf("restart recovery command = %#v, want same durable discarded-backlog token %q", cmd, token)
	}
	h.SealOffset(0)
	if err := h.sink.RecoverySnapshot([]schema.JournalRecord{{SessionID: testMachineID + "/fresh", Type: "roster", Group: "working"}}, 2, token); err != nil {
		t.Fatalf("recovery snapshot: %v", err)
	}
	eventually(t, "matching recovery echo did not clear durable pending token", func() bool {
		return durableRecovery() == ""
	})

	next := schema.JournalRecord{Cursor: 3, SessionID: testMachineID + "/next-live", Type: "launched", Group: "working"}
	eventually(t, "recovery snapshot was not acknowledged for next live event", func() bool {
		err := h.sink.Event(next)
		if err == nil {
			return true
		}
		if errors.Is(err, relay.ErrQuotaExceeded) {
			return false
		}
		t.Fatalf("append next live event: %v", err)
		return false
	})
	eventually(t, "next live event after restarted recovery was not accepted", func() bool {
		return phoneRosterSawSession(t, h, "/next-live")
	})
}

func TestRefreshRoster_AConsumedBudgetCannotCompactAndStopBeforeReplacement(t *testing.T) {
	h := newHarnessWithRelayConfig(t, func(cfg *relay.Config) {
		cfg.Quotas.MailboxMaxItems = 2
	})
	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/old-a", Type: "launched"})
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testMachineID + "/old-b", Type: "launched"})
	eventually(t, "the stale-age head never raised the inbound refusal", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "offline"
	})

	// Spend the journal budget without freeing the full phone mailbox. Resync rewinds the
	// local read coordinate and sends a command to the machine's separate mailbox; after its
	// drain has re-observed the stale head, RefreshRoster must refuse BEFORE self-discard.
	if err := h.App.Resync(phonecore.StreamJournal); err != nil {
		t.Fatalf("prime journal refresh budget: %v", err)
	}
	h.AwaitCommand(schema.ActionJournalResync)
	eventually(t, "the rewound drain did not re-observe the stale-age head", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "offline"
	})

	durableCursor := func() uint64 {
		t.Helper()
		store, err := phonecore.OpenStore(filepath.Join(h.CoreDir, phonecore.StateFileName), h.Machine,
			h.Custody.wakeSealer(), h.Custody.contentSealer())
		if err != nil {
			t.Fatalf("open phone state: %v", err)
		}
		return store.Load().RelayCursor
	}
	before := durableCursor()
	if err := h.App.RefreshRoster(); err == nil {
		t.Fatal("RefreshRoster with a consumed journal budget succeeded, want rate refusal")
	}
	if after := durableCursor(); after != before {
		t.Fatalf("rate-refused RefreshRoster advanced relay cursor %d -> %d; mailbox was compacted without a replacement command", before, after)
	}
}

func phoneRosterSawSession(t *testing.T, h *harness, suffix string) bool {
	t.Helper()
	roster, err := h.App.Roster()
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	n, err := roster.Count()
	if err != nil {
		t.Fatalf("SessionList.Count: %v", err)
	}
	for i := 0; i < n; i++ {
		session, err := roster.At(i)
		if err != nil {
			t.Fatalf("SessionList.At: %v", err)
		}
		if strings.HasSuffix(session.ID, suffix) {
			return true
		}
	}
	return false
}
