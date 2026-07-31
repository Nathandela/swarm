package remotegw

// FAILING-FIRST (TDD RED, GG-5) fence for ADR-007 B125's F-2: the relay's own error CODE was
// trusted as evidence about the relay's own STORAGE.
//
// ClassifyAppend named relay.ErrQuotaExceeded / ErrNotAuthorized / ErrRevoked "a DEFINITIVE
// pre-commit refusal -- the relay replied before storing anything, so the seq was never
// spent", and sealAtSeqLocked reissued that seq for a freshly sealed DIFFERENT plaintext.
// That is a true statement about the HONEST relay (handleMailboxAppend does refuse before it
// stores) and a CHOICE for the adversary, which this design names as the relay: the code on
// the reply is minted by the same party whose storage state it is offered as evidence about.
//
// A relay that STORES and then answers "quota_exceeded" ends up holding two rival envelopes
// at one seq and chooses which one lands -- and NO GAP IS REPORTED, because the seq was
// consumed by its rival, so every staleness mechanism stays silent. That is B121's "staleness
// by silence" reached while the phone is actively receiving, and it is exactly what the same
// file's AppendUnknown comment already forbids for the delivery-unknown case: "two rival
// envelopes at one seq, of which the phone keeps whichever lands first and stale-drops the
// other -- silent journal loss or reordering."
//
// THE RULE THIS FENCE PINS:
//
//	A seq may be reissued only where the frame PROVABLY never crossed the process
//	boundary. Once the bytes are handed to the appender the seq is SPENT, whatever the
//	relay says about them.
//
// No weaker rule is available. The relay speaks last on the append and authors the mailbox
// read as well, so nothing it can return establishes non-commitment; there is no coordinate
// to substitute for the sentinel. The cost is recorded in ADR-007 B127: a refusal now burns
// its seq, and a CONTIGUOUS run of burns costs ONE gap, not one per burn
// (crypto.MailboxReceiver: gap := seen && seq > hi+1).
//
// SCOPE is the cursor==0 frames -- terminal snapshots, roster records, Reconcile and the
// journal reseed, which is the only journal repair channel. Outbox-backed journal Events were
// never exposed: their reservation owns the seq and the retry re-appends the IDENTICAL bytes.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// storeThenRefuseAppender is the ADVERSARY RELAY at the one seam the gateway talks to it
// through: it STORES every envelope it is handed and answers the nominated call with one of
// the three refusal sentinels. Nothing here is a malfunction -- storing is what the relay
// does with bytes it has been given, and the reply code is its own to write.
type storeThenRefuseAppender struct {
	sentinel error
	refuseOn int // 1-based index of the append to store-and-deny

	mu     sync.Mutex
	calls  int
	stored [][]byte
}

func (a *storeThenRefuseAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.stored = append(a.stored, append([]byte(nil), env...)) // STORED, always
	if a.calls == a.refuseOn {
		return 0, a.sentinel // ...and denied
	}
	return uint64(len(a.stored)), nil
}

func (a *storeThenRefuseAppender) all() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.stored...)
}

// TestRelaySink_ARelayAuthoredRefusalNeverReissuesASeq drives every cursor==0 path against a
// relay that stores what it denies, for each of the three sentinels ClassifyAppend called
// definitive, and asserts the two properties a reissued seq destroys: no seq may carry two
// different sealed envelopes, and nothing the gateway sealed may be silently stale-dropped by
// a rival at its own seq.
func TestRelaySink_ARelayAuthoredRefusalNeverReissuesASeq(t *testing.T) {
	key := inboundKey(41)
	sender := [8]byte{'m', 'a', 'c', 'h', 'i', 'n', 'e', '1'}

	// Each path emits EXACTLY ONE cursor==0 frame per call, so frame 2 is the one the relay
	// stores and denies and frame 3 is the fresh content a reissued seq would collide with.
	paths := []struct {
		name string
		// authorities is nil where the path must not drag a reconcile record along with it.
		authorities ReconcileSource
		emit        func(s *RelaySink, n int, label string) error
	}{
		{
			name: "terminal snapshot",
			emit: func(s *RelaySink, _ int, label string) error {
				return s.Terminal("m/s1", []string{label}, 80, 24)
			},
		},
		{
			name: "roster record",
			emit: func(s *RelaySink, _ int, label string) error {
				return s.Snapshot([]protocol.JournalRecord{{SessionID: "m/" + label, Type: "launched"}}, 0)
			},
		},
		{
			name:        "reconcile record",
			authorities: stubReconcileSource{inbound: 42, reply: 5, grantEpoch: 9, grantSeq: 2},
			emit:        func(s *RelaySink, _ int, _ string) error { return s.Reconcile() },
		},
		{
			name: "journal reseed",
			emit: func(s *RelaySink, n int, label string) error {
				return s.Reseed(protocol.JournalReseed{
					Roster: []protocol.JournalRecord{{SessionID: "m/" + label, Type: "launched"}},
					Cursor: uint64(n),
				})
			},
		},
	}
	sentinels := []struct {
		name string
		err  error
	}{
		{"quota_exceeded", relay.ErrQuotaExceeded},
		{"not_authorized", relay.ErrNotAuthorized},
		{"revoked", relay.ErrRevoked},
	}

	for _, p := range paths {
		for _, sent := range sentinels {
			t.Run(p.name+"/"+sent.name, func(t *testing.T) {
				app := &storeThenRefuseAppender{sentinel: sent.err, refuseOn: 2}
				sink := NewRelaySink(RelayConfig{
					Appender: app, Target: "phone", Machine: "m", EpochID: 9, Key: key,
					SenderKeyID: sender, Authorities: p.authorities,
				})

				const frames = 3
				for n := 1; n <= frames; n++ {
					err := p.emit(sink, n, [frames]string{"one", "two-stored-and-denied", "three-live"}[n-1])
					switch {
					case n == 2 && err == nil:
						t.Fatalf("frame 2 returned nil; the fence needs the relay's lie to surface as an error")
					case n == 2:
						if !errors.Is(err, sent.err) {
							t.Fatalf("frame 2 surfaced %v, want the relay's %s sentinel", err, sent.name)
						}
					case err != nil:
						t.Fatalf("frame %d: %v", n, err)
					}
				}

				stored := app.all()
				if len(stored) != frames {
					t.Fatalf("the relay is holding %d envelopes, want %d: this relay stores everything it "+
						"is handed, so a different count means the harness is not exercising the lie", len(stored), frames)
				}

				// (a) NO SEQ MAY CARRY TWO DIFFERENT SEALED ENVELOPES. A verbatim duplicate would
				// be free (the receiver stale-drops it); two RIVALS are the defect.
				bySeq := map[uint64][]byte{}
				for i, raw := range stored {
					env, err := crypto.ParseEnvelope(raw)
					if err != nil {
						t.Fatalf("stored envelope %d does not parse: %v", i, err)
					}
					if prev, seen := bySeq[env.Header.Seq]; seen && string(prev) != string(raw) {
						t.Fatalf("seq %d carries TWO DIFFERENT sealed envelopes: the relay answered a "+
							"STORED append with %s, that code was read as proof nothing was stored, and the "+
							"seq was reissued for fresh content. The relay holds both and chooses which one "+
							"lands. A relay-authored error code is not evidence about the relay's own "+
							"storage (ADR-007 B125 F-2)", env.Header.Seq, sent.name)
					}
					bySeq[env.Header.Seq] = append([]byte(nil), raw...)
				}

				// (b) NOTHING SEALED MAY BE SILENTLY LOST. Served in the order the relay holds
				// them, a real receiver must accept every frame -- a rival at an already-consumed
				// seq is dropped as ErrStaleSeq with NO gap reported anywhere, so the loss is
				// invisible to every staleness mechanism the design has.
				recv := crypto.NewMailboxReceiver()
				accepted, gaps := 0, 0
				for i, raw := range stored {
					env, err := crypto.ParseEnvelope(raw)
					if err != nil {
						t.Fatalf("stored envelope %d does not parse: %v", i, err)
					}
					res, err := recv.Accept(key, env)
					if err != nil {
						t.Errorf("the phone REFUSED stored envelope %d (seq %d): %v", i+1, env.Header.Seq, err)
						continue
					}
					if res.Gap {
						gaps++
					}
					accepted++
				}
				if accepted != frames {
					t.Errorf("the phone accepted %d of %d sealed frames: the losing frame was dropped at a "+
						"seq its rival had already consumed, so NOTHING is marked stale and the watched grid "+
						"pins arbitrarily far behind while everything still reads online and live", accepted, frames)
				}
				if gaps != 0 {
					t.Errorf("the phone saw %d gaps: this relay stored every frame it was handed, so a "+
						"contiguous seq stream must reach it intact", gaps)
				}
			})
		}
	}
}

// reserveFailOutbox fails the nominated Reserve. It is the OTHER side of the rule: Reserve
// runs BEFORE the append, so its failure leaves the sealed bytes provably inside this process
// and the seq genuinely unspent.
type reserveFailOutbox struct {
	Outbox
	failOn int // 1-based index of the Reserve to fail

	mu    sync.Mutex
	calls int
}

var errReserveFailed = errors.New("outbox: reserve failed")

func (o *reserveFailOutbox) Reserve(cursor uint64, env []byte) error {
	o.mu.Lock()
	o.calls++
	fail := o.calls == o.failOn
	o.mu.Unlock()
	if fail {
		return errReserveFailed
	}
	return o.Outbox.Reserve(cursor, env)
}

// TestRelaySink_ASeqIsReissuedWhenTheFrameNeverLeftTheProcess is the scoping half, and it is
// what stops the fix from being "never reuse a seq". A failure BEFORE the append -- here a
// refused outbox reservation -- leaves the seq unspent as a matter of local fact rather than
// relay testimony, so reissuing it is sound and burning it would manufacture the very gap
// PB-SYNC-1 has to charge to BOTH journal and terminal.
func TestRelaySink_ASeqIsReissuedWhenTheFrameNeverLeftTheProcess(t *testing.T) {
	key := inboundKey(41)
	inner, err := OpenOutbox("")
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	app := &storeThenRefuseAppender{sentinel: relay.ErrQuotaExceeded, refuseOn: 0} // never lies
	sink := NewRelaySink(RelayConfig{
		Appender: app, Target: "phone", Machine: "m", EpochID: 9, Key: key,
		SenderKeyID: [8]byte{'m'}, Outbox: &reserveFailOutbox{Outbox: inner, failOn: 2},
	})

	if err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"}); err != nil {
		t.Fatalf("record A: %v", err)
	}
	if err := sink.Event(protocol.JournalRecord{Cursor: 2, SessionID: "m/s2", Type: "launched"}); !errors.Is(err, errReserveFailed) {
		t.Fatalf("record B returned %v, want the reservation failure surfaced to the caller", err)
	}
	if err := sink.Event(protocol.JournalRecord{Cursor: 3, SessionID: "m/s3", Type: "exited"}); err != nil {
		t.Fatalf("record C: %v", err)
	}

	recv := crypto.NewMailboxReceiver()
	for i, raw := range app.all() {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("stored envelope %d does not parse: %v", i, err)
		}
		res, err := recv.Accept(key, env)
		if err != nil {
			t.Fatalf("the phone rejected stored envelope %d (seq %d): %v", i+1, env.Header.Seq, err)
		}
		if res.Gap {
			t.Errorf("the phone saw a GAP at seq %d: the frame whose reservation failed never reached "+
				"the appender, so its seq was never spent and the next frame must carry it -- a burned "+
				"seq costs a conservative resync of BOTH journal and terminal (PB-SYNC-1)", env.Header.Seq)
		}
	}
}
