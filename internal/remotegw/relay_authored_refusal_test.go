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

// relayChoice is what the relay elects to do with one append it has been handed. Every one of
// these is available to it at every append: the bytes are in its hands before it writes a
// reply, and it authors the read as well.
type relayChoice int

const (
	storeAndAck        relayChoice = iota // the honest success
	honestRefusal                         // refused before the store, exactly as handleMailboxAppend does
	theLie                                // STORED, then answered with a refusal sentinel, then served
	theLieThenWithhold                    // STORED, then denied, and never revealed on the read
)

func (c relayChoice) String() string {
	switch c {
	case honestRefusal:
		return "honest refusal (stores nothing)"
	case theLie:
		return "stores it, denies it, serves it"
	case theLieThenWithhold:
		return "stores it, denies it, hides it"
	}
	return "stores it and acks"
}

// choiceAppender applies one nominated choice to one nominated append and is honest for the
// rest. `served` is what the relay is WILLING to reveal on a mailbox read, which is the only
// thing the phone can ever see -- so a frame the relay stored but withholds counts as lost,
// exactly as it would in production.
type choiceAppender struct {
	choice   relayChoice
	choiceOn int // 1-based

	mu     sync.Mutex
	calls  int
	served [][]byte
}

func (a *choiceAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	c := storeAndAck
	if a.calls == a.choiceOn {
		c = a.choice
	}
	switch c {
	case honestRefusal, theLieThenWithhold:
		// Nothing the phone can ever read. Whether the relay kept a private copy (the lie) or
		// truly stored nothing (the honest refusal) is INDISTINGUISHABLE from outside, which
		// is the whole reason its reply cannot be evidence.
		return 0, relay.ErrQuotaExceeded
	case theLie:
		a.served = append(a.served, append([]byte(nil), env...))
		return 0, relay.ErrQuotaExceeded
	default:
		a.served = append(a.served, append([]byte(nil), env...))
		return uint64(len(a.served)), nil
	}
}

func (a *choiceAppender) revealed() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.served...)
}

// TestRelaySink_TheRelayCannotCauseLOSSWithoutCausingAGAP is the fence for the defect's
// TEETH, and it is a stronger statement than "no seq is reissued".
//
// Losing a cursor==0 frame is survivable: PB-SYNC-1 marks the shared bucket stale off the
// gap bit and the phone resyncs. What is NOT survivable is losing one SILENTLY, and that is
// what the reissued seq bought the relay: the loser was stale-dropped at a seq its rival had
// already consumed, so crypto.MailboxReceiver computed gap := seen && seq > hi+1 over an
// UNBROKEN run and reported nothing. Every staleness mechanism downstream is driven off that
// one bit (phonecore commitReceive -> markStale -> StreamState, pinned by
// TestS10_ASharedBucketGapStalesJournalAndTerminal), so a false gap bit silences all of them
// at once while the phone is actively receiving.
//
// THE INVARIANT, over every choice the relay has at an append:
//
//	frames the gateway sealed but the phone never accepted  >  0   =>   a GAP is reported
//
// A fix that merely loses the frame more safely would satisfy the seq-collision fence above
// and still fail here.
//
// MEASURED UNDER MUTATION, and it corrects how the reuse was described for seven rounds: with
// the reuse reinstated this fails on the HONEST-REFUSAL arm too (sealed=3 accepted=2
// gapReported=false). The reuse never preserved the refused frame's content -- it renumbered
// the NEXT frame into the hole. So the old behaviour was not "a gap avoided for free"; it was
// a loss that happened either way, reported in one case and silent in the other.
//
// One honest qualification, so this is not read as more than it is: on the TERMINAL arm the
// next snapshot supersedes the lost one, so the user's grid is current and the practical harm
// of that single loss is small. It is in the fence anyway because the held seq is handed to
// whatever frame comes next OF ANY KIND -- journal records, roster records, reconcile records
// and reseeds all share this seq space -- so the mechanism is never confined to the stream
// that triggered it. The RESEED arm is where the teeth are: it is the only journal repair
// channel, and a silently dropped repair is one the phone believes it received.
func TestRelaySink_TheRelayCannotCauseLOSSWithoutCausingAGAP(t *testing.T) {
	key := inboundKey(41)
	sender := [8]byte{'m', 'a', 'c', 'h', 'i', 'n', 'e', '1'}

	for _, choice := range []relayChoice{honestRefusal, theLie, theLieThenWithhold} {
		for _, path := range []struct {
			name string
			emit func(s *RelaySink, n int, label string) error
		}{
			{"terminal snapshot", func(s *RelaySink, _ int, label string) error {
				return s.Terminal("m/s1", []string{label}, 80, 24)
			}},
			// The reseed is the ONLY journal repair channel: a silent loss here is a repair the
			// phone believes it received.
			{"journal reseed", func(s *RelaySink, n int, label string) error {
				return s.Reseed(protocol.JournalReseed{
					Roster: []protocol.JournalRecord{{SessionID: "m/" + label, Type: "launched"}},
					Cursor: uint64(n),
				})
			}},
		} {
			t.Run(path.name+"/"+choice.String(), func(t *testing.T) {
				app := &choiceAppender{choice: choice, choiceOn: 2}
				sink := NewRelaySink(RelayConfig{
					Appender: app, Target: "phone", Machine: "m", EpochID: 9, Key: key,
					SenderKeyID: sender,
				})

				const sealed = 3
				for n := 1; n <= sealed; n++ {
					err := path.emit(sink, n, [sealed]string{"one", "two", "three"}[n-1])
					if n != 2 && err != nil {
						t.Fatalf("frame %d: %v", n, err)
					}
				}

				recv := crypto.NewMailboxReceiver()
				accepted, gapReported := 0, false
				for i, raw := range app.revealed() {
					env, err := crypto.ParseEnvelope(raw)
					if err != nil {
						t.Fatalf("revealed envelope %d does not parse: %v", i, err)
					}
					res, err := recv.Accept(key, env)
					if err != nil {
						t.Logf("  the phone refused revealed envelope %d (seq %d): %v", i+1, env.Header.Seq, err)
						continue
					}
					if res.Gap {
						gapReported = true
					}
					accepted++
				}

				lost := sealed - accepted
				t.Logf("relay choice %q: sealed=%d accepted=%d lost=%d gapReported=%v",
					choice, sealed, accepted, lost, gapReported)
				if lost > 0 && !gapReported {
					t.Errorf("SILENT LOSS: %d of %d sealed frames never reached the phone and NO GAP was "+
						"reported. The relay chose %q, and nothing downstream can mark the bucket stale off "+
						"a gap bit that was never set -- the watched grid pins arbitrarily far behind while "+
						"StreamState still reads live (ADR-007 B125 F-2, B121)", lost, sealed, choice)
				}
				if lost == 0 && gapReported {
					t.Errorf("a gap was reported though every sealed frame arrived: a spurious gap costs a " +
						"conservative resync of BOTH journal and terminal against PB-SYNC-6's budget")
				}
			})
		}
	}
}
