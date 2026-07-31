package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for the OTHER half of PB-GW-7: what an outbound
// append is allowed to do to the seq stream when it fails.
//
// THE TRAP. The obvious remedy for a burned seq -- "a failed append never consumes a seq"
// -- is FORBIDDEN, because a failed append is not always a failed COMMIT. The relay stores
// the item and only then writes its reply (relay/server.go handleMailboxAppend), and
// relay.Client.MailboxAppend returns an error when the RESPONSE read fails (client.go
// roundtrip -> readFrame -> errConnClosed). So: relay stores seq N -> the connection dies
// before the reply -> if the gateway reuses N for different plaintext, the phone accepts
// whichever seq-N envelope lands first and stale-drops the other (crypto/envelope.go
// Accept: seq <= high-water is ErrStaleSeq). That is SILENT journal/snapshot loss or
// reordering -- strictly worse than the gap it was trying to avoid.
//
// THE RULE, as it stands after ADR-007 B127. These tests once pinned a classifier
// (ClassifyAppend / AppendOutcome) that carved out an exception: the relay's REFUSAL
// SENTINELS -- ErrQuotaExceeded, ErrNotAuthorized, ErrRevoked -- were "a DEFINITIVE
// pre-commit refusal" whose seq was safe to reissue. That exception was DELETED, because the
// party that mints the error code is the party whose storage state the code was being read as
// evidence about, and this design names that party the adversary (B125's ninth axis). The
// surviving rule is uniform and needs no classification at all:
//
//	Once the bytes are handed to the appender the seq is SPENT. A seq is reissued only
//	where the frame provably never crossed the process boundary.
//
// A delivery-unknown seq may therefore be burned, or recovered by re-appending the IDENTICAL
// sealed envelope -- and nothing else. The duplicate is free to the receiver (Accept
// stale-drops it), so this needs no relay protocol change. What remains here is the
// REAL-RELAY measurement the rule rests on, plus the outbox fences that implement the
// verbatim half; the adversary half is fenced in relay_authored_refusal_test.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// carriesARefusalSentinel reports whether err is one of the three codes the relay replies
// BEFORE it stores. It is a description of the error, never a decision about the seq: see the
// package rule at the top of this file.
func carriesARefusalSentinel(err error) bool {
	return errors.Is(err, relay.ErrQuotaExceeded) ||
		errors.Is(err, relay.ErrNotAuthorized) ||
		errors.Is(err, relay.ErrRevoked)
}

// cutProxy is a byte-transparent TCP proxy in front of a REAL relay that severs the
// connection AFTER the relay has committed an append but BEFORE its reply reaches the
// client -- the exact distributed-commit hole PB-GW-7 names, injected rather than assumed.
//
// The relay writes its reply only after storing the item, so swallowing the first
// server->client frame after Arm() is a cut that is provably post-commit: the test then
// reads the phone's mailbox and finds the item there.
type cutProxy struct {
	ln      net.Listener
	backend string
	armed   atomic.Bool
	cutOnce sync.Once
	cut     chan struct{}
}

func newCutProxy(t *testing.T, relayURL string) *cutProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cut proxy listen: %v", err)
	}
	p := &cutProxy{ln: ln, backend: strings.TrimPrefix(relayURL, "ws://"), cut: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })
	go p.serve()
	return p
}

func (p *cutProxy) URL() string          { return "ws://" + p.ln.Addr().String() }
func (p *cutProxy) Arm()                 { p.armed.Store(true) }
func (p *cutProxy) Cut() <-chan struct{} { return p.cut }

func (p *cutProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(client)
	}
}

func (p *cutProxy) handle(client net.Conn) {
	backend, err := net.Dial("tcp", p.backend)
	if err != nil {
		_ = client.Close()
		return
	}
	defer client.Close()
	defer backend.Close()
	go func() { _, _ = io.Copy(backend, client) }()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := backend.Read(buf)
		if n > 0 {
			if p.armed.Load() {
				// The relay only writes once it has STORED the item, so these bytes prove the
				// commit happened. Swallow them and sever: the client sees delivery-unknown.
				p.cutOnce.Do(func() { close(p.cut) })
				return
			}
			if _, werr := client.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

// startTestRelay boots a real relay with the given per-target append quota and returns it
// plus an authenticated phone client. Quota 0 means the shipped default.
func startTestRelay(t *testing.T, ctx context.Context, appendPerMin int) (*relay.Server, *relay.Client, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	if appendPerMin > 0 {
		cfg.Quotas.MailboxAppendPerMin = appendPerMin
	}
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	phone, err := relay.Dial(ctx, srv.URL(), relayAuthFor(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { phone.Close() })
	if err := phone.AuthorizeDevice(ctx, mPub,
		e2eConsent(mPriv, relay.RoutingID(pPub))); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	return srv, phone, mPub, mPriv
}

// TestAppendReply_IsTheRelaysOwnTestimonyAboutItsOwnStorage measures, against a REAL relay,
// the two facts the seq rule rests on -- and the reason the rule cannot key on either of them.
//
// FACT 1: a connection lost before the reply leaves the item COMMITTED, with the gateway told
// the append failed and no sentinel to say otherwise. This is why "a failed append never
// consumes a seq" is unsafe, and it is injected here rather than assumed.
//
// FACT 2: a quota refusal stores NOTHING and carries a sentinel. Both facts are properties of
// the HONEST relay, and both are reported over a channel the relay WRITES. Fact 2 looked for a
// long time like evidence that a refused seq was unspent; it is not, because the same sentinel
// is available to a relay that stored the item, and the gateway has no second source. So the
// distinction measured here is real, is worth recording, and is deliberately NOT ACTED ON:
// the seq is spent either way (ADR-007 B125 F-2, B127). Sentinel EXCLUSIVITY is still asserted
// -- a severed connection must not surface a refusal code -- because a relay-side regression
// that blurred them would silently weaken every caller that reads these errors for any purpose.
func TestAppendReply_IsTheRelaysOwnTestimonyAboutItsOwnStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Quota 2 per target: append #1 is cut post-commit, #2 succeeds, #3 is refused over quota.
	srv, phone, mPub, mPriv := startTestRelay(t, ctx, 2)
	proxy := newCutProxy(t, srv.URL())

	machine, err := relay.Dial(ctx, proxy.URL(), relayAuthFor(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial through the cut proxy: %v", err)
	}
	defer machine.Close()

	// (1) DELIVERY UNKNOWN: cut after the relay commits, before the reply is read.
	proxy.Arm()
	cutErr := discardAppend(machine.MailboxAppend(ctx, phone.RoutingID(), []byte("envelope-committed-then-cut")))
	if cutErr == nil {
		t.Fatal("the append through a severed connection returned nil; the harness never cut")
	}
	select {
	case <-proxy.Cut():
	case <-time.After(2 * time.Second):
		t.Fatal("the proxy never swallowed a reply: the cut did not land after the relay's commit")
	}
	if carriesARefusalSentinel(cutErr) {
		t.Errorf("a severed connection surfaced a refusal sentinel (%v); the sentinels must stay "+
			"exclusive to pre-commit refusals, so that no caller reading them for any purpose is "+
			"silently handed a post-commit failure wearing a refusal's clothes", cutErr)
	}

	// (2) The cut append DID commit: the item is in the phone's mailbox even though the
	// gateway was told the append failed. This is why "a failed append never consumes a seq"
	// is unsafe.
	machine2, err := relay.Dial(ctx, srv.URL(), relayAuthFor(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine re-dial: %v", err)
	}
	defer machine2.Close()
	items, err := phone.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("mailbox read after the cut: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("after a post-commit cut the phone's mailbox holds %d items, want 1: the harness "+
			"cut BEFORE the relay committed, so it is not exercising the delivery-unknown case", len(items))
	}

	// (3) DEFINITIVE PRE-COMMIT REFUSAL: burn the remaining quota, then over-append.
	if err := discardAppend(machine2.MailboxAppend(ctx, phone.RoutingID(), []byte("envelope-2"))); err != nil {
		t.Fatalf("second append (still inside quota): %v", err)
	}
	refErr := discardAppend(machine2.MailboxAppend(ctx, phone.RoutingID(), []byte("envelope-3-over-quota")))
	if !errors.Is(refErr, relay.ErrQuotaExceeded) {
		t.Fatalf("over-quota append returned %v, want relay.ErrQuotaExceeded", refErr)
	}
	items, err = phone.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("mailbox read after the refusal: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("after a quota refusal the phone's mailbox holds %d items, want 2: the refused append "+
			"must store nothing (that is what makes it safely pre-commit)", len(items))
	}
}

// TestAppendFailure_NoUnsentinelledErrorImpersonatesARefusal is what survives of the old
// classifier's conservative half. `relay.decodeError` maps only the codes in `codeToErr`, so
// `bad_request` (which mailbox_append returns for a malformed or oversized request),
// `auth_failed` and `unsupported` arrive as a bare `fmt.Errorf("relay: %s", code)` that
// errors.Is cannot tell apart from a transport failure. Nothing in the tree may recognise a
// refusal by its message TEXT: a decision resting on a string the relay never promised breaks
// silently the moment the wording moves (ADR-007 B113).
func TestAppendFailure_NoUnsentinelledErrorImpersonatesARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		// The exact shape relay.decodeError produces for a code with no sentinel.
		{"relay bad_request (no sentinel)", fmt.Errorf("relay: %s", "bad_request")},
		{"connection closed under the caller", errors.New("relay: connection closed")},
		{"bounded-append deadline", context.DeadlineExceeded},
		{"raw network failure", io.ErrUnexpectedEOF},
	} {
		if carriesARefusalSentinel(tc.err) {
			t.Errorf("%s matches a refusal sentinel; only the relay's SENTINELS may ever be "+
				"recognised as refusals, never a message string", tc.name)
		}
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"quota", relay.ErrQuotaExceeded},
		{"not authorized", relay.ErrNotAuthorized},
		{"revoked", relay.ErrRevoked},
		{"wrapped quota", fmt.Errorf("append journal record: %w", relay.ErrQuotaExceeded)},
	} {
		if !carriesARefusalSentinel(tc.err) {
			t.Errorf("%s does not match any refusal sentinel; a wrapped sentinel must stay "+
				"recognisable through errors.Is", tc.name)
		}
	}
}

// TestRelaySink_DeliveryUnknownNeverReusesASeqForDifferentPlaintext is the regression guard
// on the FORBIDDEN remedy, driven end to end over a real relay with a real post-commit cut.
//
// Today's failure mode is the mirror image and just as wrong: the sink returns the error,
// Gateway.deliver declines to advance the cursor, the reconnect re-reads the record and the
// sink RE-SEALS it at a FRESH seq -- so the phone accepts the same journal record twice, as
// two distinct frames it has no way to dedup. The fix (PB-GW-8's outbox) makes the recovery
// re-append the IDENTICAL sealed envelope, which the receiver stale-drops for free.
//
// Both failure modes are asserted here, so neither the current behaviour nor the naive
// remedy can pass:
//   - no seq may ever carry two DIFFERENT ciphertexts (the forbidden remedy), and
//   - no journal record may be ACCEPTED by the phone twice (today).
func TestRelaySink_DeliveryUnknownNeverReusesASeqForDifferentPlaintext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, phone, mPub, mPriv := startTestRelay(t, ctx, 0)
	proxy := newCutProxy(t, srv.URL())

	key := budgetTestKey()
	sender := [8]byte{3, 1, 4, 1, 5, 9, 2, 6}
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "outbound-journal.seq")
	outboxPath := filepath.Join(dir, "outbound-journal.outbox")

	newSink := func(app MailboxAppender) *RelaySink {
		seq, err := OpenSeqSource(seqPath)
		if err != nil {
			t.Fatalf("open durable seq: %v", err)
		}
		ob, err := OpenOutbox(outboxPath)
		if err != nil {
			t.Fatalf("open outbox: %v", err)
		}
		return NewRelaySink(RelayConfig{
			Appender:    app,
			Target:      phone.RoutingID(),
			EpochID:     4,
			Key:         key,
			SenderKeyID: sender,
			Seq:         seq,
			Outbox:      ob,
			Now:         func() time.Time { return time.Unix(1_700_000_000, 0) },
		})
	}

	recA := protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"}
	recB := protocol.JournalRecord{Cursor: 2, SessionID: "m/s2", Type: "launched"}
	recC := protocol.JournalRecord{Cursor: 3, SessionID: "m/s3", Type: "exited"}

	machine, err := relay.Dial(ctx, proxy.URL(), relayAuthFor(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial through the cut proxy: %v", err)
	}
	sink := newSink(machine)
	if err := sink.Event(recA); err != nil {
		t.Fatalf("record A: %v", err)
	}
	// Record B commits at the relay and the reply is swallowed: delivery unknown.
	proxy.Arm()
	errB := sink.Event(recB)
	if errB == nil {
		t.Fatal("record B's append returned nil through a severed connection; the harness never cut")
	}
	select {
	case <-proxy.Cut():
	case <-time.After(2 * time.Second):
		t.Fatal("the proxy never swallowed a reply: the cut did not land after the relay's commit")
	}
	if carriesARefusalSentinel(errB) {
		t.Fatalf("the severed append surfaced a refusal sentinel (%v); the harness is not exercising "+
			"the delivery-unknown case", errB)
	}
	machine.Close()

	// Recovery, exactly as production does it: a restarted gateway replays its outbox, then
	// the journal bridge re-delivers from the cursor that never advanced (B), then life goes on.
	machine2, err := relay.Dial(ctx, srv.URL(), relayAuthFor(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine re-dial: %v", err)
	}
	defer machine2.Close()
	sink2 := newSink(machine2)
	if err := sink2.Replay(); err != nil {
		t.Fatalf("outbox replay after the cut: %v", err)
	}
	if err := sink2.Event(recB); err != nil {
		t.Fatalf("re-delivered record B: %v", err)
	}
	if err := sink2.Event(recC); err != nil {
		t.Fatalf("record C: %v", err)
	}

	items, err := phone.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("mailbox read: %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("the phone's mailbox holds %d items; A, B (committed before the cut) and C must all "+
			"be there", len(items))
	}

	// (a) THE FORBIDDEN REMEDY: no seq may ever carry two different ciphertexts. A verbatim
	// re-append of the same sealed envelope is allowed (and is stale-dropped for free).
	bySeq := map[uint64][]byte{}
	for i, it := range items {
		env, err := crypto.ParseEnvelope(it.Envelope)
		if err != nil {
			t.Fatalf("mailbox item %d does not parse: %v", i, err)
		}
		prev, seen := bySeq[env.Header.Seq]
		if seen && string(prev) != string(it.Envelope) {
			t.Fatalf("seq %d carries TWO DIFFERENT sealed envelopes: the relay had already committed "+
				"the first one, so the phone keeps whichever lands first and stale-drops the other -- "+
				"silent journal loss or reordering. On delivery-unknown a seq may only be burned or "+
				"re-appended VERBATIM (PB-GW-7)", env.Header.Seq)
		}
		bySeq[env.Header.Seq] = append([]byte(nil), it.Envelope...)
	}

	// (b) TODAY'S BUG: the phone must not ACCEPT the same journal record twice. Re-sealing a
	// delivery-unknown record at a fresh seq gets it accepted twice, with nothing on the wire
	// to tell the phone they are the same record.
	receiver := crypto.NewMailboxReceiver()
	accepted := map[uint64]int{}
	for i, it := range items {
		env, err := crypto.ParseEnvelope(it.Envelope)
		if err != nil {
			t.Fatalf("mailbox item %d does not parse: %v", i, err)
		}
		res, err := receiver.Accept(key, env)
		if errors.Is(err, crypto.ErrStaleSeq) {
			continue // a verbatim re-append: deduped by the receiver, for free
		}
		if err != nil {
			t.Fatalf("the phone rejected mailbox item %d (seq %d): %v", i, env.Header.Seq, err)
		}
		var f outboundFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			t.Fatalf("mailbox item %d plaintext is not decodable: %v", i, err)
		}
		accepted[f.Cursor]++
	}
	for _, rec := range []protocol.JournalRecord{recA, recB, recC} {
		if n := accepted[rec.Cursor]; n != 1 {
			t.Errorf("the phone accepted journal record cursor=%d %d times, want exactly 1: a "+
				"delivery-unknown record must be recovered by re-appending the IDENTICAL sealed "+
				"envelope (which Accept stale-drops), never by re-sealing it at a fresh seq (PB-GW-8)",
				rec.Cursor, n)
		}
	}
}

// commitThenFailNthAppender STORES every envelope and reports failure on the Nth call
// only: the post-commit reply loss, without a socket. The stored bytes are what a real
// relay is already holding when the gateway is told the append failed.
type commitThenFailNthAppender struct {
	failOn int

	mu     sync.Mutex
	calls  int
	stored [][]byte
}

func (a *commitThenFailNthAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.stored = append(a.stored, append([]byte(nil), env...))
	if a.calls == a.failOn {
		return 0, errors.New("relay: connection closed")
	}
	return uint64(len(a.stored)), nil
}

func (a *commitThenFailNthAppender) all() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.stored...)
}

// TestRelaySink_DeliveryUnknownRetryIsVerbatimNotReSealed is the IN-PROCESS half of the
// same rule -- the half where the forbidden remedy actually bites. A gateway that "returns"
// the seq on a failed append does not need a restart to do damage: the very next retry, on
// the same live sink, re-seals the record at the same seq with a fresh nonce, and the relay
// is already holding the first one. The phone keeps whichever lands first and stale-drops
// the other.
//
// Both wrong answers are asserted against, so neither today's behaviour (re-seal at a fresh
// seq -> the record is accepted twice) nor the forbidden remedy (reuse the seq -> two rival
// envelopes) can pass.
func TestRelaySink_DeliveryUnknownRetryIsVerbatimNotReSealed(t *testing.T) {
	key := budgetTestKey()
	app := &commitThenFailNthAppender{failOn: 2}
	sink := NewRelaySink(RelayConfig{
		Appender:    app,
		Target:      "phone-routing-id",
		EpochID:     6,
		Key:         key,
		SenderKeyID: [8]byte{8, 6, 7, 5, 3, 0, 9, 1},
		Now:         func() time.Time { return time.Unix(1_700_000_000, 0) },
	})

	recA := protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"}
	recB := protocol.JournalRecord{Cursor: 2, SessionID: "m/s2", Type: "launched"}
	if err := sink.Event(recA); err != nil {
		t.Fatalf("record A: %v", err)
	}
	errB := sink.Event(recB)
	if errB == nil {
		t.Fatal("record B returned nil though its reply was lost; the error must reach the caller so " +
			"Gateway.deliver declines to advance its cursor")
	}
	if carriesARefusalSentinel(errB) {
		t.Fatalf("the lost-reply append surfaced a refusal sentinel (%v); the harness is not "+
			"exercising the delivery-unknown case", errB)
	}
	// Gateway.deliver did not advance its cursor, so the record is re-delivered to the SAME
	// live sink (ackcursor_test drives exactly this).
	if err := sink.Event(recB); err != nil {
		t.Fatalf("re-delivered record B: %v", err)
	}

	stored := app.all()
	bySeq := map[uint64][]byte{}
	receiver := crypto.NewMailboxReceiver()
	accepted := map[uint64]int{}
	for i, raw := range stored {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("stored envelope %d does not parse: %v", i, err)
		}
		if prev, seen := bySeq[env.Header.Seq]; seen && string(prev) != string(raw) {
			t.Fatalf("seq %d carries TWO DIFFERENT sealed envelopes: the relay already holds the first, "+
				"so the phone keeps whichever lands first and stale-drops the other. Un-issuing the seq "+
				"of a delivery-unknown append is the FORBIDDEN remedy -- a retry must re-append the "+
				"identical bytes (PB-GW-7)", env.Header.Seq)
		}
		bySeq[env.Header.Seq] = append([]byte(nil), raw...)

		res, err := receiver.Accept(key, env)
		if errors.Is(err, crypto.ErrStaleSeq) {
			continue // a verbatim re-append: deduped by the receiver, for free
		}
		if err != nil {
			t.Fatalf("the phone rejected stored envelope %d (seq %d): %v", i, env.Header.Seq, err)
		}
		var f outboundFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			t.Fatalf("stored envelope %d plaintext is not decodable: %v", i, err)
		}
		accepted[f.Cursor]++
	}
	if n := accepted[recB.Cursor]; n != 1 {
		t.Fatalf("the phone accepted journal record cursor=%d %d times, want exactly 1: re-sealing a "+
			"delivery-unknown record at a FRESH seq gets it delivered twice, with nothing on the wire "+
			"for the phone to dedup on (PB-GW-7)", recB.Cursor, n)
	}
}

// refuseNthAppender definitively refuses the Nth append with the relay's own pre-commit
// sentinel and stores NOTHING for it -- exactly what handleMailboxAppend does over quota.
type refuseNthAppender struct {
	refuse int

	mu     sync.Mutex
	calls  int
	stored [][]byte
}

func (a *refuseNthAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.calls == a.refuse {
		return 0, relay.ErrQuotaExceeded
	}
	a.stored = append(a.stored, append([]byte(nil), env...))
	return uint64(len(a.stored)), nil
}

// TestRelaySink_ARefusedOutboxFrameKeepsItsSeqThroughTheReservation pins that a refused
// JOURNAL record costs no gap -- and, since ADR-007 B127, pins the MECHANISM that earns it.
//
// This test predates B127 and passed before it, which is exactly why its rationale is
// restated here: the seq survives because the OUTBOX RESERVATION owns it, so Event's retry
// re-appends the IDENTICAL sealed envelope at the same seq. It has never depended on the
// refusal being "definitive", and it must not be read as evidence that a relay-authored
// refusal code says anything about the relay's storage -- the old doc comment on this test
// said exactly that, and the reuse it justified is deleted.
//
// The property is worth its own fence because the alternative is expensive: PB-SYNC-1 cannot
// attribute a gap in the shared bucket to journal or terminal, so one burned seq costs a
// conservative resync of BOTH streams against PB-SYNC-6's budget. Outbox-backed frames avoid
// that cost SOUNDLY, by holding the bytes rather than by believing the relay. The cursor==0
// frames have no such reservation and now pay the gap: see B127's arithmetic.
func TestRelaySink_ARefusedOutboxFrameKeepsItsSeqThroughTheReservation(t *testing.T) {
	key := budgetTestKey()
	sender := [8]byte{2, 7, 1, 8, 2, 8, 1, 8}
	app := &refuseNthAppender{refuse: 2}
	sink := NewRelaySink(RelayConfig{
		Appender:    app,
		Target:      "phone-routing-id",
		EpochID:     9,
		Key:         key,
		SenderKeyID: sender,
		Now:         func() time.Time { return time.Unix(1_700_000_000, 0) },
	})

	recA := protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"}
	recB := protocol.JournalRecord{Cursor: 2, SessionID: "m/s2", Type: "launched"}
	if err := sink.Event(recA); err != nil {
		t.Fatalf("record A: %v", err)
	}
	errB := sink.Event(recB)
	if !errors.Is(errB, relay.ErrQuotaExceeded) {
		t.Fatalf("record B returned %v, want relay.ErrQuotaExceeded surfaced to the caller so "+
			"Gateway.deliver declines to advance its cursor", errB)
	}
	// The gateway re-delivers B from the un-advanced cursor, exactly as ackcursor_test drives it.
	if err := sink.Event(recB); err != nil {
		t.Fatalf("re-delivered record B: %v", err)
	}

	app.mu.Lock()
	stored := append([][]byte(nil), app.stored...)
	app.mu.Unlock()
	if len(stored) != 2 {
		t.Fatalf("the relay stored %d envelopes, want 2 (A, then B on re-delivery)", len(stored))
	}
	phone := crypto.NewMailboxReceiver()
	for i, raw := range stored {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("stored envelope %d does not parse: %v", i, err)
		}
		res, err := phone.Accept(key, env)
		if err != nil {
			t.Fatalf("the phone rejected stored envelope %d (seq %d): %v", i, env.Header.Seq, err)
		}
		if res.Gap {
			t.Fatalf("the phone saw a GAP at seq %d: the refused append lost the seq its outbox "+
				"reservation was holding, so the retry did not re-append the identical envelope at it. "+
				"A burned seq costs a conservative resync of BOTH journal and terminal, because "+
				"MailboxResult.Gap carries no frame kind (PB-GW-7, PB-GW-8, PB-SYNC-1)", env.Header.Seq)
		}
	}
}

// discardAppend drops MailboxAppend's cursor so a call reads as a single error expression.
func discardAppend(_ uint64, err error) error { return err }
