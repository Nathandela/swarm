// PB-NET-6 (FAILING FIRST): Phase A's relay-adversary properties hold through the
// real client ACROSS A PROCESS RESTART -- seq gating, replay/reorder/dup rejection,
// mailbox cap, hostile-pagination termination.
//
// The trap this requirement exists to close (opus M1): every one of these
// properties is trivially satisfiable inside a single process, because the
// in-memory crypto.MailboxReceiver holds the high-water mark for the life of the
// process. On a handset the process dies constantly -- backgrounded, Doze,
// low-memory kill -- and a receiver rebuilt from nothing accepts a frame it already
// accepted. So every assertion below spans a simulated process death: the session
// is closed, its in-memory state discarded, and a NEW session is built over the same
// durable coordinates.
//
// The durable coordinates are the transport's contract with PB-STATE (S7): the
// relay cursor is the transport's own, the per-(sender, epoch) high-water belongs
// to the caller that holds the key, and both live behind one Store so S7 can later
// seal them and commit them in one transaction (PB-STATE-7). transport.Store holds
// no key material and no plaintext, so it does not violate PB-NET-3.
package transport_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// drainInto drains one page and feeds every item to the caller-side receiver,
// recording each accepted seq in the durable store. It is the composition a phone
// actually performs: the transport moves opaque bytes, the core opens them.
func drainInto(t *testing.T, s *transport.Session, rcv *crypto.MailboxReceiver, p peers, st transport.Store) ([]*crypto.MailboxResult, []error) {
	t.Helper()
	var results []*crypto.MailboxResult
	var failures []error
	if _, err := s.Drain(testCtx(t), func(it relay.Item) error {
		env, perr := crypto.ParseEnvelope(it.Envelope)
		if perr != nil {
			failures = append(failures, perr)
			return nil
		}
		res, aerr := rcv.Accept(p.party.keys.ContentKey, env)
		if aerr != nil {
			failures = append(failures, aerr)
			return nil
		}
		if serr := st.SetHighWater(env.Header.SenderKeyID, env.Header.EpochID, env.Header.Seq); serr != nil {
			t.Fatalf("SetHighWater: %v", serr)
		}
		results = append(results, res)
		return nil
	}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	return results, failures
}

// seededReceiver rebuilds a receiver the way a restarted process must: from the
// durable high-water, not from zero.
func seededReceiver(t *testing.T, st transport.Store, p peers) *crypto.MailboxReceiver {
	t.Helper()
	rcv := crypto.NewMailboxReceiver()
	hw, ok, err := st.HighWater(p.party.sender, p.party.epochID)
	if err != nil {
		t.Fatalf("HighWater: %v", err)
	}
	if ok {
		rcv.SeedHighWater(p.party.sender, p.party.epochID, hw)
	}
	return rcv
}

// TestReplayIsRefusedAcrossAProcessRestart is the core PB-NET-6 assertion. A frame
// the device already accepted is re-served after the process dies; the restarted
// device must refuse it as a replay, not apply it a second time.
func TestReplayIsRefusedAcrossAProcessRestart(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	dir := t.TempDir()

	st1, err := transport.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	s1 := devSession(t, srv.URL(), p, func(o *transport.Options) { o.Store = st1 })

	replayed := p.seal(t, 2, []byte("second-event"))
	stream := [][]byte{
		p.seal(t, 1, []byte("first-event")),
		replayed,
		p.seal(t, 3, []byte("third-event")),
	}
	for i, env := range stream {
		if _, aerr := p.machine.MailboxAppend(testCtx(t), p.deviceRID, env); aerr != nil {
			t.Fatalf("MailboxAppend(seq %d): %v", i+1, aerr)
		}
	}

	rcv1 := seededReceiver(t, st1, p)
	got, failures := drainInto(t, s1, rcv1, p, st1)
	if len(failures) != 0 {
		t.Fatalf("pre-restart drain rejected %d genuine item(s): %v", len(failures), failures)
	}
	if len(got) != 3 {
		t.Fatalf("pre-restart drain yielded %d items, want 3", len(got))
	}

	// --- process death -----------------------------------------------------
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The relay (or anyone who kept a copy) re-serves an already-accepted frame.
	if _, err := p.machine.MailboxAppend(testCtx(t), p.deviceRID, replayed); err != nil {
		t.Fatalf("MailboxAppend(replay): %v", err)
	}

	st2, err := transport.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore(after restart): %v", err)
	}
	hw, ok, err := st2.HighWater(p.party.sender, p.party.epochID)
	if err != nil {
		t.Fatalf("HighWater(after restart): %v", err)
	}
	if !ok || hw != 3 {
		t.Fatalf("high-water after restart = (%d, %v), want (3, true): the replay guard did not survive the process", hw, ok)
	}

	s2 := devSession(t, srv.URL(), p, func(o *transport.Options) { o.Store = st2 })
	rcv2 := seededReceiver(t, st2, p)
	applied, rejected := drainInto(t, s2, rcv2, p, st2)
	if len(applied) != 0 {
		t.Fatalf("the restarted device APPLIED %d replayed frame(s); a retained frame must be refused (PB-STATE-2)", len(applied))
	}
	if len(rejected) != 1 || !errors.Is(rejected[0], crypto.ErrStaleSeq) {
		t.Fatalf("replay rejection: got %v, want exactly one crypto.ErrStaleSeq", rejected)
	}

	// Why the seeding is load-bearing: an unseeded receiver -- today's behaviour on
	// a phone that persists nothing -- accepts the very same frame.
	blind := crypto.NewMailboxReceiver()
	env, perr := crypto.ParseEnvelope(replayed)
	if perr != nil {
		t.Fatalf("ParseEnvelope: %v", perr)
	}
	if _, aerr := blind.Accept(p.party.keys.ContentKey, env); aerr != nil {
		t.Fatalf("fixture check: an unseeded receiver should accept the replay, got %v", aerr)
	}
}

// TestDurableCursorSurvivesProcessRestart asserts the transport's own coordinate.
// The relay here RETAINS: it serves the same items forever and ignores acks, which
// is what a hostile or merely lossy relay does when an ack never lands. A restarted
// session with no durable cursor re-delivers everything; with one, it does not.
func TestDurableCursorSurvivesProcessRestart(t *testing.T) {
	h := newHostileRelay(t)
	p := peers{machineRID: "machine-rid", deviceRID: "device-rid", party: newSealParty(t)}
	pub, priv := newRelayAuthKey(t)
	p.deviceAuth = authFor(pub, priv)

	h.setRetained([]relay.Item{
		{Cursor: 1, Envelope: p.seal(t, 1, []byte("a"))},
		{Cursor: 2, Envelope: p.seal(t, 2, []byte("b"))},
		{Cursor: 3, Envelope: p.seal(t, 3, []byte("c"))},
	})

	dir := t.TempDir()
	st1, err := transport.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	s1 := devSession(t, h.URL(), p, func(o *transport.Options) { o.Store = st1 })

	var first int
	if _, err := s1.Drain(testCtx(t), func(relay.Item) error { first++; return nil }); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if first != 3 {
		t.Fatalf("first drain yielded %d items, want 3", first)
	}
	if c, err := st1.Cursor(); err != nil || c != 3 {
		t.Fatalf("durable cursor after drain = (%d, %v), want (3, nil)", c, err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// --- process death -----------------------------------------------------
	st2, err := transport.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore(after restart): %v", err)
	}
	s2 := devSession(t, h.URL(), p, func(o *transport.Options) { o.Store = st2 })

	var second int
	if _, err := s2.Drain(testCtx(t), func(relay.Item) error { second++; return nil }); err != nil {
		t.Fatalf("Drain(after restart): %v", err)
	}
	if second != 0 {
		t.Fatalf("the restarted session re-delivered %d already-drained item(s); the relay cursor did not survive", second)
	}
	if got := h.lastReadCursor(); got != 3 {
		t.Fatalf("the restarted session read from cursor %d, want 3", got)
	}
}

// TestConcurrentDrainsDeliverEachItemOnce closes the read-modify-write hole in the
// cursor: Drain reads Store.Cursor(), then writes SetCursor() a round-trip later, so
// two concurrent drains both read the same cursor and both deliver the whole page.
// Every other Session method is mutex-safe, which makes this a trap rather than a
// documented single-caller contract -- and on a handset a foreground drain racing a
// push-wake drain is exactly this shape.
func TestConcurrentDrainsDeliverEachItemOnce(t *testing.T) {
	h := newHostileRelay(t)
	pub, priv := newRelayAuthKey(t)
	p := peers{machineRID: "machine-rid", deviceRID: "device-rid", party: newSealParty(t), deviceAuth: authFor(pub, priv)}

	const items = 5
	retained := make([]relay.Item, items)
	for i := range retained {
		retained[i] = relay.Item{Cursor: uint64(i + 1), Envelope: p.seal(t, uint64(i+1), []byte("item"))}
	}
	h.setRetained(retained)

	s := devSession(t, h.URL(), p, nil)

	var mu sync.Mutex
	var delivered int
	drain := func() error {
		_, err := s.Drain(testCtx(t), func(relay.Item) error {
			mu.Lock()
			delivered++
			mu.Unlock()
			return nil
		})
		return err
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = drain()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Drain #%d: %v", i+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if delivered != items {
		t.Fatalf("two concurrent drains delivered %d items for a %d-item mailbox: the cursor read-modify-write is unguarded", delivered, items)
	}
}

// TestHostilePaginationTerminates asserts a relay that claims has_more forever
// while never advancing the cursor is refused rather than followed. Phase A proved
// this at the relay's own client (phonesim's errStuckPage, codex#7); it must hold
// at the resilient session too, which is what the phone actually calls.
func TestHostilePaginationTerminates(t *testing.T) {
	h := newHostileRelay(t)
	h.setStuck(true)
	pub, priv := newRelayAuthKey(t)
	p := peers{machineRID: "machine-rid", deviceRID: "device-rid", party: newSealParty(t), deviceAuth: authFor(pub, priv)}
	s := devSession(t, h.URL(), p, nil)

	done := make(chan error, 1)
	go func() {
		_, err := s.Drain(testCtx(t), func(relay.Item) error { return nil })
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, transport.ErrStuckPage) {
			t.Fatalf("Drain against a stuck-paging relay: got %v, want ErrStuckPage", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Drain never returned against a relay that claims has_more forever: it is spinning")
	}

	if n := h.readCount(); n > 8 {
		t.Fatalf("Drain issued %d reads before giving up; a non-advancing page must terminate the scan at once", n)
	}
}

// TestMailboxCapSurfacesACleanRefusal asserts the relay's per-mailbox depth cap
// reaches the caller as a clean, typed refusal rather than a hang, a panic, or a
// silent queue. R-REL.8's "every over-limit is a CLEAN error" must survive the
// resilience layer, which is exactly the place a refusal is easy to swallow into a
// retry queue.
func TestMailboxCapSurfacesACleanRefusal(t *testing.T) {
	srv, _ := startRelay(t, func(c *relay.Config) { c.Quotas.MailboxMaxItems = 4 })
	p := newPeers(t, srv)
	s := devSession(t, srv.URL(), p, nil)

	for i := 0; i < 4; i++ {
		if err := s.SendOp(testCtx(t), p.machineRID, p.seal(t, uint64(i+1), []byte("fill"))); err != nil {
			t.Fatalf("SendOp #%d below the cap: %v", i+1, err)
		}
	}
	err := s.SendOp(testCtx(t), p.machineRID, p.seal(t, 5, []byte("over")))
	if err == nil {
		t.Fatalf("SendOp past the mailbox depth cap succeeded")
	}
	if !errors.Is(err, relay.ErrQuotaExceeded) {
		t.Fatalf("over-cap error: got %v, want relay.ErrQuotaExceeded", err)
	}
	if n := s.Queued(); n != 0 {
		t.Fatalf("%d op(s) were queued behind a definitive refusal; a quota refusal is not a retryable outage", n)
	}
	if s.State() != transport.StateConnected {
		t.Fatalf("state after a clean refusal = %q, want %q (a refusal must not tear the connection down)", s.State(), transport.StateConnected)
	}
}

// TestRelayAdversaryPropertiesHoldThroughTheSession runs Phase A's untrusted-relay
// suite (internal/remote/relay/untrusted_test.go) through the resilient session
// rather than a bare relay.Client: forgery fails the AEAD, a mid-stream drop
// surfaces a gap, a reorder is refused, and a tampered body fails the AEAD. The
// session must not "helpfully" reorder, deduplicate or repair any of it.
func TestRelayAdversaryPropertiesHoldThroughTheSession(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	st, err := transport.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	s := devSession(t, srv.URL(), p, func(o *transport.Options) { o.Store = st })

	forgedKeys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("NewEpochKeys: %v", err)
	}
	forged, err := crypto.SealMailbox(forgedKeys.ContentKey, crypto.EnvelopeHeader{
		Version:     crypto.VersionV1,
		EpochID:     p.party.epochID,
		Seq:         2,
		SenderKeyID: p.party.sender,
		IssuedAt:    time.Now().UnixMilli(),
	}, []byte("injected"))
	if err != nil {
		t.Fatalf("seal forged: %v", err)
	}

	// seq 1 genuine, then a relay forgery, then seq 4 (the relay dropped seq 3),
	// then a replay of seq 1.
	appends := [][]byte{
		p.seal(t, 1, []byte("one")),
		forged.Marshal(),
		p.seal(t, 4, []byte("four")),
		p.seal(t, 1, []byte("one")),
	}
	for i, env := range appends {
		if _, aerr := p.machine.MailboxAppend(testCtx(t), p.deviceRID, env); aerr != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, aerr)
		}
	}

	rcv := seededReceiver(t, st, p)
	var accepted []*crypto.MailboxResult
	var errs []error
	if _, derr := s.Drain(testCtx(t), func(it relay.Item) error {
		env, perr := crypto.ParseEnvelope(it.Envelope)
		if perr != nil {
			errs = append(errs, perr)
			return nil
		}
		res, aerr := rcv.Accept(p.party.keys.ContentKey, env)
		if aerr != nil {
			errs = append(errs, aerr)
			return nil
		}
		accepted = append(accepted, res)
		return nil
	}); derr != nil {
		t.Fatalf("Drain: %v", derr)
	}

	if len(accepted) != 2 {
		t.Fatalf("accepted %d items, want 2 (seq 1 and seq 4)", len(accepted))
	}
	if accepted[0].Gap {
		t.Fatalf("seq 1 spuriously reported a gap")
	}
	if !accepted[1].Gap {
		t.Fatalf("the relay's mid-stream drop of seq 3 went undetected: no gap surfaced")
	}
	if len(errs) != 2 {
		t.Fatalf("rejections = %v, want two (the forgery and the replay)", errs)
	}
	if errs[0] == nil {
		t.Fatalf("the relay-forged event was accepted")
	}
	if !errors.Is(errs[1], crypto.ErrStaleSeq) {
		t.Fatalf("replay of seq 1: got %v, want crypto.ErrStaleSeq", errs[1])
	}
}
