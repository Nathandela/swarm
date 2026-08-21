// FAILING-FIRST (TDD RED, GG-5) for the audit committee's ROUND-3 findings on the
// pump's reply accounting (codex round-2 finding 3, reproduced by the orchestrator;
// Opus round-2 finding 5).
//
// THE TOCTOU, in the round-2 code's own terms: pump() sampled c.pending.Load() == 0 and
// then performed a potentially BLOCKING send into c.frames (capacity 1), while roundtrip
// independently decremented pending when it consumed a frame. So a legitimate reply
// could pass the non-zero check while a stray still occupied the channel, park on the
// full channel, and enter the queue AFTER pending had reached zero -- the exact shift
// the drop rule exists to prevent, reintroduced by the drop rule's own race. The
// interleaving, step by step (this file's first test drives it):
//
//	1. append #2 is in flight (pending == 1); the peer volunteers a stray MsgError
//	   and then writes append #2's real MsgOK, back to back.
//	2. pump reads the stray: pending == 1, so it is enqueued (channel now full).
//	3. pump reads the real reply: pending STILL reads 1 (the consumer has not run),
//	   the zero-check passes, and the pump commits to a blocking send.
//	4. roundtrip consumes the stray as append #2's answer, decrements pending to 0,
//	   and returns the bounded casualty.
//	5. the pump's parked send now completes: append #2's real reply is queued while
//	   pending == 0, and append #3 consumes it as its own answer -- cursor 2, and
//	   every later exchange on the connection answers the question before it.
//
// The fix moves the accounting wholly into the pump: an owed-counter raised by
// roundtrip before its write, lowered by the PUMP alone, atomically with claiming the
// queue slot, so the count a frame is judged against can never go stale between the
// check and the enqueue. See client.go's owed field for the invariant.

package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// r3Script is a minimal scripted peer speaking the relay frame protocol: it
// authenticates anything, assigns mailbox_append cursors 1,2,3,... in arrival order,
// and misbehaves exactly where a test arms it. Deliberately NOT the real relay.Server:
// the defect under test is the CLIENT's reply accounting against a peer that breaks
// the one-reply-per-request contract.
type r3Script struct {
	srv *httptest.Server

	// strayBurst cues the script to volunteer that many MsgError frames, back to
	// back, correlated to nothing; burstDone signals when they are all on the wire.
	strayBurst chan int
	burstDone  chan struct{}

	mu sync.Mutex
	// strayBefore[n]: append ordinal n's reply is preceded by one stray MsgError,
	// written back to back with it -- the tightest window the TOCTOU needs.
	strayBefore map[uint64]bool
	// delayReply[n]: append ordinal n's reply is written only after this delay, so a
	// caller with a short call deadline abandons it and the reply arrives LATE.
	delayReply map[uint64]time.Duration
	appendN    uint64
}

func newR3Script(t *testing.T) *r3Script {
	t.Helper()
	s := &r3Script{
		strayBurst:  make(chan int),
		burstDone:   make(chan struct{}),
		strayBefore: map[uint64]bool{},
		delayReply:  map[uint64]time.Duration{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ws.SetReadLimit(MaxFrame + 64)
		ctx := r.Context()

		var wmu sync.Mutex
		write := func(tag MsgType, body any) {
			payload, err := json.Marshal(body)
			if err != nil {
				return
			}
			var buf bytes.Buffer
			if WriteFrame(&buf, tag, payload) != nil {
				return
			}
			wmu.Lock()
			defer wmu.Unlock()
			_ = ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
		}

		go func() { // the volunteered stray bursts, on the test's cue
			for {
				select {
				case <-ctx.Done():
					return
				case n := <-s.strayBurst:
					for i := 0; i < n; i++ {
						write(MsgError, map[string]string{"code": "bad_request"})
					}
					select {
					case s.burstDone <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()

		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			tag, payload, err := ReadFrame(bytes.NewReader(data))
			if err != nil {
				continue
			}
			switch tag {
			case MsgRelay:
				var env struct {
					Op string `json:"op"`
				}
				if json.Unmarshal(payload, &env) != nil {
					continue
				}
				switch env.Op {
				case "auth_init":
					write(MsgOK, map[string]any{"nonce": []byte("r3-nonce-16bytes")})
				case "auth_resp":
					write(MsgOK, map[string]any{"routing_id": "r3-rig"})
				}
			case MsgMailboxAppend:
				s.mu.Lock()
				s.appendN++
				n := s.appendN
				stray := s.strayBefore[n]
				delay := s.delayReply[n]
				s.mu.Unlock()
				if delay > 0 {
					time.Sleep(delay)
				}
				if stray {
					write(MsgError, map[string]string{"code": "bad_request"})
				}
				write(MsgOK, map[string]uint64{"cursor": n})
			}
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *r3Script) URL() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") }

func (s *r3Script) dial(t *testing.T, ctx context.Context) *Client {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	cl, err := Dial(ctx, s.URL(), ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(priv, c), nil },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// TestCommitteeR3_AStrayMidExchangeThenABurstKeepsEveryPairing drives the reproduced
// codex interleaving as deterministically as the seam allows: the stray and the
// displaced real reply are written back to back on the wire (steps 1-3 of the file
// comment need exactly that adjacency to fill the capacity-1 channel), and a burst of
// appends follows, each asserting its OWN cursor. Whether the round-2 race actually
// fired on a given run depended on the pump outracing the woken consumer -- which is
// why this test is run at -count, not once; the fixed accounting is deterministic.
func TestCommitteeR3_AStrayMidExchangeThenABurstKeepsEveryPairing(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	script.mu.Lock()
	script.strayBefore[2] = true
	script.mu.Unlock()

	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("a")); err != nil || cursor != 1 {
		t.Fatalf("MailboxAppend #1 = (%d, %v), want (1, nil)", cursor, err)
	}
	// The stray is written immediately before append #2's real reply, so it is
	// consumed as append #2's answer: ONE bounded casualty, by design.
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("b")); err == nil {
		t.Fatal("MailboxAppend #2 = nil error; the scripted stray preceded its reply in " +
			"order, so this exchange must be the one bounded casualty")
	}
	// The burst. Every one of these must get ITS cursor: under the round-2 race the
	// displaced real reply of append #2 enters the queue after pending hits zero, and
	// append #3 reads cursor 2 -- then 4 reads 3, and so on, forever.
	for k := uint64(3); k <= 10; k++ {
		cursor, err := cl.MailboxAppend(ctx, "peer", []byte{byte(k)})
		if err != nil || cursor != k {
			t.Fatalf("burst MailboxAppend #%d = (%d, %v), want (%d, nil).\n"+
				"The stale reply displaced by the mid-exchange stray entered the pump queue "+
				"after the owed count reached zero (the check-then-blocking-send TOCTOU), and "+
				"every exchange from here on answers the question before it", k, cursor, err, k)
		}
	}
	// The casualty is bounded to one exchange; the CONNECTION survives. Tearing it
	// down here would hand any peer able to volunteer one frame a reconnect lever,
	// and the round-2 committee fence pins the bounded-casualty semantics.
	select {
	case <-cl.Done():
		t.Fatal("the connection was torn down over a bounded stray; the drop rule must " +
			"absorb it without killing the transport")
	default:
	}
}

// TestCommitteeR3_ALateReplyToAnAbandonedCallNeverAnswersTheNextOne pins the OTHER half
// of the rewritten accounting. Round 3 satisfied it with a roundtrip-side skip; round 4
// (Opus F3) replaced that with the pump-side discard ledger -- abandonReply moves the
// abandoned exchange's credit out of owed, and the pump spends it on the straggler
// ahead of the live reply. The assertion is unchanged either way: a reply that arrives
// after its caller timed out must be discarded, never adopted as the next exchange's
// answer.
func TestCommitteeR3_ALateReplyToAnAbandonedCallNeverAnswersTheNextOne(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	script.mu.Lock()
	script.delayReply[1] = 600 * time.Millisecond
	script.mu.Unlock()

	cl.conn.callTimeout = 200 * time.Millisecond
	if _, err := cl.MailboxAppend(ctx, "peer", []byte("late")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("MailboxAppend #1 against a delayed reply = %v, want ErrTimeout", err)
	}
	cl.conn.callTimeout = DefaultCallTimeout

	// Issued while reply #1 is still in flight; its own reply queues behind the late
	// one, and the skip must discard exactly the late one.
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("next")); err != nil || cursor != 2 {
		t.Fatalf("MailboxAppend #2 = (%d, %v), want (2, nil): the abandoned call's late "+
			"reply (cursor 1) was adopted as the next exchange's answer instead of being "+
			"discarded", cursor, err)
	}
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("steady")); err != nil || cursor != 3 {
		t.Fatalf("MailboxAppend #3 = (%d, %v), want (3, nil)", cursor, err)
	}
}

// lockedBuffer is a log sink safe for concurrent writers (the pump goroutine logs).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestCommitteeR3_TheUnsolicitedDropLogIsRateLimited: the drop path is driven entirely
// by PEER-sent frames, so a log line per dropped frame is a log-amplification lever the
// peer pulls for free (Opus round-2 finding 5). Fifty strays must not print fifty
// lines; the drops themselves must all still happen (the append after them gets ITS
// cursor, proving the pairing survived the burst).
func TestCommitteeR3_TheUnsolicitedDropLogIsRateLimited(t *testing.T) {
	script := newR3Script(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl := script.dial(t, ctx)

	sink := &lockedBuffer{}
	log.SetOutput(sink)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	const strays = 50
	select {
	case script.strayBurst <- strays:
	case <-ctx.Done():
		t.Fatal("the script never took the stray-burst cue")
	}
	select {
	case <-script.burstDone:
	case <-ctx.Done():
		t.Fatal("the script never finished writing the stray burst")
	}
	// burstDone says the frames are on the wire, not that the pump has read them; let
	// them cross and reach the pump before a request raises the owed count (the same
	// settle the round-2 test uses). A straggler arriving mid-append would claim the
	// owed slot as a bounded casualty, which is not what this test is about.
	time.Sleep(500 * time.Millisecond)

	// All fifty strays precede this append's reply on the in-order stream, so by the
	// time the reply is consumed every one of them has been through the pump.
	if cursor, err := cl.MailboxAppend(ctx, "peer", []byte("after")); err != nil || cursor != 1 {
		t.Fatalf("MailboxAppend after an idle stray burst = (%d, %v), want (1, nil)", cursor, err)
	}

	lines := 0
	for _, l := range strings.Split(sink.String(), "\n") {
		if strings.Contains(l, "unsolicited") {
			lines++
		}
	}
	if lines == 0 {
		t.Fatal("no unsolicited-drop line was logged at all; the drop must stay loud, just bounded")
	}
	if lines > 5 {
		t.Fatalf("%d strays produced %d log lines; the drop log must be rate-limited "+
			"(peer-controlled log amplification, Opus round-2 finding 5)", strays, lines)
	}
}
